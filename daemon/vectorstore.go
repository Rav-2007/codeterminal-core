package main

import (
	"context"
	"fmt"
	"strconv"

	chromem "github.com/philippgille/chromem-go"
)

// collectionName is the single chromem-go collection every workspace index
// uses. Each workspace gets its own on-disk DB (a separate directory), so
// there's no need for more than one collection name per DB.
const collectionName = "codeterminal-chunks"

// Chunk is one indexed slice of a source file: the text itself, where it
// came from, and (once embedded) its vector. Score is populated only on
// results returned from VectorStore.Query; it is zero on every other path.
type Chunk struct {
	ID        string
	FilePath  string
	StartLine int
	EndLine   int
	Content   string
	Vector    []float32
	Score     float32
}

// VectorStore persists chunks and finds the ones nearest a query vector.
// Chunks passed to Upsert must already carry their Vector — VectorStore
// itself never embeds anything, so it has no dependency on Embedder.
type VectorStore interface {
	Upsert(ctx context.Context, chunks []Chunk) error
	Query(ctx context.Context, queryVec []float32, k int) ([]Chunk, error)
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
		})
	}
	return chunks, nil
}

// Count returns the number of chunks currently stored.
func (s *ChromemStore) Count() int {
	return s.collection.Count()
}
