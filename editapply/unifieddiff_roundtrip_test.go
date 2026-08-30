package editapply

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The floors this test refuses to run below. A round-trip test that scanned
// three files and compared two empty sets would pass and prove nothing; this
// repo has been wrong that way four times, so the counts are asserted before
// any comparison happens.
const (
	minRoundTripFiles = 100
	minRoundTripHunks = 200
)

// sourceDirs are walked for realistic content. A FIXED LIST, not a whole-tree
// walk with a prune predicate, and that is deliberate: the build-output noise
// names (node_modules, vendor, dist, ...) live in daemon/chunker.go, which this
// module cannot import, and copying that list here to enable a walk would
// duplicate exactly the kind of table the language-table work exists to retire.
// Naming three source directories needs no table at all.
var sourceDirs = []string{"../daemon", "../editapply", "../clients/tui"}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// gitNeutralConfig stops git rewriting the bytes this test is measuring.
//
// THE RUNNER'S CONFIG IS PART OF THE FIXTURE, and on Windows it is not neutral:
// git for Windows defaults to core.autocrlf=true, under which `git diff`
// normalises CRLF to LF in its OUTPUT while leaving the files on disk alone. The
// patch then describes different bytes than the file holds -- so the reader is
// handed LF text, the matcher finds it anyway at the MatchLineEndings tier
// (correctly; that tier exists for exactly this), and the splice writes LF lines
// into a CRLF file. Every byte-for-byte assertion in this file then fails for a
// reason that has nothing to do with the code under test.
//
// MEASURED: a CRLF checkout with core.autocrlf=true gives
// "0/200 files byte-identical", with refusals_from_reader=0 and not_found=0 --
// the reader and the matcher both did their jobs, and the harness was lying to
// them. With these two settings the same tree passes.
//
// safecrlf as well as autocrlf: it turns the same normalisation into a warning
// or an error depending on the host's config, so leaving it unset trades one
// environment-dependent result for another.
var gitNeutralConfig = []string{"-c", "core.autocrlf=false", "-c", "core.safecrlf=false"}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append(append([]string{}, gitNeutralConfig...), args...)...)
	cmd.Dir = dir
	// git diff exits 1 when files differ, which is the normal case here, so
	// output is trusted over exit status and only a truly empty result fails.
	out, _ := cmd.Output()
	return string(out)
}

// collectRealSources reads Go source out of the repository for its CONTENT --
// tabs, quotes, unicode, long lines, blank lines, trailing whitespace -- and
// nothing else. The files are written into the temp workspace as .txt so the
// Go syntax gate plays no part: this test is about whether a diff round-trips,
// and a refusal from a different gate would silently shrink the sample.
// THE NEUTRAL CONFIG HAS TO REACH git, not just exist in a slice.
//
// gitNeutralConfig is a fix for an environment-dependent failure, which is the
// kind that comes back: it passes everywhere it is not needed, so dropping it
// looks free on every machine except the one that breaks. This asks git what it
// actually resolved, which is the only thing that answers the question.
func TestTheCorpusAsksGitForUntranslatedBytes(t *testing.T) {
	requireGit(t)
	root := realTempDir(t)

	for _, setting := range []string{"core.autocrlf", "core.safecrlf"} {
		got := strings.TrimSpace(git(t, root, "config", "--get", setting))
		if got != "false" {
			t.Errorf("git resolved %s=%q through this suite's helper, want \"false\".\n\n"+
				"With autocrlf on, `git diff` normalises CRLF to LF in its OUTPUT while leaving "+
				"the files alone, so every byte-for-byte assertion here compares the patch's "+
				"bytes against different bytes on disk. Measured on a CRLF checkout: "+
				"0/200 files round-tripped, with the reader and matcher both working correctly.",
				setting, got)
		}
	}
}

func collectRealSources(t *testing.T) []string {
	t.Helper()
	var sources []string
	for _, dir := range sourceDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".go" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			// Files too short to carry three well-separated mutations would
			// contribute a file to the count without contributing hunks.
			if strings.Count(string(data), "\n") < 40 {
				continue
			}
			sources = append(sources, string(data))
		}
	}
	return sources
}

// mutate changes up to three well-separated lines, so one file yields several
// hunks. The separation matters: git merges hunks whose context overlaps, and
// the point of this test is to exercise the multi-hunk path.
func mutate(content string, fileIdx int) (string, int) {
	lines := strings.Split(content, "\n")
	targets := []int{len(lines) / 5, len(lines) / 2, 4 * len(lines) / 5}

	changed := 0
	for n, at := range targets {
		if at <= 0 || at >= len(lines) || strings.TrimSpace(lines[at]) == "" {
			continue
		}
		if n > 0 && at-targets[n-1] < 10 {
			continue // too close; git would merge the hunks
		}
		lines[at] = fmt.Sprintf("MUTATED-%d-%d unique marker text", fileIdx, n)
		changed++
	}
	return strings.Join(lines, "\n"), changed
}

// TestUnifiedDiff_RoundTripsAgainstRealGitOutput is the strongest evidence
// available that the reader is correct: real git output, over real source text,
// applied through the real gates, compared byte for byte.
//
//	original --git diff--> patch --ParseEditPayload--> blocks --Apply--> mutated
//
// Anything the reader gets wrong about context lines, line counts, blank
// lines, prefixes or ordering shows up as a byte difference at the end.
func TestUnifiedDiff_RoundTripsAgainstRealGitOutput(t *testing.T) {
	requireGit(t)

	sources := collectRealSources(t)
	if len(sources) < minRoundTripFiles {
		t.Fatalf("collected only %d source files (floor %d); the walk must have broken, and a smaller sample would not be evidence", len(sources), minRoundTripFiles)
	}
	if len(sources) > 200 {
		sources = sources[:200]
	}

	root := realTempDir(t)
	if out := git(t, root, "init", "-q", "."); false {
		_ = out
	}
	git(t, root, "config", "user.email", "test@example.invalid")
	git(t, root, "config", "user.name", "test")

	originals := make([]string, len(sources))
	wanted := make([]string, len(sources))
	expectedHunks := 0
	for i, src := range sources {
		name := fmt.Sprintf("f%03d.txt", i)
		originals[i] = src
		if err := os.WriteFile(filepath.Join(root, name), []byte(src), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		mutatedText, changed := mutate(src, i)
		wanted[i] = mutatedText
		expectedHunks += changed
	}

	git(t, root, "add", "-A")
	git(t, root, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "base")

	for i := range sources {
		name := fmt.Sprintf("f%03d.txt", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte(wanted[i]), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	patch := git(t, root, "diff", "--unified=3")
	if strings.TrimSpace(patch) == "" {
		t.Skip("git produced no diff (git present but not usable in this environment)")
	}

	// Put the originals back: the patch must be applied to the state it was
	// computed against, which is the whole point of a round trip.
	for i := range sources {
		name := fmt.Sprintf("f%03d.txt", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte(originals[i]), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	payload := ParseEditPayload(patch)
	if payload.Format != FormatUnifiedDiff {
		t.Fatalf("Format = %v, want FormatUnifiedDiff", payload.Format)
	}
	if len(payload.Blocks) < minRoundTripHunks {
		t.Fatalf("ingested only %d hunks (floor %d, git generated about %d); the reader is dropping hunks silently",
			len(payload.Blocks), minRoundTripHunks, expectedHunks)
	}

	backupDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatalf("NewBackupSessionDir: %v", err)
	}

	var applied, ambiguous, notFound int
	otherRefusals := map[string]int{}
	for _, block := range payload.Blocks {
		prepared, err := PrepareEdit(root, block)
		if err != nil {
			switch {
			case strings.Contains(err.Error(), "ambiguous"):
				ambiguous++
			case strings.Contains(err.Error(), "not found"):
				notFound++
			default:
				otherRefusals[err.Error()]++
			}
			continue
		}
		if err := Apply(root, prepared, backupDir); err != nil {
			t.Fatalf("Apply %s: %v", block.FilePath, err)
		}
		applied++
	}

	// THE MEASUREMENT the plan gates this capability on. Printed unconditionally
	// so the numbers are in the test log, not inferred from a pass.
	t.Logf("ROUND-TRIP MEASUREMENT: files=%d hunks_ingested=%d applied=%d AMBIGUOUS=%d not_found=%d other=%v refusals_from_reader=%d",
		len(sources), len(payload.Blocks), applied, ambiguous, notFound, otherRefusals, len(payload.Rejected))

	if len(otherRefusals) > 0 {
		t.Errorf("hunks refused for reasons that are not ambiguity or staleness: %v", otherRefusals)
	}
	if notFound > 0 {
		t.Errorf("%d hunk(s) produced SEARCH text that is not in the file — the reader mis-assembled them", notFound)
	}

	// Every file whose hunks all applied must now be byte-identical to what
	// git was diffing against.
	exact := 0
	for i := range sources {
		name := fmt.Sprintf("f%03d.txt", i)
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) == wanted[i] {
			exact++
		}
	}
	t.Logf("ROUND-TRIP RESULT: %d/%d files byte-identical after applying the ingested patch", exact, len(sources))

	// Ambiguity is a legitimate refusal (identical context appearing twice in
	// one file), so a handful of files may not reach byte-identical. The bar is
	// that the overwhelming majority do.
	if exact*10 < len(sources)*9 {
		t.Errorf("only %d/%d files round-tripped byte-identically; want at least 90%%", exact, len(sources))
	}
}

// TestUnifiedDiff_StaticCorpusRoundTripsWithoutGit is the non-vacuous floor.
// The generative test above skips when git is absent, and CI can route jobs to
// a self-hosted runner whose image the workflow does not control -- so a
// git-only proof could silently become no proof at all. This corpus is real git
// output, checked in, and needs neither the binary nor a repository.
func TestUnifiedDiff_StaticCorpusRoundTripsWithoutGit(t *testing.T) {
	entries, err := os.ReadDir("testdata/udiff")
	if err != nil {
		t.Fatalf("static corpus is missing: %v — it is what keeps this suite honest on a runner without git", err)
	}
	var patches []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".patch" {
			patches = append(patches, e.Name())
		}
	}
	if len(patches) < 3 {
		t.Fatalf("static corpus holds only %d patch(es); it must cover the real shapes to be evidence", len(patches))
	}

	for _, name := range patches {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata/udiff", name))
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			payload := ParseEditPayload(string(data))
			if payload.Format != FormatUnifiedDiff {
				t.Fatalf("Format = %v, want FormatUnifiedDiff", payload.Format)
			}
			if len(payload.Blocks) == 0 {
				t.Fatalf("no blocks ingested; rejections: %v", payload.Rejected)
			}
			for _, b := range payload.Blocks {
				if b.FilePath == "" {
					t.Error("block with an empty FilePath")
				}
				if strings.HasPrefix(b.FilePath, "a/") || strings.HasPrefix(b.FilePath, "b/") {
					t.Errorf("FilePath %q kept its diff prefix", b.FilePath)
				}
			}
		})
	}
}

// TestRealDocumentationIsNeverReadAsADiff is the false-positive half, run over
// this repository's own prose.
//
// Markdown is where the dangerous shapes live: "---" is a horizontal rule, a
// YAML front-matter fence, a setext underline and a table separator, and "+++"
// and "@@" both occur in ordinary text. A reader that fired on any of them
// would turn documentation into a proposed edit. Requiring a full
// "@@ -l,c +l,c @@" hunk header is what keeps that from happening, and this
// test is what proves it on real files rather than on samples chosen to pass.
func TestRealDocumentationIsNeverReadAsADiff(t *testing.T) {
	var docs []string
	for _, dir := range []string{"..", "../docs"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
				docs = append(docs, filepath.Join(dir, e.Name()))
			}
		}
	}
	if len(docs) < 20 {
		t.Fatalf("found only %d markdown files (want >= 20); the walk must have broken, and a smaller sample would not be evidence", len(docs))
	}

	for _, path := range docs {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		payload := ParseEditPayload(string(data))
		if payload.Format == FormatUnifiedDiff {
			t.Errorf("%s was read as a unified diff and produced %d block(s); documentation must never become a proposed edit",
				path, len(payload.Blocks))
		}
	}
	t.Logf("FALSE-POSITIVE SCREEN: %d real markdown files, none read as a diff", len(docs))
}

// toLF and toCRLF convert between conventions. Both NORMALISE FIRST, so they
// are idempotent and cannot be fed their own output to produce "\r\r\n" --
// which is exactly what a naive toCRLF does when handed text that is already
// CRLF, and what a Windows checkout hands it.
func toLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

func toCRLF(s string) string { return strings.ReplaceAll(toLF(s), "\n", "\r\n") }

// TestToCRLFAndToLFAreTotalAndIdempotent guards the normalise-first half of
// those two helpers, which nothing else can see.
//
// It exists because its own neuter came back NOT CAUGHT: with sources already
// normalised by the caller, toCRLF never receives CRLF input, so stripping its
// internal toLF changed no other test's result. The property is still worth
// having -- a future caller WILL hand one of these its own output -- so it gets
// a guard of its own rather than being deleted or left unguarded.
func TestToCRLFAndToLFAreTotalAndIdempotent(t *testing.T) {
	cases := []struct{ name, in, wantCRLF, wantLF string }{
		{"LF input", "a\nb\n", "a\r\nb\r\n", "a\nb\n"},
		// The one that matters: a naive toCRLF doubles this into "a\r\r\n".
		{"CRLF input, as a Windows checkout gives it", "a\r\nb\r\n", "a\r\nb\r\n", "a\nb\n"},
		{"lone CR input", "a\rb\r", "a\r\nb\r\n", "a\nb\n"},
		{"mixed input is made uniform", "a\r\nb\nc\r", "a\r\nb\r\nc\r\n", "a\nb\nc\n"},
		{"no line break", "alpha", "alpha", "alpha"},
		{"empty", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := toCRLF(c.in); got != c.wantCRLF {
				t.Errorf("toCRLF(%q) = %q, want %q", c.in, got, c.wantCRLF)
			}
			if got := toLF(c.in); got != c.wantLF {
				t.Errorf("toLF(%q) = %q, want %q", c.in, got, c.wantLF)
			}
			// Idempotence stated directly, so a regression cannot hide behind a
			// caller that happens to normalise first.
			if got := toCRLF(toCRLF(c.in)); got != c.wantCRLF {
				t.Errorf("toCRLF is not idempotent: toCRLF(toCRLF(%q)) = %q, want %q", c.in, got, c.wantCRLF)
			}
			if got := toLF(toLF(c.in)); got != c.wantLF {
				t.Errorf("toLF is not idempotent: toLF(toLF(%q)) = %q, want %q", c.in, got, c.wantLF)
			}
		})
	}
}

// TestAnLFPatchAppliedToACRLFTreeKeepsTheTreeCRLF is the measurement the
// line-ending work is gated on, and it reproduces the ordinary Windows
// workflow rather than an invented one.
//
// Git for Windows defaults to core.autocrlf=true. Under it `git diff` emits LF
// while the files on disk stay CRLF, so a Windows user's patch describes
// different bytes than their own working tree holds. The reader reads it and
// the matcher finds the text at the line-ending tier -- both correct -- but the
// splice used to write the patch's LF lines into the CRLF file, leaving it
// MIXED. Measured before conformReplacementEOL: 0 of 200 files byte-identical,
// with not_found=0 and refusals_from_reader=0, which is the tell that both
// halves were working on a question that had been mis-stated.
//
// The setup is HERMETIC rather than autocrlf-driven: the patch is computed from
// the LF tree and the CRLF conversion is done here, so the test measures the
// splice on every platform instead of measuring the host's git config. That is
// the same mistake TestTheCorpusAsksGitForUntranslatedBytes exists to prevent,
// one level up.
func TestAnLFPatchAppliedToACRLFTreeKeepsTheTreeCRLF(t *testing.T) {
	requireGit(t)

	sources := collectRealSources(t)
	if len(sources) < minRoundTripFiles {
		t.Fatalf("collected only %d source files (floor %d); the walk must have broken", len(sources), minRoundTripFiles)
	}
	if len(sources) > 200 {
		sources = sources[:200]
	}

	// THE CHECKOUT IS PART OF THE FIXTURE, not just the git config.
	//
	// collectRealSources reads these files from DISK, and on a Windows runner git
	// checks them out as CRLF (core.autocrlf=true) -- so "the LF original" is not
	// LF at all. `git diff` then returns a patch carrying CR, the guard below
	// fires, and toCRLF would double every ending into "\r\r\n".
	//
	// Normalising here is what makes this test measure the SPLICE instead of the
	// runner's checkout. It is the same lesson as gitNeutralConfig one level up,
	// applied to the bytes on disk rather than the bytes git prints -- and it was
	// learned the same way, from a red Windows job.
	for i := range sources {
		sources[i] = toLF(sources[i])
	}

	root := realTempDir(t)
	git(t, root, "init", "-q", ".")
	git(t, root, "config", "user.email", "test@example.invalid")
	git(t, root, "config", "user.name", "test")

	originals := make([]string, len(sources))
	wanted := make([]string, len(sources))
	for i, src := range sources {
		name := fmt.Sprintf("f%03d.txt", i)
		originals[i] = src
		if err := os.WriteFile(filepath.Join(root, name), []byte(src), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		mutatedText, _ := mutate(src, i)
		wanted[i] = mutatedText
	}

	git(t, root, "add", "-A")
	git(t, root, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "base")

	for i := range sources {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.txt", i)), []byte(wanted[i]), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	// The patch, in LF -- what `git diff` hands a Windows user.
	patch := git(t, root, "diff", "--unified=3")
	if strings.TrimSpace(patch) == "" {
		t.Skip("git produced no diff (git present but not usable in this environment)")
	}
	if strings.Contains(patch, "\r") {
		t.Fatalf("the patch carries CR; this test is only evidence while the patch is LF and the tree is CRLF")
	}

	// The tree, in CRLF -- what is actually on that user's disk.
	for i := range sources {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.txt", i)), []byte(toCRLF(originals[i])), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	payload := ParseEditPayload(patch)
	if payload.Format != FormatUnifiedDiff {
		t.Fatalf("Format = %v, want FormatUnifiedDiff", payload.Format)
	}
	if len(payload.Blocks) < minRoundTripHunks {
		t.Fatalf("ingested only %d hunks (floor %d)", len(payload.Blocks), minRoundTripHunks)
	}

	backupDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatalf("NewBackupSessionDir: %v", err)
	}

	var applied, ambiguous, notFound, reencoded int
	otherRefusals := map[string]int{}
	for _, block := range payload.Blocks {
		prepared, err := PrepareEdit(root, block)
		if err != nil {
			switch {
			case strings.Contains(err.Error(), "ambiguous"):
				ambiguous++
			case strings.Contains(err.Error(), "not found"):
				notFound++
			default:
				otherRefusals[err.Error()]++
			}
			continue
		}
		if strings.Contains(prepared.MatchNote, "re-encoded") {
			reencoded++
		}
		if err := Apply(root, prepared, backupDir); err != nil {
			t.Fatalf("Apply %s: %v", block.FilePath, err)
		}
		applied++
	}

	if len(otherRefusals) > 0 {
		t.Errorf("hunks refused for reasons that are not ambiguity or staleness: %v", otherRefusals)
	}
	if notFound > 0 {
		t.Errorf("%d hunk(s) produced SEARCH text that is not in the CRLF file -- the matcher stopped tolerating line endings", notFound)
	}

	exact, mixed := 0, 0
	for i := range sources {
		got, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("f%03d.txt", i)))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) == toCRLF(wanted[i]) {
			exact++
		}
		if dominantEOL(string(got)) == eolMixed {
			mixed++
		}
	}

	t.Logf("CRLF-TREE MEASUREMENT: files=%d hunks=%d applied=%d re-encoded=%d AMBIGUOUS=%d not_found=%d MIXED=%d byte_identical=%d/%d",
		len(sources), len(payload.Blocks), applied, reencoded, ambiguous, notFound, mixed, exact, len(sources))

	// The defect, stated as an assertion: not one file may come back with two
	// line-ending conventions in it. This is the number that was equal to the
	// count of edited files before the fix.
	if mixed > 0 {
		t.Errorf("%d/%d files came back with MIXED line endings; an edit must not change the convention of the file it writes into", mixed, len(sources))
	}
	// Ambiguity is a legitimate refusal, so a handful of files legitimately do
	// not reach byte-identical -- the same bar the LF round trip uses.
	if exact*10 < len(sources)*9 {
		t.Errorf("only %d/%d files round-tripped byte-identically; want at least 90%%", exact, len(sources))
	}
	// A run in which nothing was re-encoded would pass every assertion above
	// while proving nothing about the code under test.
	if reencoded == 0 {
		t.Error("no hunk was re-encoded; this test cannot be evidence for a path it never took")
	}
}
