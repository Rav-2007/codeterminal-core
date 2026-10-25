package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// WHAT A TOOL CALL WAS ABOUT, FOR THE LINE THE CLIENT DRAWS (2026-10-25).
//
// The activity line used to say only which tool ran and how many bytes went
// back -- "builtin__read_file — 17747 bytes to the model in 0ms" -- and the
// owner called it what it was: "so lame and stale, the reading file should
// look something highlighted". The client cannot do better on its own, because
// an activity carries no arguments. So the daemon names the target here: the
// path a read or edit touches, the command it runs, the query or URL it asks.
//
// ONLY BUILT-IN TOOLS, whose arguments this file knows the meaning of. An MCP
// server's arguments are its own business and are not guessed at.
//
// The target is the MODEL'S text, so it is treated as untrusted on the way out:
// control characters dropped, every run of whitespace one space (a command or
// query can hold newlines, and a line is a line), and at most maxTargetRunes.
// It goes only to the local client, the same user the approval prompt already
// shows the full arguments to, and is never logged or persisted.

// maxTargetRunes bounds a target: one line of a terminal, not a whole command.
const maxTargetRunes = 200

// withCallTarget fills in what the client is told a call is: the tool that was
// resolved, what it acts on, and whether it makes a new file -- "Write", in the
// terminal, rather than "Edit" (2026-10-25: the owner asked for write, modify,
// read and list each to read as itself, with the thing highlighted).
func withCallTarget(a protocol.ToolActivity, server, tool, arguments string) protocol.ToolActivity {
	a.Server, a.Tool, a.Target = server, tool, activityTarget(server, tool, arguments)
	a.Creates = server == mcp.BuiltinServerName && tool == "propose_edit" && editCreates(arguments)
	return a
}

// editCreates reports whether a propose_edit makes a new file: an empty search,
// as its description tells the model.
func editCreates(arguments string) bool {
	var args struct {
		Search *string `json:"search"`
	}
	return json.Unmarshal([]byte(arguments), &args) == nil && args.Search != nil && *args.Search == ""
}

// activityTarget is what a built-in tool call acts on, or "".
func activityTarget(server, tool, arguments string) string {
	if server != mcp.BuiltinServerName {
		return ""
	}
	var args map[string]any
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return ""
	}
	str := func(key string) string { s, _ := args[key].(string); return s }
	num := func(key string) (int, bool) { f, ok := args[key].(float64); return int(f), ok }
	quoted := func(s string) string {
		if s == "" {
			return ""
		}
		return `"` + s + `"`
	}
	within := func(target, path string) string {
		if path == "" || path == "." {
			return target
		}
		return target + " in " + path
	}

	var t string
	switch tool {
	case "read_file":
		t = str("path")
		if start, ok := num("start_line"); ok {
			t += fmt.Sprintf(":%d-", start)
			if end, ok := num("end_line"); ok {
				t += fmt.Sprint(end)
			}
		}
	case "list_directory", "propose_edit":
		t = str("path")
	case "propose_ast_edit":
		t = strings.TrimSpace(str("path") + " " + str("symbol"))
	case "query_compiler_definition", "query_compiler_references":
		t = str("path")
		if line, ok := num("line"); ok && t != "" {
			t += fmt.Sprintf(":%d", line)
		}
	case "search_code", "web_search":
		t = quoted(str("query"))
	case "grep":
		t = within(quoted(str("pattern")), str("path"))
	case "find_files":
		t = within(str("pattern"), str("path"))
	case "sandbox_exec":
		t = str("command")
	case "web_fetch":
		t = str("url")
	case "rename_symbol":
		if from, to := str("symbol"), str("new_name"); from != "" && to != "" {
			t = from + " → " + to
		}
	case "git_history":
		t = strings.TrimSpace(str("command") + " " + str("path"))
	case "github":
		t = str("what")
		if n, ok := num("number"); ok && n > 0 {
			t += fmt.Sprintf(" #%d", n)
		}
	case "investigate":
		t = quoted(str("question"))
	}
	return boundTarget(t)
}

// boundTarget makes model text safe to show on one line: no control
// characters, each whitespace run a single space, at most maxTargetRunes.
func boundTarget(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = b.Len() > 0
		case unicode.IsControl(r) || r == utf8.RuneError:
			// dropped
		default:
			if space {
				b.WriteByte(' ')
				space = false
			}
			b.WriteRune(r)
		}
	}
	out := b.String()
	if utf8.RuneCountInString(out) <= maxTargetRunes {
		return out
	}
	runes := []rune(out)
	return string(runes[:maxTargetRunes-1]) + "…"
}
