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
//
// One pair per inference, for the reasons on OnnxEmbedder.Embed: thirty
// 512-token passages in one tensor is the same gigabyte there, and a passage's
// score no longer depends on which other passages it was scored beside.
func (c *CrossEncoder) Score(query string, passages []string) ([]float32, error) {
	if len(passages) == 0 {
		return nil, nil
	}
	toks := make([]tokenized, len(passages))
	for i, p := range passages {
		t, err := c.tokenizePair(query, p)
		if err != nil {
			return nil, fmt.Errorf("tokenizing passage %d: %w", i, err)
		}
		toks[i] = t
	}

	scores := make([]float32, len(passages))
	for i, t := range toks {
		score, err := c.scoreOne(t)
		if err != nil {
			return nil, fmt.Errorf("passage %d of %d: %w", i, len(passages), err)
		}
		scores[i] = score
	}
	return scores, nil
}

// scoreOne runs the cross-encoder over one tokenized pair.
func (c *CrossEncoder) scoreOne(t tokenized) (float32, error) {
	out, err := runOne(c.session, t)
	if err != nil {
		return 0, err
	}
	defer func() { _ = out.Destroy() }()
	if shape := out.GetShape(); len(shape) != 2 || shape[0] != 1 || shape[1] != 1 {
		return 0, fmt.Errorf("unexpected logits shape %v, want [1 1]", shape)
	}
	data := out.GetData()
	if len(data) != 1 {
		return 0, fmt.Errorf("the cross-encoder returned %d scores for one pair", len(data))
	}
	return data[0], nil
}
