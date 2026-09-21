package main

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLogChunkScrub_WritesFiresToTheDurableSink closes the one gap
// TestWarnSink_NormalAppend (nofollow_writers_test.go) leaves open.
//
// That test proves the WRITER works — hand it an event and a file appears. It
// says nothing about whether anything ever hands it one. A correct writer that
// is never called is indistinguishable from a correct writer that is, right up
// until you go looking for the data and find an empty directory, which is
// exactly what happened here: `.mochiii/logs/` existed with no
// `warnmode.jsonl` in it for twelve days while the record said fire-rate data
// was accumulating (see docs/CHUNK_SCRUB_FIRE_RATE.md). The cause turned out to
// be no grounded traffic rather than a broken wire, but nothing in the suite
// could have told those two apart.
//
// This asserts the wire: a chunk carrying a warn-mode-detectable token, pushed
// through the real logChunkScrub, must land as a durable JSON line.
//
// Neuter-check: delete the s.warnSink.write(...) call in logChunkScrub and this
// test fails on the missing file, while TestWarnSink_NormalAppend still passes.
func TestLogChunkScrub_WritesFiresToTheDurableSink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "warnmode.jsonl")
	s := &Server{
		cfg:      &Config{},
		logger:   log.New(io.Discard, "", 0),
		warnSink: newWarnSink(path),
	}

	// A keyword-detector fire: a credential-named assignment with a value long
	// enough to clear isNonSecretValue's floor. Deliberately NOT a structural
	// signature — Option A would redact one of those before the deferred
	// detectors ever saw it, so a structural secret would prove nothing about
	// this path.
	s.logChunkScrub([]Chunk{{
		FilePath:  "config/app.go",
		StartLine: 10,
		EndLine:   12,
		Class:     FileClassCode,
		Content:   `password = "hunter2hunter2hunter2"`,
	}})

	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("logChunkScrub recorded no durable fire: %v", err)
	}

	line, _, _ := strings.Cut(strings.TrimSpace(string(blob)), "\n")
	var ev struct {
		Detector  string `json:"detector"`
		File      string `json:"file"`
		StartLine int    `json:"start_line"`
		Class     string `json:"class"`
		Note      string `json:"note"`
		Indicator string `json:"indicator"`
	}
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("sink line is not the JSON the triage tooling reads: %v (line %q)", err, line)
	}

	if ev.Detector != "keyword" {
		t.Errorf("detector = %q, want %q", ev.Detector, "keyword")
	}
	if ev.File != "config/app.go" || ev.StartLine != 10 {
		t.Errorf("fire lost its location: file=%q start=%d", ev.File, ev.StartLine)
	}
	// FileClass and the shape/indicator labels are what make the accumulated
	// data triageable at all (follow-up B, 6028d96) — a fire with no class is
	// a count, not evidence.
	if ev.Class != string(FileClassCode) {
		t.Errorf("class = %q, want %q", ev.Class, FileClassCode)
	}
	if !strings.HasPrefix(ev.Indicator, "sha256:") {
		t.Errorf("indicator = %q, want a truncated-SHA indicator", ev.Indicator)
	}

	// Gate 3 — the non-negotiable one. The durable file must never contain the
	// suspected value itself, only labels and a one-way hash of it.
	if strings.Contains(string(blob), "hunter2hunter2hunter2") {
		t.Error("the durable sink recorded raw suspected-secret content")
	}
}
