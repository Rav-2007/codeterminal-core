package main

import (
	"bytes"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"mochiii/editapply"
)

// The terminal is a sink for model output, and `edits apply` writes to it
// directly.
//
// applyEditBlocks prints block.FilePath with %s to os.Stdout BEFORE PrepareEdit
// runs (daemon/apply_cmd.go), and prints it a second time inside PrepareEdit's
// own error. Both strings come from a model response. The only thing standing
// between them and a raw terminal is editapply.RejectUnprintablePath, which the
// parser runs while building the block -- so this test is the consequence half
// of that gate, asserted where the bytes actually leave the process.
//
// It drives the REAL chain: ParseEditPayload, exactly as runEditsApplyCommand
// calls it, then applyEditBlocks with a buffer standing in for os.Stdout. A test
// that handed applyEditBlocks a synthetic EditBlock would prove nothing about
// production, because in production nothing constructs one without the parser.
func TestEditsApply_NeverWritesAControlCharacterToTheTerminal(t *testing.T) {
	// The C1 codes a terminal in 8-bit mode acts on, plus a C0 control for the
	// contrast: C0 was always refused, C1 was not, and both must stay refused.
	for name, r := range map[string]rune{
		"C0 ESC": 0x1b,
		"C1 NEL": 0x85,
		"C1 CSI": 0x9b,
		"C1 OSC": 0x9d,
		"C1 APC": 0x9f,
	} {
		t.Run(name, func(t *testing.T) {
			ws := t.TempDir()
			if err := os.WriteFile(filepath.Join(ws, "target.txt"), []byte("ORIGINAL\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			root, err := editapply.ResolveRealWorkspaceRoot(ws)
			if err != nil {
				t.Fatal(err)
			}

			hostile := "tar" + string(r) + "get.txt"
			resp := strings.Join([]string{
				"Here is the change:",
				"",
				"path: " + hostile,
				"<<<<<<< SEARCH",
				"ORIGINAL",
				"=======",
				"REPLACED",
				">>>>>>> REPLACE",
			}, "\n")

			payload := editapply.ParseEditPayload(resp)
			var out bytes.Buffer
			// "n" declines every confirmation, so nothing is written to disk and
			// the test is about the OUTPUT only.
			_ = applyEditBlocks(root, payload.Blocks, payload.Rejected,
				strings.NewReader("n\n"), &out, log.New(io.Discard, "", 0))

			for i, c := range out.String() {
				if unicode.IsControl(c) && c != '\n' && c != '\t' {
					t.Fatalf("`edits apply` wrote U+%04X to its terminal output at byte %d\n"+
						"full output: %q", c, i, out.String())
				}
			}

			// The vacuity floor. If the parser ever stops producing anything for
			// this payload -- a format change, a rename, a refusal that also
			// silences the report -- the loop above passes over an empty buffer
			// and proves nothing. Something must have been printed.
			if out.Len() == 0 {
				t.Fatalf("applyEditBlocks wrote nothing; this test cannot have checked what it claims "+
					"(blocks=%d rejected=%d)", len(payload.Blocks), len(payload.Rejected))
			}
		})
	}
}
