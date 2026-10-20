package helper

import "mochiii/helper/internal/ort"

// Version is this helper's release version. The program that runs Main sets
// it from its own stamp: `mochiii` passes the version release.yml stamped into
// it, and cmd/mochiii-embedder-helper takes -X main.version. "dev" means a
// local build.
var Version = "dev"

// CodeSearchBuilt is whether this build can run code search at all. It is
// false only in the static build for musl Linux, `-tags nosearch`, which
// leaves ONNX Runtime's loader out (internal/ort/load_nosearch.go).
const CodeSearchBuilt = ort.Built
