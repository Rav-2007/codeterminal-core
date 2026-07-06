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
	"time"
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

	blocks, err := ParseEditBlocks(string(input))
	if err != nil {
		return fmt.Errorf("parsing edit blocks: %w", err)
	}
	if len(blocks) == 0 {
		fmt.Println("no edit blocks found in input")
		return nil
	}

	realRoot, err := resolveRealWorkspaceRoot(*workspace)
	if err != nil {
		return err
	}

	return applyEditBlocks(realRoot, blocks, confirmIn, os.Stdout, logger)
}

func resolveRealWorkspaceRoot(workspace string) (string, error) {
	absRoot, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", workspace, err)
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", fmt.Errorf("resolving workspace root %s: %w", workspace, err)
	}
	return realRoot, nil
}

// applyEditBlocks runs the safety tripod over every block in order and
// writes confirmed edits to disk. in/out are separated from stdin/stdout so
// tests can feed scripted confirmation input and capture prompts.
func applyEditBlocks(realWorkspaceRoot string, blocks []EditBlock, in io.Reader, out io.Writer, logger *log.Logger) error {
	reader := bufio.NewReader(in)

	var backupDir string
	backedUp := make(map[string]bool)

	var applied, skipped, refused int
	for i, block := range blocks {
		fmt.Fprintf(out, "\n--- edit %d/%d: %s ---\n", i+1, len(blocks), block.FilePath)

		prepared, prepErr := prepareEdit(realWorkspaceRoot, block)
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
			backupDir, err = newBackupSessionDir(realWorkspaceRoot)
			if err != nil {
				return fmt.Errorf("creating backup dir: %w", err)
			}
			fmt.Fprintf(out, "backing up to %s\n", backupDir)
		}
		if !backedUp[prepared.targetPath] {
			if err := backupOriginal(backupDir, realWorkspaceRoot, prepared); err != nil {
				return fmt.Errorf("backing up %s: %w", block.FilePath, err)
			}
			backedUp[prepared.targetPath] = true
		}

		if err := os.WriteFile(prepared.targetPath, []byte(prepared.newContent), prepared.fileMode); err != nil {
			return fmt.Errorf("writing %s: %w", block.FilePath, err)
		}
		if err := backupAfter(backupDir, realWorkspaceRoot, prepared); err != nil {
			return fmt.Errorf("recording post-apply snapshot for %s: %w", block.FilePath, err)
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
func printEditDiff(out io.Writer, p *preparedEdit) {
	fmt.Fprintf(out, "file: %s (lines %d-%d)\n", p.block.FilePath, p.startLine, p.endLine)
	for _, l := range strings.Split(p.block.Search, "\n") {
		fmt.Fprintf(out, "- %s\n", l)
	}
	for _, l := range strings.Split(p.block.Replace, "\n") {
		fmt.Fprintf(out, "+ %s\n", l)
	}
	fmt.Fprintf(out, "syntax check: %s\n", p.syntaxNote)
}

// newBackupSessionDir creates a fresh, uniquely-named directory under
// .codeterminal/backups for one `edits apply` run. That parent directory is
// already covered by chunker.go's ignoredDirNames and the RAG .gitignore
// entry, so backups are automatically excluded from indexing and git.
func newBackupSessionDir(realWorkspaceRoot string) (string, error) {
	base := filepath.Join(realWorkspaceRoot, ".codeterminal", "backups")
	ts := time.Now().Format("20060102-150405")
	dir := filepath.Join(base, ts)
	for suffix := 1; ; suffix++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		dir = filepath.Join(base, fmt.Sprintf("%s-%d", ts, suffix))
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// backupOriginal saves p's pre-edit content under backupDir/before/<relpath>
// — the copy `edits undo` restores from. Called once per file per run, on
// its first write.
func backupOriginal(backupDir, realWorkspaceRoot string, p *preparedEdit) error {
	return writeBackupCopy(backupDir, "before", realWorkspaceRoot, p.targetPath, []byte(p.original), p.fileMode)
}

// backupAfter records p's post-edit content under backupDir/after/<relpath>.
// It's (re)written after every write to a given path in this run, so by the
// time the run finishes it holds each file's final on-disk content — the
// baseline `edits undo` compares the file's current content against to
// detect whether it was touched again after this apply run.
func backupAfter(backupDir, realWorkspaceRoot string, p *preparedEdit) error {
	return writeBackupCopy(backupDir, "after", realWorkspaceRoot, p.targetPath, []byte(p.newContent), p.fileMode)
}

func writeBackupCopy(backupDir, subdir, realWorkspaceRoot, targetPath string, content []byte, mode os.FileMode) error {
	rel, err := filepath.Rel(realWorkspaceRoot, targetPath)
	if err != nil {
		return err
	}
	dest := filepath.Join(backupDir, subdir, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	return os.WriteFile(dest, content, mode)
}

// runEditsUndoCommand implements
// `codeterminal-daemon edits undo [--workspace path] [--session ts] [--force]`.
func runEditsUndoCommand(args []string, logger *log.Logger) error {
	fset := flag.NewFlagSet("edits undo", flag.ExitOnError)
	workspace := fset.String("workspace", ".", "workspace root")
	session := fset.String("session", "", "backup session timestamp to restore (default: most recent)")
	force := fset.Bool("force", false, "also restore files that changed after the apply run, without prompting")
	fset.Parse(args)

	realRoot, err := resolveRealWorkspaceRoot(*workspace)
	if err != nil {
		return err
	}

	backupsRoot := filepath.Join(realRoot, ".codeterminal", "backups")
	sessionDir, err := resolveBackupSession(backupsRoot, *session)
	if err != nil {
		return err
	}

	return runUndoSession(realRoot, sessionDir, *force, os.Stdin, os.Stdout, logger)
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
func runUndoSession(realWorkspaceRoot, sessionDir string, force bool, in io.Reader, out io.Writer, logger *log.Logger) error {
	beforeDir := filepath.Join(sessionDir, "before")

	var relPaths []string
	err := filepath.WalkDir(beforeDir, func(path string, d fs.DirEntry, err error) error {
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
		return fmt.Errorf("reading backup session %s: %w", sessionDir, err)
	}
	if len(relPaths) == 0 {
		fmt.Fprintf(out, "no backed-up files in %s\n", sessionDir)
		return nil
	}

	var safe, guarded []string
	for _, rel := range relPaths {
		afterContent, err := os.ReadFile(filepath.Join(sessionDir, "after", rel))
		if err != nil {
			// No recorded post-apply snapshot: be conservative and guard it.
			guarded = append(guarded, rel)
			continue
		}
		currentContent, err := os.ReadFile(filepath.Join(realWorkspaceRoot, rel))
		if err != nil || string(currentContent) != string(afterContent) {
			guarded = append(guarded, rel)
			continue
		}
		safe = append(safe, rel)
	}

	var restored int
	for _, rel := range safe {
		if err := restoreOne(realWorkspaceRoot, beforeDir, rel); err != nil {
			return fmt.Errorf("restoring %s: %w", rel, err)
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
			for _, rel := range guarded {
				if err := restoreOne(realWorkspaceRoot, beforeDir, rel); err != nil {
					return fmt.Errorf("restoring %s: %w", rel, err)
				}
				fmt.Fprintf(out, "restored %s (forced)\n", rel)
				restored++
			}
		} else {
			fmt.Fprintln(out, "left as-is")
		}
	}

	fmt.Fprintf(out, "\n%d file(s) restored from %s\n", restored, sessionDir)
	logger.Printf("edits undo: restored %d file(s) from %s (%d guarded)", restored, sessionDir, len(guarded))
	return nil
}

func restoreOne(realWorkspaceRoot, beforeDir, rel string) error {
	src := filepath.Join(beforeDir, rel)
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	dest := filepath.Join(realWorkspaceRoot, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	return os.WriteFile(dest, data, info.Mode())
}
