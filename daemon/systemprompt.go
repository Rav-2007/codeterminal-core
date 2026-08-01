package main

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
)

// The system prompt is a BUILD ARTIFACT, not configuration. It is versioned
// with the code that depends on its wording, it is not something an operator is
// expected to hand-edit per deployment, and shipping it as a loose file next to
// a binary is how it goes missing.
//
// It used to be read from `daemon/prompts/system.txt`, a CWD-relative default,
// which meant the daemon could only be started from the repo root. That is the
// same defect resolveHelperBinPath documents (Fix 8) and it is fatal for a
// packaged install: an extension that bundles the binary has no repo root, and
// a user launching from anywhere but one specific directory got
// `reading system prompt daemon/prompts/system.txt: no such file or directory`.
//
//go:embed prompts/system.txt
var defaultSystemPrompt string

// resolveSystemPrompt returns the prompt to run with.
//
// An explicit --system-prompt path is honoured as given and is an error if it
// cannot be read: someone who named a file meant it, and silently falling back
// to the embedded copy would run a prompt they did not choose. Empty (the
// default) uses the embedded copy, which always exists.
func resolveSystemPrompt(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return defaultSystemPrompt, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading system prompt %s: %w", path, err)
	}
	return string(b), nil
}
