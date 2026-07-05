package main

import "hash/fnv"

// stubDim matches the real model's output width (384) so that anything
// exercising the wire protocol in this step sees a correctly shaped vector.
const stubDim = 384

// stubEmbed is a placeholder embedding function: it proves the helper can
// receive texts and return real-shaped, deterministic vectors over the
// wire. It is NOT the real BGE model and is NOT semantically meaningful —
// no ONNX Runtime, no tokenizer, no model file is involved. This entire
// file is replaced by real ONNX Runtime inference in the next step; nothing
// outside this file depends on how it computes its output.
func stubEmbed(texts []string) [][]float32 {
	vecs := make([][]float32, len(texts))
	for i, text := range texts {
		vecs[i] = stubEmbedOne(text)
	}
	return vecs
}

func stubEmbedOne(text string) []float32 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	seed := h.Sum64()

	vec := make([]float32, stubDim)
	vec[seed%stubDim] = 1
	return vec
}
