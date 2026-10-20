package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
)

// runHelperSmoketestCommand implements
// `mochiii-daemon helper-smoketest [--helper-bin path] [text...]`: a
// one-shot manual check. It starts the helper (requires `download-model` to
// have already fetched the model + onnxruntime lib), waits for it to become
// healthy, sends exactly one real embedding request, prints the resulting
// vector's length and first few values, and shuts the helper down cleanly.
func runHelperSmoketestCommand(args []string, logger *log.Logger) error {
	fset := flag.NewFlagSet("helper-smoketest", flag.ExitOnError)
	helperBin := fset.String("helper-bin", "", "a separate helper program to run (default: this binary's own helper)")
	fset.Parse(args)

	text := "hello from the mochiii daemon"
	if fset.NArg() > 0 {
		text = strings.Join(fset.Args(), " ")
	}

	modelDir, onnxRuntimeLib, err := resolveModelPaths()
	if err != nil {
		return err
	}

	bin, roleArgs := *helperBin, []string(nil)
	if bin == "" {
		if bin, roleArgs, err = resolveHelperProgram(); err != nil {
			return err
		}
	}
	h := NewHelperProcess(bin, modelDir, onnxRuntimeLib, logger)
	h.roleArgs = roleArgs
	logger.Printf("helper-smoketest: starting helper %s %s", bin, strings.Join(roleArgs, " "))
	if err := h.Start(); err != nil {
		return fmt.Errorf("starting embedder helper: %w", err)
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
