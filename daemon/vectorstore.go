package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	chromem "github.com/philippgille/chromem-go"
)

// collectionName is the single chromem-go collection every workspace index
// uses. Each workspace gets its own on-disk DB (a separate directory), so
// there's no need for more than one collection name per DB.
const collectionName = "mochiii-chunks"

// Chunk is one indexed slice of a source file: the text itself, where it
// came from, and (once embedded) its vector. Score is populated only on
// results returned from VectorStore.Query; it is zero on every other path.
// Class is set at index time (see classifyFile in fileclass.go) and
// persisted in the store's metadata. RawScore is populated only by
// retrieveTopK's re-rank step (rerank.go): it holds the original
// vector-similarity score before class weighting, kept alongside Score (the
// effective, weighted score that determines final ranking) purely for
// logging/observability — it is never itself persisted.
// FilePath (and the ID derived from it) is ALWAYS in forward-slash form,
// workspace-relative, on every platform. It is an index key and a wire value,
// not a filesystem path: the same chunk must be addressable by the indexer,
// by reindexFile, and by a model that has only ever seen forward slashes.
//
// Convert with filepath.FromSlash at the point of touching the filesystem,
// never by storing the native form. On Linux and macOS the two forms are
// identical, which is exactly why this drifted unnoticed — filepath.Rel
// yields `src\main.go` on Windows, and DeleteByFilePath("src/main.go") from
// the apply path then matched nothing, so every applied edit doubled that
// file's chunks and left the pre-edit version in the index.
type Chunk struct {
	ID        string
	FilePath  string
	StartLine int
	EndLine   int
	Content   string
	Vector    []float32
	Score     float32
	Class     FileClass
	RawScore  float32

	// EmbedText is what the EMBEDDER was given, which is not what Content
	// holds: every chunk is embedded with its file path prepended, so its vector
	// records which document it came from (chunkcontext.go, where the six arms
	// that established this are tabulated).
	//
	// THREE PROPERTIES, and they are why this is a separate field rather than a
	// richer Content.
	//
	// It is NOT STORED AS A FIELD. ChromemStore.Upsert builds its document
	// field by field and stores Content; only the PREFIX is persisted, under the
	// "embed_prefix" metadata key, and only Existing reassembles the two. A
	// chunk from Query has EmbedText == "", which embedTextOf handles by falling
	// back to Content.
	//
	// It is NEVER RENDERED AND NEVER EGRESSES. Only Content reaches a prompt,
	// through the scrubber (context.go). Nothing new leaves the machine — and
	// the prefix is a path already present in every rendered chunk's header line.
	//
	// Content STAYS BYTE-EXACT to the file's lines, and that is not tidiness:
	// chunkLinesOf (chunkmerge.go) refuses to splice any chunk whose content
	// line count contradicts its declared range, so a prefix written INTO
	// Content would make every merge and every neighbour expansion silently
	// refuse, and would break the line-number attribution the model is shown.
	EmbedText string
}

// VectorStore persists chunks and finds the ones nearest a query vector.
// Chunks passed to Upsert must already carry their Vector — VectorStore
// itself never embeds anything, so it has no dependency on Embedder.
type VectorStore interface {
	Upsert(ctx context.Context, chunks []Chunk) error
	Query(ctx context.Context, queryVec []float32, k int) ([]Chunk, error)
	// DeleteByFilePath removes every chunk belonging to one workspace-relative
	// file. Upsert alone cannot keep a re-indexed file correct: chunk IDs encode
	// their line range ("path:12-51"), so a file that SHRINKS leaves its former
	// tail chunks behind under IDs the new content never regenerates, and those
	// orphans keep matching queries with pre-edit code. Deleting by file first
	// is what makes a re-index a replacement rather than a merge.
	DeleteByFilePath(ctx context.Context, relPath string) error
	// AllIDs returns the ID of every chunk the store currently holds.
	//
	// It exists so a full index build can tell what it is REPLACING. Upsert
	// cannot: it writes the chunks the scan produced and is blind to anything
	// already stored under an ID the scan no longer generates, so without this
	// the store only ever grows. See pruneOrphanedChunks (index_cmd.go) for the
	// three measured ways that happens.
	//
	// probeDim is the embedder's vector width. It is a parameter rather than
	// something the store works out for itself because the chromem backend can
	// only enumerate by running a query, a query needs a vector, and chromem
	// returns "vectors must have the same length" on a mismatch. Guessing the
	// width would turn a wrong guess into an empty list -- which reads exactly
	// like "nothing to prune" and would silently restore the bug this method
	// exists to fix.
	AllIDs(ctx context.Context, probeDim int) ([]string, error)
	Count() int
}

// ChromemStore is a VectorStore backed by a local, on-disk chromem-go
// collection.
type ChromemStore struct {
	db         *chromem.DB
	collection *chromem.Collection
}

// refuseEmbeddingFunc guards against chromem-go's default embedding function
// (which calls the OpenAI API) ever running. Every chunk passed to Upsert
// already carries a precomputed vector, so this should never be invoked; if
// it ever is, that indicates a bug upstream of this file, and failing loudly
// beats silently making a network call.
func refuseEmbeddingFunc(_ context.Context, _ string) ([]float32, error) {
	return nil, fmt.Errorf("vectorstore: refusing to embed implicitly; every chunk must carry a precomputed Vector before Upsert")
}

// NewChromemStore opens (creating if absent) a persistent chromem-go DB
// rooted at indexDir, and gets or creates its single collection.
func NewChromemStore(indexDir string) (*ChromemStore, error) {
	db, err := chromem.NewPersistentDB(indexDir, false)
	if err != nil {
		return nil, fmt.Errorf("opening vector store at %s: %w", indexDir, err)
	}

	collection, err := db.GetOrCreateCollection(collectionName, nil, refuseEmbeddingFunc)
	if err != nil {
		return nil, fmt.Errorf("opening collection %s: %w", collectionName, err)
	}

	return &ChromemStore{db: db, collection: collection}, nil
}

// Upsert writes chunks to the store, keyed by Chunk.ID. Re-upserting an
// existing ID overwrites it (chromem-go documents are keyed by ID map), so
// re-indexing the same file's chunks replaces rather than duplicates them.
func (s *ChromemStore) Upsert(ctx context.Context, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	docs := make([]chromem.Document, 0, len(chunks))
	for _, c := range chunks {
		docs = append(docs, chromem.Document{
			ID: c.ID,
			Metadata: map[string]string{
				"file_path":  c.FilePath,
				"start_line": strconv.Itoa(c.StartLine),
				"end_line":   strconv.Itoa(c.EndLine),
				"class":      string(c.Class),
				// What was PREPENDED before embedding, stored separately so
				// Content stays byte-exact. Existing reconstructs EmbedText from
				// it, which is the only way the reuse check can ask whether the
				// stored vector still describes what we would embed today. See
				// carryOverUnchanged (index_cmd.go).
				"embed_prefix": strings.TrimSuffix(c.EmbedText, c.Content),
			},
			Embedding: c.Vector,
			Content:   c.Content,
		})
	}

	// concurrency=1: embeddings are already computed, so there's no
	// concurrent work to parallelize here; keep it simple and deterministic.
	if err := s.collection.AddDocuments(ctx, docs, 1); err != nil {
		return fmt.Errorf("upserting %d chunk(s): %w", len(chunks), err)
	}
	return nil
}

// DeleteByFilePath removes every stored chunk whose file_path metadata matches
// relPath. chromem-go treats an empty result as success, so deleting a file
// that was never indexed is a no-op rather than an error.
func (s *ChromemStore) DeleteByFilePath(ctx context.Context, relPath string) error {
	if err := s.collection.Delete(ctx, map[string]string{"file_path": relPath}, nil); err != nil {
		return fmt.Errorf("deleting chunks for %s: %w", relPath, err)
	}
	return nil
}

// Query returns the k chunks whose vectors are most similar to queryVec. It
// clamps k to the number of stored documents (chromem-go errors if k exceeds
// that) and returns an empty result for an empty store instead of erroring.
func (s *ChromemStore) Query(ctx context.Context, queryVec []float32, k int) ([]Chunk, error) {
	count := s.collection.Count()
	if count == 0 || k <= 0 {
		return nil, nil
	}
	if k > count {
		k = count
	}

	results, err := s.collection.QueryEmbedding(ctx, queryVec, k, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("querying vector store: %w", err)
	}

	chunks := make([]Chunk, 0, len(results))
	for _, r := range results {
		startLine, _ := strconv.Atoi(r.Metadata["start_line"])
		endLine, _ := strconv.Atoi(r.Metadata["end_line"])
		chunks = append(chunks, Chunk{
			ID:        r.ID,
			FilePath:  r.Metadata["file_path"],
			StartLine: startLine,
			EndLine:   endLine,
			Content:   r.Content,
			Vector:    r.Embedding,
			Score:     r.Similarity,
			Class:     FileClass(r.Metadata["class"]),
		})
	}
	return chunks, nil
}

// AllIDs returns every stored chunk ID.
//
// IMPLEMENTED AS A QUERY, which looks odd and is not. chromem-go exposes no
// listing API: Collection offers Add/Get/Delete/Count/Query and nothing that
// walks its documents (the map is unexported). A k=Count() query is the only
// read path that yields IDs, and it yields ALL of them -- verified against a
// 4,400-document store seeded so half the vectors were ORTHOGONAL to the probe,
// which any similarity floor would have dropped. None were dropped, in 6.9ms.
// That is once per index build, against a build measured in seconds.
//
// The probe is a fixed unit vector, not a zero vector: cosine similarity
// against a zero vector is undefined, and ranking by an undefined quantity is a
// good way to depend on an implementation detail.
func (s *ChromemStore) AllIDs(ctx context.Context, probeDim int) ([]string, error) {
	n := s.collection.Count()
	if n == 0 {
		return nil, nil
	}
	if probeDim <= 0 {
		return nil, fmt.Errorf("listing stored chunk ids: probe dimension %d is not usable", probeDim)
	}
	probe := make([]float32, probeDim)
	probe[0] = 1
	docs, err := s.Query(ctx, probe, n)
	if err != nil {
		return nil, fmt.Errorf("listing stored chunk ids: %w", err)
	}
	ids := make([]string, 0, len(docs))
	for _, d := range docs {
		ids = append(ids, d.ID)
	}
	return ids, nil
}

// Count returns the number of chunks currently stored.
func (s *ChromemStore) Count() int {
	return s.collection.Count()
}

// Existing returns the stored chunk for id, and whether it was found.
//
// It exists for ONE caller -- buildIndex's reuse check (index_cmd.go) -- and it
// returns Content alongside Vector because both are needed to answer the only
// question that matters there: is the chunk under this id byte-for-byte what we
// are about to index? An id match alone is not enough. Chunk ids encode a line
// RANGE ("path:40-79"), so an edit that leaves a later chunk starting on the
// same line produces the same id over different text, and reusing a vector on
// that evidence would silently serve the embedding of code that no longer
// exists.
//
// A miss is not an error. An id that was never indexed, a store that cannot
// answer, or a document that came back without an embedding all mean the same
// thing to the caller -- embed it -- so they collapse to (zero, false) rather
// than to an error nobody could act on differently.
func (s *ChromemStore) Existing(ctx context.Context, id string) (Chunk, bool) {
	doc, err := s.collection.GetByID(ctx, id)
	if err != nil || len(doc.Embedding) == 0 {
		return Chunk{}, false
	}
	startLine, _ := strconv.Atoi(doc.Metadata["start_line"])
	endLine, _ := strconv.Atoi(doc.Metadata["end_line"])
	return Chunk{
		ID:        doc.ID,
		FilePath:  doc.Metadata["file_path"],
		StartLine: startLine,
		EndLine:   endLine,
		Content:   doc.Content,
		Vector:    doc.Embedding,
		Class:     FileClass(doc.Metadata["class"]),
		// EmbedText is reconstructed HERE AND NOWHERE ELSE. Query deliberately
		// leaves it empty: a retrieved chunk flows on to merging, expansion and
		// rendering, and a populated EmbedText there is a trap -- spliceChunk
		// has to clear it for exactly that reason. Existing has one caller and
		// one question, "would we embed the same text today", and that question
		// cannot be answered from Content alone.
		EmbedText: doc.Metadata["embed_prefix"] + doc.Content,
	}, true
}
