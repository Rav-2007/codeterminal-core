package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A REQUEST REFUSED AS TOO LARGE, AND WHAT CAN HONESTLY BE DONE ABOUT IT.
//
// The agent loop used to classify the refusal (ClassContextTooLarge) and stop,
// telling the user to /compact -- which, MEASURED 2026-10-24, could not have
// helped the turn that met it: a fresh chat, refused on its second call at
// 7,941 tokens against a 7,000-a-minute plan, four seconds after the first
// call had billed 6,345 of that same minute. Nothing removable was left, and
// no second call fits in a minute that already holds the first.
//
// So the loop now asks two questions of the provider's own figures. Could a
// smaller request get through? Then it removes what it can, says so in the
// answer, and sends it once more -- once per turn, never again. Can it not?
// Then the user is told the numbers and the two things that do change them.

// sizeLimit is what a provider said about a request it refused as too large.
type sizeLimit struct {
	limit, requested int
	// per is "minute" or "day" for a plan's allowance, "" for a limit on one
	// request (a context window).
	per string
	// output marks a limit on what the model may WRITE: no shorter request
	// changes it (MEASURED 2026-10-24: "output tokens per minute (OTPM): Limit
	// 1000, Requested 1448").
	output bool

	// Set by the agent loop when it found a smaller request could not help.
	// carried is what every request carries whatever is removed -- the
	// instructions, the tool list and the question -- in tokens.
	carried int
	// used is what an earlier step of the same answer took of the same minute.
	used int
}

// contextWindowWords reads a context-window refusal, which is worded unlike a
// plan's limit: "maximum context length is 8192 tokens. However, your messages
// resulted in 9191 tokens" (OpenAI) or "... you requested about 140211 tokens"
// (OpenRouter).
var contextWindowWords = regexp.MustCompile(`(?i)maximum context length is (\d+) tokens.*?(?:resulted in|requested about) (\d+) tokens`)

// sizeLimitFrom reads the figures out of a refusal's body, or returns the zero
// value when there are none or they do not describe a request over a limit.
func sizeLimitFrom(body string) sizeLimit {
	var s sizeLimit
	if m := limitRequested.FindStringSubmatch(body); m != nil {
		s.limit, _ = strconv.Atoi(m[1])
		s.requested, _ = strconv.Atoi(m[2])
		lower := strings.ToLower(body)
		switch {
		case strings.Contains(lower, "per minute"):
			s.per = "minute"
		case strings.Contains(lower, "per day"):
			s.per = "day"
		}
		s.output = strings.Contains(lower, "output tokens")
	} else if m := contextWindowWords.FindStringSubmatch(body); m != nil {
		s.limit, _ = strconv.Atoi(m[1])
		s.requested, _ = strconv.Atoi(m[2])
	}
	if s.limit <= 0 || s.requested <= s.limit {
		return sizeLimit{}
	}
	return s
}

func (s sizeLimit) known() bool { return s.limit > 0 }

// message is the refusal in the user's terms. Like every client message it
// names no host, status or provider (modelerror.go, clientMessages): the
// numbers are the user's own plan and request, parsed as integers, so they can
// carry nothing else.
func (s sizeLimit) message() string {
	options := "choose a model with a higher limit with /model, or connect a key on a plan that allows more with /connect"
	allows := fmt.Sprintf("your plan with this provider allows %s a %s", thousands(s.limit), s.per)
	if s.per == "" {
		options = "choose a model with a larger context window with /model"
		allows = fmt.Sprintf("the model takes at most %s", thousands(s.limit))
	}
	if s.output {
		per := s.per
		if per == "" {
			per = "request"
		}
		return fmt.Sprintf("the answer could run to %s tokens and your plan with this provider allows %s output tokens a %s; "+
			"a shorter chat does not change that — %s", thousands(s.requested), thousands(s.limit), per, options)
	}
	head := fmt.Sprintf("this request is %s tokens and %s", thousands(s.requested), allows)
	switch {
	case s.used > 0:
		return fmt.Sprintf("%s, and the step before it in this answer used %s of the same minute, so about one step a minute "+
			"fits; /compact does not change that — %s", head, thousands(s.used), options)
	case s.carried >= s.requested:
		return fmt.Sprintf("%s, and none of it could be left out — the instructions, the tool list and your question go "+
			"with every request — so /compact cannot bring it under; %s", head, options)
	case s.carried > 0:
		return fmt.Sprintf("%s, and %s of them could not be left out — the instructions, the tool list and your question "+
			"go with every request — so /compact cannot bring it under; %s", head, thousands(s.carried), options)
	}
	return head + " — /compact the chat, start a new one, or shorten the request; if it is still too large, " + options
}

// thousands writes n the way the message is read: 7941 as "7,941".
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

const (
	// shrinkMargin is how far under the limit a smaller request aims, for the
	// same reason as shrinkCompactBudget's: bytes only estimate tokens.
	shrinkMargin = 0.85
	// shrunkResult replaces an older tool result that was removed. The pairing
	// with its call stays, which the provider requires.
	shrunkResult = "[this output was removed to fit the provider's size limit; call the tool again, for less, if it is still needed]"
	// clippedResult ends a newest tool result that was cut short.
	clippedResult = "\n[… the rest of this output was removed to fit the provider's size limit; ask for a smaller part if it is needed]"
	// keepOfNewest is the least of a newest tool result that is kept: the
	// model is about to act on it.
	keepOfNewest = 1024
)

// shrink is a smaller copy of a request and what was taken out of it.
type shrink struct {
	messages []chatMessage
	// results were replaced, dropped earlier messages removed, clipped cut short.
	results, dropped, clipped int
	saved                     int
}

// messageBytes is a message's size as the provider tokenises it: its text and
// its tool calls' arguments, not their JSON escaping.
func messageBytes(m chatMessage) int {
	n := len(m.Content)
	for _, c := range m.ToolCalls {
		n += len(c.Function.Name) + len(c.Function.Arguments)
	}
	return n
}

// shrinkMessages takes need bytes out of messages, in this order, and stops as
// soon as it has:
//
//  1. this turn's OLDER tool results, oldest first -- replaced, not removed,
//     so each still answers its call;
//  2. the chat before the question, oldest first, a whole exchange at a time
//     so what is left still starts with the user;
//  3. the NEWEST tool results, cut to what is over, never below keepOfNewest.
//
// Never the system message, never the question (messages[prompt]), never the
// model's own tool calls. ok is false when all of that is not enough; saved
// then says how much there was.
func shrinkMessages(messages []chatMessage, prompt, need int) (sh shrink, ok bool) {
	out := append([]chatMessage(nil), messages...)
	newest := len(out)
	for newest > prompt+1 && out[newest-1].Role == "tool" {
		newest--
	}
	for i := prompt + 1; i < newest && sh.saved < need; i++ {
		if out[i].Role != "tool" || len(out[i].Content) <= len(shrunkResult) {
			continue
		}
		sh.saved += len(out[i].Content) - len(shrunkResult)
		out[i].Content = shrunkResult
		sh.results++
	}
	first := 0
	for first < prompt && out[first].Role == "system" {
		first++
	}
	cut := first
	for cut < prompt && sh.saved < need {
		sh.saved += messageBytes(out[cut])
		cut++
		for cut < prompt && out[cut].Role != "user" {
			sh.saved += messageBytes(out[cut])
			cut++
		}
	}
	sh.dropped = cut - first
	for i := newest; i < len(out) && sh.saved < need; i++ {
		c := out[i].Content
		keep := max(keepOfNewest, len(c)-len(clippedResult)-(need-sh.saved))
		if keep >= len(c)-len(clippedResult) {
			continue
		}
		out[i].Content = clipUTF8(c, keep) + clippedResult
		sh.saved += len(c) - len(out[i].Content)
		sh.clipped++
	}
	if sh.saved < need {
		return shrink{saved: sh.saved}, false
	}
	sh.messages = append(out[:first:first], out[cut:]...)
	return sh, true
}

// notice is what the user reads in the answer: what was left out, and why.
func (sh shrink) notice(s sizeLimit) string {
	var gone []string
	if sh.dropped > 0 {
		gone = append(gone, "the earliest "+plural(sh.dropped, "message")+" of this chat")
	}
	if sh.results > 0 {
		gone = append(gone, "the output of "+plural(sh.results, "earlier tool call"))
	}
	if sh.clipped > 0 {
		gone = append(gone, "part of the latest tool output")
	}
	what := strings.Join(gone, ", ")
	if i := strings.LastIndex(what, ", "); i >= 0 {
		what = what[:i] + " and " + what[i+2:]
	}
	over := "the request was too large for the model"
	if s.known() {
		over = fmt.Sprintf("the request was over the provider's limit (%s tokens; it takes %s)", thousands(s.requested), thousands(s.limit))
	}
	return fmt.Sprintf("\n\n[%s, so it is being sent again without %s…]\n\n", over, what)
}

// fitToLimit answers a request the provider refused as too large: a notice and
// a smaller turn.messages to send once more, or "" and the error to end the
// turn with, carrying what the user needs to know.
//
// ONCE PER TURN, held on the ledger so a pipeline's phases share it: a smaller
// request refused again ends the turn. There is no second shrink to loop on.
func (s *Server) fitToLimit(ctx context.Context, turn *agentTurn, ledger *turnLedger, tools []toolSpec, refused *ModelError) (string, error) {
	size := refused.size
	if ledger.shrunk || size.output {
		return "", refused
	}
	ledger.shrunk = true
	total := 0
	for _, m := range turn.messages {
		total += messageBytes(m)
	}
	if b, err := json.Marshal(tools); err == nil && len(tools) > 0 {
		total += len(b)
	}
	need, perToken := total/2, 0.0
	minuteHeld := 0
	if size.known() {
		perToken = float64(total) / float64(size.requested)
		target := int(float64(size.limit) * shrinkMargin)
		// A MINUTE ALREADY PART-SPENT holds less than the limit. The provider's
		// own count of this answer's last step is what it holds, when that step
		// was less than a minute ago.
		if size.per == "minute" && !turn.lastCallAt.IsZero() && time.Since(turn.lastCallAt) < time.Minute {
			if _, last := usageTallyFrom(ctx).tokens(); last > 0 && size.limit-last < target {
				target, minuteHeld = size.limit-last, last
			}
		}
		need = total - int(float64(max(target, 0))*perToken)
	}
	sh, ok := shrinkMessages(turn.messages, turn.prompt, need)
	if !ok {
		s.logger.Printf("agent: the provider refused a request as too large (%d bytes of text); at most %d of them could be "+
			"removed, short of the %d needed, so it is not sent again", total, sh.saved, need)
		if !size.known() {
			return "", refused
		}
		e := *refused
		if minuteHeld > 0 {
			e.size.used = minuteHeld
		} else {
			e.size.carried = carriedTokens(size.requested, total, sh.saved)
		}
		return "", &e
	}
	s.logger.Printf("agent: the provider refused a %d-byte request as too large; sending it again %d bytes smaller "+
		"(%d earlier message(s) left out, %d older tool output(s) removed, %d clipped)",
		total, sh.saved, sh.dropped, sh.results, sh.clipped)
	turn.messages = sh.messages
	turn.prompt -= sh.dropped
	return sh.notice(size), nil
}

// carriedTokens is how many of the requested tokens are text that cannot be
// left out: the provider's count, scaled by the share of the bytes that stay.
// In whole numbers, so that nothing removable is exactly all of them. The float
// form, (total-saved) / (total/requested), came out a hair under for about one
// request size in seven, and int() turned 6,345 tokens into "6,344 of them
// could not be left out" where nothing could be (CI, 2026-10-25: 26,162 bytes).
func carriedTokens(requested, total, saved int) int {
	if total <= 0 {
		return 0
	}
	return int(int64(requested) * int64(total-saved) / int64(total))
}
