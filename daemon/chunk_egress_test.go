package main

import (
	"strings"
	"testing"
)

// TestBuildAugmentedUserMessage_ScrubsChunkContentOnTheEgressPath asserts the
// property the whole FAIL-1 chunk-exit finding is about, on the artifact that
// actually leaves the machine.
//
// Retrieved chunk text is not local-only. The query embedding is computed
// locally, but the chunk text itself is POSTed to the hosted completion
// provider on every grounded turn, folded into the message this function
// builds. Option A (scrub() inside renderChunk) is what stands between a
// secret in the index and the wire.
//
// Everything around this was already tested and none of it tested THIS:
// scrub_test.go covers the redactor, chunkscrub_test.go covers the detectors,
// and TestBuildAugmentedUserMessage_LabelsAndDelimitsChunks covers the
// formatting. A regression that dropped the scrub call from renderChunk would
// have left all three green. The BACKLOG records this property as "verified
// live before/after" -- a human ran it once, in July, by hand.
//
// Neuter-check: drop the scrub() call from renderChunk and this fails on the
// leaked key while every test named above still passes.
func TestBuildAugmentedUserMessage_ScrubsChunkContentOnTheEgressPath(t *testing.T) {
	const awsKey = "AKIAIOSFODNN7EXAMPLE"
	chunks := []Chunk{{
		FilePath:  "deploy/config.go",
		StartLine: 1,
		EndLine:   3,
		Class:     FileClassCode,
		Content:   "func creds() string {\n\treturn \"" + awsKey + "\"\n}",
	}}

	msg := buildAugmentedUserMessage("how do I deploy?", chunks, false)

	if strings.Contains(msg, awsKey) {
		t.Error("a structural secret in retrieved chunk content reached the outbound message")
	}
	if !strings.Contains(msg, "[REDACTED:aws_access_key]") {
		t.Errorf("expected a labeled placeholder in place of the key, got:\n%s", msg)
	}
	// Redaction must be span-scoped: the surrounding code is what makes the
	// chunk worth retrieving, and replacing the whole chunk would silently
	// degrade grounding rather than protect it.
	if !strings.Contains(msg, "func creds() string") {
		t.Error("scrubbing removed surrounding code; it must redact the span, not the chunk")
	}
}

// TestBuildAugmentedUserMessage_LeavesOpaqueSecretsAlone pins the CURRENT,
// deliberate state of the deferred detectors: warn-mode is log-only, and an
// opaque secret with no recognizable structure still goes out.
//
// This is not an endorsement of that behavior -- it is the open half of
// P3-FAIL-1, and docs/CHUNK_SCRUB_FIRE_RATE.md is the measurement for deciding
// it. The test exists so that flipping entropy or keyword detection to
// redacting is a deliberate act that breaks a test naming the decision, rather
// than something that happens quietly and gets discovered in a diff review.
//
// If you are here because this test failed after you enabled Design B or C:
// that is the test working. Confirm the founder decision was actually made,
// then update this test to assert the new behavior.
func TestBuildAugmentedUserMessage_LeavesOpaqueSecretsAlone(t *testing.T) {
	// High entropy, credential-named, but no structural prefix any scrubPattern
	// matches -- exactly the class Designs B and C were proposed to cover.
	const opaque = "aB3dEfGh1jKlMn0pQrStUvWxYz0123456789"
	chunks := []Chunk{{
		FilePath:  "deploy/config.go",
		StartLine: 1,
		EndLine:   2,
		Class:     FileClassCode,
		Content:   "api_key = \"" + opaque + "\"",
	}}

	msg := buildAugmentedUserMessage("how do I deploy?", chunks, false)

	if !strings.Contains(msg, opaque) {
		t.Error("an opaque secret was redacted; warn-mode is supposed to be LOG-ONLY " +
			"pending the Design B-vs-C decision (docs/CHUNK_SCRUB_FIRE_RATE.md). " +
			"If that decision was made, update this test rather than deleting it.")
	}
}

// TestBuildAugmentedUserMessage_NoScrubHonoursTheEscapeHatch confirms the
// --no-scrub flag reaches this far. It is a real escape hatch (a user debugging
// their own credential-handling code needs to see the real value), so it must
// work -- and it must be the ONLY way structural secrets get through, which is
// what the first test above pins from the other side.
func TestBuildAugmentedUserMessage_NoScrubHonoursTheEscapeHatch(t *testing.T) {
	const awsKey = "AKIAIOSFODNN7EXAMPLE"
	chunks := []Chunk{{
		FilePath: "deploy/config.go",
		Content:  `key = "` + awsKey + `"`,
	}}

	if msg := buildAugmentedUserMessage("q", chunks, true); !strings.Contains(msg, awsKey) {
		t.Error("--no-scrub did not reach the chunk render path")
	}
}
