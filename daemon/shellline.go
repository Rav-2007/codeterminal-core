package main

import (
	"path/filepath"
	"strings"
)

// READING A SHELL LINE FOR WHAT IT RUNS (2026-10-26). Which programs a
// run_command line starts decides where it runs (runplace.go) and whether it
// built or tested the project (turnevidence.go); sandbox_exec reads a leading
// NAME=VALUE the same way (execdir.go). Nothing here runs anything: the user's
// shell runs the line itself.

// shellCommandWords cuts a shell line into its commands -- at unquoted &&, ||,
// ;, |, &, parentheses and line breaks -- and each command into its words, with
// quotes removed. A here-document's body is skipped. It is for telling which
// programs a line starts, nothing more: the user's shell runs the line itself.
func shellCommandWords(line string) [][]string {
	var (
		cmds     [][]string
		words    []string
		cur      strings.Builder
		inWord   bool
		quote    rune
		heredocs []string // delimiters whose bodies start after this line
	)
	endWord := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	endCommand := func() {
		endWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
			words = nil
		}
	}
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if quote != 0 {
			switch {
			case r == quote:
				quote = 0
			case quote == '"' && r == '\\' && i+1 < len(rs):
				i++
				cur.WriteRune(rs[i])
			default:
				cur.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote, inWord = r, true
		case '\\':
			if i+1 < len(rs) && rs[i+1] != '\n' {
				i++
				cur.WriteRune(rs[i])
				inWord = true
			}
		case ' ', '\t':
			endWord()
		case '\n':
			endCommand()
			for _, delim := range heredocs {
				i = skipHeredoc(rs, i+1, delim)
			}
			heredocs = nil
		case '&':
			// Part of a redirection (2>&1, &>log), not a command run in the background.
			if (inWord && strings.HasSuffix(cur.String(), ">")) || (i+1 < len(rs) && rs[i+1] == '>') {
				cur.WriteRune(r)
				inWord = true
				continue
			}
			endCommand()
		case ';', '|', '(', ')':
			endCommand()
		case '<':
			if i+1 < len(rs) && rs[i+1] == '<' && (i+2 >= len(rs) || rs[i+2] != '<') {
				endWord()
				delim, next := heredocDelimiter(rs, i+2)
				if delim != "" {
					heredocs = append(heredocs, delim)
				}
				i = next - 1
				continue
			}
			cur.WriteRune(r)
			inWord = true
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	endCommand()
	return cmds
}

// heredocDelimiter reads the word after "<<" (an optional "-", spaces, quotes
// removed) and returns it with the index just past it.
func heredocDelimiter(rs []rune, i int) (string, int) {
	if i < len(rs) && rs[i] == '-' {
		i++
	}
	for i < len(rs) && (rs[i] == ' ' || rs[i] == '\t') {
		i++
	}
	var b strings.Builder
	for ; i < len(rs); i++ {
		r := rs[i]
		if r == ' ' || r == '\t' || r == '\n' || r == ';' || r == '&' || r == '|' || r == '<' || r == '>' {
			break
		}
		if r != '\'' && r != '"' && r != '\\' {
			b.WriteRune(r)
		}
	}
	return b.String(), i
}

// skipHeredoc returns the index of the line break that ends a here-document
// whose body starts at i, or the end of the line when it never ends.
func skipHeredoc(rs []rune, i int, delim string) int {
	for i < len(rs) {
		end := i
		for end < len(rs) && rs[end] != '\n' {
			end++
		}
		if strings.TrimLeft(string(rs[i:end]), "\t") == delim {
			return end
		}
		i = end + 1
	}
	return len(rs)
}

// commandWrappers run the command after their own options: `time make`,
// `env CC=gcc make`, `timeout 60 pytest`.
var commandWrappers = map[string]bool{"env": true, "time": true, "nice": true, "nohup": true, "command": true,
	"exec": true, "builtin": true, "timeout": true, "stdbuf": true}

// commandCore is one command of a line as its program and arguments: no
// redirections, no NAME=VALUE in front, no wrapper (`env`, `time`, `timeout 60`).
func commandCore(words []string) []string {
	words = withoutRedirections(words)
	for len(words) > 0 && looksLikeEnvAssignment(words[0]) {
		words = words[1:]
	}
	for len(words) > 0 && commandWrappers[filepath.Base(words[0])] {
		words = words[1:]
		for len(words) > 0 && (strings.HasPrefix(words[0], "-") || looksLikeEnvAssignment(words[0]) ||
			strings.Trim(words[0], "0123456789.smhd") == "") {
			words = words[1:]
		}
	}
	return words
}

// withoutRedirections drops a command's redirections: `>out`, `2>/dev/null`,
// and the file after a bare `>`.
func withoutRedirections(words []string) []string {
	var out []string
	for i := 0; i < len(words); i++ {
		w := strings.TrimLeft(words[i], "0123456789&")
		if strings.HasPrefix(w, ">") || strings.HasPrefix(w, "<") {
			if strings.Trim(w, "<>&|") == "" {
				i++ // the file it names
			}
			continue
		}
		out = append(out, words[i])
	}
	return out
}

// looksLikeEnvAssignment reports a word in the NAME=VALUE shape a shell puts in
// front of a command.
func looksLikeEnvAssignment(word string) bool {
	name, _, found := strings.Cut(word, "=")
	if !found || name == "" {
		return false
	}
	for i, r := range name {
		letter := r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
		if !letter && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}
