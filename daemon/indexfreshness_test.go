package main

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codeterminal/protocol"
)

// Stage 4 — index honesty.
//
// The bug these tests exist for was not a crash. It was the product reporting
// `grounded ✓` against a 22-day-old snapshot with total confidence, which is
// worse than an error: an error is the tool being honest about its limits, and
// this was the tool being confidently wrong while wearing a confidence marker.
//
// Every assertion below is about NOT OVERCLAIMING, in both directions. The
// staleness signal must fire when the workspace really has moved on, and must
// stay silent when it merely might have.

// newFreshnessTestServer builds the minimum Server the freshness path reads:
// a workspace, a non-nil store+embedder pair (the check is skipped when the
// semantic tier is not serving), a logger, and a cache.
func newFreshnessTestServer(t *testing.T, root string) *Server {
	t.Helper()
	return &Server{
		workspace: root,
		store:     emptyStore{},
		embedder:  NewPlaceholderEmbedder(384),
		logger:    log.New(io.Discard, "", 0),
		freshness: newFreshnessCache(30 * time.Second),
	}
}

func writeFileAt(t *testing.T, path string, body string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if !mod.IsZero() {
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}
}

// A workspace whose files are all OLDER than the build.
func freshWorkspace(t *testing.T, builtAt time.Time) string {
	t.Helper()
	root := t.TempDir()
	old := builtAt.Add(-1 * time.Hour)
	writeFileAt(t, filepath.Join(root, "main.go"), "package main\n", old)
	writeFileAt(t, filepath.Join(root, "pkg", "util.go"), "package pkg\n", old)
	writeFileAt(t, filepath.Join(root, "README.md"), "# hi\n", old)
	return root
}

func TestScanIndexFreshness_CurrentWhenNothingChanged(t *testing.T) {
	builtAt := time.Now().Add(-30 * time.Minute)
	root := freshWorkspace(t, builtAt)

	res := scanIndexFreshness(root, builtAt)

	if res.State != freshnessCurrent {
		t.Fatalf("state = %v, want freshnessCurrent (changed=%d)", res.State, res.Changed)
	}
	if res.Changed != 0 {
		t.Fatalf("changed = %d, want 0", res.Changed)
	}
}

func TestScanIndexFreshness_StaleWhenAFileIsNewerThanTheBuild(t *testing.T) {
	builtAt := time.Now().Add(-30 * time.Minute)
	root := freshWorkspace(t, builtAt)

	// One file edited after the index was built. This is the whole bug.
	writeFileAt(t, filepath.Join(root, "pkg", "util.go"), "package pkg // edited\n", builtAt.Add(10*time.Minute))

	res := scanIndexFreshness(root, builtAt)

	if res.State != freshnessStale {
		t.Fatalf("a file modified after the build did not make the index stale: state=%v changed=%d", res.State, res.Changed)
	}
	if res.Changed != 1 {
		t.Fatalf("changed = %d, want exactly 1", res.Changed)
	}
	if !res.Newest.After(builtAt) {
		t.Fatalf("Newest (%v) should be after BuiltAt (%v)", res.Newest, builtAt)
	}
}

// THE CONSERVATIVE CASE, and the one most likely to be "simplified" away by
// someone who reads freshnessUnknown as a missing feature.
//
// Every index built before the stamp carried a timestamp decodes to the zero
// time. Reporting those as stale would put an entire installed base into a
// warning state on upgrade, over a condition nobody measured. Unknown is not
// evidence.
func TestScanIndexFreshness_UnknownBuildTimeNeverAssertsStaleness(t *testing.T) {
	root := t.TempDir()
	// A file modified RIGHT NOW -- maximally "suspicious", and still not evidence.
	writeFileAt(t, filepath.Join(root, "main.go"), "package main\n", time.Now())

	res := scanIndexFreshness(root, time.Time{})

	if res.State != freshnessUnknown {
		t.Fatalf("a zero BuiltAt produced state=%v; unknown must never be reported as stale "+
			"-- every pre-timestamp index looks exactly like this", res.State)
	}
	if d := indexStaleDegradation(res); d != nil {
		t.Fatalf("unknown freshness reported a degradation to the client: %+v", d)
	}
}

// The sweep must use the INDEXER's notion of an eligible file. If the two
// disagree, this reports staleness for files the index was never going to
// cover -- a false alarm that re-indexing cannot clear, which is the worst kind
// because the obvious remedy does not work.
func TestScanIndexFreshness_IgnoresWhatTheIndexerIgnores(t *testing.T) {
	builtAt := time.Now().Add(-30 * time.Minute)
	root := freshWorkspace(t, builtAt)
	after := builtAt.Add(10 * time.Minute)

	writeFileAt(t, filepath.Join(root, ".gitignore"), "ignored.txt\nbuild/\n", builtAt.Add(-time.Hour))

	// All modified AFTER the build, none of them eligible.
	writeFileAt(t, filepath.Join(root, "ignored.txt"), "x", after)
	writeFileAt(t, filepath.Join(root, "build", "out.bin"), "x", after)
	writeFileAt(t, filepath.Join(root, "node_modules", "dep", "index.js"), "x", after)
	writeFileAt(t, filepath.Join(root, ".git", "COMMIT_EDITMSG"), "x", after)

	res := scanIndexFreshness(root, builtAt)

	if res.State == freshnessStale {
		t.Fatalf("changes to ignored/pruned files reported the index as stale (changed=%d). "+
			"A .gitignore'd or pruned file is not in the index, so changing it cannot "+
			"invalidate it -- and re-indexing would not clear this warning", res.Changed)
	}
	if res.State != freshnessCurrent {
		t.Fatalf("state = %v, want freshnessCurrent", res.State)
	}
}

// FOUND BY RUNNING IT AGAINST A REAL TREE, not by fixture.
//
// The first version of the sweep checked gitignore and symlinks itself and
// stopped there. On this repository it counted 464 eligible files where the
// indexer scans 445, because shouldSkipFile ALSO rejects secret-named files,
// noise files, oversized files and binaries.
//
// Each of those 19 was a permanent false alarm waiting to happen: touch a .env
// or drop a binary in the tree and the index reads "stale" forever, and
// re-indexing cannot clear it, because the file was never going to be indexed.
// A warning whose obvious remedy does not work is worse than no warning.
//
// NEUTER: replace the shouldSkipFile call with a bare gitignore check and this
// fails on the .env and the binary.
func TestScanIndexFreshness_UsesTheIndexersFullEligibilityGate(t *testing.T) {
	builtAt := time.Now().Add(-30 * time.Minute)
	root := freshWorkspace(t, builtAt)
	after := builtAt.Add(10 * time.Minute)

	// Modified after the build, and none of them indexable -- so none may be
	// evidence that the index is behind.
	writeFileAt(t, filepath.Join(root, ".env"), "OPENROUTER_API_KEY=sk-or-v1-nope\n", after)
	writeFileAt(t, filepath.Join(root, "id_rsa"), "-----BEGIN PRIVATE KEY-----\n", after)
	writeFileAt(t, filepath.Join(root, "blob.bin"), "\x00\x01\x02binary\x00content", after)
	writeFileAt(t, filepath.Join(root, "package-lock.json"), "{}\n", after)

	res := scanIndexFreshness(root, builtAt)

	if res.State == freshnessStale {
		t.Fatalf("a file the INDEXER skips was counted as evidence of staleness (changed=%d). "+
			"Secret-named files, noise files and binaries are never in the index, so changing "+
			"one cannot invalidate it -- and the user could re-index forever without clearing "+
			"the warning", res.Changed)
	}
}

// ANTI-VACUITY for the test above. If the walk silently saw nothing at all --
// wrong root, broken matcher, an early return -- then "ignored files did not
// trigger staleness" would pass while proving nothing. Prove the same fixture
// DOES react to an eligible file.
func TestScanIndexFreshness_TheIgnoreTestIsNotVacuous(t *testing.T) {
	builtAt := time.Now().Add(-30 * time.Minute)
	root := freshWorkspace(t, builtAt)
	after := builtAt.Add(10 * time.Minute)

	writeFileAt(t, filepath.Join(root, ".gitignore"), "ignored.txt\n", builtAt.Add(-time.Hour))
	writeFileAt(t, filepath.Join(root, "ignored.txt"), "x", after)

	if res := scanIndexFreshness(root, builtAt); res.State != freshnessCurrent {
		t.Fatalf("precondition: ignored file should not be stale, got %v", res.State)
	}

	// Same tree, one ELIGIBLE file touched.
	writeFileAt(t, filepath.Join(root, "main.go"), "package main // edited\n", after)

	res := scanIndexFreshness(root, builtAt)
	if res.State != freshnessStale {
		t.Fatalf("the fixture does not detect an eligible change at all, so the ignore test "+
			"above proves nothing: state=%v", res.State)
	}
}

func TestIndexStaleDegradation_LeaksNoPathAndCarriesCounts(t *testing.T) {
	builtAt := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	d := indexStaleDegradation(indexFreshnessResult{
		State:   freshnessStale,
		Changed: 47,
		Newest:  builtAt.Add(time.Hour),
		BuiltAt: builtAt,
	})
	if d == nil {
		t.Fatal("stale freshness produced no degradation")
	}
	if d.Component != protocol.DegradedIndexStale {
		t.Fatalf("component = %q, want %q", d.Component, protocol.DegradedIndexStale)
	}
	if got := d.Metadata["changed_files"]; got != 47 {
		t.Fatalf("changed_files = %v, want 47", got)
	}
	if got, ok := d.Metadata["index_built_at"].(string); !ok || got != "2026-08-08T12:00:00Z" {
		t.Fatalf("index_built_at = %v, want RFC3339 UTC", d.Metadata["index_built_at"])
	}
	// The Degradation contract forbids paths in Detail. Metadata must not
	// smuggle one in either.
	for k, v := range d.Metadata {
		if s, ok := v.(string); ok && (filepath.IsAbs(s) || len(s) > 0 && s[0] == '/') {
			t.Fatalf("metadata %q looks like a path (%q); a health response must not carry "+
				"workspace content", k, s)
		}
	}
}

func TestFreshnessCache_MemoisesWithinTheWindow(t *testing.T) {
	c := newFreshnessCache(30 * time.Second)
	calls := 0
	compute := func() indexFreshnessResult {
		calls++
		return indexFreshnessResult{State: freshnessCurrent}
	}

	base := time.Now()
	c.get(base, compute)
	c.get(base.Add(5*time.Second), compute)
	c.get(base.Add(29*time.Second), compute)
	if calls != 1 {
		t.Fatalf("compute ran %d times inside the TTL; status polling would turn a health "+
			"check into a directory walk per call", calls)
	}

	c.get(base.Add(31*time.Second), compute)
	if calls != 2 {
		t.Fatalf("compute ran %d times after the TTL expired, want 2 -- a cache that never "+
			"refreshes would report a stale index as fresh forever after one re-index", calls)
	}
}

// THE STRUCTURAL SEPARATION. degradations() documents that everything it
// reports is constant for the daemon's lifetime, and callers compute it
// per-prompt on that basis. Index staleness is not constant in either
// direction. If someone folds it in, this fails.
func TestStatusDegradations_StalenessIsNotOnThePromptPath(t *testing.T) {
	builtAt := time.Now().Add(-30 * time.Minute)
	root := freshWorkspace(t, builtAt)
	writeFileAt(t, filepath.Join(root, "main.go"), "package main // edited\n", builtAt.Add(10*time.Minute))

	indexDir := filepath.Join(root, indexDirName)
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatalf("mkdir index dir: %v", err)
	}
	stamp, err := json.Marshal(embedderStamp{
		EmbedderID: "test", Dim: 384, IndexSchemaVersion: currentIndexSchemaVersion, BuiltAt: builtAt,
	})
	if err != nil {
		t.Fatalf("marshal stamp: %v", err)
	}
	if err := os.WriteFile(filepath.Join(indexDir, embedderStampFileName), stamp, 0o644); err != nil {
		t.Fatalf("write stamp: %v", err)
	}

	s := newFreshnessTestServer(t, root)

	hasStale := func(ds []protocol.Degradation) bool {
		for _, d := range ds {
			if d.Component == protocol.DegradedIndexStale {
				return true
			}
		}
		return false
	}

	if hasStale(s.degradations()) {
		t.Fatal("degradations() reported index staleness. That function's contract is that " +
			"everything it returns is constant for the daemon's lifetime, and callers compute " +
			"it per-prompt on that basis -- staleness changes when a file is edited or the " +
			"index is rebuilt, and it costs a directory walk")
	}
	if !hasStale(s.statusDegradations(time.Now())) {
		t.Fatal("statusDegradations() did not report a genuinely stale index")
	}
}

// A nil freshness cache disables the check rather than panicking -- every test
// Server that does not opt in gets one, deliberately, so no unrelated test pays
// for a directory walk.
func TestStatusDegradations_NilCacheIsSafe(t *testing.T) {
	s := newFreshnessTestServer(t, t.TempDir())
	s.freshness = nil

	for _, d := range s.statusDegradations(time.Now()) {
		if d.Component == protocol.DegradedIndexStale {
			t.Fatal("staleness reported with no freshness cache configured")
		}
	}
}

func TestWriteEmbedderStamp_RecordsTheBuildTime(t *testing.T) {
	dir := t.TempDir()
	before := time.Now().Add(-time.Second)

	if err := writeEmbedderStamp(dir, NewPlaceholderEmbedder(384)); err != nil {
		t.Fatalf("writeEmbedderStamp: %v", err)
	}

	got := readStampBuiltAt(dir)
	if got.IsZero() {
		t.Fatal("no build time recorded; every freshness answer would be 'unknown' forever")
	}
	if got.Before(before) {
		t.Fatalf("BuiltAt %v predates the call (%v)", got, before)
	}
	if got.Location() != time.UTC {
		t.Fatalf("BuiltAt is not UTC (%v); a stamp written in one timezone and compared in "+
			"another would give a freshness answer that depends on where the laptop was", got.Location())
	}
}

// An index stamped by an older binary has no built_at key at all. It must
// decode to the zero time and be treated as unknown -- never as 1970, which
// would make every such index look infinitely stale.
func TestReadStampBuiltAt_OldStampWithoutTheFieldIsUnknown(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"embedder_id":"bge","dim":384,"index_schema_version":2}`
	if err := os.WriteFile(filepath.Join(dir, embedderStampFileName), []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy stamp: %v", err)
	}

	if got := readStampBuiltAt(dir); !got.IsZero() {
		t.Fatalf("legacy stamp decoded BuiltAt = %v, want the zero time", got)
	}
	if res := scanIndexFreshness(dir, readStampBuiltAt(dir)); res.State != freshnessUnknown {
		t.Fatalf("a legacy stamp produced state=%v; every index built before this field "+
			"existed looks like this and must not be reported stale", res.State)
	}
}

func TestReadStampBuiltAt_MissingOrCorruptIsUnknownNotAnError(t *testing.T) {
	if got := readStampBuiltAt(t.TempDir()); !got.IsZero() {
		t.Fatalf("missing stamp = %v, want zero", got)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, embedderStampFileName), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := readStampBuiltAt(dir); !got.IsZero() {
		t.Fatalf("corrupt stamp = %v, want zero. checkEmbedderStamp is what REJECTS a bad "+
			"stamp; this must not become a second, quieter copy of that policy", got)
	}
}
