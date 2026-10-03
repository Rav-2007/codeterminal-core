package main

// Build output and dependency trees stay out of the index on EVERY path, not
// only the full walk. Until 2026-10 the walk pruned them and the incremental
// paths did not, and this repository's own live index held 62 files and 334
// chunks from clients/vscode/out -- compiled copies of the TypeScript sources
// and their source maps -- one of which outranked the real answer to a locate
// question.

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTheGateRefusesAFileInsideABuildDirectory(t *testing.T) {
	root := realTempDir(t)
	for _, rel := range []string{"clients/vscode/out/extension.js", "web/dist/app.js", "node_modules/x/index.js", "src/out.go", "build.go"} {
		writeTempFile(t, root, rel, "package x\n")
	}
	ignore := newGitignoreMatcher(root)
	for rel, refused := range map[string]bool{
		"clients/vscode/out/extension.js": true,
		"web/dist/app.js":                 true,
		"node_modules/x/index.js":         true,
		// The file's own name is not a directory: these are source files.
		"src/out.go": false,
		"build.go":   false,
	} {
		native := filepath.FromSlash(rel)
		reason, skip, err := shouldSkipFile(filepath.Join(root, native), native, ignore)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if skip != refused {
			t.Errorf("%s: skipped=%t (%s), want %t", rel, skip, reason, refused)
		}
	}
}

// The re-index the watcher and an applied edit run is the path that let build
// output in. Neuter check: drop the underPrunedDir check from shouldSkipFile and
// the compiled file is indexed.
func TestReindexNeverAdmitsBuildOutput(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "clients/vscode/out/daemonSupervisor.js", "function start() { return 1; }\n")
	store := &recordingStore{}
	srv := &Server{logger: discardLogger(), workspace: root, embedder: NewPlaceholderEmbedder(embedDim), store: store}

	if err := srv.reindexFile(context.Background(), root, "clients/vscode/out/daemonSupervisor.js"); err != nil {
		t.Fatalf("reindexFile: %v", err)
	}
	if got := store.indexedContent("clients/vscode/out/daemonSupervisor.js"); got != "" {
		t.Errorf("a compiled file under out/ was indexed: %q", got)
	}
}

func TestTheWatcherDoesNotWatchWhatTheWalkPrunes(t *testing.T) {
	for name := range noiseDirNames {
		if watchableDirName(name) {
			t.Errorf("the watcher would watch %q, which the walk prunes", name)
		}
	}
	for _, name := range []string{"daemon", "src", "clients"} {
		if !watchableDirName(name) {
			t.Errorf("the watcher would not watch %q", name)
		}
	}
}

// An index that already holds build output is cleaned on the next start, in
// both stores, through the real call site. Neuter check: drop the
// purgePrunedFromIndex call from catchUpIndex and the compiled file stays.
func TestStartupDropsBuildOutputAlreadyInTheIndex(t *testing.T) {
	root := realTempDir(t)
	ctx := context.Background()
	compiled := chunkContent([]byte("function start() {}\n"), "clients/vscode/out/daemonSupervisor.js")
	source := chunkContent([]byte("package daemon\n"), "daemon/x.go")

	store := &recordingStore{}
	if err := store.Upsert(ctx, append(append([]Chunk{}, compiled...), source...)); err != nil {
		t.Fatal(err)
	}
	lexical, err := NewFTSChunkStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lexical.Close() }()
	if err := lexical.Upsert(ctx, append(append([]Chunk{}, compiled...), source...)); err != nil {
		t.Fatal(err)
	}

	srv := &Server{logger: discardLogger(), workspace: root, embedder: NewPlaceholderEmbedder(embedDim),
		store: store, lexicalStore: lexical}
	srv.catchUpIndex() // no index stamp here: the purge runs before catch-up's own early return

	if got := store.indexedContent("clients/vscode/out/daemonSupervisor.js"); got != "" {
		t.Errorf("the vector store still holds the compiled file: %q", got)
	}
	if store.indexedContent("daemon/x.go") == "" {
		t.Error("the purge removed a source file")
	}
	ids, err := lexical.AllIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if filePathOfChunkID(id) != "daemon/x.go" {
			t.Errorf("the keyword index still holds %s", id)
		}
	}
	if len(ids) != len(source) {
		t.Errorf("the keyword index holds %d chunk(s), want the %d of daemon/x.go", len(ids), len(source))
	}
}
