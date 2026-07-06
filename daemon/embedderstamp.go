package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
const currentIndexSchemaVersion = 2

// embedderStamp records which embedder built an index (so a later retrieve
// using a different or updated embedder can detect the mismatch instead of
// silently comparing incompatible vectors — Dim alone can't do this job,
// since PlaceholderEmbedder and BgeEmbedder are both 384-dimensional by
// design) and which index schema version it was built with.
type embedderStamp struct {
	EmbedderID         string `json:"embedder_id"`
	Dim                int    `json:"dim"`
	IndexSchemaVersion int    `json:"index_schema_version"`
}

// writeEmbedderStamp stamps indexDir with embedder's identity and the
// current index schema version, overwriting any previous stamp. Called
// after a successful index build.
func writeEmbedderStamp(indexDir string, embedder Embedder) error {
	stamp := embedderStamp{EmbedderID: embedder.ID(), Dim: embedder.Dim(), IndexSchemaVersion: currentIndexSchemaVersion}
	data, err := json.MarshalIndent(stamp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(indexDir, embedderStampFileName), data, 0644)
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
	if stamp.IndexSchemaVersion != currentIndexSchemaVersion {
		return fmt.Errorf(
			"index was built with schema version %d, but this binary requires %d (e.g. chunks predate file-class metadata); re-index required: run `index` again",
			stamp.IndexSchemaVersion, currentIndexSchemaVersion)
	}
	return nil
}
