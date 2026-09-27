package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// /spec: write a spec, make it the ACTIVE spec, check the project against it.
// The daemon side (what a spec is, how it anchors a turn, how a check is
// graded) is daemon/spec.go; this is the client's half.
//
//	/spec <goal>       write a spec for goal (a turn in mode "spec")
//	/spec use <file>   make specs/<file> the active spec
//	/spec off          no active spec
//	/spec show         (or bare /spec) the active spec and the specs there are
//	/spec check        check the project against the active spec
//
// The active spec rides on every prompt (PromptRequest.Spec) until switched
// off, and is remembered per workspace across restarts.

const (
	modeSpec  = "spec"
	modeCheck = "check"
	specsDir  = "specs"
)

// specStateFile is where the active spec is remembered for one workspace.
// Under the state dir the daemon uses too ($XDG_STATE_HOME/mochiii or
// ~/.local/state/mochiii), never in the project.
func specStateFile(workspaceRoot string) (string, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "mochiii", "active-spec-"+protocol.WorkspaceTag(workspaceRoot)), nil
}

// loadActiveSpec returns the remembered active spec, or "" -- also when the
// file it names has since gone, so a deleted spec is not sent forever.
func loadActiveSpec(workspaceRoot string) string {
	path, err := specStateFile(workspaceRoot)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	rel := strings.TrimSpace(string(data))
	if _, err := os.Stat(filepath.Join(workspaceRoot, filepath.FromSlash(rel))); err != nil {
		return ""
	}
	return rel
}

func saveActiveSpec(workspaceRoot, rel string) error {
	path, err := specStateFile(workspaceRoot)
	if err != nil {
		return err
	}
	if rel == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(rel+"\n"), 0o600)
}

// isSpecPath reports whether a workspace-relative path is a spec file.
func isSpecPath(rel string) bool {
	rel = filepath.ToSlash(filepath.Clean(rel))
	return strings.HasPrefix(rel, specsDir+"/") && strings.EqualFold(filepath.Ext(rel), ".md") &&
		!strings.Contains(rel, "..")
}

// resolveSpecArg turns what the user typed -- "verbose-flag", "verbose-flag.md"
// or "specs/verbose-flag.md" -- into an existing spec's path.
func resolveSpecArg(workspaceRoot, arg string) (string, error) {
	arg = filepath.ToSlash(strings.TrimSpace(arg))
	if arg == "" {
		return "", errors.New("name a spec: /spec use <file>")
	}
	if !strings.HasPrefix(arg, specsDir+"/") {
		arg = specsDir + "/" + arg
	}
	if !strings.HasSuffix(strings.ToLower(arg), ".md") {
		arg += ".md"
	}
	if !isSpecPath(arg) {
		return "", fmt.Errorf("%s is not a spec: specs are .md files under %s/", arg, specsDir)
	}
	if _, err := os.Stat(filepath.Join(workspaceRoot, filepath.FromSlash(arg))); err != nil {
		return "", fmt.Errorf("%s does not exist", arg)
	}
	return arg, nil
}

// listSpecs lists the specs in the project.
func listSpecs(workspaceRoot string) []string {
	var out []string
	_ = filepath.WalkDir(filepath.Join(workspaceRoot, specsDir), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil //nolint:nilerr // an unreadable entry is simply not listed
		}
		if rel, err := filepath.Rel(workspaceRoot, path); err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// handleSpecCommand is /spec and its subcommands.
func (m chatModel) handleSpecCommand(args string) (tea.Model, tea.Cmd) {
	args = strings.TrimSpace(args)
	sub, rest, _ := strings.Cut(args, " ")
	reply := ""
	switch strings.ToLower(sub) {
	case "", "show":
		reply = m.specStatus()
	case "use":
		rel, err := resolveSpecArg(m.workspaceRoot, rest)
		if err != nil {
			reply = err.Error()
			break
		}
		reply = m.activateSpec(rel)
	case "off":
		m.activeSpec = ""
		if err := saveActiveSpec(m.workspaceRoot, ""); err != nil {
			reply = "no active spec (could not forget it for next time: " + err.Error() + ")"
		} else {
			reply = "no active spec"
		}
	case "check":
		if m.activeSpec == "" {
			reply = "no active spec to check against: /spec use <file>, or write one with /spec <goal>"
			break
		}
		return m.beginTurn("/spec check", "Check the project against the spec "+m.activeSpec+".", "", modeCheck, nil)
	default:
		return m.beginTurn("/spec "+args, args, "", modeSpec, nil)
	}
	m.appendTurn(turn{role: roleAssistant, text: reply})
	m.resizeViewport()
	m.refreshViewport()
	return m, nil
}

// activateSpec makes rel the active spec and says what that means.
func (m *chatModel) activateSpec(rel string) string {
	m.activeSpec = rel
	msg := "active spec: " + rel + " -- every prompt now works to it. /spec check grades the project against it; /spec off stops."
	if err := saveActiveSpec(m.workspaceRoot, rel); err != nil {
		msg += " (could not remember it for next time: " + err.Error() + ")"
	}
	return msg
}

func (m chatModel) specStatus() string {
	var b strings.Builder
	if m.activeSpec == "" {
		b.WriteString("no active spec")
	} else {
		b.WriteString("active spec: " + m.activeSpec)
	}
	if specs := listSpecs(m.workspaceRoot); len(specs) > 0 {
		b.WriteString("\nspecs in this project:")
		for _, s := range specs {
			b.WriteString("\n  " + s)
		}
	}
	b.WriteString("\n\n/spec <goal> writes a new one · /spec use <file> · /spec check · /spec off")
	return b.String()
}

// activateAppliedSpec makes the spec a /spec turn just wrote active, once the
// user has accepted it in the review.
func (m *chatModel) activateAppliedSpec() string {
	if m.turnMode != modeSpec {
		return ""
	}
	for _, p := range m.reviewAppliedPaths {
		if isSpecPath(p) {
			return m.activateSpec(filepath.ToSlash(filepath.Clean(p)))
		}
	}
	return ""
}

// specReportText renders a check's verdicts.
func specReportText(rep *protocol.SpecReport) string {
	if rep == nil {
		return ""
	}
	met := 0
	for _, c := range rep.Criteria {
		if c.Status == protocol.SpecMet {
			met++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "spec check -- %s: %d of %d criteria met", sanitizeText(rep.Spec), met, len(rep.Criteria))
	if len(rep.Criteria) == 0 {
		b.WriteString("\n  (the spec has no \"- [ ] C1: ...\" success criteria to check)")
	}
	for _, c := range rep.Criteria {
		mark := "?"
		switch c.Status {
		case protocol.SpecMet:
			mark = "✓"
		case protocol.SpecUnmet:
			mark = "✗"
		}
		fmt.Fprintf(&b, "\n%s %s: %s", mark, sanitizeText(c.ID), sanitizeText(c.Text))
		detail := c.Evidence
		if c.Note != "" {
			detail = c.Note
		}
		if detail = strings.TrimSpace(sanitizeText(detail)); detail != "" {
			b.WriteString("\n    " + strings.ReplaceAll(detail, "\n", "\n    "))
		}
	}
	return b.String()
}
