package main

import (
	"os"
	"regexp"
	"testing"
)

// WHY THIS IS A SOURCE CHECK AND NOT A BEHAVIOUR ONE.
//
// The bug is that the undo report printed `docs\notes.md` on Windows for a file
// the model's edit block spelled `docs/notes.md` -- two spellings of one file
// inside the one workflow whose purpose is letting a user check that undo
// reverted what apply did. TestEndToEnd_EditAndCreateApplyThenUndo is the
// regression test for it: it drives the real flow and asserts the real string,
// and it is what failed on the first Windows CI run.
//
// A unit test of displayRel would add nothing. filepath.ToSlash is the IDENTITY
// on POSIX, so any such test passes on Linux with displayRel neutered to
// `return rel` -- it would be measuring the platform, not the code, which is
// the trap this session already fell into once in editapply/fileerror_test.go.
//
// What CAN be checked on every platform is the thing the end-to-end test cannot
// see: that no NEW user-facing site is added without it. That is the real
// failure mode here -- one Fprintf out of thirteen left unconverted reintroduces
// exactly half the inconsistency, and on Linux nothing would ever say so.
func TestUndoOutputAlwaysGoesThroughDisplayRel(t *testing.T) {
	src, err := os.ReadFile("apply_cmd.go")
	if err != nil {
		t.Fatal(err)
	}

	// A user-facing print or refusal whose final argument is a bare rel/f.rel/
	// s.rel rather than displayRel(...). The report and every refusal a user
	// reads is built this way.
	bare := regexp.MustCompile(`fmt\.(Fprintf|Fprintln|Errorf)\([^)\n]*?, ((?:f\.|s\.)?rel)\)`)

	for _, m := range bare.FindAllStringSubmatch(string(src), -1) {
		t.Errorf("apply_cmd.go prints %s directly:\n\t%s\n"+
			"a workspace-relative path reaching a user must go through displayRel, or it is "+
			"spelled with backslashes on Windows while the apply run that made the backup "+
			"spelled the same file with forward slashes", m[2], m[0])
	}
}
