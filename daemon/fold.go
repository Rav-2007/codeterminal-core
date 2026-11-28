package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mochiii/protocol"
)

// FOLDING: WHAT WOULD BE DROPPED IS SUMMARISED INSTEAD (2026-11-27).
//
// THE DEFECT. prepareHistory (history.go) keeps at most maxHistoryTurns
// messages of the conversation, and 256 KB of them, and drops the oldest. Twelve
// messages is six exchanges, so the model forgot what the user said first after
// six questions however short they were -- and the one place that said so was
// /context. "It forgot what I said" was not a full context window. It was this.
//
// THE FIX IS AT THE CUT, NOT A GUESS ABOUT IT. foldOrPrepare runs where
// prepareHistory always ran, on the history the client actually sent, and
// changes nothing unless that history is about to be cut. Then the stored
// chat's older part -- everything but the newest compactKeepRecent turns -- is
// written into ONE summary pair (a user turn naming the chat, an assistant turn
// holding the summary), the stored chat becomes that pair plus the kept turns,
// and the turn is answered from it. A later fold summarises the previous
// summary plus what came after it, so there is never a summary of a summary of
// a summary. With the twelve-message cap a fold happens about once every four
// exchanges: the provider's cached prefix changes no more often than the old
// block cut made it (every three), and every request stays the size it was.
//
// THE CLIENT CHANGES WHAT IT SENDS, NOT WHAT IT SHOWS. The client sends the
// history with every question, so it is told (protocol.FoldInfo) and from then
// on sends the summary in place of what it stands for. Only a client that
// declared protocol.CapFold is folded for: one that did not would keep sending
// the whole conversation and every question would be cut again.
//
// MOSTLY NOBODY WAITS. When a turn ends with the chat already past the cap, the
// summary the next question will need is written as soon as that connection
// closes -- never alongside the turn's own model call, which on a provider with
// a per-minute token allowance could cost the turn its answer -- and kept until
// the next question uses it (foldCache). A question that finds none starts one,
// and waits for it at most foldOnPathDeadline: past that it goes ahead with the
// old cut and says so, and the summary carries on, for the next question.
//
// A FOLD DELETES WHAT IT SUMMARISED, and that is the point (the owner, asked
// about storage: "compacting is best"). memory.db shrinks: ReplaceWorkspace
// reclaims the freed pages. A chat the user SAVED is the exception that proves
// the rule -- they asked for it to be kept -- so its saved copy is brought up to
// date before anything is folded away, and saveChatLocked extends that copy past
// the summary pair later (summaryLead), so the saved chat stays whole and the
// summary is never written into it. A fold never creates a saved chat; a chat
// first saved AFTER it folded is saved from what the client still shows
// (saveFoldedChatLocked; the owner's decision, 2026-11-28).
//
// WHAT THE SUMMARY IS WRITTEN FROM is text anyone can have written -- an answer
// quoting a file or a page -- so the request is built to hold against it
// (summaryguard.go, which has the measurement).

const (
	// foldShortWindow: a model with a window under this many tokens gets the
	// short summary; everything else the longer one.
	foldShortWindow = 32_768
	foldShortWords  = 250
	foldLongWords   = 700

	// foldMaxInputChars bounds everything one fold sends to be summarised. A
	// resumed chat can be 1,000 turns (memory.go) and 8 MB (chatarchive.go):
	// summarised whole, in compactInputChars calls, that was some 140 calls in
	// a row before the question was answered. Past this bound every folded
	// message is cut to one shared length first (clipToShare: the longest give
	// way, as compactTranscript does), so a fold is a handful of calls whatever
	// the chat's size, and every exchange is still in it.
	foldMaxInputChars = 4 * compactInputChars
	// foldWrapBytes is what wrapping one message in a summary request adds
	// (wrappedMessage): counted wherever a request's size is.
	foldWrapBytes = 70
	// foldMinExcerpt is the shortest a folded message is cut to.
	foldMinExcerpt = 40
	// foldLeadIn and foldNextIn frame a summary request's two parts.
	foldLeadIn = "The summary so far:\n\n"
	foldNextIn = "\n\nThe next messages:\n\n"

	// minLearnedWindow: a refusal that names a window smaller than this is not
	// believed. No chat model has one, and an error text (a proxy's, a broken
	// provider's) that said 16 would have every question fold.
	minLearnedWindow = 2048

	// foldCacheChats bounds how many chats foldCache keeps a summary for, and
	// foldJobKeep how long a finished one waits for its question.
	foldCacheChats = 64
	foldJobKeep    = time.Hour

	// foldFailuresToPause summaries of one chat failing in a row pause its
	// folding for foldPause: found by the live QA pass, 2026-11-28, where a
	// provider that refused every summary was asked twice a question (written
	// ahead, then again while the question waited) -- harmless when it refuses
	// at once, a wait of up to foldOnPathDeadline on every question when it is
	// slow or rate-limited. Paused, a question goes ahead with the old cut at
	// once; /compact still works; the first question after the pause tries again.
	foldFailuresToPause = 2
	foldPause           = 10 * time.Minute

	// continuingPrefix opens the user turn of a summary pair, the same words
	// /compact has always used (chatcompact.go), so a saved chat's title reads
	// the same either way.
	continuingPrefix = "Continuing: "
	// summaryLeadPrefix opens the assistant turn of the summary pairs this
	// daemon writes today, the folded kind and /compact's alike.
	summaryLeadPrefix = "Summary of the conversation so far ("
	foldSummaryLead   = summaryLeadPrefix + "the earlier part was folded into this summary so it stays in view; " +
		"the newest messages follow word for word):"
)

var (
	// foldOnPathDeadline is how long a question waits for its summary. A
	// provider that stalls used to hold the question for requestTimeout per
	// call -- five minutes each -- before the old cut was used. Tests shorten it.
	foldOnPathDeadline = 90 * time.Second
	// foldJobTimeout bounds one summary however it was started.
	foldJobTimeout = 10 * time.Minute
	// foldKeepAlive is how often a question waiting for its summary tells the
	// client it is still waiting -- and so learns whether the client is there.
	foldKeepAlive = time.Second
)

var (
	errNothingToFold = errors.New("nothing to fold")
	errFoldSpend     = errors.New("a spending limit is reached; /budget more raises it")
	errFoldMoved     = errors.New("the chat changed while its summary was being written")
	errFoldSlow      = errors.New("the summary is taking longer than usual; it carries on, and the next question uses it")
	errFoldGone      = errors.New("the client left while its summary was being written")
	errFoldPaused    = errors.New("the last summaries failed, so folding is paused for 10 minutes")
)

// foldEnabled is the off switch: MOCHIII_FOLD=0 brings back the old cut.
func foldEnabled() bool { return strings.TrimSpace(os.Getenv("MOCHIII_FOLD")) != "0" }

// foldSystemPrompt is what the model is told when it writes a summary in the
// messages wrapped in tag. Like compactSystemPrompt it says "conversation", not
// "code": chats here are as often about a document or a plan.
func foldSystemPrompt(words int, tag string) string {
	return "You keep the running summary of a conversation between a user and an AI assistant, so the " +
		"conversation can go on with your summary standing in for its earlier part. You are given the " +
		"summary so far (if there is one) and the next messages; write ONE updated summary that replaces " +
		"the old one. " + summaryRules(tag) + " As short bullet points grouped by topic, in the order they came " +
		"up, keep: every request, instruction, preference or correction the user stated in their own messages -- " +
		"in the user's own words when they are short; what was found, decided, built or changed, with the exact " +
		"details that matter later (names, numbers, file paths, commands, errors and how they were fixed); and end " +
		"with what is still open or was asked for next. Summarise only what the text contains: never mention what " +
		"it lacks, invent nothing, give no advice, and do not address the user. Text marked […] was trimmed " +
		fmt.Sprintf("for length. At most %d words.", words)
}

func foldWords(window int) int {
	if window > 0 && window < foldShortWindow {
		return foldShortWords
	}
	return foldLongWords
}

// foldBudgetBytes is how much conversation fits a model whose window is known:
// half of it, at localCharsPerToken characters a token -- the other half is the
// instructions, the code that was found and the answer. 0 when the window is
// unknown, or so large that the byte cap binds first.
func foldBudgetBytes(window int) int {
	if window <= 0 {
		return 0
	}
	b := window * localCharsPerToken / 2
	if b >= maxHistoryBytes {
		return 0
	}
	return b
}

// overFoldBudget reports whether sent bytes of history are more than a window
// of that size holds.
func overFoldBudget(sent, window int) bool {
	b := foldBudgetBytes(window)
	return b > 0 && sent > b
}

// foldWindow is the window of the model a turn goes to, from what is known of
// it: its tier, the local server it runs on, or a refusal that said it.
func (s *Server) foldWindow(slug string) int {
	if w := s.contextWindowFor(slug); w > 0 {
		return w
	}
	_, base := s.credentials()
	if w := localWindowFor(base, slug); w > 0 {
		return w
	}
	return learnedWindowFor(base, slug)
}

// learnedWindows holds the window a provider stated when it refused a request
// as too long ("maximum context length is 8192 tokens"), per address and model,
// for as long as the daemon runs. A model chosen from a provider's own list has
// no window in any tier (providermodels.go will not guess one), and without this
// a conversation that outgrew it would be refused again on every question.
var learnedWindows sync.Map

// noteWindow keeps the window a too-large refusal named. Only a limit on one
// request counts: a per-minute or per-day allowance, or a limit on the answer,
// says nothing about how much conversation fits -- and nor does a figure no
// chat model has (minLearnedWindow).
func noteWindow(base, model string, me *ModelError) {
	if me == nil || me.Class != ClassContextTooLarge || me.size.limit < minLearnedWindow || me.size.per != "" || me.size.output {
		return
	}
	learnedWindows.Store(dialectKey(base, model), me.size.limit)
}

func learnedWindowFor(base, model string) int {
	if v, ok := learnedWindows.Load(dialectKey(base, model)); ok {
		return v.(int)
	}
	return 0
}

// foldOrPrepare is prepareHistory for a turn, folding first when the history
// is about to be cut and this client takes folds. req is the turn's own
// request: after a fold its ChatRevision becomes the folded chat's, so the
// turn's Done does not report the daemon's own replace as another client's.
func (s *Server) foldOrPrepare(ctx context.Context, enc *json.Encoder, hs protocol.HandshakeRequest,
	req *protocol.PromptRequest, d RouteDecision, history []protocol.Turn, fromStore bool) historyOutcome {
	outcome := prepareHistory(history, s.noScrub())
	if !hs.HasCapability(protocol.CapFold) || s.memory == nil || !foldEnabled() {
		return outcome
	}
	window := s.foldWindow(d.Slug)
	defer s.prefetchFoldAfter(ctx, req.Session, d, window)
	// Summaries this chat paid for and never used (a chat that moved on before
	// its question came) go to the client with whatever is sent next (F6).
	sc := s.scope(ctx)
	s.folds.retireStale(sc, s.chatOf(sc).gen.Load())
	owed := s.folds.takeOwed(sc)
	tell := func(info *protocol.FoldInfo) {
		if err := enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion,
			TokenExtras: protocol.TokenExtras{Fold: info}}); err != nil {
			s.logger.Printf("fold notice write error: %v", err)
		}
	}
	byCap := outcome.Truncated
	if fromStore || (!byCap && !overFoldBudget(outcome.SentBytes, window)) {
		if owed != nil {
			tell(&protocol.FoldInfo{Usage: owed})
		}
		return outcome
	}
	turns, info, unused, err := s.foldChat(ctx, enc, req, d, window, !byCap)
	if err == nil {
		s.logger.Printf("fold: %d earlier message(s) are now a summary; %d kept word for word", info.Folded, info.Kept)
		req.ChatRevision = info.Revision
		info.Usage = sumUsage(info.Usage, owed)
		tell(info)
		return prepareHistory(turns, s.noScrub())
	}
	bill := sumUsage(unused, owed)
	reason := foldSkipReason(err)
	switch {
	case errors.Is(err, errNothingToFold):
	case errors.Is(err, errFoldGone), errors.Is(err, context.Canceled):
		s.logger.Printf("fold: %v; nothing was folded", err)
	default:
		s.logger.Printf("fold: %v; the oldest messages are left out instead", err)
	}
	switch {
	case reason != "":
		tell(&protocol.FoldInfo{Skipped: reason, Dropped: outcome.ReceivedTurns - outcome.DroppedInvalid - outcome.KeptTurns,
			Usage: bill})
	case bill != nil:
		tell(&protocol.FoldInfo{Usage: bill})
	}
	return outcome
}

// foldSkipReason is why a fold was not made, in the words the client shows
// after "could not be summarised" -- or "" when there is nothing to say: there
// was nothing to fold, or the person stopped the question.
func foldSkipReason(err error) string {
	switch {
	case err == nil, errors.Is(err, errNothingToFold), errors.Is(err, context.Canceled), errors.Is(err, errFoldGone):
		return "" // nothing to fold, or nobody to tell
	case errors.Is(err, errFoldSpend), errors.Is(err, errFoldSlow), errors.Is(err, errFoldMoved), errors.Is(err, errFoldPaused):
		return err.Error()
	}
	msg := strings.TrimPrefix(err.Error(), "the model could not write the summary: ")
	return "the model could not write it: " + clipRunes(oneLine(msg), 160)
}

// sumUsage is the bill for two lots of calls; nil when both are.
func sumUsage(a, b *protocol.TurnUsage) *protocol.TurnUsage {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	u := *a
	u.Calls += b.Calls
	u.PromptTokens += b.PromptTokens
	u.CompletionTokens += b.CompletionTokens
	u.CachedTokens += b.CachedTokens
	u.ReasoningTokens += b.ReasoningTokens
	u.CostUSD += b.CostUSD
	u.CostMissing = u.CostMissing || b.CostMissing
	if u.Model == "" {
		u.Model, u.ContextWindow = b.Model, b.ContextWindow
	}
	return &u
}

// foldPlan is what one fold would do to a stored chat.
type foldPlan struct {
	lead   int          // 2 when the chat already begins with a summary pair
	prev   string       // that pair's summary, "" when there is none
	title  string       // the chat's own words for itself, without continuingPrefix
	cut    int          // stored[:cut] become the new pair; stored[cut:] are kept
	folded []storedTurn // stored[lead:cut]: what the new summary has to take in
}

// planFold decides the cut the way compactChat does: the newest
// compactKeepRecent turns stay, moved back so the kept part starts where an
// exchange starts. ok is false when there is not a whole exchange to fold.
func planFold(stored []storedTurn) (foldPlan, bool) {
	var p foldPlan
	if len(stored) >= 2 {
		if title, ok := pairTitle(stored[0].Role, stored[0].Content, stored[1].Role, stored[1].Content); ok {
			p.lead, p.prev, p.title = 2, summaryBody(stored[1].Content), title
		}
	}
	cut := len(stored) - compactKeepRecent
	for cut > p.lead && stored[cut].Role != "user" {
		cut--
	}
	if cut-p.lead < 2 {
		return p, false
	}
	p.cut, p.folded = cut, stored[p.lead:cut]
	if p.title == "" {
		for _, t := range p.folded {
			if t.Role == "user" && strings.TrimSpace(t.Content) != "" {
				p.title = strings.TrimPrefix(t.Content, continuingPrefix)
				break
			}
		}
	}
	p.title = clipRunes(oneLine(p.title), archiveTitleRunes)
	return p, true
}

// summaryPairShapes are the summary pairs this daemon has ever written: how
// the assistant turn opens, and what the user turn ends with after "Continuing:
// " and the title. Recognising a pair rests on these EXACT shapes, not on words
// a person can type: a first message "Continuing: ..." answered with "Summary
// of the conversation so far (as you asked) ..." is a conversation, and taking
// it for a summary would shrink that first message to a title at the next fold.
var summaryPairShapes = []struct{ lead, userTail string }{
	{foldSummaryLead + "\n\n", ""},
	{compactSummaryLead + "\n\n", ""},
	// /compact before dd88479 (October 2026) put its explanation on the user
	// turn; chats compacted then are still in memory.db and in saved files.
	{"Summary of the conversation so far:\n\n",
		"\n\n(The earlier part of this conversation was summarised by /compact. The full chat is saved in history.)"},
}

// pairTitle is the title of a summary pair, and whether the two turns are one.
func pairTitle(userRole, userText, asstRole, asstText string) (string, bool) {
	if userRole != "user" || asstRole != "assistant" || !strings.HasPrefix(userText, continuingPrefix) {
		return "", false
	}
	for _, sh := range summaryPairShapes {
		if !strings.HasPrefix(asstText, sh.lead) || !strings.HasSuffix(userText, sh.userTail) {
			continue
		}
		title := strings.TrimSuffix(strings.TrimPrefix(userText, continuingPrefix), sh.userTail)
		if strings.ContainsAny(title, "\r\n") || utf8.RuneCountInString(title) > archiveTitleRunes {
			continue // every title this daemon writes is one clipped line
		}
		return title, true
	}
	return "", false
}

// isSummaryPair reports whether two turns are a summary pair -- a fold's or
// /compact's, today's or an older one's.
func isSummaryPair(userRole, userText, asstRole, asstText string) bool {
	_, ok := pairTitle(userRole, userText, asstRole, asstText)
	return ok
}

// summaryBody is a summary turn without its lead line.
func summaryBody(text string) string {
	if _, body, ok := strings.Cut(text, "\n\n"); ok {
		return strings.TrimSpace(body)
	}
	return ""
}

// summaryLead is how many turns at the start of a chat are a summary pair: 2 or
// 0. saveChatLocked extends a saved copy with what comes AFTER them, so a folded
// chat keeps continuing the copy it was saved as.
func summaryLead(turns []storedTurn) int {
	if len(turns) >= 2 && isSummaryPair(turns[0].Role, turns[0].Content, turns[1].Role, turns[1].Content) {
		return 2
	}
	return 0
}

// sameStoredTurns reports whether two reads of a chat hold the same turns.
func sameStoredTurns(a, b []storedTurn) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || a[i].Content != b[i].Content {
			return false
		}
	}
	return true
}

func storedBytes(turns []storedTurn) int {
	n := 0
	for _, t := range turns {
		n += len(t.Content)
	}
	return n
}

// foldDigest names exactly what a summary takes in: the previous summary and
// the turns folded into it. foldCache matches a summary to a fold by it.
func foldDigest(prev string, turns []storedTurn) string {
	h := sha256.New()
	h.Write([]byte(prev))
	for _, t := range turns {
		h.Write([]byte{0})
		h.Write([]byte(t.Role))
		h.Write([]byte{0})
		h.Write([]byte(t.Content))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func protocolTurns(stored []storedTurn) []protocol.Turn {
	out := make([]protocol.Turn, 0, len(stored))
	for _, t := range stored {
		if pt := (protocol.Turn{Role: t.Role, Content: t.Content}); validTurn(pt) {
			out = append(out, pt)
		}
	}
	return out
}

// foldChat folds the stored chat and returns the turns this turn is answered
// from (the new pair and the kept turns) and what to tell the client.
// mustShrink is set when the cut was the model's window, not the caps: a fold
// that would free little of it is not worth a summary each question. On
// failure, unused is the bill for a summary this question paid for and could
// not use.
func (s *Server) foldChat(ctx context.Context, enc *json.Encoder, req *protocol.PromptRequest, d RouteDecision,
	window int, mustShrink bool) (turns []protocol.Turn, info *protocol.FoldInfo, unused *protocol.TurnUsage, err error) {
	sc := s.scope(ctx)
	st := s.chatOf(sc)
	gen := st.gen.Load() // before the turns -- the order chatsync.go explains
	stored, err := s.memory.LoadAllTurns(ctx, sc)
	if err != nil {
		return nil, nil, nil, err
	}
	p, ok := planFold(stored)
	if !ok || (mustShrink && storedBytes(p.folded) < storedBytes(stored)/4) {
		return nil, nil, nil, errNothingToFold
	}
	summary, usage, err := s.awaitFold(ctx, enc, sc, gen, p, req.Session, d, window)
	if err != nil {
		return nil, nil, usage, err
	}

	archive, archiveErr := s.archiveFor(ctx)
	st.mu.Lock()
	defer st.mu.Unlock()
	// The summary took a model call; anything that landed meanwhile (another
	// client's turn, ctrl+n, a resume) means it summarises a chat that is no
	// longer this one. The question then goes ahead with the old cut.
	if st.gen.Load() != gen {
		return nil, nil, usage, errFoldMoved
	}
	now, err := s.memory.LoadAllTurns(ctx, sc)
	if err != nil {
		return nil, nil, usage, err
	}
	if !sameStoredTurns(now, stored) {
		return nil, nil, usage, errFoldMoved
	}
	linked := archiveErr == nil && archive.readLink().ID != ""
	if linked {
		if _, _, _, err := s.saveChatLocked(ctx, archive, req.Spec, ""); err != nil {
			return nil, nil, usage, fmt.Errorf("bringing the saved copy up to date before folding: %w", err)
		}
	}
	at := time.Now().UTC().Format(time.RFC3339)
	pair := []storedTurn{
		{Role: "user", CreatedAt: at, Content: continuingPrefix + p.title},
		{Role: "assistant", CreatedAt: at, Content: foldSummaryLead + "\n\n" + summary},
	}
	next := append(append([]storedTurn(nil), pair...), stored[p.cut:]...)
	if err := s.memory.ReplaceWorkspace(ctx, sc, next); err != nil {
		return nil, nil, usage, fmt.Errorf("replacing the chat with its summary: %w", err)
	}
	if linked {
		// The copy is the chat as it was; memory.db now holds fewer turns, and
		// listChats compares the two counts to say whether work is unsaved.
		if err := archive.writeLink(archive.readLink().ID, len(next)); err != nil {
			s.logger.Printf("fold: %v", err)
		}
	}
	rev := s.chatReplacedLocked(ctx, sc)
	return protocolTurns(next), &protocol.FoldInfo{
		Summary:  protocolTurns(pair),
		Kept:     len(stored) - p.cut,
		Folded:   len(p.folded),
		Revision: rev,
		Usage:    usage,
	}, nil, nil
}

// awaitFold is the summary a fold of p needs and its bill: from a job already
// writing it -- or the first part of it -- for this chat, else from a new one,
// waited for at most foldOnPathDeadline. A job outlives the wait: if the
// question goes ahead without it, the next question uses what it wrote, and no
// question ever starts a second summary beside one still being written.
func (s *Server) awaitFold(ctx context.Context, enc *json.Encoder, sc chatScope, gen uint64, p foldPlan,
	session string, d RouteDecision, window int) (string, *protocol.TurnUsage, error) {
	deadline := time.Now().Add(foldOnPathDeadline)
	if s.folds.paused(sc) {
		return "", nil, errFoldPaused
	}
	j := s.folds.find(sc, gen, p)
	if j != nil && s.folds.waitedOut(j) && !j.finished() {
		// A question already waited the whole deadline for this one: waiting
		// again would make every question after a slow summary that slow too.
		return "", nil, errFoldSlow
	}
	if j == nil {
		if stop := s.spend.reached(sessionID(session), 0); stop != nil {
			return "", nil, errFoldSpend
		}
		j = s.startFoldJob(sc, gen, p, session, d, window)
	}
	// While it waits, the question neither reads nor writes, so a client that
	// left -- Esc closes the connection -- went unnoticed, and the fold was made
	// for a client that never heard of it: found by attack, 2026-11-28. Its next
	// question then found itself behind, and the terminal reprinted the whole
	// chat with the summary in place of the messages. So the waiting notice is
	// sent again every foldKeepAlive; a write that fails means nobody is there,
	// nothing is folded, and the summary stays for the next question.
	pending := protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion,
		TokenExtras: protocol.TokenExtras{Fold: &protocol.FoldInfo{Pending: true, Folded: len(p.folded)}}}
	gone := func(err error) error { return fmt.Errorf("%w: %v", errFoldGone, err) }
	waited := !j.finished() || j.n < len(p.folded)
	if waited {
		if err := enc.Encode(pending); err != nil {
			return "", nil, gone(err)
		}
	}
	wait := time.NewTimer(time.Until(deadline))
	defer wait.Stop()
	alive := time.NewTicker(foldKeepAlive)
	defer alive.Stop()
	for running := true; running; {
		select {
		case <-j.done:
			running = false
		case <-alive.C:
			if err := enc.Encode(pending); err != nil {
				return "", nil, gone(err)
			}
		case <-wait.C:
			s.folds.markWaitedOut(j)
			return "", nil, errFoldSlow
		case <-ctx.Done():
			return "", nil, ctx.Err()
		}
	}
	if j.err != nil {
		// A failed summary is not kept: the next question starts afresh.
		return "", s.folds.claim(sc, j), j.err
	}
	summary := j.summary
	if j.n == len(p.folded) {
		if waited {
			if err := enc.Encode(pending); err != nil { // still there, now that it is written?
				return "", nil, gone(err)
			}
		}
		return summary, s.folds.claim(sc, j), nil
	}
	// The job took in the first j.n turns (written ahead of a question that
	// came later than expected); fold the rest in on top of it. The job stays
	// cached until this succeeds, so a question that runs out of time here
	// leaves it for the next one.
	rctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	more, rest, err := s.writeFoldSummary(rctx, session, d, window, foldPlan{prev: summary, folded: p.folded[j.n:]})
	if err != nil {
		if errors.Is(rctx.Err(), context.DeadlineExceeded) {
			err = errFoldSlow
		}
		return "", rest, err
	}
	if err := enc.Encode(pending); err != nil {
		return "", rest, gone(err)
	}
	return more, sumUsage(s.folds.claim(sc, j), rest), nil
}

// startFoldJob starts writing the summary for p as the chat's job -- unless
// another question, or the summary written ahead, got there first, whose job it
// then returns. It runs on the daemon's own context, not a question's: a
// question that stops waiting leaves it to finish.
func (s *Server) startFoldJob(sc chatScope, gen uint64, p foldPlan, session string, d RouteDecision, window int) *foldJob {
	j, begun := s.folds.findOrBegin(sc, gen, p)
	if !begun {
		return j
	}
	base := s.shutdownContext()
	go func() {
		ctx, cancel := context.WithTimeout(base, foldJobTimeout)
		defer cancel()
		summary, usage, err := s.writeFoldSummary(ctx, session, d, window, p)
		s.folds.finish(sc, j, summary, usage, err)
	}()
	return j
}

// writeFoldSummary asks the turn's model for the summary, billed like any call:
// in a usage tally that goes to the client, and against the spending limits,
// which are checked first. The folded turns go in as many calls as it takes, each
// one the summary so far plus the next part, so a long resumed chat is never
// trimmed to fit one request -- and never more than foldMaxInputChars of it.
func (s *Server) writeFoldSummary(ctx context.Context, session string, d RouteDecision, window int,
	p foldPlan) (string, *protocol.TurnUsage, error) {
	if stop := s.spend.reached(sessionID(session), 0); stop != nil {
		return "", nil, errFoldSpend
	}
	ctx, tally := withUsageTally(ctx)
	tally.logger = s.logger
	tally.spend, tally.session = s.spend, sessionID(session)
	routing := s.tierConfig().routingFor(d.Tier)
	words := foldWords(window)

	summary := p.prev
	rest := clipToShare(forSummary(p.folded, s.noScrub()), foldMaxInputChars-len(summary))
	budget, shrinks := compactInputChars, 0
	for len(rest) > 0 {
		tag := newMsgTag()
		chunk, n := firstFoldChunk(rest, max(budget-len(summary), compactMinInputChars), tag)
		next, err := s.foldOnce(ctx, d.Slug, routing, summary, chunk, words, tag)
		var me *ModelError
		if errors.As(err, &me) && me.Class == ClassContextTooLarge && shrinks < compactShrinkAttempts {
			// The provider's figures are for the whole request: what does not
			// shrink (the prompt, the lead-ins, the wrapping) comes off the budget.
			fixed := len(foldSystemPrompt(words, tag)) + len(foldLeadIn) + len(foldNextIn) + foldWrapBytes
			if smaller := shrinkCompactBudget(len(chunk)+len(summary)+fixed, me.detail) - fixed; smaller >= compactMinInputChars {
				s.logger.Printf("fold: %s refused a %d-character summary request as too large; trying %d",
					d.Slug, len(chunk)+len(summary), smaller)
				budget, shrinks = smaller, shrinks+1
				continue
			}
		}
		if err != nil {
			return "", tally.report(d.Slug, s.contextWindowFor(d.Slug)), err
		}
		summary, rest = next, rest[n:]
		if len(rest) > 0 {
			if stop := tally.spendReached(); stop != nil {
				return "", tally.report(d.Slug, s.contextWindowFor(d.Slug)), errFoldSpend
			}
		}
	}
	return summary, tally.report(d.Slug, s.contextWindowFor(d.Slug)), nil
}

// clipToShare is turns cut, when together they are over budget bytes, to one
// shared length per message -- the largest that fits, foldMinExcerpt at least --
// so the longest messages give way and every exchange stays. Only if even that
// does not fit are the earliest turns left out.
func clipToShare(turns []storedTurn, budget int) []storedTurn {
	fits := func(from, limit int) bool {
		n := 0
		for _, t := range turns[from:] {
			n += min(len(t.Content), limit) + foldWrapBytes
		}
		return n <= budget
	}
	longest := 0
	for _, t := range turns {
		longest = max(longest, len(t.Content))
	}
	if fits(0, longest) {
		return turns
	}
	from := 0
	for from < len(turns)-1 && !fits(from, foldMinExcerpt) {
		from++
	}
	lo, hi := foldMinExcerpt, longest
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(from, mid) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	out := make([]storedTurn, 0, len(turns)-from)
	for _, t := range turns[from:] {
		if len(t.Content) > lo {
			t.Content = clipUTF8(t.Content, lo) + " […]"
		}
		out = append(out, t)
	}
	return out
}

// firstFoldChunk is the oldest turns that render within room, and how many they
// are: at least one, which compactTranscript trims to fit when it alone is over.
func firstFoldChunk(turns []storedTurn, room int, tag string) (string, int) {
	n, size := 0, 0
	for n < len(turns) {
		add := len(turns[n].Content) + foldWrapBytes
		if n > 0 && size+add > room {
			break
		}
		size += add
		n++
	}
	return compactTranscript(turns[:n], room, tag), n
}

func (s *Server) foldOnce(ctx context.Context, model string, routing providerRouting, summary, chunk string,
	words int, tag string) (string, error) {
	prev := "(none yet)"
	if strings.TrimSpace(summary) != "" {
		prev = wrappedMessage(tag, "summary", defuseMessageMarkup(summary))
	}
	key, base := s.credentials()
	limit := summaryCapBytes(words)
	collect, out := summaryCollector(limit)
	_, err := streamWithRetry(ctx, base, key, model,
		[]chatMessage{
			{Role: "system", Content: foldSystemPrompt(words, tag)},
			{Role: "user", Content: foldLeadIn + prev + foldNextIn + chunk},
		},
		nil, routing, collect, nil, nil, nil, s.logger)
	if err != nil && !errors.Is(err, errSummaryFull) {
		return "", fmt.Errorf("the model could not write the summary: %w", err)
	}
	text := finishSummary(out.String(), limit)
	if text == "" {
		return "", errors.New("the model returned an empty summary")
	}
	return text, nil
}

// foldCache holds, per chat, the one summary being written -- or written and not
// yet used -- and the bills of summaries the chat paid for and will never use.
type foldCache struct {
	mu    sync.Mutex
	jobs  map[string]*foldJob
	owed  map[string]*protocol.TurnUsage
	fails map[string]*foldFails
}

// foldFails is a chat's run of failed summaries, and until when its folding
// is paused (foldFailuresToPause).
type foldFails struct {
	n     int
	until time.Time
}

// foldJob is one summary: of the previous summary and the first n folded turns
// (digest names both), as the chat stood at generation gen.
type foldJob struct {
	gen    uint64
	digest string
	n      int
	done   chan struct{}

	// Set before done closes; read after.
	summary string
	usage   *protocol.TurnUsage
	err     error

	// Under foldCache.mu.
	ended     time.Time
	billed    bool // its bill went to a question, or to the chat's owed bill: never both
	waitedOut bool // a question waited foldOnPathDeadline for it and went ahead
}

func (j *foldJob) finished() bool {
	select {
	case <-j.done:
		return true
	default:
		return false
	}
}

func foldChatKey(sc chatScope) string {
	return sc.Workspace + "\x00" + string(sc.Surface) + "\x00" + sc.Window
}

// find is the chat's job that summarises what p begins with: the same
// generation, the same previous summary, the same first n folded turns. A job
// that failed is never handed out.
func (c *foldCache) find(sc chatScope, gen uint64, p foldPlan) *foldJob {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.findLocked(sc, gen, p)
}

func (c *foldCache) findLocked(sc chatScope, gen uint64, p foldPlan) *foldJob {
	j := c.jobs[foldChatKey(sc)]
	if j == nil || j.gen != gen || j.n > len(p.folded) || (j.finished() && j.err != nil) {
		return nil
	}
	if foldDigest(p.prev, p.folded[:j.n]) != j.digest {
		return nil
	}
	return j
}

// findOrBegin is find, or -- when there is no job to use -- a new one for p,
// in one step: two questions of one chat at once start one summary, not two.
// begun says the caller has to start it.
func (c *foldCache) findOrBegin(sc chatScope, gen uint64, p foldPlan) (j *foldJob, begun bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if j := c.findLocked(sc, gen, p); j != nil {
		return j, false
	}
	return c.beginLocked(sc, gen, p), true
}

// begin makes a new job for p the chat's job. The one it replaces is never
// waited for again, and its bill is owed to the chat.
func (c *foldCache) begin(sc chatScope, gen uint64, p foldPlan) *foldJob {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.beginLocked(sc, gen, p)
}

func (c *foldCache) beginLocked(sc chatScope, gen uint64, p foldPlan) *foldJob {
	if c.jobs == nil {
		c.jobs, c.owed, c.fails = map[string]*foldJob{}, map[string]*protocol.TurnUsage{}, map[string]*foldFails{}
	}
	k := foldChatKey(sc)
	if old := c.jobs[k]; old != nil {
		c.retireLocked(k, old)
	}
	c.evictLocked(time.Now())
	j := &foldJob{gen: gen, digest: foldDigest(p.prev, p.folded), n: len(p.folded), done: make(chan struct{})}
	c.jobs[k] = j
	return j
}

// finish records what job j wrote. A job no longer in the cache owes its bill
// to the chat, for no question will claim it.
func (c *foldCache) finish(sc chatScope, j *foldJob, summary string, usage *protocol.TurnUsage, err error) {
	c.mu.Lock()
	j.summary, j.usage, j.err, j.ended = summary, usage, err, time.Now()
	k := foldChatKey(sc)
	if c.jobs[k] != j {
		c.oweLocked(k, j)
	}
	switch {
	case err == nil:
		delete(c.fails, k)
	case !errors.Is(err, errFoldSpend) && !errors.Is(err, context.Canceled):
		f := c.fails[k]
		if f == nil {
			if len(c.fails) >= foldCacheChats {
				break // bounded: an unrecorded failure only means one more try
			}
			f = &foldFails{}
			c.fails[k] = f
		}
		if f.n++; f.n >= foldFailuresToPause {
			f.until = j.ended.Add(foldPause)
		}
	}
	c.mu.Unlock()
	close(j.done)
}

// paused reports whether the chat's summaries have been failing lately
// (foldFailuresToPause).
func (c *foldCache) paused(sc chatScope) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.fails[foldChatKey(sc)]
	return f != nil && time.Now().Before(f.until)
}

func (c *foldCache) waitedOut(j *foldJob) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return j.waitedOut
}

func (c *foldCache) markWaitedOut(j *foldJob) {
	c.mu.Lock()
	j.waitedOut = true
	c.mu.Unlock()
}

// claim hands j's bill to the question that used j (or saw it fail), once, and
// takes j out of the cache.
func (c *foldCache) claim(sc chatScope, j *foldJob) *protocol.TurnUsage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if k := foldChatKey(sc); c.jobs[k] == j {
		delete(c.jobs, k)
	}
	if j.billed {
		return nil
	}
	j.billed = true
	return j.usage
}

// retireStale takes out a job written for an earlier generation of the chat --
// before a /clear, a resume, a /compact -- so its bill is owed now, not when the
// cache gets round to it.
func (c *foldCache) retireStale(sc chatScope, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := foldChatKey(sc)
	if j := c.jobs[k]; j != nil && j.gen != gen {
		c.retireLocked(k, j)
	}
}

// takeOwed is the chat's bill for summaries it paid for and never used.
func (c *foldCache) takeOwed(sc chatScope) *protocol.TurnUsage {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := foldChatKey(sc)
	u := c.owed[k]
	delete(c.owed, k)
	return u
}

// retireLocked takes old out of the cache. A finished job's bill is owed now; a
// running one owes it when it finishes (finish).
func (c *foldCache) retireLocked(k string, old *foldJob) {
	if c.jobs[k] == old {
		delete(c.jobs, k)
	}
	if old.finished() {
		c.oweLocked(k, old)
	}
}

func (c *foldCache) oweLocked(k string, j *foldJob) {
	if j.billed || j.usage == nil {
		j.billed = true
		return
	}
	j.billed = true
	if len(c.owed) >= foldCacheChats && c.owed[k] == nil {
		return // bounded: /budget's ledger still has it (the tally recorded it)
	}
	c.owed[k] = sumUsage(c.owed[k], j.usage)
}

// evictLocked keeps no finished job past foldJobKeep, and room for one more
// chat within foldCacheChats -- the oldest finished job goes first, then the
// oldest started.
func (c *foldCache) evictLocked(now time.Time) {
	for k, j := range c.jobs {
		if j.finished() && now.Sub(j.ended) > foldJobKeep {
			c.retireLocked(k, j)
		}
	}
	for len(c.jobs) >= foldCacheChats {
		victim, oldest := "", time.Time{}
		for k, j := range c.jobs {
			if t := j.ended; j.finished() && (victim == "" || t.Before(oldest)) {
				victim, oldest = k, t
			}
		}
		if victim == "" {
			for k := range c.jobs {
				victim = k
				break
			}
		}
		c.retireLocked(victim, c.jobs[victim])
	}
}

// prefetchFoldAfter writes, once this turn's connection has closed, the summary
// the NEXT question will need -- when the chat is already past the cut. After
// the turn and never alongside it: two calls at once can cost a turn its answer
// on a provider that allows so many tokens a minute.
func (s *Server) prefetchFoldAfter(ctx context.Context, session string, d RouteDecision, window int) {
	sc := s.scope(ctx)
	base := s.shutdownContext()
	go func() {
		<-ctx.Done()
		if base.Err() != nil {
			return
		}
		pctx, cancel := context.WithTimeout(base, requestTimeout)
		defer cancel()
		gen := s.chatOf(sc).gen.Load()
		stored, err := s.memory.LoadAllTurns(pctx, sc)
		if err != nil {
			return
		}
		o := prepareHistory(protocolTurns(stored), true)
		if !o.Truncated && !overFoldBudget(o.SentBytes, window) {
			return
		}
		// The same rule foldChat applies: a fold the window asks for, that would
		// free little of it, is not made -- so its summary is not paid for.
		p, ok := planFold(stored)
		if !ok || (!o.Truncated && storedBytes(p.folded) < storedBytes(stored)/4) {
			return
		}
		if s.folds.find(sc, gen, p) != nil || s.folds.paused(sc) || s.spend.reached(sessionID(session), 0) != nil {
			return // already written or being written; or failing lately; or not to be paid for
		}
		s.startFoldJob(sc, gen, p, session, d, window)
	}()
}

// saveFoldedChatLocked is saveChatLocked for a save that brought the
// conversation's earlier part along (protocol.HistoryRequestExtras.Folded): the
// messages a fold summarised, as the client still shows them. A chat that was
// never saved and begins with a summary is saved as those messages and all
// that follows the summary -- what the person can still read, not the summary
// standing in for it. Anything else is saved exactly as before.
func (s *Server) saveFoldedChatLocked(ctx context.Context, archive *chatArchive, spec, name string,
	folded []protocol.Turn) (id string, h archiveHeader, pruned int, fromClient bool, err error) {
	asBefore := func() (string, archiveHeader, int, bool, error) {
		id, h, pruned, err := s.saveChatLocked(ctx, archive, spec, name)
		return id, h, pruned, false, err
	}
	if len(folded) == 0 || archive.readLink().ID != "" {
		return asBefore()
	}
	stored, err := s.memory.LoadAllTurns(ctx, archive.scope)
	if err != nil {
		return "", archiveHeader{}, 0, false, err
	}
	if summaryLead(stored) != 2 {
		return asBefore()
	}
	early, ok := foldedForSave(folded, stored[0].CreatedAt, s.noScrub())
	if !ok {
		s.logger.Print("history save: the earlier messages sent with this save are not a conversation; " +
			"the chat is saved from its summary")
		return asBefore()
	}
	toSave := append(early, readableTurns(stored[2:])...)
	now := time.Now()
	id, err = archive.saveWith(toSave, spec, name, false, now)
	if err != nil {
		return "", archiveHeader{}, 0, false, err
	}
	if id == "" {
		return "", archiveHeader{}, 0, false, errNothingToSave
	}
	// len(stored): what memory.db holds, the count listChats compares (chatLink).
	if err := archive.writeLink(id, len(stored)); err != nil {
		s.logger.Printf("history save: %v", err)
	}
	h, _ = headerFor(toSave, spec, now)
	h.Name = clipRunes(oneLine(name), archiveTitleRunes)
	pruned = pruneHistory(archive.root, maxArchivesPerWorkspace, maxSavedChatBytes, now)
	return id, h, pruned, true, nil
}

// foldedForSave is a client's copy of folded messages as they may be saved:
// user and assistant turns only, each one a real message, no more of them than
// memory.db would hold or a saved chat can, and every user turn scrubbed as
// persistTurn scrubs it. at stands in for the times the copy does not carry.
// ok is false when the copy is not a conversation.
func foldedForSave(folded []protocol.Turn, at string, scrubDisabled bool) ([]storedTurn, bool) {
	if len(folded) > maxTurnsPerWorkspace {
		return nil, false
	}
	out := make([]storedTurn, 0, len(folded))
	size := 0
	for _, t := range folded {
		if !validTurn(t) {
			return nil, false
		}
		c := t.Content
		if t.Role == "user" {
			c, _ = scrub(c, scrubDisabled)
		}
		if size += len(c); size > maxArchiveBytes {
			return nil, false
		}
		out = append(out, storedTurn{Role: t.Role, CreatedAt: at, Content: c})
	}
	return out, true
}
