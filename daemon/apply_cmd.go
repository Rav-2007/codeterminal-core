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
