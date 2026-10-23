package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// THE MIRRORS ARE ENFORCED NOW, NOT REQUESTED.
//
// Three files in this project carry a comment asking a human to keep two
// clients in step:
//
//	slash.go:12    "Slash command surface for the TUI (and mirrored in the VS Code client)"
//	slash.go:189   "Mirrored verbatim in clients/vscode/src/safeGit.ts"
//	slashCommands.ts:1  "Keep names, kinds, and preambles in sync when either side changes"
//
// That arrangement has now failed twice, both times in the same direction --
// the TUI was fixed and the VS Code client was not, or the reverse:
//
//	4918a0c  the TUI still passed --config ./models.json, hours after the
//	         identical bug was fixed in the VS Code client
//	76a87d0  /init in the VS Code client still TOLD the user to run
//	         --config ./models.json, long after the TUI's copy of that same
//	         line was corrected
//
// A comment cannot fail a build. These tests can, and they cover the mirrors
// where drift is not cosmetic:
//
//	the slash catalog   preambles go ON THE WIRE to the model, so drift means
//	                    the same command behaves differently in the two clients
//	neutralisedGitConfig  a SECURITY list -- if one client neutralises a git
//	                    config key and the other does not, one client is
//	                    silently exploitable by a repository
//
// ANTI-VACUITY: a parser that quietly matches nothing would make all of this
// pass while comparing empty lists against empty lists -- the exact failure
// this project has hit three times now. Every parse asserts a plausible count
// before comparing, and TestClientParityParsersAreNotVacuous drives the
// parsers over known-bad input to prove they can fail at all.

const (
	vscodeSlashCommands = "../vscode/src/slashCommands.ts"
	vscodeSafeGit       = "../vscode/src/safeGit.ts"
)

// tsUnquote turns a single-quoted TypeScript string literal into its value.
// Only the escapes this project's catalog actually uses are handled, and an
// unknown escape is an ERROR rather than a silent passthrough -- a parser that
// guesses is a parser that reports false agreement.
func tsUnquote(lit string) (string, error) {
	lit = strings.TrimSpace(lit)
	if len(lit) < 2 || lit[0] != '\'' || lit[len(lit)-1] != '\'' {
		return "", fmt.Errorf("not a single-quoted literal: %q", lit)
	}
	body := lit[1 : len(lit)-1]

	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			b.WriteByte(body[i])
			continue
		}
		i++
		if i >= len(body) {
			return "", fmt.Errorf("trailing backslash in %q", lit)
		}
		switch body[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '\'':
			b.WriteByte('\'')
		case '\\':
			b.WriteByte('\\')
		default:
			return "", fmt.Errorf("unhandled escape \\%c in %q", body[i], lit)
		}
	}
	return b.String(), nil
}

// tsArrayObjects returns the source text of each top-level {...} object inside
// the first bracketed array following marker. Brace-depth aware, and
// string-aware so a brace inside a preamble cannot end an object early.
func tsArrayObjects(src, marker string) ([]string, error) {
	start := strings.Index(src, marker)
	if start < 0 {
		return nil, fmt.Errorf("marker %q not found", marker)
	}
	// Anchor on the ASSIGNMENT, not on the first bracket. A declaration reads
	// `export const SLASH_CATALOG: SlashDef[] = [`, so the first '[' after the
	// marker belongs to the TYPE -- and scanning from there meets that type's
	// ']' immediately, returning zero objects and no error. Measured: the first
	// run of this file parsed 0 entries and was caught only by the count
	// assertion in the caller.
	eq := strings.Index(src[start:], "=")
	if eq < 0 {
		return nil, fmt.Errorf("no assignment after marker %q", marker)
	}
	open := strings.Index(src[start+eq:], "[")
	if open < 0 {
		return nil, fmt.Errorf("no [ after the assignment for marker %q", marker)
	}
	i := start + eq + open + 1

	var objs []string
	depth, objStart, inStr, escaped := 0, -1, false, false
	for ; i < len(src); i++ {
		c := src[i]
		if inStr {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '\'':
				inStr = false
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
		case '{':
			if depth == 0 {
				objStart = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && objStart >= 0 {
				objs = append(objs, src[objStart:i+1])
				objStart = -1
			}
		case ']':
			if depth == 0 {
				return objs, nil
			}
		}
	}
	return nil, fmt.Errorf("unterminated array after marker %q", marker)
}

// tsField reads `key: <value>` out of one object's source. Returns the raw
// value text, trimmed of its trailing comma.
func tsField(obj, key string) (string, bool) {
	needle := key + ":"
	idx := 0
	for {
		rel := strings.Index(obj[idx:], needle)
		if rel < 0 {
			return "", false
		}
		at := idx + rel
		// Must be a field position, not a substring of a longer key.
		if at > 0 {
			prev := obj[at-1]
			if prev != '{' && prev != ',' && prev != '\n' && prev != ' ' && prev != '\t' {
				idx = at + len(needle)
				continue
			}
		}
		rest := obj[at+len(needle):]

		// Scan to the value's end: a comma or newline at string depth zero.
		inStr, escaped := false, false
		for i := 0; i < len(rest); i++ {
			c := rest[i]
			if inStr {
				switch {
				case escaped:
					escaped = false
				case c == '\\':
					escaped = true
				case c == '\'':
					inStr = false
				}
				continue
			}
			if c == '\'' {
				inStr = true
				continue
			}
			if c == ',' || c == '\n' || c == '}' {
				return strings.TrimSpace(rest[:i]), true
			}
		}
		return strings.TrimSpace(rest), true
	}
}

type tsSlashDef struct {
	name, kind, summary, preamble, promptKind, mode string
	needsArgs                                       bool
}

func parseVSCodeCatalog(t *testing.T) map[string]tsSlashDef {
	t.Helper()
	raw, err := os.ReadFile(vscodeSlashCommands)
	if err != nil {
		t.Fatalf("reading the VS Code catalog: %v", err)
	}
	objs, err := tsArrayObjects(string(raw), "export const SLASH_CATALOG")
	if err != nil {
		t.Fatalf("parsing SLASH_CATALOG: %v", err)
	}

	out := map[string]tsSlashDef{}
	for _, o := range objs {
		var d tsSlashDef
		str := func(key string) string {
			v, ok := tsField(o, key)
			if !ok {
				return ""
			}
			s, err := tsUnquote(v)
			if err != nil {
				t.Fatalf("field %q in %s: %v", key, o, err)
			}
			return s
		}
		d.name = str("name")
		d.kind = str("kind")
		d.summary = str("summary")
		d.preamble = str("preamble")
		d.promptKind = str("promptKind")
		d.mode = str("mode")
		if v, ok := tsField(o, "needsArgs"); ok {
			d.needsArgs = v == "true"
		}
		if d.name == "" {
			t.Fatalf("catalog object with no name: %s", o)
		}
		out[d.name] = d
	}
	return out
}

// summaryMayDiffer lists commands whose one-line summary is ALLOWED to differ,
// with the reason. Anything not listed here must match exactly.
var summaryMayDiffer = map[string]string{
	"connect": "the TUI's /connect also takes `local` (or a server's name) to find a model " +
		"server on this computer, a terminal-only feature by the owner's decision " +
		"(2026-10-23); the extension's /connect takes a provider and a key only",
	// Both read the provider's own bill rather than estimating -- VS Code's
	// stopped estimating in Stage 3 of the 0.2.0 overhaul, which is why this
	// moved out of notYetPorted. What differs is the WINDOW each reports over:
	// the TUI sums the session (it holds a spend tally across turns), and the
	// extension reports the LAST TURN, because TurnUsage is what reaches it on
	// the done message and it keeps no running total yet. Summarising one as the
	// other would be the lie the measurement work existed to remove.
	"usage": "the TUI sums the session; the extension reports the last turn, which is " +
		"what TurnUsage gives it on the done message (0.2.0 Stage 3)",

	"model": "the TUI's /model on its own opens a list to choose from (owner, 2026-10-12); the " +
		"extension's still prints the tiers, so its summary must not promise a list",
	"exit": "the command genuinely does different things: the TUI quits the " +
		"process, the extension closes a panel and leaves the editor running",
	"mcp-server": "the TUI's asks the RUNNING daemon for its tools and adds, " +
		"changes or removes the user's own MCP servers in the terminal " +
		"(2026-10-11); the extension's opens its Customize page on the MCP " +
		"servers, with the marketplace beside them (2026-10-13), where they " +
		"are added and managed with buttons, so each summary names what its " +
		"own command does",
	"clear": "the TUI's /clear starts a new chat, on the daemon too (2026-10-11): " +
		"emptying only the screen left the stored chat behind, and /save wrote " +
		"it. The extension's still empties only its panel and sends no reset -- " +
		"nor does its own new-chat (see notYetPorted: history) -- so its summary " +
		"must go on saying that until the port",
}

// tuiOnlyCommands are commands that exist in the terminal client and MUST NOT
// exist in the VS Code one, with the reason. This is a narrow list on purpose:
// the default is parity, and every entry here is a claim that the command is
// meaningless in the other client rather than merely unimplemented.
var tuiOnlyCommands = map[string]string{
	"mouse": "it toggles terminal mouse reporting, which decides whether the " +
		"terminal handles click-drag selection itself. The extension is a " +
		"webview: the editor owns selection there and there is nothing to trade",
	"agent": "it sets what a model on this computer may do, and models on this " +
		"computer are a terminal feature by the owner's decision (2026-10-23): " +
		"the extension reaches one only through its mochiii.apiBase setting",
	"web": "it lets a model on this computer reach the internet, part of the " +
		"same terminal-only feature as /agent (owner, 2026-10-23)",
}

// vscodeOnlyCommands is tuiOnlyCommands' mirror: commands that exist in the VS
// Code client and MUST NOT exist in the terminal one. Same narrow bar -- each
// entry claims the command is MEANINGLESS in a terminal, not merely unwritten
// there.
//
// This direction did not exist until the 0.2.0 overhaul, because until then the
// extension had never led on a command: every divergence ran TUI-first, which is
// why the two maps below are younger than the two above.
var vscodeOnlyCommands = map[string]string{
	"config": "it opens the chat's Settings page (Stage 8; VS Code's own settings " +
		"editor before that). The terminal client has no settings UI to open -- its " +
		"configuration is models.json and the environment, and /mcp-server and " +
		"/connect already reach the parts a command can change",
}

// notYetPortedToTUI is notYetPorted's mirror: commands the terminal client WILL
// have and does not yet. Each reads as a debt rather than a decision, and each
// arrived in the VS Code client in Stage 6 of the 0.2.0 overhaul
// (docs/VSCODE_UX_OVERHAUL_2026-10-12.md), where the stage's scope was the
// extension.
//
// THE DEBT IS REAL AND IS RECORDED HERE RATHER THAN SETTLED, because porting ten
// commands into the terminal client was not in that stage's scope and doing it
// unasked would have put ten untested commands in a second client. The gate
// still fails on any NEW divergence, which is what keeps this list from becoming
// a way to silence it.
var notYetPortedToTUI = map[string]string{
	"diff":   "shows the workspace's uncommitted changes; the terminal has /git for status and gets this with it",
	"copy":   "copies the last reply to the system clipboard; a terminal can do it and does not yet",
	"export": "writes the conversation to a Markdown file through a save dialog; the terminal needs its own path prompt first",
	"effort": "sets reasoning_effort from the chat; the terminal sets it per tier in models.json and has no per-turn command",
	"mode":   "sets the edit mode from the chat; the terminal has no mode picker to agree with yet",
	"cost":   "the last turn's bill on its own. The TUI's /usage reports the session, which covers the same figures over a wider window",
	"status": "workspace, model, tools, key and daemon in one answer; the terminal spreads these across /context, /account and /mcp-server",
	"doctor": "checks the things that commonly stop a first run working; written for the install the extension has and the terminal does not",
	"mcp": "opens a dialog of every MCP server by source, with its state and Authenticate, Set key and Check connection " +
		"(2026-10-15); the terminal manages its servers with /mcp-server and has no such dialog yet",
}

// notYetPorted are commands the VS Code client WILL have and does not yet --
// unlike tuiOnlyCommands, which it must never have. Each entry names where the
// port is scheduled, so the list reads as a debt with a due date rather than
// a design decision.
var notYetPorted = map[string]string{
	// The daemon side is client-neutral (protocol.HistoryRequest); the VS Code
	// port is the next step of the /history plan. Until it lands, VS Code's own
	// "new chat" still never sends a reset, so it neither deleted chats before
	// nor saves them now.
	"history": "VS Code port is the next step of the /history plan (README, /history)",
	// /save is /history save under a shorter name (2026-10-05); it has nothing
	// to save through until /history itself is ported, and goes with it.
	"save":   "the short form of /history save; ported together with /history",
	"resume": "the short form of /history resume (2026-10-05); ported together with /history",
	// The limits themselves hold for every client -- the daemon keeps them
	// (daemon/spend.go) -- and one that names no session is held by the day's
	// only, and told to raise it in the configuration. What VS Code lacks is
	// the command that raises a limit from the client (2026-10-08).
	"budget": "the daemon limits every client; VS Code has no way to raise a limit until it sends a session and gets this command",
	// Saved rules reach every client's turns already -- the daemon adds them
	// (daemon/userrules.go). What VS Code lacks is the three commands that
	// change them (2026-10-13), held back while its 0.2.0 overhaul settles.
	"remember": "the daemon applies saved rules to every client; the commands that change them come to VS Code after its 0.2.0 overhaul",
	"rules":    "ported together with /remember",
	"forget":   "ported together with /remember",
	// GitHub (protocol/github.go) is client-neutral in the daemon, as the rules
	// are; the terminal has the commands first (owner's plan, 2026-10-15).
	"github": "the daemon keeps the token for every client; the extension gets the commands after its 0.2.0 overhaul",
	"issue":  "ported together with /github",
	"issues": "ported together with /github",
	"pr":     "ported together with /github",
	// Long tasks (daemon/longtask.go) are terminal-first by the owner's
	// decision (2026-09-30): the daemon side is client-neutral, and VS Code has
	// no task meter or /task review yet.
	"task": "long tasks are terminal-first (owner, 2026-09-30); VS Code gets them when it is ported",
	"hunt": "long tasks are terminal-first (owner, 2026-09-30); VS Code gets them when it is ported",
	"provider": "the provider list is the terminal client's setup screen (2026-10-11), opened by its own " +
		"name since 2026-10-12; the extension has no such screen yet, and its /connect takes a key only",
}

// longTaskAheadOfVSCode are commands both clients have, which the TUI now runs
// as LONG TASKS (a daemon-enforced mode) while VS Code still sends its old
// one-turn preamble. Narrow on purpose, and checked in both directions: the TUI
// side must really send a long-task mode, the VS Code side must really still be
// the old form -- so the exception retires itself the day VS Code is ported.
//
// Not a security gap in either direction: the mode adds a method, a budget and
// a finish gate; it restricts nothing the other client's turn would reach.
var longTaskAheadOfVSCode = map[string]string{
	"fix":      "runs as a long task in the TUI; VS Code left as it is for now (owner, 2026-09-30)",
	"debug":    "runs as a long task in the TUI; VS Code left as it is for now (owner, 2026-09-30)",
	"refactor": "runs as a long task in the TUI; VS Code left as it is for now (owner, 2026-09-30)",
}

func TestSlashCatalogsAgreeAcrossClients(t *testing.T) {
	ts := parseVSCodeCatalog(t)

	// ANTI-VACUITY: a parser that found nothing would pass every comparison
	// below by never running one.
	if len(ts) < 20 {
		t.Fatalf("parsed only %d entries from %s; the parser is not reading the "+
			"catalog and every comparison below is vacuous", len(ts), vscodeSlashCommands)
	}
	// Counted from BOTH sides' allowlists. Subtracting only the TUI's was
	// correct while every divergence ran TUI-first; with commands the extension
	// has and the terminal does not, the same equation has to discount those too
	// or it reports a size difference that both lists already explain.
	goShared := len(slashCatalog) - len(tuiOnlyCommands) - len(notYetPorted)
	tsShared := len(ts) - len(vscodeOnlyCommands) - len(notYetPortedToTUI)
	if tsShared != goShared {
		t.Errorf("catalog size differs: Go has %d (%d TUI-only, %d not yet in VS Code) -> %d shared; "+
			"VS Code has %d (%d VS Code-only, %d not yet in the TUI) -> %d shared",
			len(slashCatalog), len(tuiOnlyCommands), len(notYetPorted), goShared,
			len(ts), len(vscodeOnlyCommands), len(notYetPortedToTUI), tsShared)
	}
	for name := range notYetPorted {
		if _, present := ts[name]; present {
			t.Errorf("/%s is listed as not yet ported but the VS Code client now has it; remove the entry", name)
		}
	}

	// A command listed as TUI-only must actually be absent over there. Without
	// this the list would be a way to silence the gate rather than to record a
	// decision: adding /mouse to the extension later would leave this entry
	// claiming a difference that no longer exists.
	for name, why := range tuiOnlyCommands {
		if _, present := ts[name]; present {
			t.Errorf("/%s is listed as TUI-only (%s) but the VS Code client now has it. "+
				"Remove the exception and mirror the summary.", name, why)
		}
	}

	for _, g := range slashCatalog {
		v, ok := ts[g.Name]
		if !ok {
			if why, allowed := tuiOnlyCommands[g.Name]; allowed {
				t.Logf("/%s is TUI-only on purpose: %s", g.Name, why)
				continue
			}
			if why, pending := notYetPorted[g.Name]; pending {
				t.Logf("/%s is not in the VS Code client YET: %s", g.Name, why)
				continue
			}
			t.Errorf("/%s exists in the TUI but not in the VS Code client", g.Name)
			continue
		}
		wantKind := "steered"
		if g.Kind == slashLocal {
			wantKind = "local"
		}
		if v.kind != wantKind {
			t.Errorf("/%s kind differs: Go=%s VS Code=%s", g.Name, wantKind, v.kind)
		}
		if v.needsArgs != g.NeedsArgs {
			t.Errorf("/%s needsArgs differs: Go=%v VS Code=%v", g.Name, g.NeedsArgs, v.needsArgs)
		}
		if why, ahead := longTaskAheadOfVSCode[g.Name]; ahead {
			switch {
			case !isLongTaskMode(g.Mode):
				t.Errorf("/%s is listed as a long task ahead of VS Code, but the TUI sends mode %q; "+
					"remove the entry or restore the mode", g.Name, g.Mode)
			case v.mode == g.Mode:
				t.Errorf("/%s now sends the same long-task mode in VS Code; remove it from "+
					"longTaskAheadOfVSCode and mirror the summary", g.Name)
			case v.mode != "" || v.preamble == "":
				t.Errorf("/%s in VS Code is neither the old one-turn preamble nor the TUI's mode "+
					"(mode %q, preamble %q); the two clients have drifted some third way", g.Name, v.mode, v.preamble)
			default:
				t.Logf("/%s: %s", g.Name, why)
			}
			continue
		}
		if v.promptKind != g.PromptKind {
			t.Errorf("/%s promptKind differs: Go=%q VS Code=%q", g.Name, g.PromptKind, v.promptKind)
		}
		// MODE GOES ON THE WIRE AND IS A SECURITY CONTROL, which makes it the
		// most load-bearing field compared here -- more so than the preamble
		// the comment below rightly protects.
		//
		// It was not compared until 2026-09-01, and the gap was live: deleting
		// `mode: 'plan'` from the VS Code catalog left TypeScript compiling
		// (the field is optional), chatPanel's `slash.def.mode ?? mode` falling
		// back to the picker, VS Code's /plan silently reverting to the full
		// menu including sandbox_exec -- and this test green. One deleted line
		// reopened the defect db2c2b2 was written to close.
		if v.mode != g.Mode {
			t.Errorf("/%s MODE differs -- mode goes on the wire and decides which tools the "+
				"daemon registers, so drift here means one client enforces a restriction the "+
				"other only describes:\n  Go:      %q\n  VS Code: %q", g.Name, g.Mode, v.mode)
		}
		// The preamble is the one that is not cosmetic: it is prepended to the
		// user's text and sent to the model, so drift here means the same
		// command produces different behaviour in the two clients.
		if v.preamble != g.Preamble {
			t.Errorf("/%s PREAMBLE differs -- this text goes on the wire to the model, so the "+
				"same command behaves differently in the two clients:\n  Go:      %q\n  VS Code: %q",
				g.Name, g.Preamble, v.preamble)
		}
		if v.summary != g.Summary {
			if why, allowed := summaryMayDiffer[g.Name]; allowed {
				t.Logf("/%s summary intentionally not mirrored: %s", g.Name, why)
			} else {
				t.Errorf("/%s summary differs:\n  Go:      %q\n  VS Code: %q\n"+
					"If this is deliberate, add it to summaryMayDiffer with the reason",
					g.Name, g.Summary, v.summary)
			}
		}
	}

	for name := range ts {
		if lookupSlash(name) != nil {
			continue
		}
		if why, allowed := vscodeOnlyCommands[name]; allowed {
			t.Logf("/%s is VS Code-only on purpose: %s", name, why)
			continue
		}
		if why, pending := notYetPortedToTUI[name]; pending {
			t.Logf("/%s is not in the TUI YET: %s", name, why)
			continue
		}
		t.Errorf("/%s exists in the VS Code client but not in the TUI. Port it, or add it to "+
			"vscodeOnlyCommands (meaningless in a terminal) or notYetPortedToTUI (a debt) with "+
			"the reason", name)
	}

	// AND THE TWO NEW LISTS MUST DESCRIBE REAL DIFFERENCES, exactly as the
	// tuiOnlyCommands check above insists. Without this they would be a way to
	// silence the gate rather than to record a decision: porting /doctor to the
	// terminal later would leave an entry claiming a difference that had gone.
	for name, why := range vscodeOnlyCommands {
		if lookupSlash(name) != nil {
			t.Errorf("/%s is listed as VS Code-only (%q) but the TUI has it now; remove the entry", name, why)
		}
		if _, present := ts[name]; !present {
			t.Errorf("/%s is listed as VS Code-only but the VS Code client does not have it either", name)
		}
	}
	for name, why := range notYetPortedToTUI {
		if lookupSlash(name) != nil {
			t.Errorf("/%s is listed as not yet in the TUI (%q) but it is there now; remove the entry", name, why)
		}
		if _, present := ts[name]; !present {
			t.Errorf("/%s is listed as not yet in the TUI but the VS Code client does not have it either", name)
		}
	}
}

// tsStringArray returns the string literals of the first bracketed array
// assigned after marker.
//
// Anchored on the ASSIGNMENT rather than the first bracket, for the reason
// tsArrayObjects learned the hard way and this declaration would have repeated:
// `export const TEAM_PIPELINE: readonly string[] = [...]` carries a '[' in its
// TYPE, and scanning from there meets that type's ']' immediately and returns
// nothing at all.
func tsStringArray(src, marker string) ([]string, error) {
	start := strings.Index(src, marker)
	if start < 0 {
		return nil, fmt.Errorf("marker %q not found", marker)
	}
	eq := strings.Index(src[start:], "=")
	if eq < 0 {
		return nil, fmt.Errorf("no assignment after marker %q", marker)
	}
	open := strings.Index(src[start+eq:], "[")
	if open < 0 {
		return nil, fmt.Errorf("no [ after the assignment for marker %q", marker)
	}
	from := start + eq + open + 1
	end := strings.Index(src[from:], "]")
	if end < 0 {
		return nil, fmt.Errorf("unterminated array for marker %q", marker)
	}

	var out []string
	for _, field := range strings.Split(src[from:from+end], ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		v, err := tsUnquote(field)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// THE DEFAULT TEAM SHAPE IS A WIRE VALUE, in exactly the same class as a
// steered preamble: it is what the client asks the daemon to run. Two clients
// sending different shapes for the same command is two products, and the shape
// is not a detail -- §14 measured researcher-then-coder ahead 2-1 while the
// four-phase shape lost 1-2 at three and a half times the wall clock, so drift
// here is drift into the measured loser.
//
// The two constants live in separate modules with no shared source, which is
// why this is a test and not a comment asking someone to remember.
func TestTheTeamShapeAgreesAcrossClients(t *testing.T) {
	raw, err := os.ReadFile(vscodeSlashCommands)
	if err != nil {
		t.Fatalf("reading the VS Code catalog: %v", err)
	}
	got, err := tsStringArray(string(raw), "TEAM_PIPELINE")
	if err != nil {
		t.Fatalf("parsing TEAM_PIPELINE: %v", err)
	}
	// ANTI-VACUITY: a parser that found nothing would agree with nothing.
	if len(got) == 0 {
		t.Fatal("parsed an empty TEAM_PIPELINE; this test is comparing nothing")
	}
	if len(got) != len(teamPipeline) {
		t.Fatalf("shape length differs: Go %v, VS Code %v", teamPipeline, got)
	}
	for i := range got {
		if got[i] != teamPipeline[i] {
			t.Errorf("phase %d differs: Go %q, VS Code %q", i, teamPipeline[i], got[i])
		}
	}
}

// The security mirror. If one client neutralises a git config key and the other
// does not, that client is exploitable by a repository through exactly the
// vector both files were written to close.
func TestNeutralisedGitConfigAgreesAcrossClients(t *testing.T) {
	raw, err := os.ReadFile(vscodeSafeGit)
	if err != nil {
		t.Fatalf("reading the VS Code safeGit: %v", err)
	}

	start := strings.Index(string(raw), "const NEUTRALISED_CONFIG")
	if start < 0 {
		t.Fatal("NEUTRALISED_CONFIG not found; the VS Code client may have renamed or " +
			"removed the list this test exists to compare against")
	}
	open := strings.Index(string(raw)[start:], "[")
	end := strings.Index(string(raw)[start:], "]")
	if open < 0 || end < 0 || end < open {
		t.Fatal("could not delimit NEUTRALISED_CONFIG")
	}

	var got []string
	for _, line := range strings.Split(string(raw)[start+open+1:start+end], "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ","))
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		v, err := tsUnquote(line)
		if err != nil {
			t.Fatalf("parsing NEUTRALISED_CONFIG entry: %v", err)
		}
		got = append(got, v)
	}

	// ANTI-VACUITY.
	if len(got) == 0 {
		t.Fatal("parsed zero entries from NEUTRALISED_CONFIG; this test would compare " +
			"nothing and pass")
	}
	if len(got) != len(neutralisedGitConfig) {
		t.Fatalf("the two clients neutralise a DIFFERENT NUMBER of git config keys, so one of "+
			"them is weaker against a hostile repository:\n  Go:      %v\n  VS Code: %v",
			neutralisedGitConfig, got)
	}
	for i, want := range neutralisedGitConfig {
		if got[i] != want {
			t.Errorf("neutralised key %d differs: Go=%q VS Code=%q", i, want, got[i])
		}
	}
}

// The TUI half of the /init assertion. Its VS Code mirror is
// clients/vscode/src/test/suite/localCommandsHostile.test.ts, and both exist
// because this exact line drifted between the clients: the TUI's was corrected
// when the vector was fixed and the VS Code copy went on recommending it.
func TestInitChecklistDoesNotRecommendARelativeConfig(t *testing.T) {
	text := formatInitChecklist("/some/workspace")
	if strings.Contains(text, "--config ./") || strings.Contains(text, "--config .\\") {
		t.Fatalf("/init tells the user to pass a relative --config, which resolves against "+
			"whatever directory they happen to be in, and `mcp list` STARTS the servers a "+
			"config names:\n%s", text)
	}
}

// The parsers must be able to FAIL. Everything above is only as good as this.
func TestClientParityParsersAreNotVacuous(t *testing.T) {
	if _, err := tsArrayObjects("const X = [{ name: 'a' }];", "NOT_PRESENT"); err == nil {
		t.Error("tsArrayObjects accepted a marker that does not exist")
	}
	if _, err := tsUnquote("not quoted"); err == nil {
		t.Error("tsUnquote accepted an unquoted value")
	}
	if _, err := tsUnquote(`'bad \q escape'`); err == nil {
		t.Error("tsUnquote silently passed through an unknown escape, which would let a " +
			"preamble compare equal when it is not")
	}

	got, err := tsUnquote(`'a\'b\nc'`)
	if err != nil || got != "a'b\nc" {
		t.Errorf("tsUnquote(%q) = %q, %v; want %q", `'a\'b\nc'`, got, err, "a'b\nc")
	}

	objs, err := tsArrayObjects("export const C = [\n{ name: 'a', s: 'x}y' },\n{ name: 'b' },\n];", "export const C")
	if err != nil {
		t.Fatalf("tsArrayObjects: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("tsArrayObjects found %d objects, want 2 (a brace inside a string must not "+
			"end an object): %q", len(objs), objs)
	}

	// The TYPE ANNOTATION case, which is how the real declaration is written and
	// which this file got wrong on its first run: anchoring on the first '['
	// after the marker finds the one in `Def[]` and parses zero objects while
	// returning no error.
	typed, err := tsArrayObjects("export const C: Def[] = [\n{ name: 'a' },\n];", "export const C")
	if err != nil {
		t.Fatalf("tsArrayObjects with a type annotation: %v", err)
	}
	if len(typed) != 1 {
		t.Fatalf("a `Def[]` type annotation defeated the parser: found %d objects, want 1", len(typed))
	}

	if v, ok := tsField(`{ name: 'a', needsArgs: true }`, "needsArgs"); !ok || v != "true" {
		t.Errorf("tsField(needsArgs) = %q, %v", v, ok)
	}
	// A key that only appears as a substring of another key must not match.
	if v, ok := tsField(`{ xname: 'wrong', name: 'right' }`, "name"); !ok || v != "'right'" {
		t.Errorf("tsField matched a substring key: got %q, %v", v, ok)
	}
}
