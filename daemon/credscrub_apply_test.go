package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// The four application points (credscrub.go, credscrub_apply.go), each driven
// through the real entry the model's request takes, each tested per encoded
// form. Fake credentials only.

// applyForms is the set of encoded forms exercised at every application point:
// one of each family, plus the %-escaped and reversed shapes.
func applyForms(raw string) map[string]string {
	return map[string]string{
		"raw":       raw,
		"base64":    base64.StdEncoding.EncodeToString([]byte(raw)),
		"base64url": base64.RawURLEncoding.EncodeToString([]byte(raw)),
		"hex":       hex.EncodeToString([]byte(raw)),
		"hexupper":  strings.ToUpper(hex.EncodeToString([]byte(raw))),
		"urlenc":    url.QueryEscape(raw),
		"reversed":  revStr(raw),
	}
}

func credServer(t *testing.T, key string) *Server {
	t.Helper()
	s := builtinTestServer(t)
	s.apiKey = key
	s.rebuildCredScrubber()
	return s
}

// (a) OUTBOUND web_fetch: a URL carrying the credential is REFUSED and NOTHING
// is sent -- proven by a counting server that must receive zero requests.
func TestCredScrubApply_WebFetchRefusesAndSendsNothing(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	s := credServer(t, fakeCred)
	s.cfg = &Config{MCP: MCPConfig{Web: MCPWebConfig{AllowHosts: []string{"example.com"}}}}

	for name, form := range applyForms(fakeCred) {
		t.Run(name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"url":%q}`, srv.URL+"/?token="+form)
			res, err := s.builtinWebFetch(context.Background(), json.RawMessage(raw))
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError || !strings.Contains(res.Content, "refused") {
				t.Errorf("web_fetch was not refused for form %q: %+v", name, res)
			}
			if strings.Contains(res.Content, form) || strings.Contains(res.Content, fakeCred) {
				t.Errorf("the refusal echoed the credential for form %q: %q", name, res.Content)
			}
		})
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("the counting server received %d request(s); a refused fetch must send nothing", n)
	}
}

// (a) OUTBOUND web_search: a query carrying the credential is refused with no
// request to the search endpoint.
func TestCredScrubApply_WebSearchRefusesAndSendsNothing(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	s := credServer(t, fakeCred)
	s.cfg = &Config{MCP: MCPConfig{Web: MCPWebConfig{Endpoint: srv.URL}}}

	for name, form := range applyForms(fakeCred) {
		t.Run(name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"query":%q}`, "look up "+form)
			res, err := s.builtinWebSearch(context.Background(), json.RawMessage(raw))
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError || !strings.Contains(res.Content, "refused") {
				t.Errorf("web_search was not refused for form %q: %+v", name, res)
			}
		})
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("the search endpoint received %d request(s); a refused search must send nothing", n)
	}
}

// (b) TOOL RESULTS: a tool that reads a file holding the credential must not
// hand it to the model, in any form. Driven through the real agent loop: the
// model calls read_file, the daemon reads the file, and what reaches the model
// (the request bodies after the first) must carry the placeholder, not the key.
//
// fakeOpaque (no known prefix) is used so the HEURISTIC scrub never fires: this
// isolates the literal scrubber as the sole actor, so its own placeholder must
// appear. The filename is neutral (not "secret"/"key"), or MatchesSecretName
// would refuse the read before any of this -- the exact fixture trap the
// credential-read pass recorded.
func TestCredScrubApply_ToolResultRedactedEndToEnd(t *testing.T) {
	for name, form := range applyForms(fakeOpaque) {
		t.Run(name, func(t *testing.T) {
			base, _, bodies := agentUpstream(t,
				toolCallSSE("c1", "read_file", `{"path":"notes.txt"}`),
				textSSE("done"),
			)
			s := loopServer(t, base, allowReads())
			s.apiKey = fakeOpaque
			s.rebuildCredScrubber()
			if err := os.WriteFile(filepath.Join(s.workspace, "notes.txt"),
				[]byte("the value is "+form+" keep it safe"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := runLoop(t, s); err != nil {
				t.Fatalf("runAgentLoop: %v", err)
			}
			sent := sentToModel(bodies)
			if strings.Contains(sent, form) || strings.Contains(sent, fakeOpaque) {
				t.Errorf("the credential (form %q) reached the model in a tool result", name)
			}
			if !strings.Contains(sent, credPlaceholder) {
				t.Errorf("no credential placeholder in what reached the model for form %q", name)
			}
		})
	}
}

// (c) RETRIEVED CHUNKS + OUTGOING PROMPT: a chunk holding the credential, folded
// into the user message exactly as server.go does (buildAugmentedUserMessage
// then credRedact), reaches the provider with the key redacted, in any form.
func TestCredScrubApply_ChunkAndPromptRedacted(t *testing.T) {
	s := credServer(t, fakeOpaque) // heuristic-immune, so the literal scrubber is the sole actor
	for name, form := range applyForms(fakeOpaque) {
		t.Run(name, func(t *testing.T) {
			chunk := Chunk{FilePath: "config.go", StartLine: 1, EndLine: 2, Content: "const value = \"" + form + "\""}
			augmented := buildAugmentedUserMessage("what is the value?", []Chunk{chunk}, s.noScrub())
			out, n := s.credRedact(augmented)
			if n == 0 || strings.Contains(out, form) || strings.Contains(out, fakeOpaque) {
				t.Errorf("the credential (form %q) survived in the outgoing prompt: %q", name, out)
			}
		})
	}
	// The wiring is present: server.go redacts the assembled message before it
	// goes out. (A behaviour test cannot easily reach that line without a full
	// retrieval setup; this pins that the call is there.)
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "s.credRedact(augmentedPrompt)") {
		t.Error("server.go no longer redacts augmentedPrompt before sending it")
	}
}

// (d) --debug-context: the full chunk content it writes to the daemon log (and,
// with --log-file, to disk) must have the credential redacted, even though
// --debug-context is a verbose operator flag.
func TestCredScrubApply_DebugContextRedacted(t *testing.T) {
	logger, buf := bufferLogger()
	s := credServer(t, fakeCred)
	s.logger = logger
	s.debugContext = true
	form := base64.StdEncoding.EncodeToString([]byte(fakeCred))
	s.logRetrieval(retrievalOutcome{Chunks: []Chunk{
		{FilePath: "c.go", StartLine: 1, EndLine: 1, Content: "key=" + form},
	}})
	out := buf.String()
	if out == "" {
		t.Fatal("vacuity: --debug-context logged nothing")
	}
	if strings.Contains(out, form) || strings.Contains(out, fakeCred) {
		t.Errorf("--debug-context wrote the credential to the log: %q", out)
	}
	if !strings.Contains(out, credPlaceholder) {
		t.Errorf("no placeholder in the debug-context log: %q", out)
	}
}

// NOT GOVERNED BY --no-scrub: the literal scrubber runs even when the heuristic
// is disabled, at every application point. --no-scrub is scrubDisabled=true.
func TestCredScrubApply_NotDisableableByNoScrub(t *testing.T) {
	s := credServer(t, fakeCred)
	s.cfg = &Config{NoScrub: true, MCP: MCPConfig{Web: MCPWebConfig{AllowHosts: []string{"example.com"}}}}
	if !s.noScrub() {
		t.Fatal("test setup: --no-scrub not in effect")
	}
	form := base64.StdEncoding.EncodeToString([]byte(fakeCred))

	// Outbound refusal still fires.
	res, _ := s.builtinWebFetch(context.Background(), json.RawMessage(fmt.Sprintf(`{"url":"https://example.com/?t=%s"}`, form)))
	if !res.IsError || !strings.Contains(res.Content, "refused") {
		t.Error("--no-scrub disabled the outbound credential refusal")
	}
	// Redaction still happens (the heuristic scrub is what --no-scrub turns off).
	if out, n := s.credRedact("here: " + form); n == 0 || strings.Contains(out, form) {
		t.Error("--no-scrub disabled literal credential redaction")
	}
}

// /connect REBUILDS the registry, and forget keeps the LIVE key protected. The
// brief asked for "forget -> value no longer redacted"; the daemon deliberately
// does NOT clear the in-memory key on forget (connect_handler.go: cutting a
// serving daemon's credential mid-session would be an outage), so the live key
// stays registered -- which is the safe direction. What "the registry actually
// updates" means here is the SWAP: setProvider to a new key drops the old.
func TestCredScrubApply_RegistryUpdatesOnSwap(t *testing.T) {
	s := credServer(t, fakeCred)
	if _, n := s.credRedact("holding " + fakeCred); n == 0 {
		t.Fatal("the starting key is not protected")
	}
	// Swap to a different provider/key, as /connect does.
	s.setProvider(fakeOpaque, "", nil)
	if _, n := s.credRedact("holding " + fakeCred); n != 0 {
		t.Error("after a swap the OLD key is still redacted -- the registry did not update")
	}
	if _, n := s.credRedact("holding " + fakeOpaque); n == 0 {
		t.Error("after a swap the NEW key is not redacted")
	}
}
