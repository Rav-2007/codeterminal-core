//go:build !windows

package ort

import (
	"fmt"
	"strings"

	"github.com/ebitengine/purego"
)

// library is a dlopen handle.
type library struct{ handle uintptr }

// openLibrary dlopens path the way onnxruntime_go did (RTLD_LAZY) and finds
// OrtGetApiBase in it.
func openLibrary(path string) (library, uintptr, error) {
	h, err := purego.Dlopen(path, purego.RTLD_LAZY)
	if err != nil {
		return library{}, 0, fmt.Errorf("loading the ONNX Runtime library %s: %w", path, err)
	}
	sym, err := purego.Dlsym(h, "OrtGetApiBase")
	if err != nil {
		_ = purego.Dlclose(h)
		return library{}, 0, fmt.Errorf("looking up OrtGetApiBase in %s: %w", path, err)
	}
	return library{handle: h}, sym, nil
}

func (l library) close() {
	if l.handle != 0 {
		_ = purego.Dlclose(l.handle)
	}
}

func syscallN(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := purego.SyscallN(fn, args...)
	return r
}

// cPath is a model path in the form CreateSession takes here: UTF-8 bytes,
// NUL-terminated (ORTCHAR_T is char off Windows).
type cPath []byte

func osPath(p string) (cPath, error) {
	if strings.IndexByte(p, 0) >= 0 {
		return nil, fmt.Errorf("model path %q contains a NUL byte", p)
	}
	return cPath(cString(p)), nil
}
