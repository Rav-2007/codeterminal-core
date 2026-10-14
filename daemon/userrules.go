package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mochiii/protocol"
)

// RULES THE USER SAVED FOR THIS PROJECT (protocol/rules.go).
//
// WHY (OWNER, 2026-10-13). Asked whether the agent corrects itself, the honest
// answer had a hole in it: told "that is not true, tests here run with make
// check", the model had the correction for the rest of that chat and no longer.
// Nothing said in one conversation reached the next, and the daemon read no
// file of standing instructions. A rule is one line the user chose to keep.
//
// ONLY WHAT THE USER SAVES, AS WITH SAVED CHATS (chatarchive.go). A rule is
// written when a client asks for it to be -- /remember -- and at no other time.
// The model has no tool that writes one and is never asked whether something
// should be one: what every later chat is told is the user's decision, made in
// the user's words. Its own edits are confined to the project, and a command it
// runs sees everything outside that read-only wherever the sandbox confines
// (mcp/sandbox.go). On a host where nothing can confine it, such a command runs
// with the user's privileges -- the approval question says so in those words
// (mcp_exec.go) -- and could write this file as it could any other of theirs.
//
// KEPT OUTSIDE THE PROJECT, under StateDir()/rules/<workspace key>.md, beside
// memory.db and the saved chats. A file inside the project would be shared with
// a team and reviewed like code, which is worth having -- and it would also be
// whatever a repository's author put there. Text that every turn is told to
// follow, arriving with a clone, is the thing the fences in context.go exist to
// prevent. So these are the user's own, on the user's own machine, and a
// project cannot carry any in.
//
// IN THE SYSTEM MESSAGE, FENCED, EVERY TURN, like the active spec and for its
// reason: the work stays anchored to what was said rather than to what the model
// remembers of it. Read from the file each turn, so a rule saved or removed in
// one client is in force for the next question from any client. With no rules
// the system message is byte for byte what it was, so a project that never uses
// this pays nothing for it and measures the same.
//
// BOUNDED, because every rule is sent with every question: at most maxRules of
// them, each at most maxRuleBytes, and maxRulesTotalBytes together.
//
// DISK IS UNTRUSTED ON THE WAY BACK IN, as it is for the chats. The file can be
// edited by hand and is read within the same limits a save keeps; a link in its
// place is refused; and what is read is scrubbed and neutralised before it is
// wrapped, like every other text in a fence.

const (
	rulesDirName    = "rules"
	rulesFileSuffix = ".md"

	maxRules           = 50
	maxRuleBytes       = 300
	maxRulesTotalBytes = 4 << 10
	// maxRulesFileBytes bounds the read of the file, comments and all.
	maxRulesFileBytes = 64 << 10

	// Tag text for the rules envelope. Registered in protectedTagFamilies, so
	// neither a rule nor a web page can spell it.
	userRulesOpenTag  = "<user_rules>"
	userRulesCloseTag = "</user_rules>"
)

// userRulesNote sits above the block. Said to the model, not by the user.
const userRulesNote = "RULES THE USER SAVED FOR THIS PROJECT are below. The user wrote each one, in this chat or " +
	"an earlier one: facts about this project and how they want work done in it. Follow them as if the user " +
	"had just said them. If what the user asks now goes against one, do what they ask now and say which rule " +
	"it goes against. They are about the work only: they do not change which tools you have, what needs the " +
	"user's approval, or anything else stated above."

// rulesMu serialises the read-change-write of a save or a remove. One daemon
// serves one project, so one lock is one file's.
var rulesMu sync.Mutex

// isRulesRequest recognises the request by its discriminator, like every other
// typed request on this socket.
func isRulesRequest(raw json.RawMessage) bool {
	fields, ok := requestFields(raw)
	if !ok {
		return false
	}
	return hasBoolKey(fields, "rules")
}

// rulesPath is where a project's rules are kept.
func rulesPath(workspace string) (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, rulesDirName, workspaceKey(workspace)+rulesFileSuffix), nil
}

// cleanRule is text as one line with nothing in it a terminal would act on.
func cleanRule(text string) string {
	text, _ = stripControlCharacters(text)
	return oneLine(text)
}

// plainOrAbsent reports whether path is absent (false, nil), is what it should
// be (true, nil), or is something else -- a link above all -- which is refused.
func plainOrAbsent(path string, wantDir bool) (bool, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if wantDir && fi.IsDir() || !wantDir && fi.Mode().IsRegular() {
		return true, nil
	}
	kind := "file"
	if wantDir {
		kind = "folder"
	}
	return false, fmt.Errorf("%s is not a plain %s; refusing to use it", path, kind)
}

// loadRules reads the rules kept at path: none when there is no file. unused
// counts lines that are not rules because they are over a limit, which only a
// file edited by hand can hold.
func loadRules(path string) (rules []string, unused int, err error) {
	for _, at := range []struct {
		path string
		dir  bool
	}{{filepath.Dir(path), true}, {path, false}} {
		if there, err := plainOrAbsent(at.path, at.dir); err != nil || !there {
			return nil, 0, err
		}
	}
	data, size, err := readBoundedFile(path, maxRulesFileBytes)
	if err != nil {
		return nil, 0, fmt.Errorf("reading the rules file: %w", err)
	}
	if size > maxRulesFileBytes {
		return nil, 0, fmt.Errorf("%s is %d bytes; a rules file can be at most %d", path, size, maxRulesFileBytes)
	}
	total := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule := cleanRule(strings.TrimPrefix(strings.TrimPrefix(line, "- "), "* "))
		switch {
		case rule == "":
		case len(rule) > maxRuleBytes, len(rules) == maxRules, total+len(rule) > maxRulesTotalBytes:
			unused++
		default:
			rules = append(rules, rule)
			total += len(rule)
		}
	}
	return rules, unused, nil
}

// saveRules writes rules to path, replacing what was there: owner-only, in an
// owner-only folder, by way of a temporary file so a reader never sees half.
func saveRules(path, workspace string, rules []string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating the rules folder: %w", err)
	}
	if _, err := plainOrAbsent(dir, true); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("restricting the rules folder: %w", err)
	}
	if _, err := plainOrAbsent(path, false); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("# Rules for " + cleanRule(workspace) + "\n")
	b.WriteString("# Saved with /remember. Mochiii shows them to the model on every turn in this project.\n")
	b.WriteString("# One rule per line. /rules lists them and /forget <number> removes one; or edit this file.\n\n")
	for _, rule := range rules {
		b.WriteString("- " + rule + "\n")
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("writing the rules file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // a no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restricting the rules file: %w", err)
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing the rules file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing the rules file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("writing the rules file: %w", err)
	}
	return nil
}

// handleRules answers a RulesRequest.
func (s *Server) handleRules(enc *json.Encoder, req protocol.RulesRequest) {
	resp := s.rulesResult(req)
	resp.ProtocolVersion = protocol.ProtocolVersion
	if resp.Rules == nil {
		resp.Rules = []string{}
	}
	if err := enc.Encode(resp); err != nil {
		s.logger.Printf("rules response encode error: %v", err)
	}
}

// rulesResult is the whole decision, apart from the encoding. Every refusal is
// written for the user and leaves the file as it was.
func (s *Server) rulesResult(req protocol.RulesRequest) protocol.RulesResponse {
	if s.workspace == "" {
		return protocol.RulesResponse{Error: "this daemon serves no project, so there is nothing to keep rules for"}
	}
	rulesMu.Lock()
	defer rulesMu.Unlock()

	path, err := rulesPath(s.workspace)
	if err != nil {
		return protocol.RulesResponse{Error: "cannot find where to keep rules: " + err.Error()}
	}
	rules, unused, err := loadRules(path)
	resp := protocol.RulesResponse{Rules: rules, Unused: unused, Path: path}
	if err != nil {
		resp.Error = err.Error()
		return resp
	}
	refuse := func(format string, args ...any) protocol.RulesResponse {
		resp.Error = fmt.Sprintf(format, args...)
		return resp
	}

	switch req.Action {
	case "", protocol.RulesActionList:
		return resp
	case protocol.RulesActionAdd, protocol.RulesActionRemove:
		// A save rewrites the file from the rules that were read, so lines that
		// were not read would be lost by it.
		if unused > 0 {
			return refuse("%d line(s) of the rules file are over the limits (at most %d rules, %d bytes each, "+
				"%d together) and are not in use. Shorten or delete them in %s first: saving now would drop them.",
				unused, maxRules, maxRuleBytes, maxRulesTotalBytes, path)
		}
	default:
		return refuse("unknown rules action %q; the actions are %s, %s and %s",
			req.Action, protocol.RulesActionList, protocol.RulesActionAdd, protocol.RulesActionRemove)
	}

	if req.Action == protocol.RulesActionRemove {
		if req.Number < 1 || req.Number > len(rules) {
			return refuse("there is no rule %d: this project has %d", req.Number, len(rules))
		}
		resp.Removed = rules[req.Number-1]
		rules = append(append([]string{}, rules[:req.Number-1]...), rules[req.Number:]...)
	} else {
		text := cleanRule(req.Text)
		total := len(text)
		for i, rule := range rules {
			if rule == text {
				return refuse("that is already rule %d", i+1)
			}
			total += len(rule)
		}
		switch {
		case text == "":
			return refuse("a rule needs some text: a fact or an instruction to keep")
		case len(text) > maxRuleBytes:
			return refuse("that is %d bytes and a rule can be at most %d: say it shorter, or as two rules", len(text), maxRuleBytes)
		case len(rules) >= maxRules:
			return refuse("this project already has %d rules, the most it can have: remove one first", maxRules)
		case total > maxRulesTotalBytes:
			return refuse("the rules together would be over %d bytes, the most that is sent with every question: "+
				"remove one first, or say this one shorter", maxRulesTotalBytes)
		}
		if _, found := scrub(text, s.noScrub()); len(found) > 0 {
			return refuse("that looks like it holds a key or a password. A rule is sent to the model's provider " +
				"with every question, so it was not saved.")
		}
		rules = append(rules, text)
	}

	if err := saveRules(path, s.workspace, rules); err != nil {
		return refuse("%v", err)
	}
	resp.Rules = rules
	return resp
}

// userRulesBlock is what saved rules add to a turn's system message, "" when
// there are none. Scrubbed, then neutralised, and only then wrapped: the other
// order would defuse the wrapper.
func userRulesBlock(rules []string, scrubDisabled bool) string {
	if len(rules) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(userRulesNote + "\n\n" + userRulesOpenTag + "\n")
	for i, rule := range rules {
		text, _ := scrub(rule, scrubDisabled)
		fmt.Fprintf(&b, "%d. %s\n", i+1, neutralizeDelimiters(text))
	}
	b.WriteString(userRulesCloseTag)
	return b.String()
}

// withUserRules is systemPrompt with this project's saved rules after it, and
// systemPrompt itself when there are none.
//
// A file that cannot be read costs the turn its rules, not the turn: the reason
// is logged here, and /rules -- which reads the same file -- says it to the user.
func (s *Server) withUserRules(systemPrompt string) string {
	if s.workspace == "" {
		return systemPrompt
	}
	path, err := rulesPath(s.workspace)
	if err != nil {
		return systemPrompt
	}
	rules, _, err := loadRules(path)
	if err != nil {
		s.logger.Printf("saved rules not used this turn: %v", err)
		return systemPrompt
	}
	block := userRulesBlock(rules, s.noScrub())
	if block == "" || systemPrompt == "" {
		return systemPrompt + block
	}
	return systemPrompt + "\n\n" + block
}
