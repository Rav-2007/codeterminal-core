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

// embedderStamp records which embedder built an index, so a later retrieve
// using a different (or updated) embedder can detect the mismatch instead
// of silently comparing incompatible vectors. Dim alone can't do this job:
// PlaceholderEmbedder and BgeEmbedder are both 384-dimensional by design, so
// EmbedderID is the field that actually distinguishes them.
type embedderStamp struct {
	EmbedderID string `json:"embedder_id"`
	Dim        int    `json:"dim"`
}

// writeEmbedderStamp stamps indexDir with embedder's identity, overwriting
// any previous stamp. Called after a successful index build.
func writeEmbedderStamp(indexDir string, embedder Embedder) error {
	stamp := embedderStamp{EmbedderID: embedder.ID(), Dim: embedder.Dim()}
	data, err := json.MarshalIndent(stamp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(indexDir, embedderStampFileName), data, 0644)
}

// checkEmbedderStamp refuses to proceed if indexDir's stamp doesn't match
// embedder's identity — including when the stamp is entirely missing, which
// is exactly what every index built before this check existed looks like.
// An index with zero documents (storeIsEmpty) is let through regardless,
// since there's nothing stale to compare yet.
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
	return nil
}
