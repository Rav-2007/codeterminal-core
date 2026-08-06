// Command fakelsp is a language server that misbehaves on purpose.
//
// daemon/lsp_bridge.go spawns gopls, typescript-language-server or
// pyright-langserver by NAME, resolved through PATH. So the honest way to test
// it is to put a binary named gopls on PATH and let the real exec.Command find
// it: real process, real pipes, real Content-Length framing. Nothing is
// stubbed, which is the point — the sandbox defect this campaign found was
// invisible precisely because its tests asserted on an argument list instead of
// running anything.
//
// It cannot be configured through the environment. mcp.ServerEnv scrubs
// everything except PATH, HOME and a per-language toolchain allow-list, so a
// LSPFAKE_MODE variable would never arrive. That is the credential fix working,
// so instead of weakening it the mode is read from $HOME/mode and the received
// environment is dumped to $HOME/env.dump — which lets the test assert, from
// inside a real child process, that no inference credential came along.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	home := os.Getenv("HOME")

	// Record the environment we were actually given, so the test can assert on
	// it from the far side of the exec boundary rather than from source.
	if home != "" {
		_ = os.WriteFile(filepath.Join(home, "env.dump"),
			[]byte(strings.Join(os.Environ(), "\n")), 0o600)
	}

	mode := "ok"
	if b, err := os.ReadFile(filepath.Join(home, "mode")); err == nil {
		mode = strings.TrimSpace(string(b))
	}

	in := bufio.NewReader(os.Stdin)
	for {
		id, method, err := readRequest(in)
		if err != nil {
			return
		}
		if method == "" {
			continue // a notification; nothing to answer
		}
		respond(id, method, mode)
	}
}

// readRequest reads one LSP frame and returns its id and method. A notification
// (no id) yields an empty method so the caller knows not to answer.
func readRequest(in *bufio.Reader) (int64, string, error) {
	var length int
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return 0, "", err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break // end of headers
		}
		if v, ok := strings.CutPrefix(line, "Content-Length:"); ok {
			length, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	if length <= 0 {
		return 0, "", io.ErrUnexpectedEOF
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(in, body); err != nil {
		return 0, "", err
	}

	var req struct {
		ID     *int64 `json:"id"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return 0, "", err
	}
	if req.ID == nil {
		return 0, "", nil
	}
	return *req.ID, req.Method, nil
}

func respond(id int64, method, mode string) {
	// The handshake always succeeds except in the modes that specifically
	// target it, so a test aimed at a query is not tripped up by initialize.
	if method == "initialize" {
		switch mode {
		case "silent-init", "slow-init":
			if mode == "slow-init" {
				time.Sleep(10 * time.Minute)
			}
			select {} // never answer, and never exit
		case "die-on-init":
			os.Exit(1)
		}
		writeFrame(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"capabilities":{}}}`, id))
		return
	}

	switch mode {
	case "negative-length":
		// THE PANIC. lsp_bridge's readLoop did make([]byte, l) with whatever
		// integer arrived here; a negative one panics the goroutine, and an
		// unrecovered goroutine panic takes the entire daemon down with it.
		os.Stdout.WriteString("Content-Length: -1\r\n\r\n")
	case "huge-length":
		// THE ALLOCATION. Unbounded before the cap: the header alone decided
		// the size of a heap allocation, with no ceiling and no relationship to
		// the bytes the server was actually willing to send.
		os.Stdout.WriteString("Content-Length: 17179869184\r\n\r\n")
	case "oversize-body":
		// One byte over the cap, with a body that genuinely arrives, so the
		// boundary is tested rather than merely the header.
		body := strings.Repeat("x", maxLSPMessageBytes+1)
		writeFrame(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":"%s"}`, id, body))
	case "atcap-body":
		// Comfortably under the cap: this one must still be accepted, so the
		// cap is shown to bound hostile input without breaking large-but-legal
		// replies.
		body := strings.Repeat("x", 64*1024)
		writeFrame(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":"%s"}`, id, body))
	case "silent":
		return // accept the request, never answer it
	case "die":
		os.Exit(1) // the server dies mid-request
	case "lsp-error":
		writeFrame(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"no"}}`, id))
	case "symbols":
		// A realistic documentSymbol tree for the fixture the tests write, so
		// propose_ast_edit can be driven all the way to a real proposal rather
		// than stopping at "symbol not found".
		//
		// Lines are 0-indexed, as LSP specifies. Target is nested inside Outer
		// so findSymbol's descent is exercised, and its range covers the method
		// from `func` to its closing brace:
		//
		//	0  package main
		//	2  type Outer struct{}
		//	4  func (o Outer) Target() {
		//	5      return
		//	6  }
		writeFrame(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":[
			{"name":"Outer","kind":23,"range":{"start":{"line":2,"character":0},"end":{"line":6,"character":1}},
			 "children":[{"name":"Target","kind":6,"range":{"start":{"line":4,"character":0},"end":{"line":6,"character":1}}}]}
		]}`, id))
	case "garbage-symbols":
		// Well-framed JSON whose SHAPE is wrong. The daemon must report this as
		// a tool error rather than panicking on a type assertion.
		writeFrame(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":"not-an-array"}`, id))
	default:
		writeFrame(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":[]}`, id))
	}
}

func writeFrame(body string) {
	fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

// maxLSPMessageBytes mirrors the daemon's cap. Kept in sync by
// TestFakeLSPCapConstantMatchesDaemon, so this file cannot drift into testing a
// boundary that no longer exists.
const maxLSPMessageBytes = 8 * 1024 * 1024
