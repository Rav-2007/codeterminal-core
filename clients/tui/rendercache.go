package main

import (
	"strings"

	"github.com/muesli/termenv"

	"github.com/charmbracelet/lipgloss"
)

// THE TRANSCRIPT RENDER CACHE.
//
// THE DEFECT IT EXISTS FOR: refreshViewport re-rendered every turn in the
// conversation through Lip Gloss on every streamed token. Linear per token,
// quadratic per answer, and unbounded, because the transcript is only trimmed
// by an explicit /compact. It runs inside Update, which Bubble Tea does not
// coalesce at frame rate, so it is time the terminal is not repainting and
// keystrokes are not being read.
//
// MEASURED per token, allocations, at 0/30/120/240 prior turns:
//
//	renderTranscript      6 -> 179 -> 679 -> 1341     <- all of the growth
//	wrapToWidth           4 ->  14 ->  16 ->   17     <- already flat
//	viewport.SetContent   1 ->   1 ->   1 ->    1     <- already flat
//
// So the cache covers exactly one stage. Wrapping still happens once, over the
// whole concatenated transcript, exactly as before -- see wrapToWidth's own
// note. That is deliberate: wrapping is already allocation-flat, and leaving it
// untouched means the wrapped bytes are computed the same way they always were,
// with no cache boundary inside them for wrap state to cross.
//
// VALIDATED, NOT INVALIDATED. This is the design decision, and it is the whole
// reason the thing is trustworthy.
//
// The usual shape for a render cache is a set of invalidation hooks: something
// changes, and whoever changed it remembers to drop the cache. That shape fails
// silently. A missed hook does not crash and does not fail a test -- it shows
// the user a STALE TRANSCRIPT, text that is no longer what the model said, and
// nothing in the program is in a position to notice.
//
// This cache instead stores, beside each rendered block, THE INPUTS IT WAS
// RENDERED FROM, and checks them on every use. A block is reused only if the
// turn at that index still has the same role, the same text and the same
// reasoning. So a mutation nobody thought of does not produce stale output; it
// produces a cache miss and a re-render.
//
// The check is O(1) per turn in the common case. Go compares strings by data
// pointer and length first, and an unmutated turn hands back the identical
// string header, so the comparison never reaches a byte. A mutated turn has a
// different header and almost always a different length, which also answers in
// the header.
//
// EVERY WAY THE TRANSCRIPT CHANGES, and how the check covers it. This is the
// enumeration task 3.2d asks for; the code above is its consequence.
//
//  1. appendTurn (chat.go)             a turn is added at the end.
//     Detected: index len(blocks) has no entry.
//     Discarded: nothing. Every earlier block
//     is still valid, which is the point.
//
//  2. token arrival, text +=           turns[streamAssistant].text grows on
//     (tokenMsg, reasoningMsg)         every token.
//     Detected: text differs from the cached
//     text at that index.
//     Discarded: that one block.
//
//  3. endStream flush                  the sanitizer's held tail is appended to
//     text and reasoning after the stream ends.
//     Detected: same as 2. This one matters --
//     it mutates a turn that is no longer the
//     one being streamed into.
//
//  4. activity line rewrite            m.turns[idx].text = line, where idx comes
//     (recordActivity, chat.go:1581)   from activityTurns[callID] and can be ANY
//     earlier index, not the last one. THIS IS
//     THE ONE AN INVALIDATION-HOOK DESIGN WOULD
//     HAVE MISSED: it rewrites a turn that has
//     long since been rendered and baked.
//     Detected: text differs at that index.
//     Discarded: that one block.
//
//  5. /compact                         turns are dropped from the FRONT, so
//     every surviving turn changes index.
//     Detected: the block cached at index i now
//     faces a different turn, whose text does
//     not match. Every entry misses.
//     Discarded: effectively all of them, and
//     the cache is truncated to the new length.
//
//  6. /clear and clearConversation     m.turns = nil.
//     Detected: the cache is truncated to
//     len(turns), which is zero.
//     Discarded: everything.
//
//  7. transcript eviction (3.5)        not yet implemented. It is the same shape
//     as /compact -- a drop from the front --
//     and needs no new handling here. Asserted
//     in advance by the compact-shaped test, so
//     3.5 cannot land a variant this misses.
//
//  8. width change                     a WindowSizeMsg changes viewport.Width.
//     renderSevered draws its rail to the edge
//     of the terminal, so a rendered block is
//     width-dependent.
//     Detected: width differs from the width
//     the cache was built at.
//     Discarded: everything.
//
//  9. colour profile change            no longer possible in production -- the
//     profile is pinned once at startup, see
//     renderprofile.go. It IS possible in
//     tests, which sweep profiles deliberately,
//     and a cache that ignored it would hand
//     those tests stale bytes and pass.
//     Detected: profile differs from the one
//     the cache was built at.
//     Discarded: everything.
//
//  10. theme change                    there is no theme input. The palette in
//     styles.go is constant and nothing
//     reassigns it; there is no setting, no
//     command and no message that changes it.
//     Listed because "we do not have one" is a
//     different answer from "it is handled",
//     and only the first one is true. If a
//     theme is ever added it belongs in the key
//     beside width and profile.
//
//  11. turns[i].incomplete = ...       changes no rendered byte: incomplete is
//     carried to the daemon by buildHistory and
//     is never drawn. Listed because it IS a
//     mutation of a cached turn, and the reason
//     it is safe is a fact about renderTranscript
//     that could stop being true.
//
//  12. re-render after an error        every error path ends in a refresh with
//     the same turns. No entry changes and none
//     should be discarded.
//
// WHAT IT COSTS. One extra copy of the rendered transcript, plus three string
// headers per turn. The turn text itself is not copied -- the cached key shares
// the turn's backing array. Bounding that growth is task 3.5's job, not this
// one's; today the transcript is unbounded with or without this cache.
//
// SAFE UNDER BUBBLE TEA'S VALUE-RECEIVER Update. The model is copied on every
// message, so two copies can share this slice's backing array and race to write
// different blocks at the same index. That cannot produce wrong output: an entry
// is only ever used when it matches the turn in front of it, so the worst case
// of a confused entry is a miss and a re-render.
type transcriptCache struct {
	// width and profile are the render inputs that are NOT per-turn. When either
	// moves, every block is stale at once.
	width   int
	profile termenv.Profile
	valid   bool

	blocks []cachedBlock
}

// cachedBlock is one turn's rendered output beside the inputs it came from.
// The three key fields are compared, never dereferenced for content.
type cachedBlock struct {
	role      turnRole
	text      string
	reasoning string
	out       string
}

// matches reports whether this block was rendered from exactly this turn.
func (b *cachedBlock) matches(t turn) bool {
	return b.role == t.role && b.text == t.text && b.reasoning == t.reasoning
}

// render returns the same bytes renderTranscript would, reusing the blocks of
// turns that have not changed.
//
// It is required to be byte-identical to renderTranscript for every input, and
// that is not a comment -- it is what the equivalence gate asserts over
// randomised transcripts and widths.
func (c *transcriptCache) render(turns []turn, width int) string {
	profile := lipgloss.ColorProfile()
	if !c.valid || c.width != width || c.profile != profile {
		c.width, c.profile, c.valid = width, profile, true
		c.blocks = c.blocks[:0]
	}
	// Shrink to fit: /compact, /clear and eviction all leave fewer turns than
	// the cache has blocks, and an entry past the end has nothing to validate
	// against.
	if len(c.blocks) > len(turns) {
		c.blocks = c.blocks[:len(turns)]
	}

	var b strings.Builder
	b.Grow(c.size() + 2*len(turns))
	for i := range turns {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if i < len(c.blocks) && c.blocks[i].matches(turns[i]) {
			b.WriteString(c.blocks[i].out)
			continue
		}
		out := renderTurnBlock(turns[i], width)
		blk := cachedBlock{role: turns[i].role, text: turns[i].text, reasoning: turns[i].reasoning, out: out}
		if i < len(c.blocks) {
			c.blocks[i] = blk
		} else {
			c.blocks = append(c.blocks, blk)
		}
		b.WriteString(out)
	}
	return b.String()
}

// size is the total length of the cached blocks, used only to size the builder.
// Approximate on purpose: a miss changes a block's length and the builder grows.
func (c *transcriptCache) size() int {
	n := 0
	for i := range c.blocks {
		n += len(c.blocks[i].out)
	}
	return n
}
