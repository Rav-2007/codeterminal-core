package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"codeterminal/editapply"
)

// runEditsCommand implements `codeterminal-daemon edits <apply|undo> ...`.
func runEditsCommand(args []string, logger *log.Logger) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: edits <apply|undo> ...")
	}

	switch args[0] {
	case "apply":
		return runEditsApplyCommand(args[1:], logger)
	case "undo":
		return runEditsUndoCommand(args[1:], logger)
	default:
		return fmt.Errorf("unknown edits subcommand %q (want apply or undo)", args[0])
	}
}

// runEditsApplyCommand implements
// `codeterminal-daemon edits apply [--workspace path] [<response-file>|-]`.
// The model response is read from the given file, or from stdin if omitted
// or given as "-". When the response itself comes from stdin, confirmation
// prompts are read from the controlling terminal (/dev/tty) instead, since
// stdin has already been consumed by the response read.
func runEditsApplyCommand(args []string, logger *log.Logger) error {
	fset := flag.NewFlagSet("edits apply", flag.ExitOnError)
	workspace := fset.String("workspace", ".", "workspace root that edits are confined to")
	fset.Parse(args)

	var input []byte
	var err error
	confirmIn := io.Reader(os.Stdin)

	if fset.NArg() == 0 || fset.Arg(0) == "-" {
		input, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading model response from stdin: %w", err)
		}
		tty, ttyErr := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if ttyErr != nil {
			return fmt.Errorf("response was read from stdin, so confirmation prompts need a controlling terminal, but none is available (%v); pass the response as a file argument instead", ttyErr)
		}
		defer tty.Close()
		confirmIn = tty
	} else {
		input, err = os.ReadFile(fset.Arg(0))
		if err != nil {
			return fmt.Errorf("reading %s: %w", fset.Arg(0), err)
		}
	}

	blocks, err := editapply.ParseEditBlocks(string(input))
	if err != nil {
		return fmt.Errorf("parsing edit blocks: %w", err)
	}
	if len(blocks) == 0 {
		fmt.Println("no edit blocks found in input")
		return nil
	}

	realRoot, err := editapply.ResolveRealWorkspaceRoot(*workspace)
	if err != nil {
		return err
	}

	return applyEditBlocks(realRoot, blocks, confirmIn, os.Stdout, logger)
}

// applyEditBlocks runs the safety tripod (via editapply.PrepareEdit) over
// every block in order and writes confirmed edits to disk using
// editapply's backup helpers. in/out are separated from stdin/stdout so
// tests can feed scripted confirmation input and capture prompts. This is
// the CLI's confirm loop; the Mochiii TUI drives the same PrepareEdit/
// backup calls through its own Bubble Tea state machine (see
// clients/tui/editreview.go) instead of a blocking stdin read, but both
// call the identical editapply core.
func applyEditBlocks(realWorkspaceRoot string, blocks []editapply.EditBlock, in io.Reader, out io.Writer, logger *log.Logger) error {
	reader := bufio.NewReader(in)

	var backupDir string

	var applied, skipped, refused int
	for i, block := range blocks {
		fmt.Fprintf(out, "\n--- edit %d/%d: %s ---\n", i+1, len(blocks), block.FilePath)

		prepared, prepErr := editapply.PrepareEdit(realWorkspaceRoot, block)
		if prepErr != nil {
			fmt.Fprintf(out, "REFUSED: %v\n", prepErr)
			logger.Printf("edits apply: refused %s: %v", block.FilePath, prepErr)
			refused++
			continue
		}

		printEditDiff(out, prepared)
		fmt.Fprint(out, "Apply this edit? [y/N]: ")
		line, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(line)) != "y" {
			fmt.Fprintln(out, "skipped")
			skipped++
			continue
		}

		var err error
		if backupDir == "" {
			backupDir, err = editapply.NewBackupSessionDir(realWorkspaceRoot)
			if err != nil {
				return fmt.Errorf("creating backup dir: %w", err)
			}
			fmt.Fprintf(out, "backing up to %s\n", backupDir)
		}

		if err := editapply.Apply(realWorkspaceRoot, prepared, backupDir); err != nil {
			return err
		}

		fmt.Fprintln(out, "applied")
		logger.Printf("edits apply: applied %s", block.FilePath)
		applied++
	}

	fmt.Fprintf(out, "\n%d applied, %d skipped, %d refused\n", applied, skipped, refused)
	if backupDir != "" {
		fmt.Fprintf(out, "backups saved to %s (restore with: edits undo)\n", backupDir)
	}
	return nil
}

// printEditDiff shows the whole SEARCH block as removed lines and the whole
// REPLACE block as added lines. This is deliberately not a line-level diff
// (no word/LCS highlighting) — edit blocks are already authored as
// self-contained SEARCH/REPLACE units (see daemon/prompts/system.txt), so
// showing them whole is the simplest faithful representation.
func printEditDiff(out io.Writer, p *editapply.PreparedEdit) {
	fmt.Fprintf(out, "file: %s (lines %d-%d)\n", p.Block.FilePath, p.StartLine, p.EndLine)
	for _, l := range strings.Split(p.Block.Search, "\n") {
		fmt.Fprintf(out, "- %s\n", l)
	}
	for _, l := range strings.Split(p.Block.Replace, "\n") {
		fmt.Fprintf(out, "+ %s\n", l)
	}
	fmt.Fprintf(out, "syntax check: %s\n", p.SyntaxNote)
}

// runEditsUndoCommand implements
// `codeterminal-daemon edits undo [--workspace path] [--session ts] [--force]`.
func runEditsUndoCommand(args []string, logger *log.Logger) error {
	fset := flag.NewFlagSet("edits undo", flag.ExitOnError)
	workspace := fset.String("workspace", ".", "workspace root")
	session := fset.String("session", "", "backup session timestamp to restore (default: most recent)")
	force := fset.Bool("force", false, "also restore files that changed after the apply run, without prompting")
	fset.Parse(args)

	realRoot, err := editapply.ResolveRealWorkspaceRoot(*workspace)
	if err != nil {
		return err
	}

	backupsRoot := filepath.Join(realRoot, ".codeterminal", "backups")
	sessionDir, err := resolveBackupSession(backupsRoot, *session)
	if err != nil {
		return err
	}

	_, _, err = runUndoSession(realRoot, sessionDir, *force, os.Stdin, os.Stdout, logger)
	return err
}

// resolveBackupSession returns the backup session directory to restore:
// backupsRoot/session if session is given, otherwise the lexically-last
// (== chronologically-last, since sessions are timestamp-named) directory
// under backupsRoot.
func resolveBackupSession(backupsRoot, session string) (string, error) {
	if session != "" {
		dir := filepath.Join(backupsRoot, session)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return "", fmt.Errorf("backup session %q not found under %s", session, backupsRoot)
		}
		return dir, nil
	}

	entries, err := os.ReadDir(backupsRoot)
	if err != nil {
		return "", fmt.Errorf("no backups found at %s: %w", backupsRoot, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no backup sessions found under %s", backupsRoot)
	}
	sort.Strings(names)
	return filepath.Join(backupsRoot, names[len(names)-1]), nil
}

// runUndoSession restores every file backed up under sessionDir/before/ back
// to realWorkspaceRoot. A file is restored silently only if its current
// on-disk content still matches sessionDir/after/<relpath> — the state the
// apply run left it in. A file that has since changed (hand-edited,
// deleted, or otherwise no longer matching) is never silently overwritten:
// it's listed as guarded and only restored if force is true or the user
// confirms when prompted afterwards.
//
// Returns the number of files actually restored and the relative paths left
// guarded (still not restored once this call returns -- either left as-is
// or, if the caller declined to force/confirm, never touched), alongside
// the same prose written to out/logger this always wrote. This is the
// single core both the CLI's `edits undo` and the daemon's UndoRequest
// handler (see daemon/server.go) call -- neither reimplements the restore
// logic; the handler only additionally needs the counts as return values
// rather than parsed out of printed text.
func runUndoSession(realWorkspaceRoot, sessionDir string, force bool, in io.Reader, out io.Writer, logger *log.Logger) (restored int, guarded []string, err error) {
	beforeDir := filepath.Join(sessionDir, "before")

	var relPaths []string
	err = filepath.WalkDir(beforeDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(beforeDir, path)
		if err != nil {
			return err
		}
		relPaths = append(relPaths, rel)
		return nil
	})
	if err != nil {
		return 0, nil, fmt.Errorf("reading backup session %s: %w", sessionDir, err)
	}
	if len(relPaths) == 0 {
		fmt.Fprintf(out, "no backed-up files in %s\n", sessionDir)
		return 0, nil, nil
	}

	var safe []string
	for _, rel := range relPaths {
		afterContent, err := os.ReadFile(filepath.Join(sessionDir, "after", rel))
		if err != nil {
			// No recorded post-apply snapshot: be conservative and guard it.
			guarded = append(guarded, rel)
			continue
		}
		// This "unchanged since apply" guard read follows symlinks and is NOT
		// itself confinement-checked. It is safe only because the write path
		// (restoreOne -> confinedRestorePath/openNoFollow, 4de7bd4) independently
		// refuses to write through a symlink, so redirecting this read cannot be
		// leveraged into a write. If that write-path confinement is ever relaxed,
		// weakened, or refactored, re-evaluate this read's symlink-following here.
		currentContent, err := os.ReadFile(filepath.Join(realWorkspaceRoot, rel))
		if err != nil || string(currentContent) != string(afterContent) {
			guarded = append(guarded, rel)
			continue
		}
		safe = append(safe, rel)
	}

	for _, rel := range safe {
		if err := restoreOne(realWorkspaceRoot, beforeDir, rel); err != nil {
			return restored, guarded, fmt.Errorf("restoring %s: %w", rel, err)
		}
		fmt.Fprintf(out, "restored %s\n", rel)
		restored++
	}

	if len(guarded) > 0 {
		fmt.Fprintf(out, "\n%d file(s) changed since this apply run and were NOT restored automatically:\n", len(guarded))
		for _, rel := range guarded {
			fmt.Fprintf(out, "  - %s\n", rel)
		}

		proceed := force
		if !proceed {
			fmt.Fprint(out, "overwrite these with their pre-apply backup anyway? [y/N]: ")
			reader := bufio.NewReader(in)
			line, _ := reader.ReadString('\n')
			proceed = strings.ToLower(strings.TrimSpace(line)) == "y"
		}

		if proceed {
			for i, rel := range guarded {
				if err := restoreOne(realWorkspaceRoot, beforeDir, rel); err != nil {
					return restored, guarded[i:], fmt.Errorf("restoring %s: %w", rel, err)
				}
				fmt.Fprintf(out, "restored %s (forced)\n", rel)
				restored++
			}
			guarded = nil
		} else {
			fmt.Fprintln(out, "left as-is")
		}
	}

	fmt.Fprintf(out, "\n%d file(s) restored from %s\n", restored, sessionDir)
	logger.Printf("edits undo: restored %d file(s) from %s (%d guarded)", restored, sessionDir, len(guarded))
	return restored, guarded, nil
}

// restoreOne writes the pre-apply snapshot at beforeDir/rel back to
// realWorkspaceRoot/rel. Undo is a write path over model-adjacent, locally
// attacker-influenceable layout (a symlink checked out with a repo, or a
// fabricated backup session), so it must enforce the same confinement the
// forward edit path (editapply.Apply via ResolveSafeTargetPath) does and that
// this function historically skipped (FAIL-2). Three gates, mirroring Apply():
//
//  1. Never restore a secret-named file — parity with Apply(), which refuses
//     to write such files in the first place, so there is no legitimate
//     backup of one to restore.
//  2. Never let the destination escape the workspace root via a symlinked
//     ANCESTOR directory (confinedRestorePath resolves the deepest existing
//     ancestor and requires it to stay inside root).
//  3. Never write THROUGH a symlinked leaf (open with O_NOFOLLOW). This also
//     guards the leaf against a symlink swapped in after the ancestor check.
//
// The source read is guarded too: a symlink under beforeDir is anomalous
// (editapply writes backup snapshots as regular files) and following it would
// read bytes from outside the backup tree.
func restoreOne(realWorkspaceRoot, beforeDir, rel string) error {
	if editapply.MatchesSecretName(filepath.Base(rel)) {
		return fmt.Errorf("path %q matches the indexer's secret-file rules; refusing to restore it", rel)
	}

	src := filepath.Join(beforeDir, rel)
	if sym, err := leafIsSymlink(src); err != nil {
		return err
	} else if sym {
		return fmt.Errorf("backup entry %q is a symlink; refusing to restore from it", rel)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	dest, err := confinedRestorePath(realWorkspaceRoot, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	f, err := openNoFollow(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// confinedRestorePath resolves realWorkspaceRoot/rel to an absolute path that
// is proven to stay inside realWorkspaceRoot (which is itself already
// symlink-resolved, see ResolveRealWorkspaceRoot). Rather than EvalSymlinks
// the full path — which requires the leaf to exist and so would reject the
// legitimate restore of a deleted-then-undone file — it resolves the deepest
// existing ANCESTOR directory and requires that to land inside root. The
// remaining components do not exist yet (so cannot be pre-planted symlinks)
// and are created as real directories by the caller's MkdirAll. This closes
// the intermediate-directory-symlink escape (Repro B) while preserving
// deleted-file restore.
func confinedRestorePath(realWorkspaceRoot, rel string) (string, error) {
	cleaned := filepath.Clean(rel)
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace root", rel)
	}

	full := filepath.Join(realWorkspaceRoot, cleaned)
	ancestor := full
	var suffix []string
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break // deepest existing ancestor (an existing leaf counts, and is
			// then EvalSymlinks-resolved below, so a symlinked leaf is caught too)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("path %q has no existing ancestor within the workspace root", rel)
		}
		suffix = append([]string{filepath.Base(ancestor)}, suffix...)
		ancestor = parent
	}

	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", rel, err)
	}
	relToRoot, err := filepath.Rel(realWorkspaceRoot, realAncestor)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q resolves outside the workspace root", rel)
	}

	return filepath.Join(append([]string{realAncestor}, suffix...)...), nil
}

// openNoFollow opens path with O_NOFOLLOW ORed into flag, so a symlink at the
// final path component is refused (ELOOP) rather than followed. It only guards
// the leaf — ancestor directories must be confined separately (see
// confinedRestorePath). Shared by the two workspace write paths that take an
// attacker-influenceable destination (restoreOne, ensureGitignoreEntry).
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag|syscall.O_NOFOLLOW, perm)
}

// leafIsSymlink reports whether path itself (not its target) is a symlink. A
// non-existent path is reported as not-a-symlink so callers can handle absence
// with their own error path.
func leafIsSymlink(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.Mode()&os.ModeSymlink != 0, nil
}

// writeFileNoFollow is os.WriteFile with O_NOFOLLOW: it refuses to write
// through a symlink at the final path component (creating path with perm if
// absent, truncating an existing regular file). Used by the daemon-owned
// writers whose paths come from constants/config rather than client input
// (the FAIL-2 Gate-5 "(b)" bucket) — defense-in-depth against a symlink
// pre-planted at a fixed name, matching the leaf guard the apply/undo writers
// already use. Not confinement (these paths are not attacker-steerable); it
// only closes the follow-a-symlink write.
func writeFileNoFollow(path string, data []byte, perm os.FileMode) error {
	f, err := openNoFollow(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}
