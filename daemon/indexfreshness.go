package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"codeterminal/protocol"
)

// IS THE INDEX STILL TRUE?
//
// The index had no freshness concept whatsoever -- no mtime, no timestamp, no
// watcher. A daemon answered from whatever snapshot existed and reported
// `grounded ✓` with the same confidence whether the index was built a minute
// ago or a month ago. This repository was the live proof: `.codeterminal/index/`
// sat 22 days behind HEAD while the product cited it as ground truth.
//
// That is the most trust-destroying bug shape a retrieval product has. A wrong
// answer with a visible caveat is a tool being honest about its limits; a wrong
// answer wearing a confidence marker is the tool lying, and users who catch it
// once stop believing the marker forever.
//
// WHAT THIS DOES: compares the index's BuiltAt stamp against the modification
// times of the files it claims to cover, and reports a Degradation when the
// workspace has moved on. Nothing is hashed, nothing is re-embedded, and no
// file's contents are read beyond the few hundred bytes shouldSkipFile already
// sniffs to recognise a binary -- the eligibility answer has to match the
// indexer's exactly, and that is where the indexer's answer lives.
//
// ── Three things deliberately NOT done, each with its reason ──
//
// 1. NOT A FILE WATCHER. It would mean a new dependency inside the
//    security-sensitive daemon, three genuinely different platform backends, and
//    a permanent inotify/kqueue budget -- to achieve something unnecessary. The
//    goal is not a fresh index at every instant; it is never claiming freshness
//    that isn't there. A stale index that SAYS it is stale is a correct product.
//
// 2. NOT AGE-BASED ("built more than 24h ago"). Age is a proxy for change, and
//    a bad one: it cries wolf on a repository nobody has touched in a week,
//    stays silent on one rewritten five minutes after indexing, and trains
//    people to ignore the signal. Users stop reading signals that lie. This
//    measures the thing itself -- did any covered file change after the build.
//
// 3. NOT ON THE PROMPT PATH. See statusDegradations in degraded.go for why the
//    separation is structural rather than a performance tweak.
//
// ── And one thing deliberately conservative ──
//
// A ZERO BuiltAt MEANS UNKNOWN, AND UNKNOWN SUPPRESSES THE SIGNAL. Every index
// built before the stamp carried a timestamp reads back as the zero time, and
// there are many in existence. Reporting those as stale would flip an entire
// installed base into a warning state on upgrade over a condition never
// measured. Unknown is not evidence of staleness; claiming otherwise would be
// the same over-confidence in the opposite direction.

// freshnessScanLimit bounds the sweep. A workspace over this many eligible
// files is already past the point where semantic search is enabled at all
// (see the 10,000-file gate that raises DegradedWorkspaceTooLarge), so this can
// only be reached by a tree that grew after indexing. Stopping and saying
// "unknown" is the honest answer; walking forever to answer a status ping is
// not.
const freshnessScanLimit = 20000

// indexFreshness is the answer to "has the workspace changed since the index
// was built". The three states are distinct on purpose and must not be
// collapsed into a bool.
type indexFreshness int

const (
	// freshnessUnknown: no BuiltAt stamp, or the sweep could not complete. NOT
	// a claim in either direction, and reports nothing to the client.
	freshnessUnknown indexFreshness = iota
	// freshnessCurrent: nothing eligible is newer than the build.
	freshnessCurrent
	// freshnessStale: at least one covered file changed after the build.
	freshnessStale
)

// indexFreshnessResult carries the verdict plus the evidence for it, so the
// daemon log can say WHY without the client learning a path.
type indexFreshnessResult struct {
	State indexFreshness
	// Changed is how many eligible files are newer than BuiltAt. Counting rather
	// than stopping at the first costs nothing (the walk is already running) and
	// turns "something changed" into "47 files changed", which is the difference
	// between a warning a user dismisses and one they act on.
	Changed int
	// Newest is the most recent modification time seen among changed files.
	Newest time.Time
	// BuiltAt is echoed back so the log can show both ends of the comparison.
	BuiltAt time.Time
}

// scanIndexFreshness walks root stat-only and compares against builtAt.
//
// It reuses the indexer's own eligibility gates -- isPrunedDir and the
// per-directory gitignoreMatcher -- rather than inventing a second notion of
// "files we care about". If the two ever disagreed, this would report staleness
// for files the index was never going to cover, which is a false alarm that
// cannot be fixed by re-indexing: the worst kind.
func scanIndexFreshness(root string, builtAt time.Time) indexFreshnessResult {
	res := indexFreshnessResult{State: freshnessUnknown, BuiltAt: builtAt}
	if builtAt.IsZero() || root == "" {
		return res
	}

	ignore := newGitignoreMatcher(root)
	seen := 0
	truncated := false

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree is not evidence of anything. Skip it rather
			// than failing the whole sweep or counting it as a change.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}

		if d.IsDir() {
			if isPrunedDir(d.Name()) || ignore.matchDir(filepath.ToSlash(rel)) {
				return fs.SkipDir
			}
			return nil
		}

		// THE INDEXER'S OWN GATE, called rather than reimplemented.
		//
		// An earlier version of this walk checked gitignore and symlinks itself
		// and stopped there. Measured against this repository, it counted 464
		// eligible files where the indexer scans 445 -- because shouldSkipFile
		// also rejects secret-named files, noise files, oversized files and
		// binaries. Every one of those 19 would have been a permanent false
		// alarm: edit a .env or drop a binary into the tree and the index reads
		// "stale" forever, with re-indexing unable to clear it, because the file
		// was never going to be indexed in the first place.
		//
		// chunker.go's comment above shouldSkipFile says the decision lives in
		// "exactly one place ... so the walk-time decision and the actual content
		// read can never diverge". A second copy here is precisely that
		// divergence, so there is no second copy.
		if _, skip, skipErr := shouldSkipFile(path, filepath.ToSlash(rel), ignore); skipErr != nil || skip {
			return nil
		}

		seen++
		if seen > freshnessScanLimit {
			truncated = true
			return filepath.SkipAll
		}

		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if mt := info.ModTime(); mt.After(builtAt) {
			res.Changed++
			if mt.After(res.Newest) {
				res.Newest = mt
			}
		}
		return nil
	})
	if err != nil || truncated {
		// A partial walk that already found changes still proves staleness --
		// the evidence is positive, and finding more would not change the
		// verdict. A partial walk that found none proves nothing.
		if res.Changed > 0 {
			res.State = freshnessStale
			return res
		}
		res.State = freshnessUnknown
		return res
	}

	if res.Changed > 0 {
		res.State = freshnessStale
	} else {
		res.State = freshnessCurrent
	}
	return res
}

// freshnessCache memoises one sweep for a short window.
//
// Status is a health check, and a health check that costs a full directory walk
// invites the thing it is meant to prevent: an operator polling it every second
// makes the daemon slower and then blames the daemon. The TTL is short enough
// that a human re-running `status` after re-indexing sees the new answer.
type freshnessCache struct {
	mu     sync.Mutex
	ttl    time.Duration
	at     time.Time
	result indexFreshnessResult
	valid  bool
}

func newFreshnessCache(ttl time.Duration) *freshnessCache {
	return &freshnessCache{ttl: ttl}
}

// get returns the cached sweep, refreshing it through compute when expired.
// now is injected so the test does not sleep.
func (c *freshnessCache) get(now time.Time, compute func() indexFreshnessResult) indexFreshnessResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.valid && now.Sub(c.at) < c.ttl {
		return c.result
	}
	c.result = compute()
	c.at = now
	c.valid = true
	return c.result
}

// detailIndexStale is the client-safe explanation. Per the Degradation
// contract it names WHAT is reduced and what it COSTS the user, and never a
// path -- the daemon log keeps the diagnostic.
const detailIndexStale = "the search index was built before the current state of this workspace, so answers may cite code that has since changed; re-run `index` to refresh it"

// indexStaleDegradation converts a sweep result into the wire shape, or nil
// when there is nothing honest to report.
func indexStaleDegradation(res indexFreshnessResult) *protocol.Degradation {
	if res.State != freshnessStale {
		return nil
	}
	meta := map[string]any{"changed_files": res.Changed}
	if !res.BuiltAt.IsZero() {
		meta["index_built_at"] = res.BuiltAt.UTC().Format(time.RFC3339)
	}
	return &protocol.Degradation{
		Component: protocol.DegradedIndexStale,
		Detail:    detailIndexStale,
		Metadata:  meta,
	}
}

// readStampBuiltAt reads just the build time out of an index directory's stamp.
// A missing or unparsable stamp is the zero time, which callers treat as
// unknown -- checkEmbedderStamp is the thing that rejects a bad stamp, and this
// must not become a second, quieter copy of that policy.
func readStampBuiltAt(indexDir string) time.Time {
	data, err := os.ReadFile(filepath.Join(indexDir, embedderStampFileName))
	if err != nil {
		return time.Time{}
	}
	var stamp embedderStamp
	if err := json.Unmarshal(data, &stamp); err != nil {
		return time.Time{}
	}
	return stamp.BuiltAt
}
