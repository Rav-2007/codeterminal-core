package editapply

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Path-confinement conformance suite — editapply half.
//
// `editapply` and `daemon` each carry their OWN path-confinement code, and that
// duplication is deliberate: editapply must not depend on daemon, and the two
// validate paths from different threat models (editapply gates a model-proposed
// edit; daemon's confinedRestorePath gates an undo/restore destination).
//
// Deliberate duplication is fine. SILENT DIVERGENCE is not — a vector closed in
// one and left open in the other is exactly the shape of S1 (nested .gitignore)
// and S2 (case-fold .GIT), both of which were found in one implementation and
// then had to be chased into the other.
//
// So: this table is MIRRORED VERBATIM in daemon/confinement_conformance_test.go.
// Editing one without the other is the bug this file exists to prevent. The two
// suites assert the same MUST-REJECT set; where the implementations legitimately
// differ, the difference is named at the bottom of each file rather than left to
// be rediscovered.

// confinementVector is one attacker-shaped relative path.
type confinementVector struct {
	name string
	// rel is the path as it would arrive from the wire.
	rel string
	// setup optionally plants files/symlinks under root before the check.
	setup func(t *testing.T, root string)
}

// mustRejectVectors are paths NEITHER implementation may ever resolve to a
// usable location inside the workspace. Mirrored in daemon.
func mustRejectVectors() []confinementVector {
	return []confinementVector{
		{
			name: "absolute path",
			rel:  "/etc/passwd",
		},
		{
			name: "bare dotdot",
			rel:  "..",
		},
		{
			name: "dotdot escape",
			rel:  "../outside.txt",
		},
		{
			name: "dotdot escape mid-path",
			rel:  "sub/../../outside.txt",
		},
		{
			name: "deep dotdot escape",
			rel:  "a/b/c/../../../../outside.txt",
		},
		{
			name: "symlinked leaf pointing outside",
			rel:  "link.txt",
			setup: func(t *testing.T, root string) {
				outside := filepath.Join(filepath.Dir(root), "outside.txt")
				if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
					t.Fatalf("planting outside file: %v", err)
				}
				if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
					t.Fatalf("planting leaf symlink: %v", err)
				}
			},
		},
		{
			name: "symlinked directory pointing outside",
			rel:  "linkdir/file.txt",
			setup: func(t *testing.T, root string) {
				outside := filepath.Join(filepath.Dir(root), "outsidedir")
				if err := os.MkdirAll(outside, 0700); err != nil {
					t.Fatalf("planting outside dir: %v", err)
				}
				if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
					t.Fatalf("planting dir symlink: %v", err)
				}
			},
		},
		{
			name: "symlinked ancestor two levels up",
			rel:  "linkdir/nested/file.txt",
			setup: func(t *testing.T, root string) {
				outside := filepath.Join(filepath.Dir(root), "outsidedir2", "nested")
				if err := os.MkdirAll(outside, 0700); err != nil {
					t.Fatalf("planting outside dir: %v", err)
				}
				if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "linkdir")); err != nil {
					t.Fatalf("planting dir symlink: %v", err)
				}
			},
		},
	}
}

// newConformanceRoot returns a workspace root with a sibling directory
// available for "outside" targets, both EvalSymlinks-resolved so the assertions
// are not defeated by /tmp itself being a symlink (it is, on macOS).
func newConformanceRoot(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}
	root := filepath.Join(base, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatalf("creating workspace root: %v", err)
	}
	return root
}

// TestConfinementConformance_EditapplyRejectsEveryEscapeVector runs the shared
// table against editapply's resolveSafeTarget.
func TestConfinementConformance_EditapplyRejectsEveryEscapeVector(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink vectors are POSIX-shaped")
	}
	for _, v := range mustRejectVectors() {
		t.Run(v.name, func(t *testing.T) {
			root := newConformanceRoot(t)
			if v.setup != nil {
				v.setup(t, root)
			}
			got, _, err := resolveSafeTarget(root, v.rel)
			if err != nil {
				return // refused, which is the requirement
			}
			rel, rerr := filepath.Rel(root, got)
			if rerr != nil || rel == ".." || filepath.IsAbs(rel) ||
				len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) {
				t.Errorf("resolveSafeTarget(%q) = %q, which is OUTSIDE the workspace root %q",
					v.rel, got, root)
			}
		})
	}
}

// TestConfinementConformance_EditapplyAllowsOrdinaryPaths is the other half: a
// suite that only asserts refusals passes trivially if the resolver refuses
// everything, which would be a total outage rather than a security property.
func TestConfinementConformance_EditapplyAllowsOrdinaryPaths(t *testing.T) {
	root := newConformanceRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "src"), 0700); err != nil {
		t.Fatalf("creating src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main"), 0600); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	for _, rel := range []string{"src/main.go", "src/new.go", "brand/new/file.go"} {
		if _, _, err := resolveSafeTarget(root, rel); err != nil {
			t.Errorf("resolveSafeTarget(%q) refused an ordinary in-workspace path: %v", rel, err)
		}
	}
}

// TestConfinementConformance_DocumentedAsymmetry is the mirror of daemon's test
// of the same name, asserting the OTHER side of the same split.
//
// editapply gates a MODEL-PROPOSED edit, so on top of confinement it refuses
// protected directories (.git and friends) and secret-named files: the model
// must not be able to write a git hook or a credentials file, whether or not
// that file is inside the workspace.
//
// daemon's confinedRestorePath deliberately does NOT apply these, because it
// resolves an UNDO destination and every path it sees was already written by an
// apply that passed these very gates.
//
// If this fails, one of the two changed. That is not automatically wrong -- but
// it must be a decision, not a drift.
func TestConfinementConformance_DocumentedAsymmetry(t *testing.T) {
	root := newConformanceRoot(t)
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0700); err != nil {
		t.Fatalf("creating .git/hooks: %v", err)
	}

	if _, _, err := resolveSafeTarget(root, ".git/hooks/pre-commit"); err == nil {
		t.Error("resolveSafeTarget ALLOWED a path inside .git -- a model could write a git hook. " +
			"This is the gate editapply has and daemon deliberately does not; " +
			"losing it here is a real regression, not a divergence to document.")
	}
	if _, _, err := resolveSafeTarget(root, "id_rsa"); err == nil {
		t.Error("resolveSafeTarget ALLOWED a secret-named file. Same note as above.")
	}
	// Case-folded .GIT must be refused too -- this is S2, found once and fixed
	// in both the indexer and the writer.
	if _, _, err := resolveSafeTarget(root, ".GIT/hooks/pre-commit"); err == nil {
		t.Error("resolveSafeTarget ALLOWED case-folded .GIT (regression of S2)")
	}
}
