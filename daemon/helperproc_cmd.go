package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
)

// defaultHelperBinPath mirrors the --system-prompt flag's convention
// elsewhere in this file: a path relative to the repo root, since that's
// where this binary is normally run from during development.
const defaultHelperBinPath = "helper/codeterminal-embedder-helper"

// runHelperSmoketestCommand implements
// `codeterminal-daemon helper-smoketest [--helper-bin path] [text...]`: a
// one-shot manual check. It starts the helper (requires `download-model` to
// have already fetched the model + onnxruntime lib), waits for it to become
// healthy, sends exactly one real embedding request, prints the resulting
// vector's length and first few values, and shuts the helper down cleanly.
func runHelperSmoketestCommand(args []string, logger *log.Logger) error {
	fset := flag.NewFlagSet("helper-smoketest", flag.ExitOnError)
	helperBin := fset.String("helper-bin", defaultHelperBinPath, "path to the codeterminal-embedder-helper binary")
	fset.Parse(args)

	text := "hello from the codeterminal daemon"
	if fset.NArg() > 0 {
		text = strings.Join(fset.Args(), " ")
	}

	modelDir, onnxRuntimeLib, err := resolveModelPaths()
	if err != nil {
		return err
	}

	h := NewHelperProcess(*helperBin, modelDir, onnxRuntimeLib, logger)
	logger.Printf("helper-smoketest: starting helper %s", *helperBin)
	if err := h.Start(); err != nil {
		return fmt.Errorf("starting embedder helper (build it first with: cd helper && go build -o codeterminal-embedder-helper .): %w", err)
	}

	vecs, embedErr := h.Embed(context.Background(), []string{text})

	logger.Print("helper-smoketest: shutting down helper")
	if stopErr := h.Stop(); stopErr != nil {
		logger.Printf("helper-smoketest: error stopping helper: %v", stopErr)
	}

	if embedErr != nil {
		return fmt.Errorf("embedding test string: %w", embedErr)
	}
	if len(vecs) != 1 {
		return fmt.Errorf("expected 1 vector back, got %d", len(vecs))
	}

	vec := vecs[0]
	preview := vec
	if len(preview) > 5 {
		preview = preview[:5]
	}
	logger.Printf("helper-smoketest: text=%q vector_length=%d first_values=%v", text, len(vec), preview)
	return nil
}
