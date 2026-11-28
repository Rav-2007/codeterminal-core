package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"mochiii/protocol"
)

// foldDaemon is a Server with conversation memory and a fake provider that
// tells summary requests from questions. A question gets "answer N". A summary
// gets "SUMMARY:" and every user line it was given -- with the summary it was
// handed first -- so a test can see the user's own words survive a fold.
type foldDaemon struct {
	srv       *Server
	mu        sync.Mutex
	bodies    []string // every provider request, in order
	summaries []string // the user message of each summary request
	systems   []string // the system message of each summary request
	requests  []fakeRequest
	answers   int
	// onSummary, when set, runs while a summary request is being answered --
	// the moment another client's turn could land.
	onSummary func()
	// summaryHandler, when set, answers a summary request itself (a failure, a
	// stall, a flood) when it returns true; in is the request's user message.
	summaryHandler func(w http.ResponseWriter, in string) bool
}

// fakeRequest is one provider request: its system and last user message.
type fakeRequest struct{ system, user string }

// streamSSE writes content as an answer in pieces, as a provider streams one,
// then a usage chunk, so a summary of any size arrives the way a real one does.
func streamSSE(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for len(content) > 0 {
		n := min(len(content), 8<<10)
		for n < len(content) && !utf8.RuneStart(content[n]) {
			n++
		}
		quoted, _ := json.Marshal(content[:n])
		if _, err := fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", quoted); err != nil {
			return // the daemon stopped reading
		}
		content = content[n:]
	}
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":10,"cost":0.001}}`+"\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func newFoldDaemon(t *testing.T) *foldDaemon {
	t.Helper()
	d := &foldDaemon{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		_ = json.Unmarshal(b, &req)
		d.mu.Lock()
		d.bodies = append(d.bodies, string(b))
		if n := len(req.Messages); n > 0 {
			d.requests = append(d.requests, fakeRequest{req.Messages[0].Content, req.Messages[n-1].Content})
		}
		var content string
		if len(req.Messages) == 2 && strings.HasPrefix(req.Messages[0].Content, "You keep the running summary") {
			in := req.Messages[1].Content
			d.summaries = append(d.summaries, in)
			d.systems = append(d.systems, req.Messages[0].Content)
			if h := d.summaryHandler; h != nil {
				d.mu.Unlock()
				if h(w, in) {
					return
				}
				d.mu.Lock()
			}
			// Read the request the way the model is told to: the tag the system
			// prompt names, and the messages it wraps.
			_, prev, msgs := readSummaryRequest(req.Messages[0].Content, in)
			var said []string
			if prev != "" {
				said = append(said, prev)
			}
			for _, m := range msgs {
				if m.role == "user" {
					said = append(said, m.text)
				}
			}
			content = "SUMMARY: " + strings.Join(said, " | ")
			if hook := d.onSummary; hook != nil {
				d.mu.Unlock()
				hook()
				d.mu.Lock()
			}
		} else {
			d.answers++
			content = fmt.Sprintf("answer %d", d.answers)
		}
		d.mu.Unlock()
		streamSSE(w, content)
	}))
	t.Cleanup(upstream.Close)
	mem, _ := openTestMemoryStore(t)
	d.srv = historyServer(t, mem)
	d.srv.apiBase, d.srv.apiKey, d.srv.cfg, d.srv.modelOverride = upstream.URL, "test-key", &Config{}, "test/model"
	return d
}

// wrappedMsg is one message of a summary request.
type wrappedMsg struct{ role, text string }

// readSummaryRequest reads a summary request as its system prompt says to: the
// one tag it names (summaryguard.go), the summary so far, and each message in
// that tag with its role. Markup in any other tag is not a message.
func readSummaryRequest(system, user string) (tag, prev string, msgs []wrappedMsg) {
	m := regexp.MustCompile(`<(msg-[0-9a-f]{16}) role="user">`).FindStringSubmatch(system)
	if m == nil {
		return "", "", nil
	}
	tag = m[1]
	re := regexp.MustCompile(`(?s)<` + tag + ` role="(\w+)">\n(.*?)\n</` + tag + `>`)
	for _, mm := range re.FindAllStringSubmatch(user, -1) {
		if mm[1] == "summary" {
			prev = mm[2]
			continue
		}
		msgs = append(msgs, wrappedMsg{mm[1], mm[2]})
	}
	return tag, prev, msgs
}

func (d *foldDaemon) counts() (summaries, answers int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.summaries), d.answers
}

// lastQuestionBody is the newest provider request that was a question.
func (d *foldDaemon) lastQuestionBody(t *testing.T) string {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := len(d.bodies) - 1; i >= 0; i-- {
		if !strings.Contains(d.bodies[i], "You keep the running summary") {
			return d.bodies[i]
		}
	}
	t.Fatal("the provider was never asked a question")
	return ""
}

// waitForSummaries waits until the provider has written n summaries in all.
func (d *foldDaemon) waitForSummaries(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := d.counts(); got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := d.counts()
	t.Fatalf("waited for %d summaries; the provider wrote %d", n, got)
}

// foldClient is a client that keeps its transcript and sends the history the
// way the terminal client does: everything, until a fold; then the summary and
// the turns from the fold point on.
type foldClient struct {
	d        *foldDaemon
	caps     []string
	rev      string
	shown    []protocol.Turn // the transcript, as the screen keeps it
	summary  []protocol.Turn
	from     int // index in shown of the first turn after the fold point
	folds    []protocol.FoldInfo
	pendings int
	skipped  []protocol.FoldInfo  // folds that could not be made, as told
	bills    []protocol.TurnUsage // every summary bill the client was given
	misfits  int                  // folds whose Kept did not line up with what this client sent
}

// catchUp is what the terminal client does on ChatBehind: fetch the stored
// chat, show it, and forget its fold point (adoptChat).
func (c *foldClient) catchUp() error {
	var out bytes.Buffer
	c.d.srv.handleHistory(context.Background(), json.NewEncoder(&out),
		protocol.HistoryRequest{Action: protocol.HistoryCurrent, Chats: true})
	var resp protocol.HistoryResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("catching up: %s", resp.Error)
	}
	c.shown, c.summary, c.from, c.rev = resp.Turns, nil, 0, resp.ChatRevision
	return nil
}

func (c *foldClient) history() []protocol.Turn {
	if c.summary == nil {
		return append([]protocol.Turn(nil), c.shown...)
	}
	return append(append([]protocol.Turn(nil), c.summary...), c.shown[c.from:]...)
}

// ask asks one question, then lets the daemon's look-ahead for the next one
// decide -- the order a person's pause between questions gives. When it decides
// is otherwise up to the scheduler: on macOS CI (2026-11-28) the next question
// often came first, and a test that assumed the usual order failed there only.
// A test that wants a question to race the look-ahead uses tryAsk.
func (c *foldClient) ask(t *testing.T, question string) protocol.TokenResponse {
	t.Helper()
	tok, err := c.tryAsk(question)
	if err != nil {
		t.Fatal(err)
	}
	c.d.settle(t)
	return tok
}

// settle waits until no look-ahead (prefetchFoldAfter) is still deciding
// whether to write a summary. Any summary one began is registered by then.
func (d *foldDaemon) settle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for d.srv.folds.lookAheads.Load() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("a look-ahead was still deciding after 10s")
		}
		time.Sleep(time.Millisecond)
	}
}

// tryAsk is ask for a goroutine, where a test may not stop: it returns what
// went wrong instead.
func (c *foldClient) tryAsk(question string) (protocol.TokenResponse, error) {
	clientConn, serverConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		c.d.srv.serveConn(serverConn)
		_ = serverConn.Close()
		close(done)
	}()
	defer func() { _ = clientConn.Close(); <-done }()
	_ = clientConn.SetDeadline(time.Now().Add(20 * time.Second))
	enc, dec := json.NewEncoder(clientConn), json.NewDecoder(clientConn)
	if err := enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion,
		ClientName: "sync-test", Capabilities: c.caps}); err != nil {
		return protocol.TokenResponse{}, err
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil || !hs.Ok {
		return protocol.TokenResponse{}, fmt.Errorf("handshake = %+v (err %v)", hs, err)
	}
	if err := enc.Encode(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: question,
		History: c.history(), ChatRevision: c.rev}); err != nil {
		return protocol.TokenResponse{}, err
	}
	asked := len(c.shown)
	var answer strings.Builder
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			return tok, fmt.Errorf("reading the answer: %v", err)
		}
		if tok.Error != "" {
			return tok, fmt.Errorf("the turn failed: %s", tok.Error)
		}
		if f := tok.Fold; f != nil {
			if f.Usage != nil {
				c.bills = append(c.bills, *f.Usage)
			}
			switch {
			case f.Pending:
				c.pendings++
			case f.Skipped != "":
				c.skipped = append(c.skipped, *f)
			case len(f.Summary) == 2 && asked-f.Kept >= 0 && asked-f.Kept <= asked:
				c.folds = append(c.folds, *f)
				c.summary, c.from, c.rev = f.Summary, asked-f.Kept, f.Revision
			case len(f.Summary) == 2:
				c.misfits++ // the terminal client ignores such a fold (foldStart); a level client never gets one
			}
		}
		answer.WriteString(tok.Token)
		if tok.Done {
			c.shown = append(c.shown, protocol.Turn{Role: "user", Content: question},
				protocol.Turn{Role: "assistant", Content: answer.String()})
			c.rev = tok.ChatRevision
			if tok.ChatBehind {
				return tok, c.catchUp()
			}
			return tok, nil
		}
	}
}

const firstWords = "ZEBRA-FIRST-QUESTION use tabs, never spaces"

func question(i int) string {
	if i == 1 {
		return "question 1: " + firstWords
	}
	return fmt.Sprintf("question %d", i)
}

// THE DEFECT, end to end: after six exchanges the first question was gone from
// every request the model saw. With folding, ten exchanges in, the user's first
// words still reach the model -- inside the summary.
func TestFold_TheFirstQuestionStillReachesTheModelTenExchangesIn(t *testing.T) {
	d := newFoldDaemon(t)
	c := &foldClient{d: d, caps: []string{protocol.CapFold}}
	for i := 1; i <= 10; i++ {
		c.ask(t, question(i))
	}
	if len(c.folds) == 0 {
		t.Fatal("ten exchanges were never folded")
	}
	if body := d.lastQuestionBody(t); !strings.Contains(body, "ZEBRA-FIRST-QUESTION") {
		t.Errorf("the tenth question's request lost the first question's words:\n%s", body)
	}
	var req struct {
		Messages []struct{ Role string } `json:"messages"`
	}
	_ = json.Unmarshal([]byte(d.lastQuestionBody(t)), &req)
	if n := len(req.Messages); n > 1+maxHistoryTurns+1 {
		t.Errorf("the request carried %d messages; the cap is %d plus the system message and the question", n, maxHistoryTurns)
	}
}

// Neuter check: drop the CapFold condition in foldOrPrepare and this test still
// passes -- it is the old behaviour's witness, so the one above is not passing
// for a reason the fold did not cause.
func TestFold_AClientThatDoesNotTakeFoldsIsCutAsBefore(t *testing.T) {
	d := newFoldDaemon(t)
	c := &foldClient{d: d}
	for i := 1; i <= 10; i++ {
		c.ask(t, question(i))
	}
	if strings.Contains(d.lastQuestionBody(t), "ZEBRA-FIRST-QUESTION") {
		t.Error("a client without CapFold kept its first question: either it was folded or the cut is gone")
	}
	if n, _ := d.counts(); n != 0 || len(c.folds) != 0 {
		t.Errorf("a client without CapFold got %d summary request(s) and %d fold(s)", n, len(c.folds))
	}
}

// The turn the fold happens on is not "behind": the replace was the daemon's
// own, and a Done that said otherwise would make the client fetch and reprint
// the whole chat.
func TestFold_TheFoldingTurnIsNotReportedAsBehind(t *testing.T) {
	d := newFoldDaemon(t)
	c := &foldClient{d: d, caps: []string{protocol.CapFold}}
	for i := 1; i <= 10; i++ {
		before := len(c.folds)
		done := c.ask(t, question(i))
		if len(c.folds) > before && done.ChatBehind {
			t.Fatalf("question %d folded the chat and its Done says the client is behind", i)
		}
	}
}

// Mostly nobody waits: the summary the next question needs is written after
// the turn that left the chat past the cap, and that question uses it instead
// of writing its own while the user waits.
func TestFold_ASummaryWrittenAheadIsUsedAndTheQuestionDoesNotWait(t *testing.T) {
	d := newFoldDaemon(t)
	c := &foldClient{d: d, caps: []string{protocol.CapFold}}
	// The seventh question carries six exchanges, twelve messages: the cap, not
	// over it. The eighth carries fourteen and is the first to be cut -- so it is
	// after the seventh turn that its summary is written ahead.
	for i := 1; i <= 6; i++ {
		c.ask(t, question(i))
	}
	if n, _ := d.counts(); n != 0 {
		t.Fatalf("%d summaries were written before the chat reached the cap", n)
	}
	c.ask(t, question(7))
	if len(c.folds) != 0 {
		t.Fatalf("the seventh question folded already: %+v", c.folds)
	}
	d.waitForSummaries(t, 1)
	c.ask(t, question(8))
	if len(c.folds) != 1 {
		t.Fatalf("the eighth question made %d folds, want 1", len(c.folds))
	}
	if c.pendings != 0 {
		t.Error("the eighth question waited for a summary although one was written ahead")
	}
	if n, _ := d.counts(); n != 1 {
		t.Errorf("%d summaries were written; the one written ahead should have been the only one", n)
	}
}

// A fold deletes what it summarised: the stored chat is the summary pair and the
// newest turns, and nothing was saved that the user did not save.
func TestFold_TheStoredChatShrinksAndNothingIsSavedBehindTheUsersBack(t *testing.T) {
	d := newFoldDaemon(t)
	c := &foldClient{d: d, caps: []string{protocol.CapFold}}
	for i := 1; i <= 8; i++ {
		c.ask(t, question(i))
	}
	if len(c.folds) == 0 {
		t.Fatal("no fold")
	}
	stored, err := d.srv.memory.LoadAllTurns(context.Background(), d.srv.scope(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) < 2 || summaryLead(stored) != 2 {
		t.Fatalf("the stored chat does not open with one summary pair: %+v", stored)
	}
	if len(stored) >= 16 {
		t.Errorf("the stored chat still holds %d turns after a fold", len(stored))
	}
	for _, s := range stored[2:] {
		if strings.Contains(s.Content, "ZEBRA-FIRST-QUESTION") {
			t.Error("the first question is still stored word for word after it was folded")
		}
	}
	if files := savedFiles(t); len(files) != 0 {
		t.Errorf("a fold wrote %d saved chat(s); only the user saves a chat: %v", len(files), files)
	}
}

// A chat the user saved keeps every word: its copy is brought up to date before
// anything is folded away, and the next save extends that same copy past the
// summary, which is never written into it.
func TestFold_ASavedChatKeepsEveryTurnAndNeverTheSummary(t *testing.T) {
	d := newFoldDaemon(t)
	c := &foldClient{d: d, caps: []string{protocol.CapFold}}
	c.ask(t, question(1))
	id := saveNow(t, d.srv, "mine").Entry.ID
	for i := 2; i <= 9; i++ {
		c.ask(t, question(i))
	}
	if len(c.folds) == 0 {
		t.Fatal("no fold")
	}
	again := saveNow(t, d.srv, "")
	if files := savedFiles(t); len(files) != 1 {
		t.Fatalf("after folding, saving again left %d saved chats, want the one: %v", len(files), files)
	}
	shown := askHistory(t, d.srv, protocol.HistoryRequest{Action: protocol.HistoryShow, ID: again.Entry.ID})
	text := turnTexts(shown.Turns)
	for i := 1; i <= 9; i++ {
		if !strings.Contains(text, question(i)) {
			t.Errorf("the saved copy lost %q", question(i))
		}
	}
	if strings.Contains(text, summaryLeadPrefix) || strings.Contains(text, continuingPrefix) {
		t.Errorf("the summary was written into the saved copy:\n%s", text)
	}
	if again.Entry.Title != "mine" {
		t.Errorf("the saved chat (first saved as %s) lost its name across the fold: %+v", id, again.Entry)
	}
}

// A later fold summarises the previous summary and what came after it -- never
// the whole chat again, and never a summary of a summary's title.
func TestFold_ASecondFoldTakesInThePreviousSummaryAndOnlyWhatCameAfter(t *testing.T) {
	d := newFoldDaemon(t)
	c := &foldClient{d: d, caps: []string{protocol.CapFold}}
	for i := 1; i <= 14 && len(c.folds) < 2; i++ {
		c.ask(t, question(i))
	}
	if len(c.folds) < 2 {
		t.Fatalf("14 exchanges made %d fold(s), want 2", len(c.folds))
	}
	d.mu.Lock()
	second := d.summaries[len(d.summaries)-1]
	d.mu.Unlock()
	prev, next, _ := strings.Cut(second, "\n\nThe next messages:\n\n")
	if !strings.Contains(prev, "SUMMARY:") || !strings.Contains(prev, "ZEBRA-FIRST-QUESTION") {
		t.Errorf("the second summary was not given the first one:\n%s", prev)
	}
	if strings.Contains(next, "question 1:") {
		t.Errorf("the second summary was given the first question again:\n%s", next)
	}
	stored, _ := d.srv.memory.LoadAllTurns(context.Background(), d.srv.scope(context.Background()))
	pairs := 0
	for i := 0; i+1 < len(stored); i++ {
		if isSummaryPair(stored[i].Role, stored[i].Content, stored[i+1].Role, stored[i+1].Content) {
			pairs++
		}
	}
	if pairs != 1 || strings.Contains(stored[0].Content, continuingPrefix+continuingPrefix) {
		t.Errorf("the chat holds %d summary pairs (want 1) and opens %q", pairs, stored[0].Content)
	}
	if got := c.folds[len(c.folds)-1].Usage; got == nil || got.Calls == 0 {
		t.Errorf("the fold did not say what its summary cost: %+v", got)
	}
}

func TestPlanFold(t *testing.T) {
	ex := func(n int) []storedTurn {
		var out []storedTurn
		for i := 1; i <= n; i++ {
			out = append(out, storedTurn{Role: "user", Content: fmt.Sprintf("q%d", i)},
				storedTurn{Role: "assistant", Content: fmt.Sprintf("a%d", i)})
		}
		return out
	}
	if _, ok := planFold(ex(2)); ok {
		t.Error("two exchanges keep four turns and leave nothing to fold")
	}
	p, ok := planFold(ex(7))
	if !ok || p.cut != 10 || len(p.folded) != 10 || p.lead != 0 || p.title != "q1" {
		t.Errorf("seven exchanges: %+v", p)
	}
	// A cut that lands on an answer moves back to where the exchange starts.
	odd := append(ex(6), storedTurn{Role: "assistant", Content: "an extra note"})
	if p, _ := planFold(odd); odd[p.cut].Role != "user" {
		t.Errorf("the kept part starts with %q, not a question", odd[p.cut].Role)
	}
	pair := []storedTurn{{Role: "user", Content: continuingPrefix + "q1"}, {Role: "assistant", Content: foldSummaryLead + "\n\nthe old summary"}}
	p, ok = planFold(append(pair, ex(5)...))
	if !ok || p.lead != 2 || p.prev != "the old summary" || p.title != "q1" || p.folded[0].Content != "q1" {
		t.Errorf("a chat with a summary pair: %+v", p)
	}
}

func TestShrinkMessages_KeepsAFoldSummaryUntilLast(t *testing.T) {
	big := strings.Repeat("x", 1000)
	msgs := []chatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: continuingPrefix + "the chat"},
		{Role: "assistant", Content: foldSummaryLead + "\n\n" + big},
		{Role: "user", Content: big}, {Role: "assistant", Content: big},
		{Role: "user", Content: "the question"},
	}
	sh, ok := shrinkMessages(msgs, 5, 1500)
	if !ok {
		t.Fatal("2,000 bytes of exchange could not free 1,500")
	}
	var roles []string
	for _, m := range sh.messages {
		roles = append(roles, m.Role+":"+clipUTF8(m.Content, 12))
	}
	if len(sh.messages) != 4 || !strings.HasPrefix(sh.messages[2].Content, summaryLeadPrefix) {
		t.Errorf("the exchange should go and the summary stay: %v", roles)
	}
	sh, ok = shrinkMessages(msgs, 5, 2900)
	if !ok || len(sh.messages) != 2 || sh.dropped != 4 {
		t.Errorf("with nothing else left the summary goes too: ok=%v dropped=%d messages=%d", ok, sh.dropped, len(sh.messages))
	}
}

func TestSummaryLead_LetsAFoldedChatExtendItsSavedCopy(t *testing.T) {
	saved := []storedTurn{{Role: "user", Content: "q1"}, {Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"}, {Role: "assistant", Content: "a2"}}
	current := []storedTurn{{Role: "user", Content: continuingPrefix + "q1"},
		{Role: "assistant", Content: foldSummaryLead + "\n\nsummary"},
		{Role: "user", Content: "q2"}, {Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"}, {Role: "assistant", Content: "a3"}}
	if _, ok := extendSaved(saved, current); ok {
		t.Fatal("without skipping the summary the copy should not be extended -- the test lost its premise")
	}
	out, ok := extendSaved(saved, current[summaryLead(current):])
	if !ok || len(out) != 6 || out[0].Content != "q1" || out[5].Content != "a3" {
		t.Errorf("extended copy = %+v (ok %v)", out, ok)
	}
	if summaryLead(saved) != 0 {
		t.Error("a chat with no summary has no summary lead")
	}
}

func TestNoteWindow_OnlyALimitOnOneRequestIsAWindow(t *testing.T) {
	base, model := "https://example.test/v1", "m-"+t.Name()
	// The windows live for the daemon's life, so a run of this test starts from none.
	learnedWindows.Delete(dialectKey(base, model))
	t.Cleanup(func() { learnedWindows.Delete(dialectKey(base, model)) })
	noteWindow(base, model, &ModelError{Class: ClassContextTooLarge, size: sizeLimit{limit: 9000, per: "minute"}})
	noteWindow(base, model, &ModelError{Class: ClassContextTooLarge, size: sizeLimit{limit: 9000, output: true}})
	noteWindow(base, model, &ModelError{Class: ClassRateLimited, size: sizeLimit{limit: 9000}})
	if w := learnedWindowFor(base, model); w != 0 {
		t.Fatalf("an allowance, an output limit or a rate limit was taken as a window: %d", w)
	}
	noteWindow(base, model, &ModelError{Class: ClassContextTooLarge, size: sizeLimit{limit: 8192}})
	if w := learnedWindowFor(base, model); w != 8192 {
		t.Errorf("window = %d, want 8192", w)
	}
}

func TestFoldBudgetBytes(t *testing.T) {
	if foldBudgetBytes(0) != 0 || foldBudgetBytes(1_000_000) != 0 {
		t.Error("an unknown or huge window must leave the caps to decide")
	}
	if b := foldBudgetBytes(16_384); b != 16_384*localCharsPerToken/2 {
		t.Errorf("16K window budget = %d", b)
	}
	if !overFoldBudget(30_000, 16_384) || overFoldBudget(20_000, 16_384) {
		t.Error("a 16K window holds about 24,000 bytes of conversation")
	}
}

func TestWriteFoldSummary_RefusedAtAReachedLimit(t *testing.T) {
	d := newFoldDaemon(t)
	d.srv.spend = newSpendLedger(MCPSpendConfig{SessionTokens: 10, SessionUSD: 1, DayTokens: 1_000_000, DayUSD: 5}, "", "k", discardLogger(), time.Now)
	d.srv.spend.add("s", 100, 0)
	p, _ := planFold([]storedTurn{{Role: "user", Content: "q1"}, {Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"}, {Role: "assistant", Content: "a2"}, {Role: "user", Content: "q3"}, {Role: "assistant", Content: "a3"}})
	if _, _, err := d.srv.writeFoldSummary(context.Background(), "s", d.srv.route("", ""), 0, p); err != errFoldSpend {
		t.Fatalf("err = %v, want errFoldSpend", err)
	}
	if n, _ := d.counts(); n != 0 {
		t.Error("a summary was asked for past the spending limit")
	}
}

// /compact's summary is a model call like any other: it comes back with its
// bill, and a reached limit refuses it.
func TestCompact_IsBilledAndRefusedAtAReachedLimit(t *testing.T) {
	d := newFoldDaemon(t)
	for i := 1; i <= 4; i++ {
		appendExchange(t, d.srv.memory, d.srv.workspace, fmt.Sprintf("q%d", i), fmt.Sprintf("a%d", i))
	}
	resp := askHistory(t, d.srv, protocol.HistoryRequest{Action: protocol.HistoryCompact})
	if resp.Error != "" || resp.Usage == nil || resp.Usage.Calls != 1 || resp.Usage.PromptTokens != 100 {
		t.Fatalf("compact = error %q, usage %+v", resp.Error, resp.Usage)
	}
	d.srv.spend = newSpendLedger(MCPSpendConfig{SessionTokens: 10, SessionUSD: 1, DayTokens: 50, DayUSD: 5}, "", "k", discardLogger(), time.Now)
	d.srv.spend.add("", 100, 0)
	for i := 5; i <= 8; i++ {
		appendExchange(t, d.srv.memory, d.srv.workspace, fmt.Sprintf("q%d", i), fmt.Sprintf("a%d", i))
	}
	calls := func() int { d.mu.Lock(); defer d.mu.Unlock(); return len(d.bodies) }
	before := calls()
	resp = askHistory(t, d.srv, protocol.HistoryRequest{Action: protocol.HistoryCompact})
	if resp.Error == "" {
		t.Error("compact went ahead past the day's limit")
	}
	if after := calls(); after != before {
		t.Errorf("the provider was called past the limit (%d → %d requests)", before, after)
	}
}

func TestFeatures_FoldIsAdvertisedOnlyWithMemory(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	with := historyServer(t, mem)
	without := &Server{logger: discardLogger(), workspace: t.TempDir()}
	has := func(s *Server) bool {
		for _, f := range s.features() {
			if f == protocol.FeatureFold {
				return true
			}
		}
		return false
	}
	if !has(with) || has(without) {
		t.Errorf("fold advertised: with memory %v, without %v", has(with), has(without))
	}
	t.Setenv("MOCHIII_FOLD", "0")
	if has(with) {
		t.Error("MOCHIII_FOLD=0 still advertises folding")
	}
}

// The wire: a Fold rides on TokenResponse as a key of its own.
func TestFoldInfo_IsAKeyOfTheTokenResponse(t *testing.T) {
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(protocol.TokenResponse{TokenExtras: protocol.TokenExtras{Fold: &protocol.FoldInfo{Kept: 4}}})
	if !strings.Contains(buf.String(), `"fold":{`) {
		t.Errorf("encoded = %s", buf.String())
	}
}

// A summary takes a model call, and the chat can change meanwhile -- another
// client's turn, ctrl+n, a resume. A summary of a chat that is no longer this
// one is thrown away, and the chat is left exactly as it is.
func TestFoldChat_AChatThatChangedWhileItsSummaryWasWrittenIsLeftAlone(t *testing.T) {
	d := newFoldDaemon(t)
	ctx := context.Background()
	sc := d.srv.scope(ctx)
	for i := 1; i <= 8; i++ {
		if err := d.srv.memory.AppendTurn(ctx, sc, "user", question(i)); err != nil {
			t.Fatal(err)
		}
		if err := d.srv.memory.AppendTurn(ctx, sc, "assistant", fmt.Sprintf("answer %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	d.onSummary = func() { _ = d.srv.memory.AppendTurn(ctx, sc, "user", "another window's question") }
	_, _, unused, err := d.srv.foldChat(ctx, json.NewEncoder(io.Discard), &protocol.PromptRequest{}, d.srv.route("", ""), 0, false)
	if err != errFoldMoved {
		t.Fatalf("err = %v, want errFoldMoved", err)
	}
	// F6: the summary was paid for, so its bill comes back to be reported.
	if unused == nil || unused.Calls != 1 {
		t.Errorf("the bill of the summary that could not be used = %+v, want one call", unused)
	}
	stored, _ := d.srv.memory.LoadAllTurns(ctx, sc)
	if len(stored) != 17 || summaryLead(stored) != 0 {
		t.Errorf("the chat was changed by a fold that should have been dropped: %d turns, lead %d", len(stored), summaryLead(stored))
	}
}

// /compact never nests its title either: compacting twice keeps "Continuing:"
// once, on the chat's own words.
func TestCompact_TwiceDoesNotNestTheTitle(t *testing.T) {
	d := newFoldDaemon(t)
	for i := 1; i <= 4; i++ {
		appendExchange(t, d.srv.memory, d.srv.workspace, fmt.Sprintf("q%d", i), fmt.Sprintf("a%d", i))
	}
	if resp := askHistory(t, d.srv, protocol.HistoryRequest{Action: protocol.HistoryCompact}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	for i := 5; i <= 8; i++ {
		appendExchange(t, d.srv.memory, d.srv.workspace, fmt.Sprintf("q%d", i), fmt.Sprintf("a%d", i))
	}
	if resp := askHistory(t, d.srv, protocol.HistoryRequest{Action: protocol.HistoryCompact}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	stored, _ := d.srv.memory.LoadAllTurns(context.Background(), tscope(d.srv.workspace))
	if len(stored) == 0 || stored[0].Content != continuingPrefix+"q1" {
		t.Errorf("after two compacts the chat opens %q, want %q", stored[0].Content, continuingPrefix+"q1")
	}
}

// The window a refusal names is learned where every model call passes.
func TestStreamWithRetry_LearnsTheWindowARefusalNames(t *testing.T) {
	srv := errorServer(t, 400, `{"error":{"message":"This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens.","code":"context_length_exceeded"}}`)
	defer srv.Close()
	model := "m-" + t.Name()
	learnedWindows.Delete(dialectKey(srv.URL, model))
	t.Cleanup(func() { learnedWindows.Delete(dialectKey(srv.URL, model)) })
	_, err := streamWithRetry(context.Background(), srv.URL, "k", model, buildChatMessages("sys", nil, "hi"), nil,
		ZDRConfig{}.resolvedProviderRouting(), func(string) error { return nil }, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("a refused request came back without an error")
	}
	if w := learnedWindowFor(srv.URL, model); w != 8192 {
		t.Errorf("learned window = %d, want 8192 (err %v)", w, err)
	}
}

// smallWindow gives the fold daemon's model a 4,096-token window: about 6 KB
// of conversation (foldBudgetBytes), so the window binds long before the caps.
func smallWindow(d *foldDaemon) {
	d.srv.cfg = &Config{Tiers: map[string]ModelTier{"small": {Slug: "test/model", ContextWindow: 4096, Active: true}}}
}

// A model with a small window -- a local one, mostly -- is folded for when the
// conversation outgrows the window, not only when it passes twelve messages.
func TestFold_ASmallWindowFoldsBeforeTheCaps(t *testing.T) {
	d := newFoldDaemon(t)
	smallWindow(d)
	c := &foldClient{d: d, caps: []string{protocol.CapFold}}
	firstFold := 0
	for i := 1; i <= 7 && firstFold == 0; i++ {
		c.ask(t, fmt.Sprintf("question %d: %s", i, strings.Repeat("w", 1500)))
		if len(c.folds) > 0 {
			firstFold = i
		}
	}
	if firstFold == 0 || firstFold > 6 {
		t.Fatalf("first fold at question %d; a 4,096-token window is full by the sixth", firstFold)
	}
}

// A fold the window asks for that would free little of it is not made: the
// conversation's weight is in its newest turns, which a fold keeps.
func TestFold_AWindowFoldThatWouldFreeLittleIsNotMade(t *testing.T) {
	d := newFoldDaemon(t)
	smallWindow(d)
	c := &foldClient{d: d, caps: []string{protocol.CapFold}}
	for i := 1; i <= 4; i++ {
		c.ask(t, fmt.Sprintf("q%d", i))
	}
	for i := 5; i <= 7; i++ {
		c.ask(t, fmt.Sprintf("q%d: %s", i, strings.Repeat("w", 5000)))
	}
	if len(c.folds) != 0 {
		t.Errorf("folded %d time(s) to free a few bytes", len(c.folds))
	}
	if n, _ := d.counts(); n != 0 {
		t.Errorf("%d summaries were paid for a fold that is not made", n)
	}
}
