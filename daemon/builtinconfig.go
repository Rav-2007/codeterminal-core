package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"os"
)

// THE MODELS.JSON BUILT INTO THIS BINARY (1.2.0). An install is one file, so
// the configuration it starts with has to be inside it: resolveConfigPath
// returns "" when no models.json sits beside the binary, and LoadConfig then
// reads this. A models.json beside the binary still wins -- the packaged VS
// Code extension ships one -- and --config wins over both.
//
// It is the repository's models.agent.json, the agent-mode configuration the
// terminal has always run with (run-tui.sh, and every 1.1.0 install archive).
// Stored gzipped so the repository's search corpus -- which this repository's
// retrieval eval indexes -- holds one copy of that file, not two.
// TestTheBuiltInConfigIsTheShippedOne fails when the two differ; `make
// builtin-config` regenerates this one from models.agent.json.
//
//go:embed builtinconfig/models.agent.json.gz
var builtinConfigGz []byte

// builtinConfigName names the built-in models.json in messages.
const builtinConfigName = "the models.json built into this program"

// readConfig reads the models.json at path, or the built-in one when path is
// "" (resolveConfigPath found none). It returns the name to report it by.
func readConfig(path string) ([]byte, string, error) {
	if path != "" {
		data, err := os.ReadFile(path)
		return data, path, err
	}
	data, err := builtinConfig()
	return data, builtinConfigName, err
}

// builtinConfig is the built-in models.json.
func builtinConfig() ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(builtinConfigGz))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(zr)
}
