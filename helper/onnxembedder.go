package main

import (
	"fmt"
	"math"
	"path/filepath"
	"sync"

	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
	ort "github.com/yalue/onnxruntime_go"

	"mochiii/helper/helperproto"
)

// embedDim is the real model's output width: BGE-small's hidden size.
const embedDim = 384

// maxSequenceLength bounds tokenized input length.
//
// The value, and the measurements behind it, live in helperproto: the daemon
// needs the same number to build the embedder ID it stamps into an index, and
// two copies of a constant that changes what a vector means is exactly the
// drift that stamp exists to catch.
const maxSequenceLength = helperproto.MaxSequenceLength

// OnnxEmbedder runs the real BGE model via ONNX Runtime. CGO — needed by
// the onnxruntime_go binding — is confined to this file (and main.go's
// environment setup) and never touches the daemon.
//
// ort.SetSharedLibraryPath and ort.InitializeEnvironment are process-global
// state in onnxruntime_go, not per-instance; main() calls them exactly once
// before constructing an OnnxEmbedder.
type OnnxEmbedder struct {
	tokenizer *tokenizer.Tokenizer
	session   *ort.DynamicAdvancedSession
}

// NewOnnxEmbedder loads the tokenizer and opens an inference session for
// the int8 BGE model found under modelDir (model_int8.onnx + tokenizer.json,
// as fetched by the daemon's EnsureModelFiles).
//
// intraOpThreads is a DIAGNOSTIC, and zero -- the production value -- means
// "pass no session options at all", which is byte-for-byte what this function
// did before the parameter existed.
//
// WHY IT EXISTS. Two CI runners embedding a byte-identical corpus on
// 2026-08-30 produced vectors that differed in the third and fourth decimal,
// enough to reorder near-ties and move the locate eval by three queries. Two
// mechanisms explain that equally well and only one is testable on a single
// machine: ONNX Runtime sizes its intra-op thread pool from the host's core
// count, and a different pool size reduces a sum in a different ORDER, which
// for floats is a different answer. The other candidate is the CPU itself
// dispatching different SIMD kernels, which no knob here can change.
//
// So this parameter is the instrument that tells those two apart --
// TestEmbeddingVariesWithThreadCount in the daemon's eval suite drives it --
// and NOT a fix. Pinning the default would be a performance change to every
// user's first index and has to be measured on its own terms.
func NewOnnxEmbedder(modelDir string, intraOpThreads int) (*OnnxEmbedder, error) {
	tk, err := pretrained.FromFile(filepath.Join(modelDir, "tokenizer.json"))
	if err != nil {
		return nil, fmt.Errorf("loading tokenizer.json: %w", err)
	}

	// nil options is not the same as default-valued options: it is the path
	// this code took for its whole life, and production must keep taking it.
	var opts *ort.SessionOptions
	if intraOpThreads > 0 {
		opts, err = ort.NewSessionOptions()
		if err != nil {
			return nil, fmt.Errorf("creating ONNX session options: %w", err)
		}
		// Destroyed as soon as the session exists: onnxruntime_go documents
		// the options as consumed by the constructor, not retained by it.
		defer func() { _ = opts.Destroy() }()
		if err := opts.SetIntraOpNumThreads(intraOpThreads); err != nil {
			return nil, fmt.Errorf("setting intra-op thread count to %d: %w", intraOpThreads, err)
		}
	}

	inputNames := []string{"input_ids", "attention_mask", "token_type_ids"}
	outputNames := []string{"last_hidden_state"}
	session, err := ort.NewDynamicAdvancedSession(filepath.Join(modelDir, "model_int8.onnx"), inputNames, outputNames, opts)
	if err != nil {
		return nil, fmt.Errorf("opening ONNX session for model_int8.onnx: %w", err)
	}

	return &OnnxEmbedder{tokenizer: tk, session: session}, nil
}

// Close releases the ONNX session. It does not call ort.DestroyEnvironment
// — that's process-global and main()'s responsibility.
func (e *OnnxEmbedder) Close() error {
	return e.session.Destroy()
}

// tokenized holds one text's token ids/type-ids/attention-mask, already
// truncated to maxSequenceLength if needed.
type tokenized struct {
	ids, typeIDs, mask []int
}

func (e *OnnxEmbedder) tokenize(text string) (tokenized, error) {
	// addSpecialTokens=true is NOT optional here: sugarme/tokenizer's
	// EncodeSingle defaults it to false, which silently omits [CLS]/[SEP]
	// rather than erroring — and without a real [CLS] token, "CLS pooling"
	// below would actually be pooling an arbitrary first word instead.
	en, err := e.tokenizer.EncodeSingle(text, true)
	if err != nil {
		return tokenized{}, err
	}

	ids := en.GetIds()
	typeIDs := en.GetTypeIds()
	mask := en.GetAttentionMask()

	if len(ids) > maxSequenceLength {
		ids = truncateKeepingFinalToken(ids, maxSequenceLength)
		typeIDs = truncateKeepingFinalToken(typeIDs, maxSequenceLength)
		mask = truncateKeepingFinalToken(mask, maxSequenceLength)
	}

	return tokenized{ids: ids, typeIDs: typeIDs, mask: mask}, nil
}

// truncateKeepingFinalToken truncates to maxLen while preserving the last
// element (the [SEP] token, in practice) so a truncated sequence still ends
// on a proper boundary rather than being cut off mid-sequence.
func truncateKeepingFinalToken(ids []int, maxLen int) []int {
	if len(ids) <= maxLen {
		return ids
	}
	out := make([]int, maxLen)
	copy(out, ids[:maxLen-1])
	out[maxLen-1] = ids[len(ids)-1]
	return out
}

// inferenceGate lets ONE inference run at a time in this process, whichever
// model it is for.
//
// It is the second half of the memory bound described on Embed. One text per
// inference bounds what a REQUEST can cost; this bounds what several requests
// arriving together can cost, which was the same bill by another route.
// MEASURED 2026-10-08, one 512-token text per request, all sent at once: 8
// requests 371 MB, 16 requests 666 MB, 32 requests 1,229 MB -- and none of it
// handed back.
//
// What it costs: those 32 took 79.7 ms a text side by side, against 87 ms a
// text in turn. Nothing sends embeds side by side for speed (an index build
// sends one batch at a time), so that is a price nobody was paying for
// anything. A search that arrives during an index build now waits for the one
// inference in flight, under a tenth of a second, instead of sharing the cores
// with a whole batch.
var inferenceGate sync.Mutex

// runOne runs session over ONE tokenized text and returns its float output,
// which the caller destroys. See Embed for why it is never more than one.
func runOne(session *ort.DynamicAdvancedSession, t tokenized) (*ort.Tensor[float32], error) {
	n := len(t.ids)
	inputIDs := make([]int64, n)
	attnMask := make([]int64, n)
	tokenTypeIDs := make([]int64, n)
	for j := range t.ids {
		inputIDs[j] = int64(t.ids[j])
		attnMask[j] = int64(t.mask[j])
		tokenTypeIDs[j] = int64(t.typeIDs[j])
	}

	// The input tensors are released when runOne returns. Destroy's error is
	// discarded on purpose: there is nothing to do about a failed release of a
	// tensor this call is finished with.
	shape := ort.NewShape(1, int64(n))
	inputIDsT, err := ort.NewTensor(shape, inputIDs)
	if err != nil {
		return nil, fmt.Errorf("building input_ids tensor: %w", err)
	}
	defer func() { _ = inputIDsT.Destroy() }()
	attnMaskT, err := ort.NewTensor(shape, attnMask)
	if err != nil {
		return nil, fmt.Errorf("building attention_mask tensor: %w", err)
	}
	defer func() { _ = attnMaskT.Destroy() }()
	tokenTypeIDsT, err := ort.NewTensor(shape, tokenTypeIDs)
	if err != nil {
		return nil, fmt.Errorf("building token_type_ids tensor: %w", err)
	}
	defer func() { _ = tokenTypeIDsT.Destroy() }()

	outputs := []ort.Value{nil}
	inferenceGate.Lock()
	err = session.Run([]ort.Value{inputIDsT, attnMaskT, tokenTypeIDsT}, outputs)
	inferenceGate.Unlock()
	if err != nil {
		return nil, fmt.Errorf("running inference: %w", err)
	}
	out, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		if outputs[0] != nil {
			_ = outputs[0].Destroy()
		}
		return nil, fmt.Errorf("unexpected output tensor type %T", outputs[0])
	}
	return out, nil
}

// Embed tokenizes and runs inference for texts, returning one L2-normalized
// 384-dim CLS-pooled vector per input. It has no notion of "query" vs
// "document" — that asymmetry is applied by the daemon's BgeEmbedder,
// before text ever reaches here.
//
// ONE TEXT PER INFERENCE, HOWEVER MANY ARRIVE. A request is still a batch on
// the wire; it is no longer a batch in the model.
//
// FOUND 2026-10-08 in a running product: the helper of a daemon that had been
// watching this repository for two hours held 6.0 GB on a 15 GB machine with
// no swap, 500 MB was left, and the load average reached 123. Embed used to
// pack a whole request into one [texts, longest] tensor, and the model's
// working memory is proportional to that tensor. Measured that day with the
// real model, 512-token chunks, a fresh helper per row:
//
//	texts in one inference    peak        ms per text
//	  1                        110 MB      79
//	  8                        399 MB     100
//	 16                        710 MB     107
//	 40  (an index batch)    1,634 MB     102
//	 58  (agentloop.go)      2,309 MB     105
//	109  (clients/tui/chat.go) 4,281 MB   104
//
// About 40 MB a text, and ONNX Runtime's arena keeps what it once needed: the
// memory after each call was the peak. The big rows are not an index build --
// that sends 40 at a time -- they are ONE SAVE of one file, because the
// watcher re-embeds every chunk of a changed file in a single request
// (daemon/reindex.go). Nine saves of ordinary files, replayed in a row, left
// 2,938 MB behind.
//
// The column on the right is why the answer is one and not eight: batching
// buys no speed on a CPU. The same 58 chunks in groups of 1, 2, 4, 8 and 16
// took 5.06, 5.76, 5.79, 5.74 and 5.86 seconds and peaked at 183, 241, 395,
// 704 and 1,384 MB. One at a time is the fastest row and the smallest, since a
// text alone is never padded to a longer neighbour's length.
//
// THE CAP IS HERE, IN THE HELPER, and not a batch size the callers are asked
// to respect: the daemon had one caller that batched (the index build, at 40,
// chosen for a deadline) and one that did not, and a bound that the next
// caller has to remember is the bound that was missing.
//
// IT ALSO MAKES A VECTOR A FUNCTION OF ITS TEXT. That was not true, and
// nothing had noticed. This is an int8 model: its quantised layers take their
// ranges from the whole input tensor, so a chunk's vector depended on which
// other chunks shared its inference. Measured on those 58 chunks: embedded
// together and embedded apart, the same text gave vectors as far apart as
// cosine 0.994 (0.02 in one component), and the five nearest neighbours of a
// chunk were the same five for only 27 of the 58. An index therefore held
// whatever the walk order and the batch size happened to group -- a file added
// near the top moved every boundary after it -- while every QUERY has always
// been embedded alone. Now both are. An index built before this is no worse
// than it was (searches over it are unchanged, bit for bit) and stops mixing
// the two as files are saved or the index is rebuilt.
func (e *OnnxEmbedder) Embed(texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// Every text is tokenized before any is embedded, so a request with a text
	// the tokenizer refuses still fails before it costs an inference.
	toks := make([]tokenized, len(texts))
	for i, text := range texts {
		t, err := e.tokenize(text)
		if err != nil {
			return nil, fmt.Errorf("tokenizing text %d: %w", i, err)
		}
		toks[i] = t
	}

	vecs := make([][]float32, len(texts))
	for i, t := range toks {
		vec, err := e.embedOne(t)
		if err != nil {
			return nil, fmt.Errorf("text %d of %d: %w", i, len(texts), err)
		}
		vecs[i] = vec
	}
	return vecs, nil
}

// embedOne runs the model over one tokenized text and returns its normalized
// [CLS] vector.
func (e *OnnxEmbedder) embedOne(t tokenized) ([]float32, error) {
	out, err := runOne(e.session, t)
	if err != nil {
		return nil, err
	}
	defer func() { _ = out.Destroy() }()

	outShape := out.GetShape()
	if len(outShape) != 3 || outShape[0] != 1 {
		return nil, fmt.Errorf("unexpected output shape %v, want [1, seq, hidden]", outShape)
	}
	hidden := int(outShape[2])
	if hidden != embedDim {
		return nil, fmt.Errorf("model produced %d-dim hidden state, want %d", hidden, embedDim)
	}
	// [CLS] is position 0, so its hidden state is the first `hidden` floats.
	data := out.GetData()
	if len(data) < hidden {
		return nil, fmt.Errorf("model returned %d values, fewer than one %d-dim hidden state", len(data), hidden)
	}
	cls := make([]float32, hidden)
	copy(cls, data[:hidden])
	return l2Normalize(cls), nil
}

func l2Normalize(v []float32) []float32 {
	var sumSq float64
	for _, x := range v {
		sumSq += float64(x) * float64(x)
	}
	if sumSq == 0 {
		return v // degenerate (all-zero) vector: nothing sensible to scale
	}
	norm := float32(math.Sqrt(sumSq))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / norm
	}
	return out
}
