package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// embedderStampFileName is written into a workspace's index directory after
// a successful index build, and checked before every retrieve.
const embedderStampFileName = "embedder_stamp.json"

// currentIndexSchemaVersion identifies the shape of what's stored per chunk
// in the index, independent of which embedder produced the vectors. Bumped
// to 2 when chunks started carrying a file Class in their metadata (see
// fileclass.go) — an index built before that bump has no "class" key at
// all, so every chunk would silently read back as an empty/unclassified
// Class. Rather than let that pass silently, an old stamp (which decodes
// IndexSchemaVersion to its zero value, since the field didn't exist yet)
// is treated as stale, the same way an embedder-ID mismatch already is.
//
// Bumped to 3 for "embed_prefix", the second metadata key, for the same reason:
// an index written before it reads back with no prefix recorded, and
// carryOverUnchanged would then compare an empty prefix against the real one and
// re-embed the entire tree on every run — correct, but silently and forever.
const currentIndexSchemaVersion = 3

// embedderStamp records which embedder built an index (so a later retrieve
// using a different or updated embedder can detect the mismatch instead of
// silently comparing incompatible vectors — Dim alone can't do this job,
// since PlaceholderEmbedder and BgeEmbedder are both 384-dimensional by
// design) and which index schema version it was built with.
type embedderStamp struct {
	EmbedderID         string `json:"embedder_id"`
	Dim                int    `json:"dim"`
	IndexSchemaVersion int    `json:"index_schema_version"`

	// ChunkerID identifies the chunk-BOUNDARY algorithm (chunker.go). Neither
	// field above covers it: EmbedderID says which model made the vectors, and
	// IndexSchemaVersion says what shape each chunk is stored in. Change how
	// text is cut and both stay identical -- so yesterday's boundaries were
	// accepted by today's binary, and since chunk IDs encode line ranges, every
	// boundary the new chunker never regenerates sat in the store describing a
	// file it no longer matched.
	//
	// NO omitempty, deliberately, unlike BuiltAt. An index built before this
	// field existed decodes to "", which mismatches and forces one re-index.
	// That is intended rather than collateral: those are exactly the indexes
	// that may carry orphans from before pruneOrphanedChunks existed, and a
	// rebuild is what clears them.
	ChunkerID string `json:"chunker_id"`

	// BuiltAt is when this index finished building, UTC. It exists because the
	// index had NO freshness concept at all -- no mtime, no timestamp, no
	// watcher -- so a daemon answered from a snapshot of unknown age and
	// reported `grounded ✓` with total confidence either way. This repository
	// demonstrated it: `.codeterminal/index/` was dated 22 days behind HEAD
	// while the product cheerfully cited it.
	//
	// A ZERO VALUE MEANS "UNKNOWN", NOT "OLD". Every index built before this
	// field existed decodes to the zero time, and there are many. Treating
	// unknown as stale would flip an entire installed base into a warning state
	// on upgrade, for a condition nobody has measured -- so unknown SUPPRESSES
	// the signal instead. See indexFreshness.
	//
	// omitempty so a healthy stamp written by an older binary and re-read by a
	// newer one is byte-identical to what it was.
	BuiltAt time.Time `json:"built_at,omitempty"`
}

// writeEmbedderStamp stamps indexDir with embedder's identity and the
// current index schema version, overwriting any previous stamp. Called
// after a successful index build.
func writeEmbedderStamp(indexDir string, embedder Embedder) error {
	stamp := embedderStamp{
		EmbedderID:         embedder.ID(),
		Dim:                embedder.Dim(),
		IndexSchemaVersion: currentIndexSchemaVersion,
		ChunkerID:          chunkerID,
		// UTC, so a stamp written in one timezone and compared in another does
		// not produce a freshness answer that depends on where the laptop was.
		BuiltAt: time.Now().UTC(),
	}
	data, err := json.MarshalIndent(stamp, "", "  ")
	if err != nil {
		return err
	}
	// O_NOFOLLOW: the stamp path is derived from indexDir (config, not client
	// input), but refuse to write through a symlink pre-planted at the stamp
	// name rather than following it out of the index dir.
	return writeFileNoFollow(filepath.Join(indexDir, embedderStampFileName), data, 0644)
}

// checkEmbedderStamp refuses to proceed if indexDir's stamp doesn't match
// embedder's identity or the current index schema version — including when
// the stamp is entirely missing, which is exactly what every index built
// before this check existed looks like. An index with zero documents
// (storeIsEmpty) is let through regardless, since there's nothing stale to
// compare yet.
func checkEmbedderStamp(indexDir string, embedder Embedder, storeIsEmpty bool) error {
	if storeIsEmpty {
		return nil
	}

	path := filepath.Join(indexDir, embedderStampFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("no embedder stamp found at %s (index predates this check, or was built with a different embedder); re-index required: run `index` again", path)
	}

	var stamp embedderStamp
	if err := json.Unmarshal(data, &stamp); err != nil {
		return fmt.Errorf("corrupt embedder stamp at %s; re-index required: run `index` again", path)
	}

	if stamp.EmbedderID != embedder.ID() || stamp.Dim != embedder.Dim() {
		return fmt.Errorf(
			"index was built with a different embedder (id=%s dim=%d) than the active one (id=%s dim=%d); re-index required: run `index` again",
			stamp.EmbedderID, stamp.Dim, embedder.ID(), embedder.Dim())
	}
	if stamp.ChunkerID != chunkerID {
		was := stamp.ChunkerID
		if was == "" {
			was = "an unrecorded chunker (the index predates this check)"
		}
		return fmt.Errorf(
			"index was built by %s, but this binary chunks as %s; chunk ids encode line ranges, so the stored boundaries no longer describe the files; re-index required: run `index` again",
			was, chunkerID)
	}
	if stamp.IndexSchemaVersion != currentIndexSchemaVersion {
		return fmt.Errorf(
			"index was built with schema version %d, but this binary requires %d (e.g. chunks predate file-class metadata); re-index required: run `index` again",
			stamp.IndexSchemaVersion, currentIndexSchemaVersion)
	}
	return nil
}
