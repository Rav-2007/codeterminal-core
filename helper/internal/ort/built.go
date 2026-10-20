//go:build !nosearch

package ort

// Built is whether this build can load ONNX Runtime at all: false only in the
// `-tags nosearch` build (load_nosearch.go).
const Built = true
