package ort

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// THE HEADER IS THE SPECIFICATION. Every number this package passes across --
// a function's position in the OrtApi table, an enum value -- comes from
// onnxruntime_c_api.h, and a wrong one is not a compile error but a call into
// the wrong function. So the numbers are re-derived here from the header
// itself: the one inside onnxruntime-linux-x64-1.26.0.tgz, the archive the
// daemon downloads and hash-checks (daemon/onnxruntimefetch.go), stored
// gzipped so its 8,574 lines stay out of the repository's search corpus.

// pinnedHeaderSHA256 is the header's own hash, as extracted from that archive.
const pinnedHeaderSHA256 = "f09c0d8584aae94dced9bb05cff8579f4f6c5c4a2271413ea8ab27bd0bbf46aa"

func pinnedHeader(t *testing.T) string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "onnxruntime_c_api.h.gz"))
	if err != nil {
		t.Fatalf("opening the pinned header: %v", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("decompressing the pinned header: %v", err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompressing the pinned header: %v", err)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != pinnedHeaderSHA256 {
		t.Fatalf("the pinned header's sha256 is %s, want %s: it is not the 1.26.0 release's header", got, pinnedHeaderSHA256)
	}
	return string(b)
}

var (
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineComment  = regexp.MustCompile(`//[^\n]*`)
)

func stripComments(s string) string {
	return lineComment.ReplaceAllString(blockComment.ReplaceAllString(s, ""), "")
}

// braceBody is the text between the '{' that ends opener and its matching '}'.
func braceBody(t *testing.T, src, opener string) string {
	t.Helper()
	i := strings.Index(src, opener)
	if i < 0 {
		t.Fatalf("the header has no %q", opener)
	}
	start := i + len(opener)
	depth := 1
	for j := start; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start:j]
			}
		}
	}
	t.Fatalf("%q is never closed", opener)
	return ""
}

var (
	declStatus  = regexp.MustCompile(`^ORT_API2?_STATUS\(\s*(\w+)`)
	declRelease = regexp.MustCompile(`^ORT_CLASS_RELEASE\(\s*(\w+)\s*\)`)
	declTyped   = regexp.MustCompile(`^ORT_API_T\(\s*[^,]+,\s*(\w+)`)
	declPointer = regexp.MustCompile(`\(\s*ORT_API_CALL\s*\*\s*(\w+)\s*\)`)
)

// ortAPIMembers lists struct OrtApi's members in declaration order, which is
// the order of the function table.
func ortAPIMembers(t *testing.T, header string) []string {
	t.Helper()
	body := stripComments(braceBody(t, header, "struct OrtApi {"))
	if strings.Contains(body, "#") {
		t.Fatal("struct OrtApi contains a preprocessor directive, so its member order depends on the build: positions cannot be derived")
	}
	// Members end at a ';' outside parentheses: a parameter list can hold a
	// function-pointer type of its own, but never a ';'.
	var decls []string
	depth, cur := 0, strings.Builder{}
	for _, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		}
		if r == ';' && depth == 0 {
			decls = append(decls, strings.Join(strings.Fields(cur.String()), " "))
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	if rest := strings.TrimSpace(cur.String()); rest != "" {
		t.Fatalf("text after the last member of struct OrtApi: %q", rest)
	}
	var names []string
	seen := map[string]bool{}
	for _, d := range decls {
		var name string
		switch {
		case declStatus.MatchString(d):
			name = declStatus.FindStringSubmatch(d)[1]
		case declRelease.MatchString(d):
			name = "Release" + declRelease.FindStringSubmatch(d)[1]
		case declTyped.MatchString(d):
			name = declTyped.FindStringSubmatch(d)[1]
		case declPointer.MatchString(d):
			name = declPointer.FindStringSubmatch(d)[1]
		default:
			t.Fatalf("a struct OrtApi member this parser cannot name: %.120s", d)
		}
		if seen[name] {
			t.Fatalf("struct OrtApi names %s twice", name)
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

func TestTheTableIndicesAreTheHeaders(t *testing.T) {
	members := ortAPIMembers(t, pinnedHeader(t))
	// The 1.26.0 table has 419 functions. A different count means a different
	// header, or a parser that has started miscounting.
	if len(members) != 419 {
		t.Fatalf("struct OrtApi has %d members, want 419", len(members))
	}
	if members[0] != "CreateStatus" || members[2] != "GetErrorMessage" || members[3] != "CreateEnv" {
		t.Fatalf("the table starts %v, want CreateStatus, GetErrorCode, GetErrorMessage, CreateEnv", members[:4])
	}
	position := map[string]int{}
	for i, m := range members {
		position[m] = i
	}
	for name, idx := range tableIndex {
		got, ok := position[name]
		switch {
		case !ok:
			t.Errorf("%s is not in struct OrtApi", name)
		case got != idx:
			t.Errorf("%s is at position %d in the header, and this package calls position %d", name, got, idx)
		}
	}
}

func TestTheAPIVersionIsTheHeaders(t *testing.T) {
	m := regexp.MustCompile(`(?m)^#define ORT_API_VERSION (\d+)$`).FindStringSubmatch(pinnedHeader(t))
	if m == nil {
		t.Fatal("the header defines no ORT_API_VERSION")
	}
	if v, _ := strconv.Atoi(m[1]); v != APIVersion {
		t.Fatalf("the header's ORT_API_VERSION is %d, and this package asks for %d", v, APIVersion)
	}
}

// enumValues evaluates every typedef'd enum in the header the way a C
// compiler numbers one: from zero, one more than the previous member, unless a
// member says otherwise with a number or with another member's name.
func enumValues(t *testing.T, header string) map[string]int {
	t.Helper()
	src := stripComments(header)
	values := map[string]int{}
	member := regexp.MustCompile(`^(\w+)\s*(?:=\s*(.+))?$`)
	for _, m := range regexp.MustCompile(`typedef enum \w+ \{`).FindAllStringIndex(src, -1) {
		body := braceBody(t, src[m[0]:], src[m[0]:m[1]])
		next := 0
		for _, item := range strings.Split(body, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			mm := member.FindStringSubmatch(item)
			if mm == nil {
				t.Fatalf("an enum member this parser cannot read: %q", item)
			}
			v := next
			if expr := strings.TrimSpace(mm[2]); expr != "" {
				if n, err := strconv.Atoi(expr); err == nil {
					v = n
				} else if prev, ok := values[expr]; ok {
					v = prev
				} else {
					// An expression this package never passes (a shift, a
					// cast): record nothing rather than a guess.
					next = 0
					continue
				}
			}
			values[mm[1]] = v
			next = v + 1
		}
	}
	return values
}

func TestTheEnumValuesAreTheHeaders(t *testing.T) {
	values := enumValues(t, pinnedHeader(t))
	for name, want := range enumValue {
		got, ok := values[name]
		switch {
		case !ok:
			t.Errorf("the header has no enum member %s", name)
		case got != want:
			t.Errorf("%s is %d in the header, and this package passes %d", name, got, want)
		}
	}
}

func TestElements(t *testing.T) {
	for _, c := range []struct {
		shape []int64
		want  int64
		ok    bool
	}{
		{[]int64{1, 7}, 7, true},
		{[]int64{1, 512, 384}, 196608, true},
		{[]int64{3, 0}, 0, true},
		{nil, 0, false},
		{[]int64{1, -1}, 0, false},
		{[]int64{1 << 20, 1 << 20}, 0, false},
	} {
		got, err := elements(c.shape)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("elements(%v) = %d, %v; want %d, ok=%v", c.shape, got, err, c.want, c.ok)
		}
	}
}

func TestGoStringReadsToTheTerminator(t *testing.T) {
	b := cString("model.onnx")
	if b[len(b)-1] != 0 {
		t.Fatal("cString did not terminate its result")
	}
	if got := goString(addr(&b[0])); got != "model.onnx" {
		t.Fatalf("goString = %q", got)
	}
	if goString(0) != "" {
		t.Fatal("goString(NULL) is not empty")
	}
	runtime.KeepAlive(b)
}

func TestInitNamesALibraryItCannotLoad(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-onnxruntime")
	err := Init(missing)
	if err == nil {
		Destroy()
		t.Fatal("Init succeeded on a library that does not exist")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("the error does not name the library it tried: %v", err)
	}
	if api != 0 || env != 0 {
		t.Fatal("a failed Init left state behind")
	}
}

// cachedLibrary is the ONNX Runtime the daemon's download-model puts in the
// user's model cache, or a skip.
func cachedLibrary(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	name := map[string]string{"windows": "onnxruntime.dll", "darwin": "libonnxruntime.dylib"}[runtime.GOOS]
	if name == "" {
		name = "libonnxruntime.so"
	}
	lib := filepath.Join(home, ".mochiii", "models", "onnxruntime-1.26.0", name)
	if _, err := os.Stat(lib); err != nil {
		t.Skipf("onnxruntime not cached: %v", err)
	}
	return lib
}

var initOnce sync.Once

func initialized(t *testing.T) {
	t.Helper()
	lib := cachedLibrary(t)
	var err error
	initOnce.Do(func() { err = Init(lib) })
	if err != nil {
		t.Fatalf("Init(%s): %v", lib, err)
	}
	if api == 0 {
		t.Skip("an earlier Init failed")
	}
}

func TestASecondInitIsRefused(t *testing.T) {
	initialized(t)
	if err := Init(cachedLibrary(t)); err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("a second Init returned %v", err)
	}
}

func TestASessionErrorCarriesONNXRuntimesMessage(t *testing.T) {
	initialized(t)
	bad := filepath.Join(t.TempDir(), "not-a-model.onnx")
	if err := os.WriteFile(bad, []byte("this is not a protobuf"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewSession(bad, []string{"input_ids"}, []string{"logits"}, 0)
	if err == nil {
		t.Fatal("a session opened on a file that is not a model")
	}
	// The status message is ONNX Runtime's own, read back through
	// GetErrorMessage: a wrong table position there would come back empty or
	// garbled, not as the library's sentence about the file.
	if !strings.Contains(err.Error(), bad) || len(err.Error()) <= len("opening "+bad+": ") {
		t.Fatalf("the error names neither the model nor ONNX Runtime's reason: %v", err)
	}
}

func TestRunRefusesInputsThatDoNotFitTheSession(t *testing.T) {
	initialized(t)
	home, _ := os.UserHomeDir()
	model := filepath.Join(home, ".mochiii", "models", "bge-small-en-v1.5-int8", "model_int8.onnx")
	if _, err := os.Stat(model); err != nil {
		t.Skipf("model not cached: %v", err)
	}
	s, err := NewSession(model, []string{"input_ids", "attention_mask", "token_type_ids"}, []string{"last_hidden_state"}, 0)
	if err != nil {
		t.Fatalf("opening the model: %v", err)
	}
	defer func() { _ = s.Destroy() }()

	ids := []int64{101, 7592, 102}
	ones := []int64{1, 1, 1}
	zeros := []int64{0, 0, 0}
	if _, err := s.Run([]int64{1, 3}, ids, ones); err == nil {
		t.Error("Run accepted two inputs for a three-input session")
	}
	if _, err := s.Run([]int64{1, 4}, ids, ones, zeros); err == nil {
		t.Error("Run accepted inputs shorter than their shape")
	}
	out, err := s.Run([]int64{1, 3}, ids, ones, zeros)
	if err != nil {
		t.Fatalf("a well-formed run failed: %v", err)
	}
	if len(out.Shape) != 3 || out.Shape[0] != 1 || out.Shape[1] != 3 || out.Shape[2] != 384 {
		t.Fatalf("output shape %v, want [1 3 384]", out.Shape)
	}
	if int64(len(out.Data)) != 3*384 {
		t.Fatalf("output has %d values, want %d", len(out.Data), 3*384)
	}
	nonzero := false
	for _, v := range out.Data {
		nonzero = nonzero || v != 0
	}
	if !nonzero {
		t.Fatal("the output is all zeros")
	}
}

// A failed session opens no state that a later good one trips over, and a
// closed session refuses to run instead of calling into freed memory.
func TestAClosedSessionRefusesToRun(t *testing.T) {
	initialized(t)
	s := &Session{}
	if _, err := s.Run([]int64{1, 1}, []int64{1}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Run on a closed session returned %v", err)
	}
}

// bgeSession opens the cached BGE model, or skips.
func bgeSession(t *testing.T) *Session {
	t.Helper()
	home, _ := os.UserHomeDir()
	model := filepath.Join(home, ".mochiii", "models", "bge-small-en-v1.5-int8", "model_int8.onnx")
	if _, err := os.Stat(model); err != nil {
		t.Skipf("model not cached: %v", err)
	}
	s, err := NewSession(model, []string{"input_ids", "attention_mask", "token_type_ids"}, []string{"last_hidden_state"}, 0)
	if err != nil {
		t.Fatalf("opening the model: %v", err)
	}
	return s
}

// THE ORDER THAT CRASHED THE HELPER (2026-10-19). Its shutdown destroyed the
// environment and THEN closed the session, which called ReleaseSession through
// a function table that was already gone: SIGSEGV, on 2 of 10 stops in one
// retrieval-eval run. Both orders must now be safe, and a session whose
// environment is gone must refuse to run rather than call into it.
func TestTeardownInEitherOrderIsSafe(t *testing.T) {
	initialized(t)
	lib := cachedLibrary(t)
	s := bgeSession(t)

	Destroy()
	if err := s.Destroy(); err != nil {
		t.Fatalf("closing a session after its environment: %v", err)
	}
	if _, err := s.Run([]int64{1, 1}, []int64{101}, []int64{1}, []int64{0}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Run after teardown returned %v, want the session refused as closed", err)
	}
	Destroy() // twice is nothing

	if err := Init(lib); err != nil {
		t.Fatalf("re-initializing after teardown: %v", err)
	}
	s = bgeSession(t)
	if err := s.Destroy(); err != nil {
		t.Fatal(err)
	}
	if err := s.Destroy(); err != nil {
		t.Fatalf("closing a session twice: %v", err)
	}
}

// Teardown waits for an inference in flight instead of releasing the session
// under it.
func TestTeardownWaitsForARunInFlight(t *testing.T) {
	initialized(t)
	lib := cachedLibrary(t)
	s := bgeSession(t)

	const n = 256
	ids, mask, types := make([]int64, n), make([]int64, n), make([]int64, n)
	for i := range ids {
		ids[i] = int64(1000 + i)
		mask[i] = 1
	}
	errs := make(chan error, 8)
	for g := 0; g < cap(errs); g++ {
		go func() {
			_, err := s.Run([]int64{1, n}, ids, mask, types)
			errs <- err
		}()
	}
	Destroy()
	for g := 0; g < cap(errs); g++ {
		if err := <-errs; err != nil && !strings.Contains(err.Error(), "closed") {
			t.Errorf("a run during teardown failed with something other than a closed session: %v", err)
		}
	}
	_ = s.Destroy()
	if err := Init(lib); err != nil {
		t.Fatalf("re-initializing after teardown: %v", err)
	}
}
