package main

import (
	"fmt"
	"math"
	"path/filepath"

	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
	ort "github.com/yalue/onnxruntime_go"
)

// embedDim is the real model's output width: BGE-small's hidden size.
const embedDim = 384

// maxSequenceLength bounds tokenized input length. BGE's own
// model_max_length is 512, but code chunks (~40 lines) rarely need
// anywhere close to that, and bounding it keeps latency and memory
// predictable. Truncation preserves the final token (assumed [SEP]) so a
// truncated sequence still ends properly.
const maxSequenceLength = 256

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
func NewOnnxEmbedder(modelDir string) (*OnnxEmbedder, error) {
	tk, err := pretrained.FromFile(filepath.Join(modelDir, "tokenizer.json"))
	if err != nil {
		return nil, fmt.Errorf("loading tokenizer.json: %w", err)
	}

	inputNames := []string{"input_ids", "attention_mask", "token_type_ids"}
	outputNames := []string{"last_hidden_state"}
	session, err := ort.NewDynamicAdvancedSession(filepath.Join(modelDir, "model_int8.onnx"), inputNames, outputNames, nil)
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

// Embed tokenizes and runs inference for texts, returning one L2-normalized
// 384-dim CLS-pooled vector per input. It has no notion of "query" vs
// "document" — that asymmetry is applied by the daemon's BgeEmbedder,
// before text ever reaches here.
func (e *OnnxEmbedder) Embed(texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	toks := make([]tokenized, len(texts))
	maxLen := 0
	for i, text := range texts {
		t, err := e.tokenize(text)
		if err != nil {
			return nil, fmt.Errorf("tokenizing text %d: %w", i, err)
		}
		toks[i] = t
		if len(t.ids) > maxLen {
			maxLen = len(t.ids)
		}
	}

	batch := len(texts)
	inputIDs := make([]int64, batch*maxLen)
	attnMask := make([]int64, batch*maxLen)
	tokenTypeIDs := make([]int64, batch*maxLen)
	for i, t := range toks {
		for j := 0; j < len(t.ids); j++ {
			idx := i*maxLen + j
			inputIDs[idx] = int64(t.ids[j])
			attnMask[idx] = int64(t.mask[j])
			tokenTypeIDs[idx] = int64(t.typeIDs[j])
		}
		// Positions beyond len(t.ids) stay zero-valued: input_ids=0 is
		// [PAD] in this vocab, attention_mask=0 excludes them from
		// self-attention, token_type_ids=0 is the standard single-sequence
		// value. CLS pooling below only ever reads position 0, which is
		// always a real (non-padded) token, so right-padding never
		// affects which vector we extract — only the model's internal
		// attention computation, which attention_mask already handles.
	}

	shape := ort.NewShape(int64(batch), int64(maxLen))
	inputIDsT, err := ort.NewTensor(shape, inputIDs)
	if err != nil {
		return nil, fmt.Errorf("building input_ids tensor: %w", err)
	}
	defer inputIDsT.Destroy()

	attnMaskT, err := ort.NewTensor(shape, attnMask)
	if err != nil {
		return nil, fmt.Errorf("building attention_mask tensor: %w", err)
	}
	defer attnMaskT.Destroy()

	tokenTypeIDsT, err := ort.NewTensor(shape, tokenTypeIDs)
	if err != nil {
		return nil, fmt.Errorf("building token_type_ids tensor: %w", err)
	}
	defer tokenTypeIDsT.Destroy()

	outputs := []ort.Value{nil}
	if err := e.session.Run([]ort.Value{inputIDsT, attnMaskT, tokenTypeIDsT}, outputs); err != nil {
		return nil, fmt.Errorf("running inference: %w", err)
	}
	outTensor, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("unexpected output tensor type %T", outputs[0])
	}
	defer outTensor.Destroy()

	data := outTensor.GetData()
	outShape := outTensor.GetShape()
	if len(outShape) != 3 {
		return nil, fmt.Errorf("unexpected output shape %v, want [batch, seq, hidden]", outShape)
	}
	hidden := int(outShape[2])
	if hidden != embedDim {
		return nil, fmt.Errorf("model produced %d-dim hidden state, want %d", hidden, embedDim)
	}

	vecs := make([][]float32, batch)
	for i := 0; i < batch; i++ {
		clsStart := i * maxLen * hidden // CLS is position 0 of item i's sequence
		cls := make([]float32, hidden)
		copy(cls, data[clsStart:clsStart+hidden])
		vecs[i] = l2Normalize(cls)
	}
	return vecs, nil
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
