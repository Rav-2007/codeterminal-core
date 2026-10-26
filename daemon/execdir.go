package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sandboxExecArgs are sandbox_exec's arguments (mcp_exec.go).
//
// DIR, BECAUSE A BUILD IS OFTEN RUN FROM A SUBFOLDER (2026-10-26). In a run of
// real fixes from real GitHub projects, the model's first test run in gson was
// `cd gson && mvn test`. It was refused -- sandbox_exec is not a shell -- and the
// model moved to run_command, the unsandboxed tool that cannot see its edits,
// for the rest of the task.
type sandboxExecArgs struct {
	Command        string `json:"command"`
	Dir            string `json:"dir"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// execDirIn is the folder a sandbox_exec call runs in: root, or the
// project-relative subfolder dir names inside it. why is set, and the call is
// not run, for a dir that is absolute, climbs out of the project (through ..
// or through a link), or is not a folder there.
func execDirIn(root, dir string) (path, why string) {
	dir = strings.TrimSpace(dir)
	if dir == "" || dir == "." {
		return root, ""
	}
	clean := filepath.Clean(filepath.FromSlash(dir))
	if filepath.IsAbs(clean) || leavesRoot(clean) {
		return "", fmt.Sprintf("dir %q must be a folder inside the project, given relative to its root", dir)
	}
	path = filepath.Join(root, clean)
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return "", fmt.Sprintf("dir %q is not a folder in the project", dir)
	}
	realRoot, err1 := filepath.EvalSymlinks(root)
	realPath, err2 := filepath.EvalSymlinks(path)
	if err1 != nil || err2 != nil {
		return "", fmt.Sprintf("dir %q could not be resolved inside the project", dir)
	}
	if rel, err := filepath.Rel(realRoot, realPath); err != nil || leavesRoot(rel) {
		return "", fmt.Sprintf("dir %q leads outside the project", dir)
	}
	return path, ""
}

// leavesRoot reports whether a cleaned relative path climbs out of its root.
func leavesRoot(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// execWord is one word of a sandbox_exec command, and whether any of it was
// quoted: a quoted ">" is an argument, a bare one is a shell's redirection.
type execWord struct {
	text   string
	quoted bool
}

// execWords cuts a sandbox_exec command into the program and its arguments as
// a shell user expects -- on spaces and tabs, with "double" and 'single' quotes
// keeping a word whole -- and does nothing else: no escapes, no variables, no
// globs, like the TUI's splitCommandLine (clients/tui/mcpserver.go). why is set
// for a quote never closed, and for a line break, which a shell would read as
// a second command.
//
// QUOTES, BECAUSE MODELS WRITE THEM (2026-10-26). Cut on spaces alone,
// `mvn test -Dtest="JsoupTest,DocumentTest"` reached Maven with the quotes in
// its test pattern, which then matched nothing, and the model spent about
// twenty calls on spellings of it.
func execWords(command string) (words []execWord, why string) {
	var (
		current        strings.Builder
		quote          rune
		inWord, quoted bool
	)
	end := func() {
		if inWord {
			words = append(words, execWord{text: current.String(), quoted: quoted})
			current.Reset()
			inWord, quoted = false, false
		}
	}
	for _, r := range command {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, inWord, quoted = r, true, true
		case r == '\n' || r == '\r':
			return nil, "sandbox_exec is not a shell, so a line break would not start a second command: " +
				"run one command per call"
		case r == ' ' || r == '\t':
			end()
		default:
			current.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Sprintf("sandbox_exec is not a shell, but a quoted word is kept whole -- and this "+
			"command has a %c quote that is never closed", quote)
	}
	end()
	return words, ""
}

// wordTexts is the words as the program receives them.
func wordTexts(words []execWord) []string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = w.text
	}
	return out
}

// shellWords are what a shell would act on, and sandbox_exec would instead
// hand to the program as arguments.
var shellWords = map[string]bool{"&&": true, "||": true, "|": true, ";": true, "&": true,
	">": true, ">>": true, "<": true, "2>&1": true}

// redirectPrefixes begin a word a shell would take as a redirection or a
// here-document: >out.txt, 2>/dev/null, &>log, <<EOF.
var redirectPrefixes = []string{">", "<", "1>", "2>", "&>"}

// execCommandRefusal says why words may not run as one sandbox_exec call, or
// "" when they may. Shell syntax is refused by name, with the way to say it:
// run as written, `make && make test` reached make as the arguments "&&",
// "make" and "test", and `cd gson` was refused only as a program not allowed.
// Only BARE words count: a quoted "a|b" is the program's own argument.
func (s *Server) execCommandRefusal(words []execWord) string {
	first := words[0]
	switch {
	case !first.quoted && first.text == "cd":
		return "sandbox_exec is not a shell, so it has no cd: give the subfolder as dir instead " +
			`(for example {"command": "mvn test", "dir": "gson"}), one command per call`
	case !first.quoted && looksLikeEnvAssignment(first.text):
		return fmt.Sprintf("sandbox_exec is not a shell, so %q would be taken as the program's name: it runs "+
			"one program with its arguments and sets no variables. Pass the setting as the program's own "+
			"flag instead (for example ctest --output-on-failure)", first.text)
	}
	for _, w := range words[1:] {
		if w.quoted {
			continue
		}
		switch p := w.text; {
		case strings.HasPrefix(p, "<<"):
			return "sandbox_exec is not a shell, so it has no here-document (<<): to give a program a file, " +
				"create it in the project with propose_edit and pass its path -- and to try a case, add it as " +
				"a test in the project's own suite"
		case strings.Contains(p, "$(") || strings.Contains(p, "`"):
			return fmt.Sprintf("sandbox_exec is not a shell, so %q is not replaced by a command's output: run "+
				"the inner command as its own call and use what it prints", p)
		case shellWords[p] || hasAnyPrefix(p, redirectPrefixes):
			return fmt.Sprintf("sandbox_exec is not a shell, so %q would reach %s as an argument: run one "+
				"command per call, each step its own call, and read the output rather than redirect it", p, first.text)
		}
	}
	return s.execRefusal(first.text)
}

// hasAnyPrefix reports whether s begins with one of prefixes.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// firstReason is the first refusal of several, or "" when none refuses.
func firstReason(reasons ...string) string {
	for _, r := range reasons {
		if r != "" {
			return r
		}
	}
	return ""
}
