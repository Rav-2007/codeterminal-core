// Package ort runs ONNX Runtime through its C API without cgo.
//
// WHY IT EXISTS (2026-10-19). The helper used github.com/yalue/onnxruntime_go,
// which is cgo, and cgo was the only reason Mochiii shipped as three programs:
// the daemon had to stay pure Go, so the model ran in a separate binary. It
// also kept the helper out of every cross-platform check (R1.26), made a C
// compiler a build requirement, and tied code search on Linux to the glibc of
// the machine that built the helper (2.34, where onnxruntime itself needs
// 2.27).
//
// ONNX Runtime's C API is one table of function pointers, so calling it needs
// no C compiler at all: the library is loaded at run time (dlopen through
// purego on macOS and Linux, LoadLibrary on Windows), OrtGetApiBase hands back
// the table, and each call goes through a pointer at a fixed position in it.
//
// THE SAME CALLS AS BEFORE, argument for argument, so a vector is the same
// vector: the environment at the ERROR log level, CPU memory info on the arena
// allocator, default session options created and released around
// CreateSession, input tensors that wrap the caller's buffers, Run with no run
// options, and the output copied out and released. That is what
// onnxruntime_go's C wrapper did for every call the helper made.
//
// Only those calls are here. The helper needs nothing else, and every function
// in this file is one the tests drive against the real library.
package ort

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unsafe"
)

// APIVersion is the OrtApi version asked for: ORT_API_VERSION in the pinned
// 1.26.0 header, the same number onnxruntime_go asked for. A library older
// than this returns no table, which Init reports with the library's version.
const APIVersion = 26

// Positions in struct OrtApi. The struct only ever grows at its end, so a
// function keeps its position in every later release.
// TestTheTableIndicesAreTheHeaders re-derives each one from the pinned header.
const (
	fnGetErrorMessage                = 2
	fnCreateEnv                      = 3
	fnCreateSession                  = 7
	fnRun                            = 9
	fnCreateSessionOptions           = 10
	fnSetIntraOpNumThreads           = 24
	fnCreateTensorWithDataAsOrtValue = 49
	fnGetTensorMutableData           = 51
	fnGetTensorElementType           = 60
	fnGetDimensionsCount             = 61
	fnGetDimensions                  = 62
	fnGetTensorTypeAndShape          = 65
	fnCreateCpuMemoryInfo            = 69
	fnReleaseEnv                     = 92
	fnReleaseStatus                  = 93
	fnReleaseMemoryInfo              = 94
	fnReleaseSession                 = 95
	fnReleaseValue                   = 96
	fnReleaseTensorTypeAndShapeInfo  = 99
	fnReleaseSessionOptions          = 100
)

// tableIndex names every position above, for the header test.
var tableIndex = map[string]int{
	"GetErrorMessage":                fnGetErrorMessage,
	"CreateEnv":                      fnCreateEnv,
	"CreateSession":                  fnCreateSession,
	"Run":                            fnRun,
	"CreateSessionOptions":           fnCreateSessionOptions,
	"SetIntraOpNumThreads":           fnSetIntraOpNumThreads,
	"CreateTensorWithDataAsOrtValue": fnCreateTensorWithDataAsOrtValue,
	"GetTensorMutableData":           fnGetTensorMutableData,
	"GetTensorElementType":           fnGetTensorElementType,
	"GetDimensionsCount":             fnGetDimensionsCount,
	"GetDimensions":                  fnGetDimensions,
	"GetTensorTypeAndShape":          fnGetTensorTypeAndShape,
	"CreateCpuMemoryInfo":            fnCreateCpuMemoryInfo,
	"ReleaseEnv":                     fnReleaseEnv,
	"ReleaseStatus":                  fnReleaseStatus,
	"ReleaseMemoryInfo":              fnReleaseMemoryInfo,
	"ReleaseSession":                 fnReleaseSession,
	"ReleaseValue":                   fnReleaseValue,
	"ReleaseTensorTypeAndShapeInfo":  fnReleaseTensorTypeAndShapeInfo,
	"ReleaseSessionOptions":          fnReleaseSessionOptions,
}

// Enum values passed across, also checked against the header.
const (
	loggingLevelError = 3 // ORT_LOGGING_LEVEL_ERROR, onnxruntime_go's level
	arenaAllocator    = 1 // OrtArenaAllocator
	memTypeDefault    = 0 // OrtMemTypeDefault
	elementTypeFloat  = 1 // ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT
	elementTypeInt64  = 7 // ONNX_TENSOR_ELEMENT_DATA_TYPE_INT64
)

// enumValue names every value above, for the header test.
var enumValue = map[string]int{
	"ORT_LOGGING_LEVEL_ERROR":             loggingLevelError,
	"OrtArenaAllocator":                   arenaAllocator,
	"OrtMemTypeDefault":                   memTypeDefault,
	"ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT": elementTypeFloat,
	"ONNX_TENSOR_ELEMENT_DATA_TYPE_INT64": elementTypeInt64,
}

const ptrSize = unsafe.Sizeof(uintptr(0))

// The process-wide state: one library, one environment. ONNX Runtime's
// environment is process-global by design, as onnxruntime_go's was, and the
// helper initialises it once before opening any session.
//
// mu ORDERS TEARDOWN AGAINST USE (2026-10-19). The helper's shutdown raced:
// its signal handler calls os.Exit while main's deferred cleanup ran Destroy
// before closing the session, so whenever the cleanup won, the session was
// released through a function table that was already gone -- a SIGSEGV on 2
// of the 10 stops in one retrieval-eval run, which is what found it. onnxruntime_go survived
// the same order by luck. Now a Run holds mu for reading, so nothing is
// released under an inference; Destroy releases every live session before the
// environment; and a session released either way is closed for good.
var (
	mu       sync.RWMutex
	lib      library
	api      uintptr // const OrtApi*
	env      uintptr // OrtEnv*
	memInfo  uintptr // OrtMemoryInfo*
	sessions = map[*Session]struct{}{}
)

// Init loads the ONNX Runtime shared library at libPath and creates the
// environment every session runs in.
func Init(libPath string) error {
	mu.Lock()
	defer mu.Unlock()
	if api != 0 {
		return errors.New("ONNX Runtime is already initialized")
	}
	l, getAPIBase, err := openLibrary(libPath)
	if err != nil {
		return err
	}
	base := syscallN(getAPIBase)
	if base == 0 {
		l.close()
		return fmt.Errorf("OrtGetApiBase in %s returned no API base", libPath)
	}
	// struct OrtApiBase { GetApi; GetVersionString; }
	getAPI := *(*uintptr)(cptr(base))
	getVersion := *(*uintptr)(cptr(base + ptrSize))
	a := syscallN(getAPI, APIVersion)
	if a == 0 {
		v := goString(syscallN(getVersion))
		l.close()
		return fmt.Errorf("%s is ONNX Runtime %s, which does not provide API version %d", libPath, v, APIVersion)
	}
	api = a

	var p pins
	defer p.Unpin()
	logID := p.bytes("mochiii-embedder-helper")
	e := p.word()
	err = check(call(fnCreateEnv, loggingLevelError, addr(&logID[0]), addr(e)), "creating the ONNX Runtime environment")
	if err != nil {
		api = 0
		l.close()
		return err
	}
	m := p.word()
	if err := check(call(fnCreateCpuMemoryInfo, arenaAllocator, memTypeDefault, addr(m)), "creating CPU memory info"); err != nil {
		call(fnReleaseEnv, *e)
		api = 0
		l.close()
		return err
	}
	lib, env, memInfo = l, *e, *m
	return nil
}

// Destroy releases the environment and unloads the library. It is a no-op
// when nothing is initialized.
func Destroy() {
	mu.Lock()
	defer mu.Unlock()
	if api == 0 {
		return
	}
	// Sessions first: ONNX Runtime requires them gone before their environment.
	for s := range sessions {
		s.release()
	}
	if memInfo != 0 {
		call(fnReleaseMemoryInfo, memInfo)
		memInfo = 0
	}
	if env != 0 {
		call(fnReleaseEnv, env)
		env = 0
	}
	api = 0
	lib.close()
}

// Session is one loaded model.
type Session struct {
	handle      uintptr // OrtSession*
	inputNames  []uintptr
	outputNames []uintptr
	names       [][]byte // the C strings the two arrays above point into
	pinner      runtime.Pinner
}

// NewSession opens the model at modelPath with the named inputs and outputs.
//
// intraOpThreads zero means default session options, which is what
// onnxruntime_go's C wrapper created when it was handed none: production has
// always run that way. A positive value sets the intra-op thread count, a
// diagnostic the eval suite uses (see helper/onnxembedder.go).
func NewSession(modelPath string, inputNames, outputNames []string, intraOpThreads int) (*Session, error) {
	mu.Lock()
	defer mu.Unlock()
	if api == 0 {
		return nil, errors.New("ONNX Runtime is not initialized")
	}
	if intraOpThreads < 0 {
		return nil, fmt.Errorf("intra-op thread count %d is negative", intraOpThreads)
	}
	path, err := osPath(modelPath)
	if err != nil {
		return nil, err
	}
	var p pins
	defer p.Unpin()
	p.Pin(&path[0])

	opts := p.word()
	if err := check(call(fnCreateSessionOptions, addr(opts)), "creating session options"); err != nil {
		return nil, err
	}
	// Released once the session exists: CreateSession copies what it needs.
	defer call(fnReleaseSessionOptions, *opts)
	if intraOpThreads > 0 {
		if err := check(call(fnSetIntraOpNumThreads, *opts, uintptr(intraOpThreads)), "setting the intra-op thread count"); err != nil {
			return nil, err
		}
	}

	s := &Session{}
	for _, n := range inputNames {
		p, err := s.name(n)
		if err != nil {
			return nil, err
		}
		s.inputNames = append(s.inputNames, p)
	}
	for _, n := range outputNames {
		p, err := s.name(n)
		if err != nil {
			return nil, err
		}
		s.outputNames = append(s.outputNames, p)
	}
	if len(s.inputNames) == 0 || len(s.outputNames) == 0 {
		s.pinner.Unpin()
		return nil, errors.New("a session needs at least one input name and one output name")
	}
	s.pinner.Pin(&s.inputNames[0])
	s.pinner.Pin(&s.outputNames[0])

	h := p.word()
	err = check(call(fnCreateSession, env, addr(&path[0]), *opts, addr(h)), "opening "+modelPath)
	if err != nil {
		s.pinner.Unpin()
		return nil, err
	}
	s.handle = *h
	sessions[s] = struct{}{}
	return s, nil
}

// name keeps a NUL-terminated copy of n for the life of the session and
// returns its address.
func (s *Session) name(n string) (uintptr, error) {
	if strings.IndexByte(n, 0) >= 0 {
		return 0, fmt.Errorf("tensor name %q contains a NUL byte", n)
	}
	b := cString(n)
	s.names = append(s.names, b)
	s.pinner.Pin(&b[0])
	return addr(&b[0]), nil
}

// Destroy releases the session. Releasing it twice, or after Destroy has
// already released it with the environment, does nothing.
func (s *Session) Destroy() error {
	mu.Lock()
	defer mu.Unlock()
	s.release()
	return nil
}

// release frees the session. mu is held for writing.
func (s *Session) release() {
	if s.handle != 0 && api != 0 {
		call(fnReleaseSession, s.handle)
	}
	s.handle = 0
	delete(sessions, s)
	s.pinner.Unpin()
}

// Tensor is a float32 output, copied out of ONNX Runtime's memory.
type Tensor struct {
	Shape []int64
	Data  []float32
}

// Run runs the session over int64 inputs that all have the given shape, one
// per input name in order, and returns its single float32 output.
func (s *Session) Run(shape []int64, inputs ...[]int64) (Tensor, error) {
	mu.RLock()
	defer mu.RUnlock()
	if s.handle == 0 {
		return Tensor{}, errors.New("the session is closed")
	}
	if len(inputs) != len(s.inputNames) {
		return Tensor{}, fmt.Errorf("the session has %d inputs, but Run was given %d", len(s.inputNames), len(inputs))
	}
	if len(s.outputNames) != 1 {
		return Tensor{}, fmt.Errorf("Run returns one output, and the session names %d", len(s.outputNames))
	}
	count, err := elements(shape)
	if err != nil || count == 0 {
		return Tensor{}, fmt.Errorf("input shape %v: %v", shape, errOr(err, "no elements"))
	}

	var p pins
	defer p.Unpin()
	dims := append([]int64(nil), shape...)
	p.Pin(&dims[0])

	values := make([]uintptr, len(inputs))
	p.Pin(&values[0])
	defer func() {
		for _, v := range values {
			if v != 0 {
				call(fnReleaseValue, v)
			}
		}
	}()
	for i, in := range inputs {
		if int64(len(in)) != count {
			return Tensor{}, fmt.Errorf("input %d has %d values, and shape %v needs %d", i, len(in), shape, count)
		}
		// The tensor WRAPS this buffer rather than copying it, so it stays
		// pinned until the value is released, which the deferred loop above
		// does before p.Unpin runs.
		p.Pin(&in[0])
		v := p.word()
		if err := check(call(fnCreateTensorWithDataAsOrtValue, memInfo, addr(&in[0]), uintptr(len(in))*8,
			addr(&dims[0]), uintptr(len(dims)), elementTypeInt64, addr(v)), fmt.Sprintf("building input tensor %d", i)); err != nil {
			return Tensor{}, err
		}
		values[i] = *v
	}

	outputs := make([]uintptr, 1)
	p.Pin(&outputs[0])
	if err := check(call(fnRun, s.handle, 0, addr(&s.inputNames[0]), addr(&values[0]), uintptr(len(values)),
		addr(&s.outputNames[0]), 1, addr(&outputs[0])), "running the model"); err != nil {
		return Tensor{}, err
	}
	defer call(fnReleaseValue, outputs[0])
	return copyFloatTensor(outputs[0])
}

// copyFloatTensor reads a float32 tensor's shape and data out of ONNX
// Runtime's memory.
func copyFloatTensor(value uintptr) (Tensor, error) {
	var p pins
	defer p.Unpin()
	info := p.word()
	if err := check(call(fnGetTensorTypeAndShape, value, addr(info)), "reading the output's type and shape"); err != nil {
		return Tensor{}, err
	}
	defer call(fnReleaseTensorTypeAndShapeInfo, *info)

	et := new(int32)
	p.Pin(et)
	if err := check(call(fnGetTensorElementType, *info, addr(et)), "reading the output's element type"); err != nil {
		return Tensor{}, err
	}
	if *et != elementTypeFloat {
		return Tensor{}, fmt.Errorf("the output's element type is %d, not float32", *et)
	}
	n := p.word()
	if err := check(call(fnGetDimensionsCount, *info, addr(n)), "reading the output's rank"); err != nil {
		return Tensor{}, err
	}
	if *n == 0 || *n > 8 {
		return Tensor{}, fmt.Errorf("the output has rank %d", *n)
	}
	shape := make([]int64, *n)
	p.Pin(&shape[0])
	if err := check(call(fnGetDimensions, *info, addr(&shape[0]), *n), "reading the output's shape"); err != nil {
		return Tensor{}, err
	}
	count, err := elements(shape)
	if err != nil {
		return Tensor{}, fmt.Errorf("output shape %v: %w", shape, err)
	}
	data := p.word()
	if err := check(call(fnGetTensorMutableData, value, addr(data)), "reading the output's data"); err != nil {
		return Tensor{}, err
	}
	out := make([]float32, count)
	if count > 0 {
		copy(out, unsafe.Slice((*float32)(cptr(*data)), count))
	}
	return Tensor{Shape: shape, Data: out}, nil
}

// maxElements bounds a tensor this package will read: far above any real
// output here (512 tokens x 384 floats is under 200,000) and far below an
// allocation that could take the process down.
const maxElements = 1 << 26

// elements is the number of values a shape holds.
func elements(shape []int64) (int64, error) {
	if len(shape) == 0 {
		return 0, errors.New("empty shape")
	}
	count := int64(1)
	for _, d := range shape {
		if d < 0 {
			return 0, fmt.Errorf("dimension %d is negative", d)
		}
		if d > 0 && count > maxElements/d {
			return 0, errors.New("too many elements")
		}
		count *= d
	}
	return count, nil
}

func errOr(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

// call calls the OrtApi function at position i.
func call(i int, args ...uintptr) uintptr {
	f := *(*uintptr)(cptr(api + uintptr(i)*ptrSize))
	return syscallN(f, args...)
}

// check turns an OrtStatus* into an error, releasing the status.
func check(status uintptr, what string) error {
	if status == 0 {
		return nil
	}
	msg := goString(call(fnGetErrorMessage, status))
	call(fnReleaseStatus, status)
	return fmt.Errorf("%s: %s", what, msg)
}

// addr is the address of a Go object being handed to ONNX Runtime. Every such
// object is pinned first (see pins), for as long as ONNX Runtime may use it.
func addr[T any](p *T) uintptr { return uintptr(unsafe.Pointer(p)) }

// pins holds the Go memory one call hands to ONNX Runtime. Pinning is what
// keeps it on the heap and in place: an address turned into a uintptr outside a
// syscall's own argument list is invisible to the runtime, so a value the
// compiler kept on the stack would move out from under ONNX Runtime the moment
// the stack grew on the way into the call.
type pins struct{ runtime.Pinner }

// word is a pinned pointer-sized out-parameter.
func (p *pins) word() *uintptr {
	w := new(uintptr)
	p.Pin(w)
	return w
}

// bytes is s as a pinned NUL-terminated C string.
func (p *pins) bytes(s string) []byte {
	b := cString(s)
	p.Pin(&b[0])
	return b
}

// cptr turns an address ONNX Runtime returned into a pointer. It is C memory,
// never Go's, so the garbage collector neither moves nor frees it; reading the
// uintptr through a pointer keeps go vet's unsafeptr check, which cannot know
// that, from flagging every read.
func cptr(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

// cString is s as a NUL-terminated byte slice.
func cString(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

// maxCString bounds goString, so a missing terminator cannot read on forever.
const maxCString = 1 << 16

// goString copies a NUL-terminated C string.
func goString(p uintptr) string {
	if p == 0 {
		return ""
	}
	var b []byte
	for i := uintptr(0); i < maxCString; i++ {
		c := *(*byte)(cptr(p + i))
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}
