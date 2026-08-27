package main

import (
	"context"
	"fmt"
	"log"

	"codeterminal/helper/helperproto"
)

// bgeEmbedderID is stamped into every index BgeEmbedder builds, and checked
// on retrieve (see embedderstamp.go). It must change whenever the embedding
// semantics change — model version, pooling method, prefix convention, or how
// much of the input is actually read — so a stale index is never silently
// queried with mismatched vectors.
//
// THE SEQUENCE LENGTH IS PART OF THE IDENTITY, and it was the item this list
// was missing. The helper truncates every input to helperproto.MaxSequenceLength
// before embedding it, so that number decides which half of a chunk the vector
// describes; changing it changes every vector in the index. Nothing here
// referenced it, so the cap could have moved -- and did, from 256 to 512 --
// while ID() went on reporting the same string, leaving a user's existing index
// silently mixing vectors of whole chunks with vectors of half-chunks. Building
// the ID from the constant means that can never be forgotten again, rather than
// being remembered correctly once.
var bgeEmbedderID = fmt.Sprintf("bge-small-en-v1.5-int8+onnxruntime-1.26.0+seq%d",
	helperproto.MaxSequenceLength)

// bgeQueryPrefix is BAAI's documented instruction prefix for embedding
// retrieval QUERIES (not documents) with BGE v1.5 models — verified against
// the model card at https://huggingface.co/BAAI/bge-small-en-v1.5. The
// exact wording, including the trailing colon and space, matters: the model
// was fine-tuned to expect precisely this string prepended to queries.
const bgeQueryPrefix = "Represent this sentence for searching relevant passages: "

// BgeEmbedder implements Embedder by delegating to a running embedder
// helper subprocess. It is the only place that knows about the query/
// document asymmetry: Embed sends documents as-is; EmbedQuery prepends
// bgeQueryPrefix. The helper itself has no notion of this distinction — it
// just embeds whatever text it's given (see helper/onnxembedder.go).
type BgeEmbedder struct {
	helper *HelperProcess
}

// NewBgeEmbedder wraps an already-started HelperProcess as an Embedder.
func NewBgeEmbedder(helper *HelperProcess) *BgeEmbedder {
	return &BgeEmbedder{helper: helper}
}

// Dim returns the real model's output width.
func (e *BgeEmbedder) Dim() int { return embedDim }

// ID identifies this embedder for the stale-index guard.
func (e *BgeEmbedder) ID() string { return bgeEmbedderID }

// Embed sends texts to the helper unmodified — this is the DOCUMENT path
// (used when indexing), which BGE expects with no instruction prefix.
func (e *BgeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	vecs, err := e.helper.Embed(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("bge embedder: %w", err)
	}
	return vecs, nil
}

// EmbedQuery prepends bgeQueryPrefix to each text before sending it to the
// helper — this is the QUERY path (used when retrieving).
func (e *BgeEmbedder) EmbedQuery(ctx context.Context, texts []string) ([][]float32, error) {
	prefixed := make([]string, len(texts))
	for i, t := range texts {
		prefixed[i] = bgeQueryPrefix + t
	}
	vecs, err := e.helper.Embed(ctx, prefixed)
	if err != nil {
		return nil, fmt.Errorf("bge embedder (query): %w", err)
	}
	return vecs, nil
}

// newActiveEmbedder resolves the cached model files and onnxruntime shared
// library, starts the embedder helper, and returns a ready-to-use
// BgeEmbedder plus a stop function the caller must defer. It fails with a
// clear, actionable message (via resolveModelPaths) if `download-model`
// hasn't been run yet for this platform.
func newActiveEmbedder(logger *log.Logger) (Embedder, func(), error) {
	modelDir, onnxRuntimeLib, err := resolveModelPaths()
	if err != nil {
		return nil, nil, err
	}

	// Resolved against the daemon's own binary, not the working directory
	// (Fix 8): a bare relative path meant retrieval silently switched itself off
	// for every launch that was not from the repo root.
	helperBin, err := resolveHelperBinPath()
	if err != nil {
		return nil, nil, err
	}

	helper := NewHelperProcess(helperBin, modelDir, onnxRuntimeLib, logger)
	if err := helper.Start(); err != nil {
		return nil, nil, fmt.Errorf("starting embedder helper (build it first with: cd helper && go build -o %s .): %w", helperBinName, err)
	}

	stop := func() {
		if err := helper.Stop(); err != nil {
			logger.Printf("stopping embedder helper: %v", err)
		}
	}
	return NewBgeEmbedder(helper), stop, nil
}
