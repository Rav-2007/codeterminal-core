package main

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
	"exit": "the command genuinely does different things: the TUI quits the " +
		"process, the extension closes a panel and leaves the editor running",
}

// tuiOnlyCommands are commands that exist in the terminal client and MUST NOT
// exist in the VS Code one, with the reason. This is a narrow list on purpose:
// the default is parity, and every entry here is a claim that the command is
// meaningless in the other client rather than merely unimplemented.
var tuiOnlyCommands = map[string]string{
	"mouse": "it toggles terminal mouse reporting, which decides whether the " +
		"terminal handles click-drag selection itself. The extension is a " +
		"webview: the editor owns selection there and there is nothing to trade",
}

func TestSlashCatalogsAgreeAcrossClients(t *testing.T) {
	ts := parseVSCodeCatalog(t)

	// ANTI-VACUITY: a parser that found nothing would pass every comparison
	// below by never running one.
	if len(ts) < 20 {
		t.Fatalf("parsed only %d entries from %s; the parser is not reading the "+
			"catalog and every comparison below is vacuous", len(ts), vscodeSlashCommands)
	}
	if len(ts) != len(slashCatalog)-len(tuiOnlyCommands) {
		t.Errorf("catalog size differs: Go has %d (%d of them TUI-only), VS Code has %d",
			len(slashCatalog), len(tuiOnlyCommands), len(ts))
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
		if lookupSlash(name) == nil {
			t.Errorf("/%s exists in the VS Code client but not in the TUI", name)
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
