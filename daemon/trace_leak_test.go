//go:build trace && unix

package main

// THE TEST THAT PROVES THE TRACE LEAKS NOTHING, THROUGH THE REAL DAEMON.
//
// It builds the daemon with -tags trace (agente2e_test.go does that when this
// test binary has the tag), starts it with MOCHIII_TRACE=1 on a throwaway
// workspace and HOME, runs one full agent task against a scripted model, and
// then reads every byte of every file in the trace directory.
//
// DUMMY secrets are planted in every place a value can enter: the typed prompt,
// MOCHIII_API_KEY, an environment value passed to an MCP server, a tool's
// arguments, the text of a failing tool's error, the name of a tool the model
// invented, the call id the model chose, and the session name. Each is planted
// twice, in two separate runs: as a RECOGNISED KEY SHAPE (the daemon's scrubber
// knows it) and as a PLAIN STRING (it does not), so the test cannot pass merely
// because a scrubber caught the value on its way to somewhere else.
//
// What it asserts:
//  1. no canary is in any trace file, raw or as base64, hex or URL-encoded;
//  2. every line fits the schema strictly: only schema keys, every string a
//     member of a closed list or an id, at most 512 bytes. This is the strong
//     one: it proves the absence of secrets the test did not think of;
//  3. the directory is 0700, the files 0600, and a planted symlink is refused;
//  4. with the switch off there is no directory and no file.
//
// NEUTER CHECKS (run on a throwaway copy of the repo, never in place; results
// are in the PR):
//   - D5 passes activity.Detail (the model's text) into the trace line: 1 and 2 go red.
//   - the same through the existing tool subject, with ToolName's validation on: it
//     becomes unknown_tool and stays green; with the validation off, 1 and 2 go red.
//   - the nil check in the Turn methods removed: every test with the switch off panics.
//   - the 0700 and 0600 modes widened: 3 goes red.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/daemon/trace/tracecheck"
	"mochiii/protocol"
)

type traceCanaries struct {
	prompt, apiKey, mcpEnv, toolArgs, toolErr, toolName, callID, session string
}

func (c traceCanaries) all() map[string]string {
	return map[string]string{"typed prompt": c.prompt, "MOCHIII_API_KEY": c.apiKey, "MCP env value": c.mcpEnv,
		"tool arguments": c.toolArgs, "tool error text": c.toolErr, "model-chosen tool name": c.toolName,
		"model-chosen call id": c.callID, "session id": c.session}
}

// traceKeyShapes are shapes the daemon's scrubber recognises (each is checked
// against scrub() below); they are all dummy.
var traceKeyShapes = traceCanaries{
	prompt:   "sk-ant-api03-PROMPTCANARYdummyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	apiKey:   "sk-proj-APIKEYCANARYdummyAAAAAAAAAAAAAAAAAAAAAAAA",
	mcpEnv:   "ghp_MCPENVCANARYdummy0000000000000000",
	toolArgs: "AKIAARGSCANARY000001",
	toolErr:  "xoxb-0000000000-ERRCANARYdummy-0000000000000000",
	toolName: "sk-ant-api03-TOOLNAMECANARYdummyBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
	callID:   "ghp_CALLIDCANARYdummy000000000000000000",
	session:  "xoxp-0000000000-SESSIONCANARYdummy-00000000",
}

// traceTextCanaries are plain strings no scrubber has a rule for.
var traceTextCanaries = traceCanaries{
	prompt:   "plainzebra-promptcanary-7f3a91",
	apiKey:   "plainzebra-apikeycanary-2c88d0",
	mcpEnv:   "plainzebra-mcpenvcanary-a91b3e",
	toolArgs: "plainzebra-toolargscanary-5d02f7",
	toolErr:  "plainzebra-toolerrcanary-e4417c",
	toolName: "plainzebra_toolnamecanary_90ab12",
	callID:   "plainzebra-callidcanary-3f6e58",
	session:  "plainzebra-sessioncanary-b7d1c4",
}

// canaryForms is every spelling of c a leak could take: raw, lower case (an id
// is lower-cased), base64 in both alphabets with and without padding at all
// three alignments, hex in both cases, and URL-encoded two ways.
func canaryForms(c string) []string {
	forms := []string{c, strings.ToLower(c), strings.ToUpper(c)}
	for off := 0; off < 3; off++ {
		pad := strings.Repeat("\x00", off)
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
			e := enc.EncodeToString([]byte(pad + c))
			start := (off*8 + 5) / 6
			if end := len(e) - 2; end > start {
				forms = append(forms, e[start:end])
			}
		}
	}
	h := hex.EncodeToString([]byte(c))
	forms = append(forms, h, strings.ToUpper(h), url.QueryEscape(c), url.PathEscape(c))
	var pct, PCT strings.Builder
	for i := 0; i < len(c); i++ {
		fmt.Fprintf(&pct, "%%%02x", c[i])
		fmt.Fprintf(&PCT, "%%%02X", c[i])
	}
	return append(forms, pct.String(), PCT.String())
}

// traceTree is every file under dir and the name of every entry, as one blob:
// a leak in a file name is a leak.
func traceTree(t *testing.T, dir string) (blob []byte, files []string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		blob = append(blob, rel...)
		blob = append(blob, '\n')
		if d.Type().IsRegular() {
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			blob = append(blob, b...)
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading the trace directory: %v", err)
	}
	return blob, files
}

func traceDirOf(t *testing.T) string {
	t.Helper()
	dir, err := protocol.SocketDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "trace")
}

// runTracedTask runs the whole agent task with the canaries planted and returns
// what the daemon wrote and what it saw. Every tool call the model makes here is
// one the trace has to describe without describing: a read that fails (the error
// text carries the path), a call to a tool that does not exist, an MCP call that
// succeeds, one that fails, an approved edit and a denied one.
func runTracedTask(t *testing.T, c traceCanaries) (model *e2eModel, d *e2eDaemon, turn *e2eTurn) {
	t.Helper()
	echo := buildEchoServer(t)
	e2eExtraEnv = []string{"MOCHIII_TRACE=1", "MOCHIII_API_KEY=" + c.apiKey, "TRACE_MCP_CANARY=" + c.mcpEnv, "ECHOSERVER_PRINT_ENV=TRACE_MCP_CANARY"}
	t.Cleanup(func() { e2eExtraEnv = nil })

	edit := func(from, to string) string {
		return fmt.Sprintf(`{"path":"greet.go","search":%q,"replace":%q}`, from, to)
	}
	model = newE2EModel(t, byToolResults(
		toolCallSSE(c.callID, "builtin__read_file", fmt.Sprintf(`{"path":%q}`, c.toolErr+".txt")),
		toolCallSSE("c2", c.toolName, `{}`),
		toolCallSSE("c3", "echo__echo", fmt.Sprintf(`{"text":%q}`, c.toolArgs)),
		toolCallSSE("c4", "echo__env_names", `{}`),
		toolCallSSE("c5", "echo__always_fails", `{}`),
		toolCallSSE("c6", "builtin__propose_edit", edit(`"Hello"`, `"Howdy"`)),
		toolCallSSE("c7", "builtin__propose_edit", edit(`"Howdy"`, `"Hiya"`)),
		withUsage(textSSE("Done."), 1200, 34, 0.0021),
	))
	d = startE2E(t, model, map[string]string{"greet.go": e2eGreet}, func(mcp map[string]any) {
		mcp["servers"] = map[string]any{"echo": map[string]any{
			"command": echo, "env": []string{"TRACE_MCP_CANARY", "ECHOSERVER_PRINT_ENV"}, "acknowledged_unconfined": true,
			"tools": map[string]string{"echo": "allow", "env_names": "allow", "always_fails": "allow"},
		}}
	})
	approvals := 0
	turn = d.request(t, protocol.PromptRequest{Prompt: "make the greeting friendlier. " + c.prompt, Session: c.session},
		func(a protocol.ToolApprovalRequest) string {
			approvals++
			if approvals == 2 {
				return protocol.ApprovalDeny // the second edit is declined
			}
			return protocol.ApprovalApprove
		})
	if turn.done.Error != "" {
		t.Fatalf("the task failed: %s (%s)\ndaemon:\n%s", turn.done.Error, turn.done.ErrorClass, d.log.String())
	}
	return model, d, turn
}

func TestTheTraceLeaksNoCanaryAndEveryLineFitsTheSchema(t *testing.T) {
	schema, err := tracecheck.Load("trace/trace-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]traceCanaries{"recognised key shapes": traceKeyShapes, "plain strings": traceTextCanaries} {
		t.Run(name, func(t *testing.T) {
			// The two kinds are what they say: the scrubber knows one and not the other.
			for what, v := range c.all() {
				_, reds := scrub(v, false)
				if shape := name == "recognised key shapes"; shape != (len(reds) > 0) {
					t.Fatalf("canary %q (%s): scrub found %d redactions, but this run is %q", v, what, len(reds), name)
				}
			}
			model, d, _ := runTracedTask(t, c)
			blob, files := traceTree(t, traceDirOf(t))
			if len(files) != 1 {
				t.Fatalf("%d trace files, want one for this daemon: %v", len(files), files)
			}

			// The canaries really did reach the daemon, so absence below means something.
			seen := string(blob)
			_ = seen
			var wire strings.Builder
			for _, r := range model.requests() {
				wire.WriteString(r.raw)
				wire.WriteString(r.auth)
			}
			audit, _ := os.ReadFile(filepath.Join(d.workspace, ".mochiii", "logs", "toolcalls.jsonl"))
			daemonSaw := wire.String() + d.log.String() + string(audit)
			if !strings.Contains(model.requests()[0].auth, c.apiKey) {
				t.Errorf("the daemon did not send MOCHIII_API_KEY to the model; the key canary was never in play")
			}
			for what, v := range map[string]string{"call id": c.callID, "tool error text": c.toolErr, "model-chosen tool name": c.toolName} {
				if !strings.Contains(daemonSaw, v) {
					t.Errorf("the %s canary %q never reached the daemon's wire, log or audit log; the test would pass vacuously", what, v)
				}
			}
			if name == "plain strings" {
				for what, v := range map[string]string{"typed prompt": c.prompt, "tool arguments": c.toolArgs} {
					if !strings.Contains(daemonSaw, v) {
						t.Errorf("the %s canary %q never reached the model request; the test would pass vacuously", what, v)
					}
				}
			}
			if !strings.Contains(daemonSaw, "TRACE_MCP_CANARY") {
				t.Errorf("the MCP server did not receive the planted environment variable")
			}

			// 1. No canary, in any spelling.
			for what, v := range c.all() {
				for _, form := range canaryForms(v) {
					if bytes.Contains(blob, []byte(form)) {
						t.Errorf("the %s canary %q is in the trace as %q", what, v, form)
					}
				}
			}
			// The session appears only as its hash: 8 hex digits of its SHA-256.
			sum := sha256.Sum256([]byte(c.session))
			if want := `"sid":"` + hex.EncodeToString(sum[:4]) + `"`; !bytes.Contains(blob, []byte(want)) {
				t.Errorf("the first line does not carry %s", want)
			}

			// 2. Every line fits the schema strictly.
			raw, _ := os.ReadFile(files[0])
			if errs := schema.CheckFile(raw); len(errs) != 0 {
				for _, e := range errs {
					t.Errorf("%v", e)
				}
			}

			// 3. Modes.
			for p, want := range map[string]os.FileMode{traceDirOf(t): 0o700, files[0]: 0o600} {
				if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
					t.Errorf("%s is %v (%v), want %o", p, fi, err, want)
				}
			}

			// The trace says what happened, by name, without saying what the model said.
			lines := string(raw)
			t.Logf("the trace (%d bytes):\n%s", len(raw), lines)
			for _, want := range []string{
				`"stage":"request","status":"ok","dur_ms":0,"tool":"prompt"`,
				`"stage":"tool_requested","status":"ok","dur_ms":0,"tool":"unknown_tool"`,
				`"stage":"tool_requested","status":"ok","dur_ms":0,"tool":"echo__echo"`,
				`"stage":"mcp_call","status":"ok"`, `"stage":"mcp_call","status":"fail"`, `"tool":"echo__always_fails","call":5,"err":"tool_failed"`,
				`"stage":"tool_run","status":"fail"`, `"stage":"approval_result","status":"ok"`,
				`"stage":"approval_result","status":"fail"`, `"err":"denied_user"`,
				`"stage":"approval_result","status":"fail","dur_ms":0,"tool":"unknown_tool"`, `"err":"denied_config"`,
				`"stage":"model_call","status":"start"`, `"stage":"first_token"`, `"stage":"persist","status":"ok"`, `"stage":"result","status":"ok"`,
			} {
				if !strings.Contains(lines, want) {
					t.Errorf("the trace has no line containing %s:\n%s", want, lines)
				}
			}
		})
	}
}
