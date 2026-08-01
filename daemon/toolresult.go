package main

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// The egress choke point for tool output.
//
// THIS IS THE MOST CONSEQUENTIAL FILE IN AGENT MODE, and it is worth being
// plain about why. A tool result is not a local artefact: it is appended to the
// message list and POSTed to the hosted completion provider on the NEXT
// iteration. So every byte a tool returns leaves the machine, and a loop that
// reads five files has sent five files' contents to a third party.
//
// That is a genuine widening of what this product transmits, and the honest
// version of the privacy claim has to account for it (see the Phase 7 docs
// item). What this file does is make the widening no worse than the retrieval
// path already is: the same structural scrub, the same warn-mode detectors, the
// same delimiter neutralisation.
//
// It deliberately MIRRORS renderChunk (context.go). Two rules copied from there
// rather than reinvented:
//
//  1. Scrubbing and truncation happen in ONE function, so the bytes that go out
//     and the bytes the budget counted are the same bytes. Sizing a result
//     somewhere else and scrubbing it here is how those two quietly diverge.
//  2. scrub() runs BEFORE neutralizeDelimiters, so secret detection sees the
//     original bytes.
//
// Structural signatures only, exactly as on the chunk path: opaque secrets with
// no recognisable prefix are not closed here either. Tool output is arguably a
// higher-risk surface than indexed chunks -- an MCP server can return anything,
// including a file the indexer would have skipped -- which is a reason to
// revisit the entropy decision, not a reason to pretend this closes it.

// truncationNotice is appended when a result is clipped. Announced rather than
// silent because a model given a silently truncated result reasons about the
// missing part as though it were absent rather than hidden -- and may call the
// same tool again to get "the rest", burning an iteration.
const truncationMarker = "\n\n[... truncated by codeterminal: %d of %d bytes shown ...]"

// renderToolResult prepares one tool's output for the model.
//
// Returns the rendered text, the redaction kinds found (for the client's
// Redactions notice, kinds only -- never the matched text), and the byte count
// actually emitted, which is what the turn's cumulative egress budget counts.
func renderToolResult(content string, maxBytes int, scrubDisabled bool) (rendered string, kinds []string, emitted int) {
	// Truncate FIRST, on the raw bytes, then scrub what survives.
	//
	// The order matters and the other way round is a real bug: scrubbing first
	// changes the length (a 40-char key becomes "[REDACTED:openai_key]"), so a
	// cap applied afterwards would cut at a position computed from different
	// text -- and could slice a redaction placeholder in half, leaving
	// "[REDACTED:openai_k" in the outbound body. Cutting first means the cap is
	// applied to what the tool actually said, and every surviving byte is then
	// scrubbed whole.
	truncated := false
	original := len(content)
	if maxBytes > 0 && len(content) > maxBytes {
		content = clipUTF8(content, maxBytes)
		truncated = true
	}

	cleaned, redactions := scrub(content, scrubDisabled)
	out := neutralizeDelimiters(cleaned)

	// Control characters go last, after scrub has seen the original bytes and
	// after the delimiters are neutralised, so neither of those is reading text
	// this already altered.
	out, controls := stripControlCharacters(out)
	if controls > 0 {
		out += fmt.Sprintf(controlMarker, controls)
	}

	if truncated {
		out += fmt.Sprintf(truncationMarker, len(content), original)
	}
	return out, redactionKinds(redactions), len(out)
}

// controlMarker is appended when control characters were removed, for the same
// reason truncationMarker exists: a silent edit leaves the model reasoning
// about text nobody sent.
const controlMarker = "\n\n[... %d control character(s) removed by codeterminal ...]"

// stripControlCharacters removes terminal control codes from tool output.
//
// WHY THIS IS HERE AND NOT AT THE CLIENT. Tool output does not reach a client
// directly -- protocol.ToolActivity carries a byte count, never bytes -- so the
// reachable path is longer: an unconfined subprocess returns escapes, they
// enter the model's context, and the model echoes some of them into an answer
// that DOES stream to a terminal. That is a weaker path than the tool-name one
// (see mcp.ValidateToolName, which is the direct one), and it is cheap to close
// at the choke point that already exists for exactly this class of bytes.
//
// DROPPED, NOT ESCAPED. Escaping \x1b as the four characters "\x1b" keeps more
// information, and would let a server inflate its egress roughly fourfold in
// tokens the user pays for -- after the byte cap has already been applied.
// Dropping is announced instead, which is this file's existing idiom.
//
// \n and \t survive: they are text, and tool output is full of both. \r does
// not, because on its own it returns the cursor to the start of the line and
// overwrites what is there, which is the behaviour being removed.
//
// IT COPIES ORIGINAL BYTES, NEVER RE-ENCODED RUNES, and that is not a detail.
// The first version used strings.Map, which decodes as UTF-8 and re-encodes
// whatever the mapping returns -- so every invalid byte became U+FFFD and grew
// from one byte to three. FuzzRenderToolResult caught it at 9.6s: 248 bytes of
// cap, 761 bytes out. A tool returning binary-ish output would have had its
// egress TRIPLED, silently, after the byte cap had already been applied --
// which is precisely the inflation this function chose dropping over escaping
// to avoid. Invalid bytes are now passed through unchanged; they were already
// there before this function ran, and making them bigger helps nobody.
func stripControlCharacters(s string) (string, int) {
	removed := 0
	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])

		// An invalid byte decodes as RuneError with size 1. Kept as itself,
		// except for a lone C1 introducer -- 0x9b is what a terminal reads as
		// CSI, and it is invalid UTF-8, so a rune-level check would never see
		// it.
		if r == utf8.RuneError && size == 1 {
			if s[i] >= 0x80 && s[i] <= 0x9f {
				removed++
			} else {
				b.WriteByte(s[i])
			}
			i++
			continue
		}

		if r != '\n' && r != '\t' && (r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)) {
			removed++
			i += size
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String(), removed
}

// clipUTF8 cuts s to at most n bytes without splitting a multi-byte rune.
//
// A naive s[:n] can leave a partial rune, which json.Marshal then re-encodes as
// U+FFFD -- so a tool returning UTF-8 would get a replacement character spliced
// into the model's view of its own output at exactly the truncation boundary.
func clipUTF8(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	// Back off to the last rune boundary at or before n. Continuation bytes are
	// 0b10xxxxxx.
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// toolResultMessage builds the "tool" role message carrying one result back to
// the model.
//
// The role is "tool" and it NEVER reaches protocol.Turn or conversation memory:
// validTurn (history.go) rejects any role but user/assistant as an injection
// defense, and that defense is not being widened for this. These messages live
// only in the in-memory list for the duration of one turn (D11).
func toolResultMessage(call toolCall, content string) chatMessage {
	return chatMessage{
		Role:       "tool",
		ToolCallID: call.ID,
		Name:       call.Function.Name,
		Content:    content,
	}
}

// assistantToolCallMessage echoes the model's own tool-call request back into
// the conversation, which the provider requires before the matching tool
// results: without it the results have nothing to pair with and the provider
// rejects the request.
//
// Content is preserved because a model may say something ("let me check that
// file") in the same turn it calls a tool, and dropping it loses a piece of the
// reply the user already watched stream past.
func assistantToolCallMessage(content string, calls []toolCall) chatMessage {
	return chatMessage{Role: "assistant", Content: content, ToolCalls: calls}
}

// summariseToolActivity renders a compact, human-readable account of what a
// turn's tools did, for the daemon log and for persistTurn's assistant text.
//
// Deliberately NOT the tool output itself. The conversation memory keeps what
// the model SAID; a transcript of everything its tools returned would balloon
// the stored turn and re-inject tool output into every future request through
// the history path, which is the one place tool content must not go.
func summariseToolActivity(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf("\n\n[used %d tool call(s): %s]", len(names), strings.Join(names, ", "))
}
