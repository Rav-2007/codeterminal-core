//go:build !windows && nosearch

package ort

import (
	"errors"
	"fmt"
	"strings"
)

// THE BUILD WITH NO CODE SEARCH (1.2.0): `-tags nosearch`, for Linux on musl
// (Alpine). Loading ONNX Runtime through purego makes a binary that needs
// glibc's dynamic loader, and a musl system has none, so a binary that can load
// it cannot even start there. This build leaves purego out entirely, which
// makes the binary fully static; it starts anywhere, and every attempt to load
// ONNX Runtime fails with ErrNotBuilt. Chat and agent mode do not need it.

// Built is false: see built.go.
const Built = false

// ErrNotBuilt is why this build loads no ONNX Runtime.
var ErrNotBuilt = errors.New("this build of Mochiii has no code search: it is the static build for musl Linux (Alpine), which cannot load ONNX Runtime")

type library struct{}

func openLibrary(path string) (library, uintptr, error) {
	return library{}, 0, ErrNotBuilt
}

func (library) close() {}

// syscallN is never reached: Init fails before any call.
func syscallN(fn uintptr, args ...uintptr) uintptr {
	panic(fmt.Sprintf("ort: call to %#x in a build without ONNX Runtime", fn))
}

type cPath []byte

func osPath(p string) (cPath, error) {
	if strings.IndexByte(p, 0) >= 0 {
		return nil, fmt.Errorf("model path %q contains a NUL byte", p)
	}
	return cPath(cString(p)), nil
}
