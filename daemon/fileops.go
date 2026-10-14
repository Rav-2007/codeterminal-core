package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
)

// DELETING, MOVING AND RENAMING FILES (2026-10-14: the user asked for it).
//
// Until now no tool could, and the agent said so and gave the user the command
// to run (machinereach.go). Edits go through review, but review is a diff of a
// file's text, and a deletion has none -- so these are not proposals. They are
// ACTIONS, each asked about before it happens, like a command:
//
//   - EVERY CALL ASKS, whatever the config says (alwaysAskTools), and the
//     question names the path. "Yes for this turn" covers that exact path only
//     (grantKey binds a destructive tool's grant to its arguments).
//   - THE SAME PLACES AN EDIT MAY WRITE, by the same code: the project, or the
//     home folder outside hidden files and folders, ~/bin, ~/Library, ~/AppData
//     and .desktop launchers -- never .git, .mochiii or another protected folder,
//     never a file the indexer treats as a secret, never the project or the home
//     folder itself, and judged again where a link really leads.
//   - A DELETE IS A MOVE TO A TRASH THE AGENT CANNOT REACH: .mochiii/trash in the
//     project (or ~/.mochiii/trash for a file in the home folder). Nothing is
//     destroyed; restore_deleted, or `mochiii-daemon trash restore`, puts it back,
//     and entries older than trashKeep are cleared.
//   - A MOVE NEVER OVERWRITES. A destination that exists is refused.
//   - THE TURN'S WORKING COPY FOLLOWS. When the turn has one (stage.go), the same
//     change is made there, so what the agent reads next is what is on disk, and
//     an edit it made to a file it then moved is still offered, at the new path.

const (
	deleteToolName  = "delete_path"
	moveToolName    = "move_path"
	restoreToolName = "restore_deleted"
	trashDirName    = "trash"
	// trashKeep is how long a deleted file waits in the trash.
	trashKeep = 30 * 24 * time.Hour
	// maxOpEntries bounds a folder a delete or move walks to check.
	maxOpEntries = 100_000
	// maxTrashEntries bounds how much of the trash is listed at once.
	maxTrashEntries = 10_000
)

// readDirBounded is os.ReadDir reading at most max entries: the number of
// names a folder holds is decided by the folder, not by this process.
func readDirBounded(path string, max int) ([]os.DirEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(max)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// alwaysAskTools are asked about on every call, whatever mcp.builtin.tools
// says (configPolicy).
var alwaysAskTools = map[string]bool{deleteToolName: true, moveToolName: true, restoreToolName: true}

// opTarget is one path a file operation names, resolved and judged.
type opTarget struct {
	abs     string // where it really is
	root    string // the project, or the home folder
	rel     string // relative to root
	display string // as said back: project-relative, or ~/...
	exists  bool
	project bool
}

// resolveOpPath judges a path exactly as an edit's is judged
// (editapply.PrepareEditAnywhere): project-relative, or an absolute or ~/ path
// in the project or the home folder.
func resolveOpPath(workspace, p string) (opTarget, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return opTarget{}, errors.New("no path was given")
	}
	home, err := editapply.RealHomeDir()
	if err != nil {
		return opTarget{}, err
	}
	abs, isAbs := "", false
	if rest, ok := editapply.CutHomePrefix(p); ok {
		abs, isAbs = filepath.Join(home, rest), true
	} else if filepath.IsAbs(p) {
		abs, isAbs = filepath.Clean(p), true
	}

	t := opTarget{root: workspace, rel: p, project: true}
	if isAbs {
		real := existingPrefixReal(abs)
		switch {
		case withinDir(workspace, real):
			t.rel, _ = filepath.Rel(workspace, real)
		case withinDir(home, abs) && withinDir(home, real):
			t.root, t.project = home, false
			t.rel, _ = filepath.Rel(home, abs)
			if why := editapply.OutsideWriteRefusal(t.rel); why != "" {
				return opTarget{}, fmt.Errorf("%s: %s", p, why)
			}
		default:
			return opTarget{}, fmt.Errorf("%s is outside the project and your home folder; Mochiii never changes files there", p)
		}
	}
	path, exists, err := editapply.ResolveSafePath(t.root, t.rel)
	if err != nil {
		return opTarget{}, err
	}
	t.abs, t.exists = path, exists
	if path == t.root || path == workspace || path == home || withinDir(path, workspace) {
		return opTarget{}, fmt.Errorf("%s is the project or a folder holding it; Mochiii never deletes or moves that", p)
	}
	if !t.project {
		// THE RESOLVED PATH IS WHAT CHANGES, so it is judged again: a harmless
		// ~/Desktop/x that is a link to ~/.ssh is refused here.
		if rel, err := filepath.Rel(home, path); err == nil {
			if why := editapply.OutsideWriteRefusal(rel); why != "" {
				return opTarget{}, fmt.Errorf("%s: %s", p, why)
			}
		}
		t.display = editapply.HomeDisplayPath(t.rel)
	} else {
		t.display = filepath.ToSlash(filepath.Clean(t.rel))
	}
	return t, nil
}

// existingPrefixReal resolves links in the longest existing prefix of path.
func existingPrefixReal(path string) string {
	rest, cur := "", path
	for {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path
		}
		rest, cur = filepath.Join(filepath.Base(cur), rest), parent
	}
}

func withinDir(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// refuseInside checks what a folder holds: a protected folder or a secret file
// inside it is no more the agent's to delete or move than one named directly.
func refuseInside(t opTarget) error {
	info, err := os.Lstat(t.abs)
	if err != nil || !info.IsDir() {
		return nil
	}
	n := 0
	return filepath.WalkDir(t.abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if n++; n > maxOpEntries {
			return fmt.Errorf("%s holds more than %d entries; delete or move it yourself", t.display, maxOpEntries)
		}
		if path == t.abs {
			return nil
		}
		rel, _ := filepath.Rel(t.abs, path)
		inner := filepath.ToSlash(filepath.Join(t.display, rel))
		// By name, file or folder: a submodule's or a worktree's .git is a FILE
		// pointing at the repository, and no less the repository's.
		if editapply.IsProtectedDirName(d.Name()) {
			return fmt.Errorf("%s holds %s, which Mochiii never changes; do this yourself if you mean it", t.display, inner)
		}
		if !d.IsDir() && editapply.MatchesSecretName(d.Name()) {
			return fmt.Errorf("%s holds %s, which looks like a credentials file Mochiii never touches; do this yourself "+
				"if you mean it", t.display, inner)
		}
		return nil
	})
}

// trashRootFor is the trash for a path: the project's, or the home folder's.
func trashRootFor(t opTarget) string {
	return filepath.Join(t.root, ".mochiii", trashDirName)
}

// trashEntry is one deleted thing, as kept beside it in the trash.
type trashEntry struct {
	ID        string    `json:"id"`
	Original  string    `json:"original"` // where it was (absolute)
	Display   string    `json:"display"`
	DeletedAt time.Time `json:"deleted_at"`
	Folder    bool      `json:"folder,omitempty"`
}

func newTrashID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// moveToTrash moves t into its trash and records where it came from.
func moveToTrash(t opTarget) (trashEntry, error) {
	root := trashRootFor(t)
	pruneTrash(root)
	entry := trashEntry{ID: newTrashID(), Original: t.abs, Display: t.display, DeletedAt: time.Now().UTC()}
	if info, err := os.Lstat(t.abs); err == nil && info.IsDir() {
		entry.Folder = true
	}
	dir := filepath.Join(root, entry.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return entry, err
	}
	meta, _ := json.MarshalIndent(entry, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "entry.json"), meta, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return entry, err
	}
	if err := os.Rename(t.abs, filepath.Join(dir, "item")); err != nil {
		_ = os.RemoveAll(dir)
		// Across file systems a rename fails; copying a deletion somewhere is
		// not what was agreed to, so it is not done.
		return entry, fmt.Errorf("%s could not be moved to Mochiii's trash (%v), so it was not deleted", t.display, err)
	}
	return entry, nil
}

// pruneTrash clears entries older than trashKeep. Best effort.
func pruneTrash(root string) {
	entries, err := readDirBounded(root, maxTrashEntries)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && e.IsDir() && time.Since(info.ModTime()) > trashKeep {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// trashEntries is every entry in the project's and the home folder's trash,
// newest first.
func trashEntries(workspace string) []trashEntry {
	home, _ := editapply.RealHomeDir()
	var out []trashEntry
	for _, root := range []string{workspace, home} {
		if root == "" {
			continue
		}
		dirs, _ := readDirBounded(filepath.Join(root, ".mochiii", trashDirName), maxTrashEntries)
		for _, d := range dirs {
			data, _, err := readBoundedFile(filepath.Join(root, ".mochiii", trashDirName, d.Name(), "entry.json"), 16<<10)
			if err != nil {
				continue
			}
			var e trashEntry
			if json.Unmarshal(data, &e) == nil && e.ID == d.Name() {
				out = append(out, e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeletedAt.After(out[j].DeletedAt) })
	return out
}

// restoreFromTrash puts an entry back where it was, if nothing is there now.
// The original path is judged again as if it were being written.
func restoreFromTrash(workspace, id string) (trashEntry, error) {
	for _, e := range trashEntries(workspace) {
		if e.ID != id {
			continue
		}
		t, err := resolveOpPath(workspace, e.Original)
		if err != nil {
			return e, err
		}
		if t.exists {
			return e, fmt.Errorf("%s exists again, so the deleted one was not put back over it", e.Display)
		}
		dir := filepath.Join(trashRootFor(t), e.ID)
		if err := os.MkdirAll(filepath.Dir(t.abs), 0o755); err != nil {
			return e, err
		}
		if err := os.Rename(filepath.Join(dir, "item"), t.abs); err != nil {
			return e, fmt.Errorf("putting %s back: %v", e.Display, err)
		}
		_ = os.RemoveAll(dir)
		return e, nil
	}
	return trashEntry{}, fmt.Errorf("nothing in Mochiii's trash has the id %q", id)
}

// THE TOOLS.

func (s *Server) fileOpTools(proposals *proposalSink, mode string) []mcp.Builtin {
	if isPlanMode(mode) || isCheckMode(mode) || isSpecMode(mode) {
		return nil
	}
	return []mcp.Builtin{
		{
			Tool: mcp.Tool{
				Name: deleteToolName,
				Description: "Delete a file or folder: in the project, or anywhere in the user's home folder (~/ path) " +
					"where an edit may write. Asks the user first, every time. Nothing is destroyed: it is moved to " +
					"Mochiii's trash, and restore_deleted puts it back. Never .git, .mochiii, hidden files outside " +
					"the project, credentials files, or the project itself.",
				Schema: schema(`{"type":"object","properties":{"path":{"type":"string","description":"Project-relative, or a ~/ path."}},` +
					`"required":["path"],"additionalProperties":false}`),
				Destructive: true,
			},
			Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
				return s.builtinDelete(raw, proposals)
			},
		},
		{
			Tool: mcp.Tool{
				Name: moveToolName,
				Description: "Move or rename a file or folder, in the project or the user's home folder (~/ paths), " +
					"creating the destination's folders. Asks the user first, every time. Never overwrites: the " +
					"destination must not exist. The same places are refused as for delete_path.",
				Schema: schema(`{"type":"object","properties":{"from":{"type":"string"},"to":{"type":"string"}},` +
					`"required":["from","to"],"additionalProperties":false}`),
				Destructive: true,
			},
			Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
				return s.builtinMove(raw, proposals)
			},
		},
		{
			Tool: mcp.Tool{
				Name: restoreToolName,
				Description: "Put back something delete_path deleted, by the id delete_path gave. With no id, lists " +
					"what is in Mochiii's trash. Never overwrites a path that exists again.",
				Schema: schema(`{"type":"object","properties":{"id":{"type":"string"}},"additionalProperties":false}`),
				// It writes a file back into the project or home folder.
				Destructive: true,
			},
			Handler: func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
				return s.builtinRestore(raw, proposals)
			},
		},
	}
}

func (s *Server) builtinDelete(raw json.RawMessage, proposals *proposalSink) (mcp.Result, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	t, err := resolveOpPath(s.workspace, args.Path)
	if err != nil {
		return toolError("not deleted: %v", err)
	}
	if !t.exists {
		// Made this turn in the working copy only: undone there.
		if t.project && proposals.stagedOnlyDelete(t.rel) {
			return mcp.Result{Content: fmt.Sprintf("Deleted %s (it existed only in this turn's work, so there was nothing "+
				"to keep).", t.display)}, nil
		}
		return toolError("not deleted: %s does not exist", t.display)
	}
	if err := refuseInside(t); err != nil {
		return toolError("not deleted: %v", err)
	}
	entry, err := moveToTrash(t)
	if err != nil {
		return toolError("%v", err)
	}
	if t.project {
		proposals.mirrorDelete(t.rel)
	}
	s.logger.Printf("agent: deleted %s (trash %s)", t.display, entry.ID)
	what := "file"
	if entry.Folder {
		what = "folder"
	}
	return mcp.Result{Content: fmt.Sprintf("Deleted the %s %s. It is in Mochiii's trash as %s: restore_deleted with that id "+
		"puts it back.", what, t.display, entry.ID)}, nil
}

func (s *Server) builtinMove(raw json.RawMessage, proposals *proposalSink) (mcp.Result, error) {
	var args struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	from, err := resolveOpPath(s.workspace, args.From)
	if err != nil {
		return toolError("not moved: %v", err)
	}
	to, err := resolveOpPath(s.workspace, args.To)
	if err != nil {
		return toolError("not moved: %v", err)
	}
	if to.exists {
		return toolError("not moved: %s exists; a move never overwrites, so delete or rename it first", to.display)
	}
	if withinDir(from.abs, to.abs) {
		return toolError("not moved: %s is inside %s", to.display, from.display)
	}
	if !from.exists {
		if from.project && to.project && proposals.stagedOnlyMove(from.rel, to.rel) {
			return mcp.Result{Content: fmt.Sprintf("Moved %s to %s (it existed only in this turn's work).", from.display, to.display)}, nil
		}
		return toolError("not moved: %s does not exist", from.display)
	}
	if err := refuseInside(from); err != nil {
		return toolError("not moved: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(to.abs), 0o755); err != nil {
		return toolError("not moved: creating %s's folder: %v", to.display, err)
	}
	if err := os.Rename(from.abs, to.abs); err != nil {
		return toolError("not moved: %v", err)
	}
	if from.project && to.project {
		proposals.mirrorMove(from.rel, to.rel)
	} else if from.project {
		proposals.mirrorDelete(from.rel)
	}
	s.logger.Printf("agent: moved %s to %s", from.display, to.display)
	return mcp.Result{Content: fmt.Sprintf("Moved %s to %s.", from.display, to.display)}, nil
}

func (s *Server) builtinRestore(raw json.RawMessage, proposals *proposalSink) (mcp.Result, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError("the arguments were not a valid JSON object: %v", err)
	}
	if strings.TrimSpace(args.ID) == "" {
		entries := trashEntries(s.workspace)
		if len(entries) == 0 {
			return mcp.Result{Content: "Mochiii's trash is empty."}, nil
		}
		var b strings.Builder
		b.WriteString("In Mochiii's trash, newest first:\n")
		for i, e := range entries {
			if i == 30 {
				fmt.Fprintf(&b, "...and %d more\n", len(entries)-i)
				break
			}
			fmt.Fprintf(&b, "%s  %s  (deleted %s)\n", e.ID, e.Display, e.DeletedAt.Local().Format("2006-01-02 15:04"))
		}
		return mcp.Result{Content: b.String()}, nil
	}
	e, err := restoreFromTrash(s.workspace, strings.TrimSpace(args.ID))
	if err != nil {
		return toolError("not restored: %v", err)
	}
	if t, err := resolveOpPath(s.workspace, e.Original); err == nil && t.project {
		proposals.mirrorRestore(t.rel)
	}
	s.logger.Printf("agent: restored %s from the trash (%s)", e.Display, e.ID)
	return mcp.Result{Content: fmt.Sprintf("Put %s back.", e.Display)}, nil
}

// runTrashCommand is `trash [list]` and `trash restore <id>`, run in the
// project: the user's own way back from a delete, no agent needed.
func runTrashCommand(args []string, _ *log.Logger) error {
	root, err := editapply.ResolveRealWorkspaceRoot(".")
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "list" {
		entries := trashEntries(root)
		if len(entries) == 0 {
			fmt.Println("Mochiii's trash is empty.")
			return nil
		}
		for _, e := range entries {
			fmt.Printf("%s  %s  (deleted %s)\n", e.ID, e.Display, e.DeletedAt.Local().Format("2006-01-02 15:04"))
		}
		return nil
	}
	if args[0] == "restore" && len(args) == 2 {
		e, err := restoreFromTrash(root, args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Put %s back.\n", e.Display)
		return nil
	}
	return fmt.Errorf("usage: trash [list] | trash restore <id>")
}
