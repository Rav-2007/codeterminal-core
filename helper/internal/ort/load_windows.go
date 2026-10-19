//go:build windows

package ort

import (
	"fmt"
	"syscall"
)

// library is a LoadLibrary handle.
type library struct{ handle syscall.Handle }

// openLibrary loads path the way onnxruntime_go did (syscall.LoadLibrary) and
// finds OrtGetApiBase in it. Windows needs no purego: the standard library
// calls a function pointer with syscall.SyscallN.
func openLibrary(path string) (library, uintptr, error) {
	h, err := syscall.LoadLibrary(path)
	if err != nil {
		return library{}, 0, fmt.Errorf("loading the ONNX Runtime library %s: %w", path, err)
	}
	sym, err := syscall.GetProcAddress(h, "OrtGetApiBase")
	if err != nil {
		_ = syscall.FreeLibrary(h)
		return library{}, 0, fmt.Errorf("looking up OrtGetApiBase in %s: %w", path, err)
	}
	return library{handle: h}, sym, nil
}

func (l library) close() {
	if l.handle != 0 {
		_ = syscall.FreeLibrary(l.handle)
	}
}

func syscallN(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, args...)
	return r
}

// cPath is a model path in the form CreateSession takes on Windows: UTF-16,
// NUL-terminated (ORTCHAR_T is wchar_t there).
type cPath []uint16

func osPath(p string) (cPath, error) {
	u, err := syscall.UTF16FromString(p)
	if err != nil {
		return nil, fmt.Errorf("model path %q: %w", p, err)
	}
	return cPath(u), nil
}
