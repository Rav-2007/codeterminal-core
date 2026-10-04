package main

import (
	"fmt"
	"path/filepath"

	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
	ort "github.com/yalue/onnxruntime_go"
)

// CrossEncoder scores how well each passage answers a query, reading the two
// together -- which a bi-encoder like BGE cannot, since it embeds each alone.
//
// MEASURE ONLY as of 2026-10-04. The model is ms-marco-MiniLM-L-6-v2 (int8
// ONNX, Apache-2.0): a 6-layer BERT whose tokenizer.json is byte-identical to
// BGE's, so the vocabulary, the special tokens and maxSequenceLength carry
// over unchanged. The outside-repository sweep is the only caller; shipping it
// would put another model download on every user, which is the owner's call.
type CrossEncoder struct {
	tokenizer *tokenizer.Tokenizer
	session   *ort.DynamicAdvancedSession
}

// crossEncoderModelFile is the ONNX file NewCrossEncoder opens in its model
// directory.
const crossEncoderModelFile = "model_quantized.onnx"

// NewCrossEncoder opens the cross-encoder in modelDir. The ONNX Runtime
// environment must already be initialised (loadEmbedder does it).
func NewCrossEncoder(modelDir string, intraOpThreads int) (*CrossEncoder, error) {
	tk, err := pretrained.FromFile(filepath.Join(modelDir, "tokenizer.json"))
	if err != nil {
		return nil, fmt.Errorf("loading the cross-encoder's tokenizer.json: %w", err)
	}
	var opts *ort.SessionOptions
	if intraOpThreads > 0 {
		opts, err = ort.NewSessionOptions()
		if err != nil {
			return nil, fmt.Errorf("creating ONNX session options: %w", err)
		}
		defer func() { _ = opts.Destroy() }()
		if err := opts.SetIntraOpNumThreads(intraOpThreads); err != nil {
			return nil, fmt.Errorf("setting intra-op thread count to %d: %w", intraOpThreads, err)
		}
	}
	session, err := ort.NewDynamicAdvancedSession(filepath.Join(modelDir, crossEncoderModelFile),
		[]string{"input_ids", "attention_mask", "token_type_ids"}, []string{"logits"}, opts)
	if err != nil {
		return nil, fmt.Errorf("opening ONNX session for %s: %w", crossEncoderModelFile, err)
	}
	return &CrossEncoder{tokenizer: tk, session: session}, nil
}

// Close releases the ONNX session.
func (c *CrossEncoder) Close() error { return c.session.Destroy() }

// tokenizePair encodes query and passage as one BERT pair -- [CLS] query
// [SEP] passage [SEP], token types 0 then 1 -- truncated to maxSequenceLength
// the embedder's way, keeping the final [SEP]. The query comes first, so a
// long passage loses its end and never the question.
func (c *CrossEncoder) tokenizePair(query, passage string) (tokenized, error) {
	en, err := c.tokenizer.EncodePair(query, passage, true)
	if err != nil {
		return tokenized{}, err
	}
	ids, typeIDs, mask := en.GetIds(), en.GetTypeIds(), en.GetAttentionMask()
	if len(ids) > maxSequenceLength {
		ids = truncateKeepingFinalToken(ids, maxSequenceLength)
		typeIDs = truncateKeepingFinalToken(typeIDs, maxSequenceLength)
		mask = truncateKeepingFinalToken(mask, maxSequenceLength)
	}
	return tokenized{ids: ids, typeIDs: typeIDs, mask: mask}, nil
}

// Score returns one relevance score per passage, in order. Higher is more
// relevant; the scale is the model's logit and means nothing on its own.
func (c *CrossEncoder) Score(query string, passages []string) ([]float32, error) {
	if len(passages) == 0 {
		return nil, nil
	}
	toks := make([]tokenized, len(passages))
	maxLen := 0
	for i, p := range passages {
		t, err := c.tokenizePair(query, p)
		if err != nil {
			return nil, fmt.Errorf("tokenizing passage %d: %w", i, err)
		}
		toks[i] = t
		maxLen = max(maxLen, len(t.ids))
	}

	batch := len(passages)
	inputIDs := make([]int64, batch*maxLen)
	attnMask := make([]int64, batch*maxLen)
	tokenTypeIDs := make([]int64, batch*maxLen)
	for i, t := range toks {
		for j := range t.ids {
			idx := i*maxLen + j
			inputIDs[idx] = int64(t.ids[j])
			attnMask[idx] = int64(t.mask[j])
			tokenTypeIDs[idx] = int64(t.typeIDs[j])
		}
	}

	// The tensors are released when Score returns. Destroy's error is
	// discarded on purpose: there is nothing to do about a failed release of
	// a tensor this call is finished with, and the scores are already read.
	shape := ort.NewShape(int64(batch), int64(maxLen))
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
	if err := c.session.Run([]ort.Value{inputIDsT, attnMaskT, tokenTypeIDsT}, outputs); err != nil {
		return nil, fmt.Errorf("running the cross-encoder: %w", err)
	}
	out, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("unexpected logits tensor type %T", outputs[0])
	}
	defer func() { _ = out.Destroy() }()
	if shape := out.GetShape(); len(shape) != 2 || shape[0] != int64(batch) || shape[1] != 1 {
		return nil, fmt.Errorf("unexpected logits shape %v, want [%d 1]", shape, batch)
	}
	return append([]float32(nil), out.GetData()...), nil
}
