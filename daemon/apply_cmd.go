package main

import (
	"bufio"
	"errors"
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

// displayRel spells a workspace-relative path the way the rest of this product
// spells one: with forward slashes, on every platform.
//
// The undo report derives its paths from filepath.Rel over the backup tree, so
// on Windows they came out `docs\notes.md` -- while the apply run that created
// the backup printed `docs/notes.md`, because THAT comes from the model's own
// edit block and edit blocks are always forward-slash. One command, one file,
// two spellings, inside a single workflow whose entire purpose is letting a
// user check that undo reverted what apply did.
//
// Forward slash is the canonical form everywhere else that matters here --
// chunk keys, the wire, @file references -- so it is the one this follows.
// Display only: rel keeps its native separators for every filesystem operation,
// which is why this is applied at the Fprintf and not at the source.
func displayRel(rel string) string { return filepath.ToSlash(rel) }

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

	blocks, rejected := editapply.ParseEditBlocks(string(input))
	if len(blocks) == 0 && len(rejected) == 0 {
		fmt.Println("no edit blocks found in input")
		return nil
	}

	realRoot, err := editapply.ResolveRealWorkspaceRoot(*workspace)
	if err != nil {
		return err
	}

	return applyEditBlocks(realRoot, blocks, rejected, confirmIn, os.Stdout, logger)
}

// applyEditBlocks runs the safety tripod (via editapply.PrepareEdit) over
// every block in order and writes confirmed edits to disk using
// editapply's backup helpers. in/out are separated from stdin/stdout so
// tests can feed scripted confirmation input and capture prompts. This is
// the CLI's confirm loop; the Mochiii TUI drives the same PrepareEdit/
// backup calls through its own Bubble Tea state machine (see
// clients/tui/editreview.go) instead of a blocking stdin read, but both
// call the identical editapply core.
//
// rejected carries the blocks the parser refused (Fix B). They are reported
// first, before anything is applied, so the user sees what will not be
// attempted before deciding on what will be — and they are counted as refusals
// in the tally, exactly like a block PrepareEdit turns down, since from the
// user's side both are "you asked for this and are not getting it". A parser
// rejection is not fatal on its own: the valid blocks in the same response
// still get their turn, which is the whole point of the change. It IS fatal
// when nothing was parseable at all, since then the run accomplished nothing
// and a zero exit status would say otherwise.
func applyEditBlocks(realWorkspaceRoot string, blocks []editapply.EditBlock, rejected []editapply.BlockError, in io.Reader, out io.Writer, logger *log.Logger) error {
	reader := bufio.NewReader(in)

	var backupDir string

	var applied, skipped, refused int

	for _, bad := range rejected {
		fmt.Fprintf(out, "\n--- unparseable block at line %d ---\nREFUSED: %v\n", bad.Line, bad.Reason)
		logger.Printf("edits apply: refused unparseable block at line %d: %v", bad.Line, bad.Reason)
		refused++
	}
	if len(blocks) == 0 && len(rejected) > 0 {
		fmt.Fprintf(out, "\n0 applied, 0 skipped, %d refused\n", refused)
		return fmt.Errorf("no usable edit blocks: all %d block(s) in the response were refused by the parser", len(rejected))
	}
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
	// A non-exact match is never silent: the user confirming this diff is told
	// that whitespace/encoding tolerance was needed to find the passage, so
	// "why did that match?" is answerable without reading the daemon log.
	if p.MatchNote != "" {
		fmt.Fprintf(out, "match: %s\n", p.MatchNote)
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

	_, _, _, err = runUndoSession(realRoot, sessionDir, *force, os.Stdin, os.Stdout, logger)
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

// runUndoSession reverts every file backed up under sessionDir/before/ back
// to realWorkspaceRoot. A file is reverted silently only if its current
// on-disk content still matches sessionDir/after/<relpath> — the state the
// apply run left it in. A file that has since changed (hand-edited,
// deleted, or otherwise no longer matching) is never silently overwritten:
// it's listed as guarded and only reverted if force is true or the user
// confirms when prompted afterwards.
//
// Reverting is one of two things, decided per file by whether the apply run
// created it (Fix C). A file the run CHANGED is restored from its before/
// snapshot. A file the run CREATED is REMOVED, because "before" for that file
// is not an empty file, it is no file — undo used to restore the 0-byte
// snapshot, report "1 file restored", and leave a 0-byte file standing where
// nothing should be. The guard above is unchanged by this and applies to both
// shapes: a created file the user has since hand-edited is guarded, not
// silently deleted.
//
// Returns the number of files actually reverted (restored + removed) and the
// relative paths left guarded (still not reverted once this call returns --
// either left as-is or, if the caller declined to force/confirm, never
// touched), alongside the same prose written to out/logger this always wrote.
// The prose distinguishes a removal from a restore per file; the count does
// not, because both are "this file was reverted" for a caller deciding whether
// anything happened. This is the single core both the CLI's `edits undo` and
// the daemon's UndoRequest handler (see daemon/server.go) call -- neither
// reimplements the restore logic; the handler only additionally needs the
// counts as return values rather than parsed out of printed text.
func runUndoSession(realWorkspaceRoot, sessionDir string, force bool, in io.Reader, out io.Writer, logger *log.Logger) (restored, removed int, guarded []string, err error) {
	// Cross-process serialization across the guard check and the commit (M4).
	// The "is this file still what the apply run left?" comparison below and the
	// restoreBatch commit at the end are a read-then-write pair: without a lock
	// spanning both, a concurrent apply lands in between, the guard passes
	// against content that no longer exists, and undo reports files restored
	// while the apply's bytes are what is actually on disk — undo's report and
	// the disk disagree, the exact failure class this package exists to prevent.
	// Two concurrent undos of one session likewise both pass the guard and both
	// "restore". The daemon's in-process mutex closed these between two socket
	// requests; the CLI `edits undo` is a separate process it cannot see. See
	// editapply.LockWorkspaceApply.
	//
	// The daemon's undo never prompts, so it never holds this across a blocking
	// read. The CLI's rare "overwrite changed files?" prompt below IS inside this
	// span — deliberately, because the guard decision it is confirming would
	// otherwise be re-raced before the commit.
	release, lockErr := editapply.LockWorkspaceApply(realWorkspaceRoot)
	if lockErr != nil {
		return 0, 0, nil, fmt.Errorf("serializing undo on %s: %w", realWorkspaceRoot, lockErr)
	}
	defer release()

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
		return 0, 0, nil, fmt.Errorf("reading backup session %s: %w", sessionDir, err)
	}
	if len(relPaths) == 0 {
		fmt.Fprintf(out, "no backed-up files in %s\n", sessionDir)
		return 0, 0, nil, nil
	}

	// Which of these files did the apply run bring into existence? Reverting
	// one of those means removing it, not restoring its 0-byte before/ snapshot
	// (Fix C). A session with no manifest created nothing, and every path here
	// takes the restore shape exactly as it always did.
	created, err := editapply.CreatedInSession(sessionDir)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("reading created-file record for %s: %w", sessionDir, err)
	}

	var batch []string
	for _, rel := range relPaths {
		afterContent, err := os.ReadFile(filepath.Join(sessionDir, "after", rel))
		if err != nil {
			// No recorded post-apply snapshot: be conservative and guard it.
			guarded = append(guarded, rel)
			continue
		}
		// This "unchanged since apply" guard read follows symlinks and is NOT
		// itself confinement-checked. It is safe only because the write path
		// (stageRestore -> confinedRestorePath + leaf symlink refusal, and a
		// commit by rename, which replaces a symlink rather than writing
		// through it) independently refuses to write through a symlink, so
		// redirecting this read cannot be leveraged into a write. If that
		// write-path confinement is ever relaxed, weakened, or refactored,
		// re-evaluate this read's symlink-following here.
		currentContent, err := os.ReadFile(filepath.Join(realWorkspaceRoot, rel))
		if err != nil || string(currentContent) != string(afterContent) {
			guarded = append(guarded, rel)
			continue
		}
		batch = append(batch, rel)
	}

	// The guarded prompt runs BEFORE any restore is committed (Fix 2), so a
	// confirmed force joins the same all-or-nothing batch as the safe files
	// instead of being a second, separately-failing pass. The user is told what
	// is at risk before the workspace is touched, not after part of it already
	// changed.
	if len(guarded) > 0 {
		fmt.Fprintf(out, "\n%d file(s) changed since this apply run and were NOT restored automatically:\n", len(guarded))
		for _, rel := range guarded {
			fmt.Fprintf(out, "  - %s\n", displayRel(rel))
		}

		proceed := force
		if !proceed {
			fmt.Fprint(out, "overwrite these with their pre-apply backup anyway? [y/N]: ")
			reader := bufio.NewReader(in)
			line, _ := reader.ReadString('\n')
			proceed = strings.ToLower(strings.TrimSpace(line)) == "y"
		}

		if proceed {
			batch = append(batch, guarded...)
			guarded = nil
		} else {
			fmt.Fprintln(out, "left as-is")
		}
	}

	revertedFiles, err := restoreBatch(realWorkspaceRoot, beforeDir, batch, created)
	// Say what actually happened to each file. A created file that undo deleted
	// is reported as removed, never as "restored" — the honesty invariant is
	// that this report and the state of the disk agree (Fix C).
	for _, f := range revertedFiles {
		if f.removed {
			removed++
			fmt.Fprintf(out, "removed %s (created by this apply run)\n", displayRel(f.rel))
		} else {
			fmt.Fprintf(out, "restored %s\n", displayRel(f.rel))
		}
	}
	restored = len(revertedFiles)
	if err != nil {
		logger.Printf("edits undo: FAILED on %s: %v (reverted %d file(s): %d restored, %d removed; %d guarded)",
			sessionDir, err, restored, restored-removed, removed, len(guarded))
		return restored, removed, guarded, err
	}

	// Directories the apply run had to create to hold a created file. Removed
	// only after every file revert has committed, and only ones this run
	// actually made — see editapply.CreatedDirsInSession for why that is
	// recorded rather than inferred from emptiness.
	//
	// Deliberately after the error return above: a partly-committed undo has
	// files still standing in these directories, and removing a directory is
	// not something to attempt on a tree in an unknown state. os.Remove would
	// refuse a non-empty one anyway; not trying is clearer than relying on that.
	removedDirs := removeCreatedSessionDirs(realWorkspaceRoot, sessionDir, logger)

	if removed > 0 {
		fmt.Fprintf(out, "\n%d file(s) reverted from %s (%d restored, %d removed)\n",
			restored, sessionDir, restored-removed, removed)
	} else {
		fmt.Fprintf(out, "\n%d file(s) restored from %s\n", restored, sessionDir)
	}
	if removedDirs > 0 {
		_, _ = fmt.Fprintf(out, "%d empty director(ies) created by that run also removed\n", removedDirs)
	}
	logger.Printf("edits undo: reverted %d file(s) from %s (%d restored, %d removed, %d dir(s) removed, %d guarded)",
		restored, sessionDir, restored-removed, removed, removedDirs, len(guarded))
	return restored, removed, guarded, nil
}

// removeCreatedSessionDirs removes the directories the apply run brought into
// existence, deepest first, and reports how many actually went.
//
// THREE THINGS BOUND WHAT THIS CAN DELETE, and it needs all three because a
// directory removal is the one undo operation with no snapshot behind it.
//
//  1. The set comes from the run's own manifest, so a directory the user made
//     is never a candidate — "remove the parent if it is now empty" would have
//     deleted an empty directory a user created before asking for a file in it.
//  2. Every path is put through confinedRestorePath, the same confinement the
//     file reverts use, so a hand-edited manifest cannot aim this outside the
//     workspace.
//  3. os.Remove is non-recursive, so anything that is not empty simply stays.
//     A directory holding a file this undo could not revert (guarded, refused)
//     is exactly such a case, and leaving it is right.
//
// Failures are logged and skipped rather than returned: the files are already
// reverted at this point, and turning "an empty directory is still there" into
// a failed undo would misreport a workspace that is correct.
func removeCreatedSessionDirs(realWorkspaceRoot, sessionDir string, logger *log.Logger) int {
	dirs, err := editapply.CreatedDirsInSession(sessionDir)
	if err != nil {
		logger.Printf("edits undo: reading the created-directory manifest for %s: %v", sessionDir, err)
		return 0
	}

	removed := 0
	for _, rel := range dirs {
		dest, err := confinedRestorePath(realWorkspaceRoot, rel)
		if err != nil {
			logger.Printf("edits undo: not removing directory %q: %v", rel, err)
			continue
		}
		// A symlink standing where the manifest recorded a directory is
		// anomalous, and os.Remove would unlink the link rather than the
		// directory. Refuse it, same posture as the leaf checks in stageRestore.
		if info, err := os.Lstat(dest); err != nil {
			continue // already gone, which is the state being aimed at
		} else if editapply.IsLinkLike(info.Mode()) || !info.IsDir() {
			logger.Printf("edits undo: %q is not a directory any more; leaving it alone", rel)
			continue
		}
		if err := os.Remove(dest); err != nil {
			// Not empty is the common case and not worth a line: something the
			// undo could not revert is still in there.
			if !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
				logger.Printf("edits undo: removing directory %q: %v", rel, err)
			}
			continue
		}
		removed++
	}
	return removed
}

// undoStagingPrefix names the temporary files restoreBatch writes beside each
// destination while staging. Dot-prefixed so it is hidden, and carrying the
// product name so a file left behind by a crashed run is identifiable.
const undoStagingPrefix = ".codeterminal-undo-"

// stagedRestore is one file's revert after it has passed every check and had
// its content written to a temp file beside its destination — everything done,
// with nothing yet visible at the destination path.
//
// A revert is one of two shapes. Most are restores: put the before/ snapshot
// back. A file the apply run CREATED is a removal instead (Fix C), because the
// state to revert to is "not there" — see editapply.CreatedInSession. A removal
// stages no temp file and reads no snapshot; it only validates, and its commit
// is the unlink.
type stagedRestore struct {
	rel         string
	dest        string
	tmp         string   // empty for a removal
	remove      bool     // revert by deleting the file, not by restoring content
	createdDirs []string // dirs this staging brought into existence, shallowest first
}

// restoreBatch reverts rels as a single all-or-nothing batch (Fix 2). Undo used
// to restore file by file in one walk and return on the first failure, which
// left every earlier file reverted, every later file untouched, and — through
// handleUndo, which dropped the count whenever an error came back — reported
// that nothing had been restored at all. A user was told their workspace was
// untouched while it sat half-reverted.
//
// It runs in two phases, the same shape as editapply.Apply's reorder (Fix 1):
// every fallible step happens first, while the workspace is still untouched.
//
//	stage:  validate confinement, read the snapshot, and write the content to a
//	        temp file beside the destination. Any failure here aborts the whole
//	        batch, discards every temp file and every directory staging created,
//	        and reverts NOTHING.
//	commit: rename each temp over its destination. A same-directory rename is
//	        atomic and needs no space, so this phase has essentially nothing
//	        left to fail on.
//
// A file the apply run CREATED is reverted by removal instead (Fix C), and
// takes the same two phases: staging validates it through every gate a restore
// passes, and the commit is the unlink. Both shapes therefore land in the same
// all-or-nothing batch — a mixed session that reverts an edit and deletes a
// created file either does both or does neither.
//
// The residual is honest rather than silent: if a commit rename does fail, the
// files already renamed stay reverted and their exact count is returned
// alongside the error, so the caller reports what is actually on disk. That is
// the floor the review asked for, and it is only reachable in this
// near-impossible window.
//
// Committing by rename also strengthens the FAIL-2 leaf guarantee it replaces:
// os.Rename replaces a symlink standing at the destination instead of writing
// through it, so even a symlink swapped in after staging validated the path
// cannot redirect the write.
func restoreBatch(realWorkspaceRoot, beforeDir string, rels []string, created map[string]bool) (reverted []revertedFile, err error) {
	staged := make([]*stagedRestore, 0, len(rels))
	for _, rel := range rels {
		s, err := stageRestore(realWorkspaceRoot, beforeDir, rel, created[rel])
		if err != nil {
			discardStaged(staged)
			return nil, fmt.Errorf("restoring %s: %w", displayRel(rel), err)
		}
		staged = append(staged, s)
	}

	for _, s := range staged {
		if err := s.commit(); err != nil {
			discardStaged(staged[len(reverted):])
			return reverted, fmt.Errorf("committing %s of %s: %w (%d of %d file(s) had already been reverted and were left reverted)",
				s.verb(), s.rel, err, len(reverted), len(staged))
		}
		reverted = append(reverted, revertedFile{rel: s.rel, removed: s.remove})
	}
	return reverted, nil
}

// revertedFile is one file this undo actually changed on disk, and how. The
// caller reports removals and restores in their own words: telling a user a
// file was "restored" when it was in fact deleted is the same class of untruth
// as the 0-byte file this distinction exists to prevent.
type revertedFile struct {
	rel     string
	removed bool
}

// commit makes one staged revert visible. Both shapes are single syscalls with
// essentially nothing left to fail on, which is what the staging phase bought:
// a same-directory rename needs no space, and an unlink of a validated regular
// file needs none either.
//
// Neither can be redirected by a symlink swapped in after validation: rename
// REPLACES a symlink standing at the destination rather than writing through
// it, and Remove unlinks the symlink itself rather than its target.
func (s *stagedRestore) commit() error {
	if s.remove {
		if err := os.Remove(s.dest); err != nil && !os.IsNotExist(err) {
			return err
		}
		// Already gone counts as done: the state being reverted to is "no file
		// here", and that is the state on disk.
		return nil
	}
	return os.Rename(s.tmp, s.dest)
}

func (s *stagedRestore) verb() string {
	if s.remove {
		return "removal"
	}
	return "restore"
}

// stageRestore runs every check and every fallible write for one file's
// revert, stopping just short of making it visible. It carries forward the
// three gates restoreOne enforced (FAIL-2) — never restore a secret-named file,
// never let the destination escape the root via a symlinked ancestor, never
// write through a symlinked leaf — and adds the staging write, so that all of
// it happens before the batch commits anything.
//
// The source read is guarded too: a symlink under beforeDir is anomalous
// (editapply writes backup snapshots as regular files) and following it would
// read bytes from outside the backup tree.
//
// remove selects the other revert shape: the apply run created this file, so
// reverting it is a delete (Fix C). EVERY gate above applies unchanged to a
// removal — a delete is a write to the tree in the sense that matters here, and
// the ability to unlink an arbitrary path via a fabricated backup session would
// be a worse capability than the ability to overwrite one. What a removal skips
// is only the parts that exist to produce content: the snapshot read and the
// temp file. Note that rel reaches here from undo's own walk of before/ and is
// then independently confinement-checked; the created-file manifest is consulted
// as a membership test on that path and is never itself a source of paths.
func stageRestore(realWorkspaceRoot, beforeDir, rel string, remove bool) (*stagedRestore, error) {
	if editapply.MatchesSecretName(filepath.Base(rel)) {
		return nil, fmt.Errorf("path %q matches the indexer's secret-file rules; refusing to restore it", displayRel(rel))
	}
	// Parity with the forward path's protected-directory refusal (Fix 3): undo
	// is a write path too, and a fabricated backup session listing
	// before/.git/hooks/pre-commit would otherwise plant an executable hook
	// through the restore. Apply now refuses to create such a backup in the
	// first place, so no legitimate session can contain one.
	if component := editapply.ProtectedDirComponent(rel); component != "" {
		return nil, fmt.Errorf("path %q is inside %s/, which holds version-control, credential, or undo state; refusing to restore it", displayRel(rel), component)
	}

	var data []byte
	var info os.FileInfo
	if !remove {
		src := filepath.Join(beforeDir, rel)
		if sym, err := leafIsSymlink(src); err != nil {
			return nil, err
		} else if sym {
			return nil, fmt.Errorf("backup entry %q is a symlink; refusing to restore from it", displayRel(rel))
		}
		var err error
		if data, err = os.ReadFile(src); err != nil {
			return nil, err
		}
		if info, err = os.Stat(src); err != nil {
			return nil, err
		}
	}

	dest, err := confinedRestorePath(realWorkspaceRoot, rel)
	if err != nil {
		return nil, err
	}
	// Leaf checks, at validation time so the batch aborts before committing.
	// The rename that commits cannot follow a symlink here, but a symlink (or a
	// directory) standing where the backup recorded a regular file is anomalous
	// and was refused by the O_NOFOLLOW writer this replaces — keep refusing it.
	if destInfo, err := os.Lstat(dest); err == nil {
		if editapply.IsLinkLike(destInfo.Mode()) {
			return nil, fmt.Errorf("%q is a symlink; refusing to restore through it", displayRel(rel))
		}
		if destInfo.IsDir() {
			return nil, fmt.Errorf("%q is a directory; refusing to replace it with a backed-up file", displayRel(rel))
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	// A removal has nothing to stage: no content to write, and no directories
	// to bring into existence for a file that is about to stop existing. Every
	// gate above has already run.
	if remove {
		return &stagedRestore{rel: rel, dest: dest, remove: true}, nil
	}

	createdDirs, err := mkdirAllTracked(filepath.Dir(dest))
	if err != nil {
		return nil, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), undoStagingPrefix+"*")
	if err != nil {
		removeCreatedDirs(createdDirs)
		return nil, err
	}
	s := &stagedRestore{rel: rel, dest: dest, tmp: tmp.Name(), createdDirs: createdDirs}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		discardStaged([]*stagedRestore{s})
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		discardStaged([]*stagedRestore{s})
		return nil, err
	}
	// os.CreateTemp always makes 0600; the restored file must carry the mode its
	// pre-apply snapshot recorded, which the O_NOFOLLOW writer passed as perm.
	if err := os.Chmod(s.tmp, info.Mode()); err != nil {
		discardStaged([]*stagedRestore{s})
		return nil, err
	}
	return s, nil
}

// discardStaged throws away staged restores that will never be committed,
// leaving no residue: the temp files first, then any directories staging
// created, deepest first. Directory removal is deliberately non-recursive, so a
// directory that turns out to hold anything else is simply left alone.
func discardStaged(staged []*stagedRestore) {
	for _, s := range staged {
		if s.tmp != "" { // a staged removal has no temp file and left no residue
			os.Remove(s.tmp)
		}
	}
	for i := len(staged) - 1; i >= 0; i-- {
		removeCreatedDirs(staged[i].createdDirs)
	}
}

// mkdirAllTracked is os.MkdirAll that also reports which directories it had to
// bring into existence (shallowest first), so an aborted staging can take them
// back out again.
func mkdirAllTracked(dir string) (created []string, err error) {
	for p := dir; ; {
		if _, err := os.Lstat(p); err == nil {
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		created = append([]string{p}, created...)
		p = parent
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return created, nil
}

// removeCreatedDirs removes dirs deepest first with a non-recursive Remove, so
// a directory that has since acquired other contents fails harmlessly and is
// kept.
func removeCreatedDirs(dirs []string) {
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i])
	}
}

// Undo is a write path over model-adjacent, locally attacker-influenceable
// layout (a symlink checked out with a repo, or a fabricated backup session),
// so it enforces the same confinement the forward edit path (editapply.Apply
// via ResolveSafeTargetPath) does and that it historically skipped (FAIL-2).
// Those three gates now live in stageRestore, which runs all of them during the
// staging phase so a refusal aborts the batch before anything is committed:
//
//  1. Never restore a secret-named file — parity with Apply(), which refuses
//     to write such files in the first place, so there is no legitimate
//     backup of one to restore.
//  2. Never let the destination escape the workspace root via a symlinked
//     ANCESTOR directory (confinedRestorePath resolves the deepest existing
//     ancestor and requires it to stay inside root).
//  3. Never write THROUGH a symlinked leaf. This was an O_NOFOLLOW open; the
//     atomic commit (Fix 2) makes it an explicit refusal plus a rename, which
//     replaces a symlink rather than following it — so a symlink swapped in
//     after the ancestor check still cannot redirect the write.
//
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
		return "", fmt.Errorf("path %q escapes the workspace root", displayRel(rel))
	}

	// The same hazard gate editapply's resolveSafeTarget runs, for the same
	// reason, on the other implementation of the same job.
	//
	// It was missing here. pathhazard.go arrived wired into ONE of the two
	// confinement resolvers, and the containment check below cannot stand in for
	// it: containment is not the property these paths violate. `<root>/nul` is
	// provably inside the workspace and still opens the console device;
	// `<root>/.git:x` is inside the workspace and still writes a stream on .git.
	// Every one of the eight shared hazard vectors resolved to a path this
	// function returned WITHOUT error, which is what the conformance test now
	// asserts against.
	//
	// Reachability is narrow and worth stating rather than overselling: undo
	// restores paths from a backup manifest this daemon wrote, and an apply that
	// wrote one today already passed the gate in editapply. The gap is manifests
	// written BEFORE the gate existed, which are still on disk. Contained,
	// low-severity -- and mirrored anyway, because "one implementation has the
	// check" is precisely how S1 and S2 happened.
	if err := editapply.RejectPathHazards(cleaned); err != nil {
		return "", err
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
			return "", fmt.Errorf("path %q has no existing ancestor within the workspace root", displayRel(rel))
		}
		suffix = append([]string{filepath.Base(ancestor)}, suffix...)
		ancestor = parent
	}

	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", displayRel(rel), err)
	}
	relToRoot, err := filepath.Rel(realWorkspaceRoot, realAncestor)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q resolves outside the workspace root", displayRel(rel))
	}

	return filepath.Join(append([]string{realAncestor}, suffix...)...), nil
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
	return editapply.IsLinkLike(info.Mode()), nil
}

// restrictSQLiteSidecars chmods a SQLite database's -wal and -shm sidecars to
// perm. Shared by OpenMemoryStore and OpenSkillStore, which have the same
// exposure for the same reason.
//
// WHY THIS IS NOT COSMETIC. In WAL mode a committed row lives in the -wal file
// until a checkpoint folds it into the main database — so immediately after a
// write the row is in the sidecar and NOT in the db file. Measured on
// memory.db: the main file did not contain a just-appended conversation turn
// and the -wal did. Locking down memory.db alone therefore protected nothing
// for exactly the data that matters most, the most recent turns; the sidecars
// were left at the driver's default 0644.
//
// WHY IT MUST BE CALLED AFTER THE SCHEMA STEP. The driver creates the sidecars
// lazily. Calling this straight after the journal_mode pragma races their
// creation and silently no-ops through the ENOENT tolerance below, leaving
// 0644 behind — the fix would look applied and do nothing. The schema DDL is a
// write, so by the time it returns the sidecars exist.
//
// ENOENT is tolerated rather than reported because a sidecar's absence is a
// legitimate state (a store opened and closed cleanly checkpoints and removes
// them); the caller's concern is only that any sidecar that DOES exist is not
// readable by other users.
func restrictSQLiteSidecars(path string, perm os.FileMode) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := restrictToOwner(path+suffix, perm); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("restricting %s permissions: %w", filepath.Base(path+suffix), err)
		}
	}
	return nil
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
