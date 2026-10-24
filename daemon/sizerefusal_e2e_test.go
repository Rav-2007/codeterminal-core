//go:build unix

package main

// A REQUEST THE PROVIDER REFUSES AS TOO LARGE, THROUGH THE REAL DAEMON.
//
// The stand-in answers with the provider's own refusals (sizelimit_test.go
// holds them). Each scenario is shaped like the turn that met it: what was in
// the chat, what the first call returned, and which call was refused.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"mochiii/protocol"
)

// newSizeModel is e2eModel with refusals that carry a body, which the
// provider's words are the whole point of.
func newSizeModel(t *testing.T, reply func(n int, chat e2eChat) sizeReply) *e2eModel {
	t.Helper()
	m := &e2eModel{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		raw := new(strings.Builder)
		buf := make([]byte, 32<<10)
		for {
			n, err := r.Body.Read(buf)
			raw.WriteString(string(buf[:n]))
			if err != nil {
				break
			}
		}
		chat := e2eChat{raw: raw.String()}
		var req struct {
			Messages []e2eMessage `json:"messages"`
		}
		_ = json.Unmarshal([]byte(chat.raw), &req)
		chat.messages = req.Messages
		m.mu.Lock()
		n := len(m.chats)
		m.chats = append(m.chats, chat)
		m.mu.Unlock()
		rep := reply(n, chat)
		if rep.status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(rep.status)
			_, _ = w.Write([]byte(rep.body))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range rep.lines {
			_, _ = fmt.Fprintf(w, "%s\n\n", line)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// twoCallsSSE is one reply asking for two tools at once, as the model did on
// 2026-10-24 (two propose_edit calls, signup.css and signup.html), after a
// sentence of text: 52 bytes of it, which decides how the turn ends.
func twoCallsSSE(name1, args1, name2, args2 string) []string {
	a1, _ := json.Marshal(args1)
	a2, _ := json.Marshal(args2)
	return []string{
		`data: {"choices":[{"delta":{"content":"I'll create signup.css and signup.html to match it."}}]}`,
		fmt.Sprintf(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":%q,"arguments":%s}}]}}]}`, name1, a1),
		fmt.Sprintf(`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","type":"function","function":{"name":%q,"arguments":%s}}]}}]}`, name2, a2),
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}
}

// requestParts is one request in bytes by component, counted as the TEXT the
// provider tokenises -- message content and tool-call arguments decoded, not
// as JSON-escaped on the wire, where every < and > is six bytes. "wire" is the
// body as sent. question is a substring of the current prompt, which is how
// its message is found.
func requestParts(t *testing.T, raw, question string) map[string]int {
	t.Helper()
	var body struct {
		Messages []struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("request body: %v", err)
	}
	current := -1
	for i, m := range body.Messages {
		if m.Role == "user" && strings.Contains(m.Content, question) {
			current = i
		}
	}
	if current < 0 {
		t.Fatalf("no message carries the question %q", question)
	}
	parts := map[string]int{"wire": len(raw), "tools": len(body.Tools)}
	for i, m := range body.Messages {
		n := len(m.Content)
		for _, c := range m.ToolCalls {
			n += len(c.Function.Name) + len(c.Function.Arguments)
		}
		switch {
		case m.Role == "system":
			parts["system"] += n
		case i < current:
			parts["history"] += n
		case i == current:
			editor := 0
			if a := strings.Index(m.Content, editorStateOpen); a >= 0 {
				if b := strings.Index(m.Content, editorStateClose); b > a {
					editor = b + len(editorStateClose) - a
				}
			}
			parts["editor state"] += editor
			parts["prompt"] += n - editor
		case m.Role == "tool":
			parts["tool results"] += n
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			parts["tool calls"] += n
		default:
			parts["this turn, other"] += n
		}
	}
	text := 0
	for k, v := range parts {
		if k != "wire" {
			text += v
		}
	}
	parts["text"] = text
	return parts
}

func logParts(t *testing.T, label string, p map[string]int) {
	t.Helper()
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%d", k, p[k])
	}
	t.Logf("%s:%s", label, b.String())
}

// The editor-state markers, as clients/vscode/src/editorContext.ts writes them.
const (
	editorStateOpen  = "--- editor state (what the user is looking at; not their words) ---"
	editorStateClose = "--- end editor state ---"
)

// loginHTML is a 125-line page, the size of the one open on 2026-10-23.
func loginHTML() string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<title>Sign in</title>\n")
	b.WriteString("<link rel=\"stylesheet\" href=\"login.css\">\n</head>\n<body>\n<main class=\"card\">\n")
	for i := 0; i < 66; i++ {
		fmt.Fprintf(&b, "  <div class=\"row r%d\"><label for=\"f%d\">Field %d</label></div>\n", i, i, i)
	}
	b.WriteString("</main>\n</body>\n</html>\n")
	return b.String()
}

// withEditorState is a prompt the way the VS Code panel sends one: the
// editor-state block first, then the user's words.
func withEditorState(file, body, question string) string {
	lines := strings.Count(body, "\n")
	return editorStateOpen + "\n" +
		fmt.Sprintf("Active file: %s (html, %d lines, cursor on line 1)\n\nFull contents of %s:\n```html\n%s```\n", file, lines, file, body) +
		editorStateClose + "\n\n" + question
}

// signupFiles are the two files the model created on 2026-10-24: 1,448
// completion tokens between them, about 5.8 KB.
func signupFiles() (css, html string) {
	var c, h strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&c, ".row.r%d { margin: %dpx 0; padding: 4px 8px; }\n", i, i%7)
	}
	h.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head><title>Sign up</title><link rel=\"stylesheet\" href=\"signup.css\"></head>\n<body><main class=\"card\">\n")
	for i := 0; i < 27; i++ {
		fmt.Fprintf(&h, "  <div class=\"row r%d\"><label for=\"s%d\">Field %d</label><input id=\"s%d\"></div>\n", i, i, i, i)
	}
	h.WriteString("</main></body></html>\n")
	return c.String(), h.String()
}

const signupQuestion = "make a signup page in the same style as login.html"

// STEP 0, MEASURED: what a refused request holds, in the two shapes it was
// refused in. Logs the breakdown; asserts only that the scenario ran as the
// incident did, so the numbers are of the right request.
func TestE2EMeasureRequestAtSizeRefusal(t *testing.T) {
	t.Run("fresh chat, refused on the second call (2026-10-24)", func(t *testing.T) {
		css, html := signupFiles()
		model := newSizeModel(t, func(n int, _ e2eChat) sizeReply {
			if n == 0 {
				return sizeReply{lines: twoCallsSSE(
					"builtin__propose_edit", argsJSON(map[string]string{"path": "signup.css", "search": "", "replace": css}),
					"builtin__propose_edit", argsJSON(map[string]string{"path": "signup.html", "search": "", "replace": html}),
				)}
			}
			return sizeReply{status: http.StatusRequestEntityTooLarge, body: refusal413ITPMBody}
		})
		page := loginHTML()
		d := startE2E(t, model, map[string]string{"login.html": page}, nil)
		prompt := withEditorState("login.html", page, signupQuestion)
		t.Logf("prompt: %d bytes (2026-10-24: 4555)", len(prompt))
		turn := d.prompt(t, prompt, e2eApproveAll)
		reqs := model.requests()
		if len(reqs) != 2 {
			t.Fatalf("the model saw %d requests, want 2 (the second refused)", len(reqs))
		}
		for i, r := range reqs {
			logParts(t, fmt.Sprintf("request %d", i+1), requestParts(t, r.raw, signupQuestion))
		}
		inc := ""
		if turn.done.Incomplete != nil {
			inc = turn.done.Incomplete.Reason + ": " + turn.done.Incomplete.Detail
		}
		t.Logf("client saw: error=%q class=%q incomplete=%q", turn.done.Error, turn.done.ErrorClass, inc)
	})

	t.Run("six turns of chat, refused on the first call (2026-10-23)", func(t *testing.T) {
		model := newSizeModel(t, func(int, e2eChat) sizeReply {
			return sizeReply{status: http.StatusRequestEntityTooLarge, body: refusal413TPMBody}
		})
		page := loginHTML()
		d := startE2E(t, model, map[string]string{"login.html": page}, nil)
		turn := d.request(t, protocol.PromptRequest{Prompt: withEditorState("login.html", page, signupQuestion), History: sixTurns(page)}, e2eApproveAll)
		// The refused request is the first; what follows it is the fix's.
		reqs := model.requests()
		if len(reqs) == 0 {
			t.Fatal("the model saw no request")
		}
		logParts(t, "request 1 (refused)", requestParts(t, reqs[0].raw, signupQuestion))
		t.Logf("client saw: error=%q class=%q", turn.done.Error, turn.done.ErrorClass)
	})
}

func argsJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// sixTurns is the 2026-10-23 chat: three exchanges, each question carrying the
// editor block as the panel stored them before item 57's containment (15,165
// bytes; the log said 14,848 to 15,150).
func sixTurns(page string) []protocol.Turn { return chatOf(page, 3) }

// chatOf is a chat of that many such exchanges.
func chatOf(page string, exchanges int) []protocol.Turn {
	var history []protocol.Turn
	for i := 0; i < exchanges; i++ {
		history = append(history,
			protocol.Turn{Role: "user", Content: withEditorState("login.html", page, fmt.Sprintf("question %d about the page", i))},
			protocol.Turn{Role: "assistant", Content: strings.Repeat(fmt.Sprintf("Answer %d explains a part of the page. ", i), 22)},
		)
	}
	return history
}

// told is the last thing the user reads about how the turn ended.
func told(turn *e2eTurn) string {
	if turn.done.Incomplete != nil {
		return turn.done.Incomplete.Detail
	}
	return turn.done.Error
}

// shrinkNotice begins what the answer says when a request is sent again smaller.
const shrinkNotice = "[the request was over the provider's limit"

func saysAll(t *testing.T, what, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("%s does not say %q:\n%s", what, w, got)
		}
	}
}

func saysNone(t *testing.T, what, got string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(got, w) {
			t.Errorf("%s says %q:\n%s", what, w, got)
		}
	}
}

// 413 ITPM, the 2026-10-24 turn: a fresh chat, refused on its second call four
// seconds after the first billed 6,345 of a 7,000-a-minute plan. Nothing that
// can be removed would make a second call fit that minute, so none is made,
// and the user is told that in numbers -- not "/compact it", which could not
// have helped, and not "the model provider failed", which it did not.
func TestE2ESizeRefusal413ITPMOnTheSecondStepIsNotSentAgainAndSaysWhy(t *testing.T) {
	css, html := signupFiles()
	model := newSizeModel(t, func(n int, _ e2eChat) sizeReply {
		if n == 0 {
			return sizeReply{lines: withUsage(twoCallsSSE(
				"builtin__propose_edit", argsJSON(map[string]string{"path": "signup.css", "search": "", "replace": css}),
				"builtin__propose_edit", argsJSON(map[string]string{"path": "signup.html", "search": "", "replace": html}),
			), 6345, 1448, 0)}
		}
		return sizeReply{status: http.StatusRequestEntityTooLarge, body: refusal413ITPMBody}
	})
	page := loginHTML()
	d := startE2E(t, model, map[string]string{"login.html": page}, nil)
	turn := d.prompt(t, withEditorState("login.html", page, signupQuestion), e2eApproveAll)

	if n := len(model.requests()); n != 2 {
		t.Fatalf("the model saw %d requests, want 2: the refused one is not sent again", n)
	}
	saysNone(t, "the answer", turn.text.String(), shrinkNotice)
	saysAll(t, "the turn's end", told(turn), "7,941 tokens", "7,000 a minute", "6,345 of the same minute", "/model", "/connect")
	saysNone(t, "the turn's end", told(turn), "/compact the chat", "/compact it", "model provider failed")
	if len(turn.done.EditProposals) != 2 {
		t.Errorf("the first step's two proposals were not kept: %d", len(turn.done.EditProposals))
	}
}

// 413 TPM, the 2026-10-23 turn: six turns of chat, refused on the first call.
// The chat is what can go, so it goes -- oldest first, the question never --
// the user is told, and the request sent again is measurably smaller: under
// the provider's limit at the refused request's own bytes per token.
func TestE2ESizeRefusal413TPMWithHistoryIsSentAgainSmallerAndSaysSo(t *testing.T) {
	model := newSizeModel(t, func(n int, _ e2eChat) sizeReply {
		if n == 0 {
			return sizeReply{status: http.StatusRequestEntityTooLarge, body: refusal413TPMBody}
		}
		return sizeReply{lines: textSSE("The signup page should reuse login.css.")}
	})
	page := loginHTML()
	d := startE2E(t, model, map[string]string{"login.html": page}, nil)
	turn := d.request(t, protocol.PromptRequest{Prompt: withEditorState("login.html", page, signupQuestion), History: sixTurns(page)}, e2eApproveAll)

	reqs := model.requests()
	if len(reqs) != 2 {
		t.Fatalf("the model saw %d requests, want 2: refused, then sent again smaller", len(reqs))
	}
	if turn.done.Error != "" || turn.done.Incomplete != nil {
		t.Fatalf("the turn did not finish: %q %+v", turn.done.Error, turn.done.Incomplete)
	}
	saysAll(t, "the answer", turn.text.String(), shrinkNotice, "9,191 tokens; it takes 8,000", "earliest", "The signup page should reuse login.css.")

	refused, sent := requestParts(t, reqs[0].raw, signupQuestion), requestParts(t, reqs[1].raw, signupQuestion)
	logParts(t, "refused", refused)
	logParts(t, "sent again", sent)
	if sent["text"] >= refused["text"] {
		t.Fatalf("the request sent again is %d bytes, the refused one %d", sent["text"], refused["text"])
	}
	estimate := sent["text"] * 9191 / refused["text"]
	t.Logf("sent again: about %d tokens at the refused request's %.2f bytes a token (limit 8,000)",
		estimate, float64(refused["text"])/9191)
	if estimate > 8000 {
		t.Errorf("the request sent again is still about %d tokens, over the limit of 8,000", estimate)
	}
	for _, k := range []string{"system", "tools", "prompt", "editor state"} {
		if sent[k] != refused[k] {
			t.Errorf("%s changed: %d bytes, was %d", k, sent[k], refused[k])
		}
	}
	if sent["history"] >= refused["history"] {
		t.Errorf("the chat was not what gave way: %d bytes, was %d", sent["history"], refused["history"])
	}
}

// 429 TPM: a throttle, not a size refusal. The request fits; the minute did
// not. It is retried as it is, by streamWithRetry, and nothing is removed.
func TestE2ESizeRefusal429TPMIsAThrottleAndIsRetriedUnchanged(t *testing.T) {
	model := newSizeModel(t, func(n int, _ e2eChat) sizeReply {
		if n == 0 {
			return sizeReply{status: http.StatusTooManyRequests, body: refusal429TPMBody}
		}
		return sizeReply{lines: textSSE("Answered after the wait.")}
	})
	page := loginHTML()
	d := startE2E(t, model, map[string]string{"login.html": page}, nil)
	turn := d.request(t, protocol.PromptRequest{Prompt: withEditorState("login.html", page, signupQuestion), History: sixTurns(page)}, e2eApproveAll)

	reqs := model.requests()
	if len(reqs) != 2 || reqs[0].raw != reqs[1].raw {
		t.Fatalf("the model saw %d requests; want the same request twice", len(reqs))
	}
	if turn.done.Error != "" {
		t.Fatalf("the turn failed: %s", turn.done.Error)
	}
	saysNone(t, "the answer", turn.text.String(), shrinkNotice)
}

// 429 OTPM: the limit is on what the model may WRITE. There is a chat to
// remove -- twelve turns, more than enough to bring the request under 1,000
// tokens' worth of its bytes, so a shrink WOULD go ahead were it attempted --
// and removing it would change nothing, so nothing is removed and the user is
// not sent to /compact.
func TestE2ESizeRefusal429OTPMIsNotShrunkAndDoesNotSendTheUserToCompact(t *testing.T) {
	model := newSizeModel(t, func(int, e2eChat) sizeReply {
		return sizeReply{status: http.StatusTooManyRequests, body: refusal429OTPMBody}
	})
	page := loginHTML()
	d := startE2E(t, model, map[string]string{"login.html": page}, nil)
	turn := d.request(t, protocol.PromptRequest{Prompt: withEditorState("login.html", page, signupQuestion), History: chatOf(page, 6)}, e2eApproveAll)

	if n := len(model.requests()); n != 1 {
		t.Fatalf("the model saw %d requests, want 1", n)
	}
	saysNone(t, "the answer", turn.text.String(), shrinkNotice)
	saysAll(t, "the turn's end", told(turn), "1,448 tokens", "1,000 output tokens a minute", "/model")
	saysNone(t, "the turn's end", told(turn), "/compact")
	if turn.done.ErrorClass != string(ClassContextTooLarge) {
		t.Errorf("error class %q", turn.done.ErrorClass)
	}
}

// A context-window refusal, in OpenAI's words -- with the 400 that provider
// sends, and with a 413. ONE smaller request, and when that is refused too the
// turn ends: no second shrink, however much is left to remove. The request is
// only a little over and the chat long, so the first shrink takes half of it
// and a second could take the rest: this is the bound, not an empty chat.
func TestE2ESizeRefusalContextWindowIsSentAgainOnceAndNoMore(t *testing.T) {
	body := strings.Replace(refusalContextWindowBody, "resulted in 9191 tokens", "resulted in 8600 tokens", 1)
	for _, status := range []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			model := newSizeModel(t, func(int, e2eChat) sizeReply {
				return sizeReply{status: status, body: body}
			})
			page := loginHTML()
			d := startE2E(t, model, map[string]string{"login.html": page}, nil)
			turn := d.request(t, protocol.PromptRequest{Prompt: withEditorState("login.html", page, signupQuestion), History: chatOf(page, 6)}, e2eApproveAll)

			reqs := model.requests()
			if len(reqs) != 2 {
				t.Fatalf("the model saw %d requests, want exactly 2: one smaller request, then stop", len(reqs))
			}
			first, second := requestParts(t, reqs[0].raw, signupQuestion), requestParts(t, reqs[1].raw, signupQuestion)
			if second["text"] >= first["text"] {
				t.Errorf("the second request is %d bytes, the first %d", second["text"], first["text"])
			}
			if second["history"] == 0 {
				t.Errorf("the first shrink took the whole chat, so this does not test the bound")
			}
			saysAll(t, "the answer", turn.text.String(), shrinkNotice)
			saysAll(t, "the turn's end", told(turn), "8,600 tokens", "at most 8,192")
		})
	}
}

// No chat and no tool output: a fresh question whose instructions, tools and
// editor state alone are over the limit. There is nothing to remove, nothing
// is sent again, and the error says so in numbers.
func TestE2ESizeRefusalWithNothingToRemoveSaysWhatCannotBeLeftOut(t *testing.T) {
	body := strings.Replace(refusal413ITPMBody, "Limit 7000, Requested 7941", "Limit 5000, Requested 6345", 1)
	model := newSizeModel(t, func(int, e2eChat) sizeReply {
		return sizeReply{status: http.StatusRequestEntityTooLarge, body: body}
	})
	page := loginHTML()
	d := startE2E(t, model, map[string]string{"login.html": page}, nil)
	turn := d.prompt(t, withEditorState("login.html", page, signupQuestion), e2eApproveAll)

	if n := len(model.requests()); n != 1 {
		t.Fatalf("the model saw %d requests, want 1", n)
	}
	saysNone(t, "the answer", turn.text.String(), shrinkNotice)
	saysAll(t, "the turn's end", told(turn), "6,345 tokens", "5,000 a minute", "none of it could be left out", "/model", "/connect")
	saysNone(t, "the turn's end", told(turn), "/compact the chat", "/compact it")
}

// The control: the same stand-in, answering. One request, the answer, no
// notice -- so a pass above is the refusal's doing, not the stand-in's.
func TestE2ESizeRefusalStandInAnswersAnOrdinaryTurn(t *testing.T) {
	model := newSizeModel(t, func(int, e2eChat) sizeReply {
		return sizeReply{lines: textSSE("An ordinary answer.")}
	})
	page := loginHTML()
	d := startE2E(t, model, map[string]string{"login.html": page}, nil)
	turn := d.request(t, protocol.PromptRequest{Prompt: withEditorState("login.html", page, signupQuestion), History: sixTurns(page)}, e2eApproveAll)

	if n := len(model.requests()); n != 1 {
		t.Fatalf("the model saw %d requests, want 1", n)
	}
	if turn.done.Error != "" || turn.done.Incomplete != nil || !strings.Contains(turn.text.String(), "An ordinary answer.") {
		t.Fatalf("the ordinary turn did not answer: %q %q %+v", turn.text.String(), turn.done.Error, turn.done.Incomplete)
	}
	saysNone(t, "the answer", turn.text.String(), shrinkNotice)
}
