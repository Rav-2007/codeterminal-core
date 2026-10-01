package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
)

// CHECKPOINTS: a named snapshot of the working copy's changes, and the way back
// to it. A debugging hypothesis is tried as an edit and abandoned by going
// back, rather than by reconstructing what the file said; a refactor moves from
// one green step to the next and can return to the last green one.
//
// A CHECKPOINT HOLDS every file the task has changed, as it stood -- the rest of
// the copy is the project itself. Restoring writes those back THROUGH THE EDIT
// GATES (st.apply), so a restore is an edit like any other: it cannot leave the
// working copy or follow a link a command planted there, and the finish gate
// wants a passing run after it as after any edit. A file created since the
// checkpoint is removed. Checkpoints live in memory for this run of the task.
const (
	maxCheckpoints     = 10
	maxCheckpointBytes = 32 << 20 // every snapshot together
)

var checkpointName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,39}$`)

// stageCheckpoint is one snapshot. files maps a changed file to its content, or
// to nil when it did not exist then.
type stageCheckpoint struct {
	name  string
	files map[string]*string
	at    time.Time
	bytes int
}

func (s *Server) builtinCheckpoint(raw json.RawMessage, p *proposalSink) (mcp.Result, error) {
	var args struct {
		Action string `json:"action"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	st, err := p.workingCopy()
	if err != nil || st == nil {
		return toolError("this task has no working copy, so there is nothing to checkpoint")
	}
	name := strings.TrimSpace(args.Name)
	switch strings.ToLower(strings.TrimSpace(args.Action)) {
	case "save":
		if !checkpointName.MatchString(name) {
			return toolError("give a short name: letters, digits, spaces, '.', '_' or '-'")
		}
		return st.saveCheckpoint(name)
	case "restore":
		cp := st.checkpoint(name)
		if cp == nil {
			return toolError("there is no checkpoint %q; saved: %s", name, st.checkpointNames())
		}
		return st.restoreCheckpoint(cp)
	case "list", "":
		return mcp.Result{Content: st.listCheckpoints()}, nil
	}
	return toolError("action must be save, restore or list")
}

func (st *stagedWorkspace) checkpoint(name string) *stageCheckpoint {
	for _, cp := range st.checkpoints {
		if cp.name == name {
			return cp
		}
	}
	return nil
}

func (st *stagedWorkspace) checkpointNames() string {
	if len(st.checkpoints) == 0 {
		return "none yet"
	}
	names := make([]string, 0, len(st.checkpoints))
	for _, cp := range st.checkpoints {
		names = append(names, fmt.Sprintf("%q", cp.name))
	}
	return strings.Join(names, ", ")
}

// readStageFile reads one file of the copy for a snapshot: its content, nil
// when it does not exist, or an error for anything that is not a regular file
// (a command can leave a link where a file was) or that lies past a link out
// of the copy (readCopyFile).
func (st *stagedWorkspace) readStageFile(rel string) (*string, error) {
	data, exists, err := st.readCopyFile(rel, 0)
	if err != nil || !exists {
		return nil, err
	}
	content := string(data)
	return &content, nil
}

func (st *stagedWorkspace) saveCheckpoint(name string) (mcp.Result, error) {
	cp := &stageCheckpoint{name: name, files: map[string]*string{}, at: time.Now()}
	for _, rel := range st.order {
		content, err := st.readStageFile(rel)
		if err != nil {
			return toolError("cannot checkpoint: %v", err)
		}
		cp.files[rel] = content
		if content != nil {
			cp.bytes += len(*content)
		}
	}
	total := cp.bytes
	kept := st.checkpoints[:0]
	for _, old := range st.checkpoints {
		if old.name != name { // saving under a name again replaces it
			kept = append(kept, old)
			total += old.bytes
		}
	}
	if len(kept) >= maxCheckpoints {
		return toolError("this run already holds %d checkpoints, its limit; save under an existing name to replace one", maxCheckpoints)
	}
	if total > maxCheckpointBytes {
		return toolError("the checkpoints would hold more than %d MB; save under an existing name to replace one", maxCheckpointBytes>>20)
	}
	st.checkpoints = append(kept, cp)
	return mcp.Result{Content: fmt.Sprintf("Saved checkpoint %q: %d changed file(s). restore it to come back here.",
		name, len(cp.files))}, nil
}

// restoreCheckpoint puts every file changed now or at the checkpoint back as it
// stood then: a file changed since is written back, one the checkpoint did not
// hold returns to the project's version, and one created since is removed.
func (st *stagedWorkspace) restoreCheckpoint(cp *stageCheckpoint) (mcp.Result, error) {
	paths := map[string]bool{}
	for rel := range cp.files {
		paths[rel] = true
	}
	for rel := range st.touched {
		paths[rel] = true
	}
	sorted := make([]string, 0, len(paths))
	for rel := range paths {
		sorted = append(sorted, rel)
	}
	sort.Strings(sorted)

	var restored, failed []string
	for _, rel := range sorted {
		want, held := cp.files[rel]
		if !held {
			// Unchanged when the checkpoint was saved: the project's version.
			var err error
			if want, err = st.projectFile(rel); err != nil {
				failed = append(failed, fmt.Sprintf("%s (%v)", filepath.ToSlash(rel), err))
				continue
			}
		}
		current, err := st.readStageFile(rel)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", filepath.ToSlash(rel), err))
			continue
		}
		switch {
		case current == nil && want == nil, current != nil && want != nil && *current == *want:
			continue
		case want == nil:
			// Created since: removed -- inside the copy only, never past a
			// link a command planted (removeCopyFile).
			if err := st.removeCopyFile(rel); err != nil {
				failed = append(failed, fmt.Sprintf("%s (%v)", filepath.ToSlash(rel), err))
				continue
			}
		default:
			search := ""
			if current != nil {
				search = *current
			}
			if _, err := st.apply(editapply.EditBlock{FilePath: rel, Search: search, Replace: *want}); err != nil {
				failed = append(failed, fmt.Sprintf("%s (%v)", filepath.ToSlash(rel), err))
				continue
			}
		}
		restored = append(restored, filepath.ToSlash(rel))
	}
	msg := fmt.Sprintf("Restored checkpoint %q: %d file(s) put back", cp.name, len(restored))
	if len(restored) > 0 {
		msg += " (" + strings.Join(restored, ", ") + ")"
	}
	msg += ". Build and test again before you finish."
	if len(failed) > 0 {
		msg += " NOT restored: " + strings.Join(failed, "; ") + "."
	}
	return mcp.Result{Content: msg}, nil
}

// projectFile is a file's content in the project itself, or nil when the
// project has no such file.
func (st *stagedWorkspace) projectFile(rel string) (*string, error) {
	if _, known := st.manifest[rel]; !known {
		return nil, nil
	}
	full, err := editapply.ResolveSafeTargetPath(st.real, rel)
	if err != nil {
		return nil, err
	}
	data, size, err := readBoundedFile(full, maxCheckpointBytes)
	if err != nil {
		return nil, err
	}
	if size > maxCheckpointBytes {
		return nil, fmt.Errorf("too large to restore")
	}
	content := string(data)
	return &content, nil
}

func (st *stagedWorkspace) listCheckpoints() string {
	if len(st.checkpoints) == 0 {
		return "No checkpoints yet. checkpoint with action save and a name marks this state to come back to."
	}
	var b strings.Builder
	b.WriteString("Checkpoints, oldest first:\n")
	for _, cp := range st.checkpoints {
		fmt.Fprintf(&b, "- %q: %d changed file(s), saved %s\n", cp.name, len(cp.files), cp.at.Format("15:04:05"))
	}
	return b.String()
}

// checkpointTool is the built-in, offered in the long-task modes.
func (s *Server) checkpointTool(p *proposalSink) mcp.Builtin {
	return mcp.Builtin{
		Tool: mcp.Tool{
			Name: "checkpoint",
			Description: "Save the working copy's current changes under a name, restore a saved state, or list them. " +
				"Save before trying a hypothesis or a risky step; restore to undo it. A restore is an edit: " +
				"build and test again after one. Checkpoints last for this run of the task.",
			Schema: schema(`{
				"type":"object",
				"properties":{
					"action":{"type":"string","enum":["save","restore","list"]},
					"name":{"type":"string","description":"A short name, e.g. baseline or before-cache-fix."}
				},
				"required":["action"],
				"additionalProperties":false
			}`),
			// It changes the working copy, never the project.
			ReadOnlyHint: true,
		},
		Handler: func(_ context.Context, raw json.RawMessage) (mcp.Result, error) {
			return s.builtinCheckpoint(raw, p)
		},
	}
}
