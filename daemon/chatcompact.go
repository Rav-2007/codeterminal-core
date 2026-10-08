package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mochiii/protocol"
)

// /compact, AS A SUMMARY. The clients' /compact used to drop all but the last
// eight turns from what they sent the model; on a short chat it said "already
// compact" and on a long one it threw context away with nothing in its place.
// This folds the older part of the chat into a summary written by the model,
// keeps the most recent exchanges word for word, and makes that the current
// chat -- the way Claude Code compacts.
//
// NOTHING IS LOST. The whole chat is saved to history first (saveChatLocked),
// and the link to that saved copy is then cleared, so the compacted chat is a
// NEW chat: saving it later adds an entry instead of overwriting the full one.

const (
	// compactKeepRecent turns stay verbatim after the summary; the cut is moved
	// back to a user turn so the kept part starts where an exchange starts.
	compactKeepRecent = 4
	// compactMinTurns: below this there is nothing worth summarising.
	compactMinTurns = 6
	// compactInputChars bounds what is sent to be summarised (compactTranscript
	// says what gives way when the chat is bigger).
	compactInputChars = 60_000
	// A provider that refuses the summary request as too large gets a smaller
	// one, up to compactShrinkAttempts times and never below
	// compactMinInputChars. MEASURED 2026-10-06: Groq's free tier takes at most
	// 7,000 input tokens a minute on qwen3.8-27b, so a 46-message chat (13,460
	// tokens) was refused outright -- and every chat long enough to need
	// /compact is past that.
	compactShrinkAttempts = 4
	compactMinInputChars  = 2_000
	// compactMinExcerpt is the shortest a message is trimmed to before the
	// earliest messages are dropped instead.
	compactMinExcerpt = 200
)

var errNothingToCompact = errors.New("nothing to compact yet")

// Not only about code: chats in this panel are as often about a document, a
// pitch or a form. MEASURED 2026-10-06: a prompt that said "coding assistant"
// and asked for "file names, functions, commands" had the model answer a chat
// about a pitch deck with "no code or repository was provided" -- a summary of
// what was absent.
const compactSystemPrompt = "You compress a conversation between a user and an AI assistant so it can " +
	"continue later from your summary alone. Write at most 250 words, as short bullet points, one group per " +
	"topic in the order they came up: what the user wanted, what was found, decided or produced, and the " +
	"exact details that matter later (names, numbers, reference ids, file names, commands, errors). End with " +
	"what is still open. Summarise only what the conversation contains: never mention what it lacks, do not " +
	"invent anything, do not give advice, and do not address the user. Text marked […] was trimmed for length."

// compactChat answers HistoryCompact. Besides the entry, the new chat and how
// many turns were folded, it returns the chat's revision after the replace
// (chatsync.go): the turns it returns are the whole new chat, so the client
// that compacted is level with it.
func (s *Server) compactChat(ctx context.Context, archive *chatArchive, spec, tier string) (protocol.HistoryEntry, []protocol.Turn, int, string, error) {
	// Held throughout, model call included: an exchange finishing meanwhile must
	// not be appended to a chat that is about to be replaced. The client holds
	// its input while this runs, and the call is one short completion.
	s.historyMu.Lock()
	defer s.historyMu.Unlock()

	turns, err := s.memory.LoadAllTurns(ctx, s.workspace)
	if err != nil {
		return protocol.HistoryEntry{}, nil, 0, "", err
	}
	cut := len(turns) - compactKeepRecent
	for cut > 0 && turns[cut].Role != "user" {
		cut--
	}
	if len(turns) < compactMinTurns || cut < 2 {
		return protocol.HistoryEntry{}, nil, 0, "", fmt.Errorf("%w: this chat has %d messages, and /compact summarises once there are more than %d",
			errNothingToCompact, len(turns), compactMinTurns-1)
	}

	id, h, _, err := s.saveChatLocked(ctx, archive, spec, "")
	if err != nil {
		return protocol.HistoryEntry{}, nil, 0, "", fmt.Errorf("saving the full chat before compacting: %w", err)
	}

	summary, err := s.summarizeTurns(ctx, turns[:cut], tier)
	if err != nil {
		return protocol.HistoryEntry{}, nil, 0, "", err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	title := h.displayTitle()
	// The first prompt is the chat's title in history, so it is only that; the
	// explanation goes with the summary.
	compacted := []storedTurn{
		{Role: "user", CreatedAt: now, Content: "Continuing: " + title},
		{Role: "assistant", CreatedAt: now, Content: "Summary of the conversation so far (the earlier part was " +
			"summarised by /compact; the full chat is saved in history):\n\n" + summary},
	}
	compacted = append(compacted, turns[cut:]...)
	if err := s.memory.ReplaceWorkspace(ctx, s.workspace, compacted); err != nil {
		return protocol.HistoryEntry{}, nil, 0, "", fmt.Errorf("replacing the chat with its summary: %w", err)
	}
	archive.clearLink()
	rev := s.chatReplacedLocked(ctx)

	out := make([]protocol.Turn, len(compacted))
	for i, t := range compacted {
		out[i] = protocol.Turn{Role: t.Role, Content: t.Content}
	}
	return s.entryFor(id, h, false), out, cut, rev, nil
}

// summarizeTurns asks the model the client has chosen (tier, as a prompt's
// Tier) for the summary, through the same provider path and retry policy as an
// ordinary turn. A refusal as too large is answered with a smaller request
// (shrinkCompactBudget); any other failure is returned as it is.
func (s *Server) summarizeTurns(ctx context.Context, turns []storedTurn, tier string) (string, error) {
	decision := s.route("", tier)
	routing := s.tierConfig().routingFor(decision.Tier)
	budget := compactInputChars
	for attempt := 0; ; attempt++ {
		transcript := compactTranscript(turns, budget)
		summary, err := s.summarizeOnce(ctx, decision.Slug, routing, transcript)
		var me *ModelError
		if err == nil || !errors.As(err, &me) || me.Class != ClassContextTooLarge || attempt == compactShrinkAttempts {
			return summary, err
		}
		next := shrinkCompactBudget(len(transcript), me.detail)
		if next < compactMinInputChars {
			return "", err
		}
		s.logger.Printf("compact: %s refused a %d-character summary request as too large; trying %d", decision.Slug, len(transcript), next)
		budget = next
	}
}

func (s *Server) summarizeOnce(ctx context.Context, model string, routing providerRouting, transcript string) (string, error) {
	key, base := s.credentials()
	var out strings.Builder
	_, err := streamWithRetry(ctx, base, key, model,
		[]chatMessage{
			{Role: "system", Content: compactSystemPrompt},
			{Role: "user", Content: "Summarise this conversation:\n\n" + transcript},
		},
		nil, routing,
		func(tok string) error { out.WriteString(tok); return nil },
		nil, nil, nil, s.logger)
	if err != nil {
		return "", fmt.Errorf("the model could not write the summary: %w", err)
	}
	summary := strings.TrimSpace(out.String())
	if summary == "" {
		return "", errors.New("the model returned an empty summary")
	}
	return summary, nil
}

// limitRequested reads "Limit 7000, Requested 13460" -- how Groq and OpenAI
// word a request over a token limit -- so the next request can be sized to fit
// in one step instead of by repeated halving.
var limitRequested = regexp.MustCompile(`(?i)limit:?\s*(\d+),\s*(?:used\s*\d+,\s*)?requested:?\s*(\d+)`)

// shrinkCompactBudget sizes the next summary request after one of size chars
// was refused as too large: scaled by the provider's own limit/requested
// figures when it gave them, with a margin for tokenisation and the system
// prompt, else halved. Either way the next try is smaller.
func shrinkCompactBudget(size int, detail string) int {
	next := size / 2
	if m := limitRequested.FindStringSubmatch(detail); m != nil {
		limit, _ := strconv.Atoi(m[1])
		requested, _ := strconv.Atoi(m[2])
		if limit > 0 && requested > limit {
			next = int(float64(size) * float64(limit) / float64(requested) * 0.85)
		}
	}
	return next
}

// compactTranscript renders turns as "User: ... / Assistant: ..." in at most
// budget bytes. Over budget, the LONGEST messages give way first: every message
// is cut to one shared length, the largest that fits, so each exchange is still
// there for the summary (it is the long answers -- tables, code -- that fill a
// chat, while the questions that say what the chat was about are short). Only
// when even compactMinExcerpt per message does not fit are the earliest
// messages dropped, the newest being closest to where the chat continues.
func compactTranscript(turns []storedTurn, budget int) string {
	type line struct{ who, text string }
	lines := make([]line, len(turns))
	longest := 0
	for i, t := range turns {
		who := "User"
		if t.Role == "assistant" {
			who = "Assistant"
		}
		lines[i] = line{who, strings.TrimSpace(t.Content)}
		longest = max(longest, len(lines[i].text))
	}
	render := func(from, limit int) string {
		var b strings.Builder
		if from > 0 {
			b.WriteString("(earliest part omitted)\n\n")
		}
		for _, l := range lines[from:] {
			text := l.text
			if len(text) > limit {
				text = clipUTF8(text, limit) + " […]"
			}
			fmt.Fprintf(&b, "%s: %s\n\n", l.who, text)
		}
		return b.String()
	}
	if out := render(0, longest); len(out) <= budget {
		return out
	}
	for from := range lines {
		if len(render(from, compactMinExcerpt)) > budget {
			continue
		}
		lo, hi := compactMinExcerpt, longest
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if len(render(from, mid)) <= budget {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		return render(from, lo)
	}
	return clipUTF8(render(len(lines)-1, compactMinExcerpt), budget)
}
