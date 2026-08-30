package editapply

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// PreparedEdit is one EditBlock after it has passed path-safety, exact-match,
// and (where applicable) syntax verification — everything needed to show a
// diff, ask for confirmation, and write it, with no further checks required.
type PreparedEdit struct {
	Block      EditBlock
	TargetPath string // absolute, confinement-checked, resolved through symlinks
	Original   string // full pre-edit file content
	NewContent string // full post-edit file content
	StartLine  int    // 1-indexed line where SEARCH begins in original
	EndLine    int    // 1-indexed line where SEARCH ends in original
	FileMode   os.FileMode
	SyntaxNote string // human-readable note on what syntax check ran (or didn't)
	Tier       MatchTier
	MatchNote  string // human-readable note on what normalization the match needed; "" when exact
	Creates    bool   // true when this edit brings a new file into existence (see IsEmptySearch)
}

// syntaxTier is how strongly this binary can judge one language, and therefore
// how forcefully it is allowed to act on what it finds.
//
// THE TIER DECIDES THE CONSEQUENCE, NOT JUST THE CHECK. tierParser has a real
// parser behind it, so a failure is ground for a refusal. tierDelimiters has a
// bracket count behind it, so a failure is ground for a note and nothing more.
// Keeping those two facts in one enum stops the pairing drifting -- there is no
// way to add a language to a tier without also deciding what a failure there is
// allowed to do, because the tier IS that decision.
type syntaxTier int

const (
	// tierNone: this build cannot say anything about the language.
	tierNone syntaxTier = iota
	// tierParser: a real parser. A failure can refuse.
	tierParser
	// tierDelimiters: structural balance only. A failure is ADVISORY and can
	// never refuse -- see delimiters.go, where that is enforced by signature.
	tierDelimiters
)

// syntaxTierFor is the ONE place that maps a language to how well this binary
// knows it. LanguageOf says what a file is; this says what we can do about it.
//
// The three delimiter-tier languages are exactly the non-Go entries in
// extensionLanguages. That is not a coincidence to be preserved by hand: a
// language earns a table entry only when this product can do something with the
// answer (see langtable.go), and Tier B is now one of the things it can do.
func syntaxTierFor(lang Language) syntaxTier {
	switch lang {
	case LangGo:
		return tierParser
	case LangTypeScript, LangJavaScript, LangPython:
		return tierDelimiters
	default:
		return tierNone
	}
}

// parseSyntax reports whether this binary can PARSE lang at all, and if so
// whether content is well-formed for it.
//
// checked == false means "no parser for this language", which is never a
// refusal. Keeping that in ONE place is the point: the knowledge of which
// languages this binary can judge lives here and in LanguageOf, and nowhere
// else, so a caller cannot decide a file is checkable by a different rule than
// the one that checks it.
//
// This is Tier A only. A language with a delimiter check but no parser is still
// `checked == false` here, because it is still true that nothing parsed it --
// checkSyntax routes on syntaxTierFor before it ever gets here.
func parseSyntax(lang Language, relPath, content string) (checked bool, err error) {
	if lang != LangGo {
		return false, nil
	}
	_, err = parser.ParseFile(token.NewFileSet(), relPath, content, parser.AllErrors)
	return true, err
}

// checkSyntax is the syntax gate: it decides whether a proposed write is
// refused, and describes what it decided.
//
// Shared by the edit and the create path, and it is a shared function rather
// than two copies for the reason it had to be written at all. The gate was
// inline in PrepareEdit and prepareCreate carried only a note, so the SAME
// model output -- a .go file that does not parse -- was refused when it arrived
// as an edit and written to disk when it arrived as a create. A model that had
// its edit refused could get the identical bytes onto disk by sending them with
// an empty SEARCH section instead. One function, one behaviour, and no second
// copy to drift.
//
// PRIOR IS WHAT MAKES THE REFUSAL TRUE. It is the file's content before this
// write, or nil when there is no before -- the create path. This gate used to
// look only at the RESULT, and so refused an edit to an already-broken file
// with "edit would make X unparseable as Go": a false statement about what the
// edit did, and one that made a broken file unfixable through edit blocks. A
// model asked to repair a syntax error could not, because its repair was judged
// against a standard the file did not meet before it started -- and repairing a
// broken file is one of the things a user most often asks for.
//
// So exactly one transition is refused:
//
//	before   after    verdict
//	parses   parses   allow
//	parses   BROKEN   REFUSE  <- the gate's whole purpose: do not break a working file
//	BROKEN   parses   allow   <- this is a repair
//	BROKEN   BROKEN   allow, and the note says the file was already broken
//
// A create has no before, so there every failure is a refusal. That asymmetry
// is not an inconsistency: an empty SEARCH section means "this file's content
// is, or should be, nothing", and nothing has no prior brokenness to inherit.
// The laundering route the shared gate exists to close stays closed too, because
// the edit path still refuses those same bytes whenever the target parses now.
//
// What the delta rule gives up, stated plainly: a file that is already broken
// can be edited into differently-broken. There is nothing to protect in that
// case -- the gate's promise is that an edit will not break a WORKING file, and
// that promise is kept exactly.
//
// Best-effort by construction: Go is the only language with a parser in this
// binary, and everything else is written unchecked. See langtable.go for why the
// list stops at Go, and parseSyntax for the single place that decides it. The
// asymmetry is honest and is reported in the returned note.
func checkSyntax(relPath string, prior *string, content string) (note string, err error) {
	lang := LanguageOf(relPath)

	// TIER B, and note what it cannot do: checkDelimiterTier returns one value.
	// There is no error to propagate, so this branch cannot refuse, and no edit
	// to it can make it refuse without changing that function's signature. See
	// delimiters.go for why the weaker check must be the quieter one.
	if syntaxTierFor(lang) == tierDelimiters {
		return checkDelimiterTier(lang, relPath, prior, content), nil
	}

	checked, parseErr := parseSyntax(lang, relPath, content)
	if !checked {
		return fmt.Sprintf("no syntax check applied (unsupported for %s)", describeExt(relPath)), nil
	}
	if parseErr != nil {
		// The prior state is only worth a second parse once the result has
		// already failed, so the common case -- a write that parses -- pays
		// nothing for this rule.
		if prior != nil {
			if _, priorErr := parseSyntax(lang, relPath, *prior); priorErr != nil {
				return "go/parser reported errors; this file did not parse before this edit either", nil
			}
		}
		return "", fmt.Errorf("edit would make %s unparseable as Go: %w", relPath, parseErr)
	}
	return "go/parser OK", nil
}

// checkEditSyntax applies the gate to a splice into an existing file, which has
// a before state and is therefore judged on the DELTA.
func checkEditSyntax(relPath, original, newContent string) (note string, err error) {
	return checkSyntax(relPath, &original, newContent)
}

// checkCreateSyntax applies the gate to a file being brought into existence,
// which has no before state and is therefore judged absolutely.
func checkCreateSyntax(relPath, content string) (note string, err error) {
	return checkSyntax(relPath, nil, content)
}

// PrepareEdit runs the safety tripod's path-safety and exact-match legs,
// plus the best-effort syntax gate, for one block. It performs no I/O beyond
// reading the target file — no prompting, no backup, no write. A non-nil
// error is always a refusal reason meant to be shown to the user verbatim,
// matching the parser's existing descriptive-error convention (never a bare
// system fault). This is the single core both the CLI (daemon/apply_cmd.go)
// and the Mochiii TUI (clients/tui) call — neither keeps its own copy.
func PrepareEdit(realWorkspaceRoot string, block EditBlock) (*PreparedEdit, error) {
	targetPath, exists, err := resolveSafeTarget(realWorkspaceRoot, block.FilePath)
	if err != nil {
		return nil, err
	}

	// An empty SEARCH section means "this file's content is, or should be,
	// nothing" — create it, or fill it if it is there and empty (Fix 7). See
	// IsEmptySearch for why this used to be three different behaviours.
	if IsEmptySearch(block.Search) {
		if exists {
			data, err := os.ReadFile(targetPath)
			if err != nil {
				return nil, describeFileError("reading", block.FilePath, targetPath, err)
			}
			if len(data) > 0 {
				return nil, fmt.Errorf("%s already exists and is not empty; an empty SEARCH section means \"create this file\", so refusing to replace its whole content — send a SEARCH section naming the text to replace", block.FilePath)
			}
		}
		return prepareCreate(block, targetPath, exists)
	}

	if !exists {
		return nil, fmt.Errorf("%s does not exist; to create it, send an edit block with an empty SEARCH section and the file's content as REPLACE", block.FilePath)
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		return nil, describeFileError("reading", block.FilePath, targetPath, err)
	}
	original := string(data)

	// Locate the SEARCH text in the file AS IT IS NOW, walking the tolerance
	// ladder (Fix 6). This is a search problem, not a safety check: whether the
	// file has since changed is decided separately and byte-exactly, by
	// VerifyUnchanged at write time. Keeping those two apart is what lets the
	// matcher be forgiving about invisible differences without ever making the
	// staleness check forgiving about anything.
	match, err := findSearch(original, block.Search, block.FilePath)
	if err != nil {
		return nil, err
	}

	// The replacement is re-encoded to the line-ending convention of the text it
	// displaces before it is spliced in. Without that, the ordinary Windows
	// workflow -- an LF patch from `git diff` under core.autocrlf=true, applied to
	// a CRLF file -- leaves the file MIXED. See conformReplacementEOL, including
	// why this is not gated on match.Tier.
	replace, eolNote := conformReplacementEOL(block.Replace, original[match.Start:match.End], original)

	newContent := original[:match.Start] + replace + original[match.End:]
	startLine := strings.Count(original[:match.Start], "\n") + 1
	endLine := startLine + strings.Count(original[match.Start:match.End], "\n")

	syntaxNote, err := checkEditSyntax(block.FilePath, original, newContent)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", block.FilePath, err)
	}

	// One note, composed rather than replaced: the tier says what the matcher had
	// to forgive to FIND the text, the re-encode says what the writer did to it.
	// Both are separate facts and a caller that sees only one is missing half of
	// what happened. MatchNote reaches the model through propose_edit's result
	// text as well as the CLI and TUI, so neither is ever silent.
	matchNote := match.Tier.Note()
	if eolNote != "" {
		if matchNote != "" {
			matchNote += "; "
		}
		matchNote += eolNote
	}

	return &PreparedEdit{
		Block:      block,
		TargetPath: targetPath,
		Original:   original,
		NewContent: newContent,
		StartLine:  startLine,
		EndLine:    endLine,
		FileMode:   info.Mode(),
		SyntaxNote: syntaxNote,
		Tier:       match.Tier,
		MatchNote:  matchNote,
	}, nil
}

// VerifyUnchanged is the content-addressed staleness check: it re-reads the
// target and requires it to be byte-for-byte what PrepareEdit read. A prepared
// edit describes a splice into one exact sequence of bytes, so applying it to
// anything else would clobber whatever arrived in between.
//
// This is deliberately a SEPARATE check from locating the SEARCH text, and
// deliberately byte-exact (Fix 6). The matcher normalizes line endings,
// whitespace, indentation and unicode in order to FIND the passage the model
// meant in the file as it is now. None of that tolerance may reach here: a file
// whose line endings were rewritten, whose indentation was retabbed, or whose
// text was renormalized to NFD since the edit was prepared HAS changed, and a
// stale splice into it must be refused even though the matcher would happily
// consider the two forms equivalent. The only comparison this function performs
// is string equality on raw bytes; it calls nothing from match.go.
//
// Before this existed, nothing re-checked the file between PrepareEdit and the
// write. The daemon holds its per-workspace lock across both, so this always
// passes there; the TUI's review flow puts a human confirmation in between, and
// that is the window this closes.
func VerifyUnchanged(prepared *PreparedEdit) error {
	current, err := os.ReadFile(prepared.TargetPath)
	if err != nil {
		// A creating edit expects exactly this: nothing there. Anything else
		// appearing in the meantime is the same staleness failure as a changed
		// file, so absence is only acceptable when absence is what was prepared.
		//
		// Note this ReadFile FOLLOWS symlinks, so a DANGLING symlink planted at
		// the target reports os.IsNotExist and reaches this "absence is fine"
		// branch. That is intentionally not caught here — staleness is this
		// function's job, not symlink safety — because the write itself no longer
		// trusts it: Apply commits through writeFileAtomicNoFollow, which refuses
		// a symlinked leaf and never writes through the link (C2). This branch
		// deciding "proceed" therefore cannot become an out-of-workspace write.
		if os.IsNotExist(err) && prepared.Creates {
			return nil
		}
		return fmt.Errorf("re-reading %s before writing: %w", prepared.Block.FilePath, err)
	}
	if prepared.Creates {
		return fmt.Errorf("%s was created by something else since this edit was prepared; refusing to overwrite it", prepared.Block.FilePath)
	}
	if string(current) != prepared.Original {
		return fmt.Errorf("%s changed since this edit was prepared; refusing to apply a stale edit", prepared.Block.FilePath)
	}
	return nil
}

// Apply writes a prepared edit to disk and records its before/after backup
// snapshots. backupDir must already exist (see NewBackupSessionDir) -- Apply
// does not create it, since a caller applying multiple blocks in one run
// creates it once and reuses it across calls. BackupOriginal is idempotent per
// (backupDir, file), so calling Apply for two blocks that target the same file
// within one backupDir still captures the true pre-run original exactly once,
// regardless of how many blocks touch that file. This is the single core both
// the CLI (daemon/apply_cmd.go) and the Mochiii TUI (clients/tui/chat.go) use;
// neither keeps its own copy of the write+backup sequence.
//
// ORDERING IS LOAD-BEARING (Fix 1). Every fallible bookkeeping step runs while
// the workspace is still untouched, and the file write is the single, last act:
//
//	BackupOriginal -> BackupAfter -> [record created] -> write
//
// The order used to be BackupOriginal -> write -> BackupAfter, which meant a
// failure recording the post-apply snapshot (a full disk, a permission
// problem) returned an error to a caller whose file had ALREADY been mutated.
// The daemon reported applied:false, the TUI reported nothing applied, and
// undo then refused to revert the change because no after/ snapshot existed to
// match the file against -- an unreported, unrevertable mutation. Recording
// the snapshot first is a pure reorder: NewContent is fully determined by
// PrepareEdit, long before any byte is written.
//
// A caller must be able to trust the converse too: after/<file> is the
// baseline undo compares the file against, so it must never advertise content
// that is not on disk. If the write itself fails, the snapshot is rolled back
// to whatever it held before this call (the previous block's content in a
// multi-block run, or absent entirely).
func Apply(realWorkspaceRoot string, prepared *PreparedEdit, backupDir string) error {
	// Cross-process serialization for the whole verify->write span (M4). The
	// staleness check below and the write at the end of this function are a
	// read-then-write pair: without a lock spanning BOTH, two concurrent writers
	// each pass VerifyUnchanged against the same original and then both write,
	// and the second silently destroys the first's edit while both report
	// success. The daemon's in-process mutex closed that for two socket requests;
	// it cannot see the CLI or TUI, which write this same file from their own
	// processes. See LockWorkspaceApply.
	release, err := LockWorkspaceApply(realWorkspaceRoot)
	if err != nil {
		return fmt.Errorf("serializing apply on %s: %w", realWorkspaceRoot, err)
	}
	defer release()

	// Byte-exact staleness check, before any bookkeeping: a file that changed
	// since the edit was prepared must not be spliced into. See VerifyUnchanged
	// for why this is separate from — and stricter than — the match ladder.
	// Now that the lock above spans this check and the write, a concurrent
	// writer can only land BEFORE it (caught here, refused as stale) or AFTER
	// the write completes (caught by ITS own check) — never in between.
	if err := VerifyUnchanged(prepared); err != nil {
		return err
	}
	if err := BackupOriginal(backupDir, realWorkspaceRoot, prepared); err != nil {
		return fmt.Errorf("backing up %s: %w", prepared.Block.FilePath, err)
	}
	rollbackAfter, err := backupAfterReversible(backupDir, realWorkspaceRoot, prepared)
	if err != nil {
		return fmt.Errorf("recording post-apply snapshot for %s: %w", prepared.Block.FilePath, err)
	}
	// Record that this file did not exist before the run, so undo removes it
	// rather than restoring its 0-byte before/ snapshot (Fix C). Reversible and
	// pre-write for the same reason the after/ snapshot is: bookkeeping that
	// outlives a failed write would have undo delete a file this run never
	// wrote.
	rollbackCreated := func() {}
	if prepared.Creates {
		rollbackCreated, err = recordCreatedReversible(backupDir, realWorkspaceRoot, prepared)
		if err != nil {
			rollbackAfter()
			return fmt.Errorf("recording %s as newly created: %w", prepared.Block.FilePath, err)
		}
	}
	// Which directories the MkdirAll below is about to bring into existence,
	// recorded BEFORE it runs so undo can take them back again. Same ordering
	// rule as every other piece of bookkeeping here (Fix 1): fallible, and
	// reversible, while the workspace is still untouched.
	rollbackDirs := func() {}
	if prepared.Creates {
		rollbackDirs, err = recordCreatedDirsReversible(backupDir, realWorkspaceRoot, prepared)
		if err != nil {
			rollbackCreated()
			rollbackAfter()
			return fmt.Errorf("recording the directories created for %s: %w", prepared.Block.FilePath, err)
		}
	}
	// A creating edit may name a directory that does not exist yet. This is the
	// one mutation that precedes the write, and deliberately the last thing
	// before it: an empty directory is not file content, and leaving one behind
	// if the write then fails costs nothing and loses nothing.
	if prepared.Creates {
		if err := os.MkdirAll(filepath.Dir(prepared.TargetPath), 0755); err != nil {
			rollbackDirs()
			rollbackCreated()
			rollbackAfter()
			return fmt.Errorf("creating parent directories for %s: %w", prepared.Block.FilePath, err)
		}
	}
	// Hardened, atomic, symlink-refusing write — the forward-path mirror of the
	// undo path's temp-file+rename restore. A plain os.WriteFile here would
	// follow a symlink planted at the leaf (workspace escape) and truncate-then-
	// write non-atomically (partial-file corruption on interruption); see
	// writeFileAtomicNoFollow for how each is closed.
	if err := writeFileAtomicNoFollow(prepared.TargetPath, []byte(prepared.NewContent), prepared.FileMode); err != nil {
		rollbackDirs()
		rollbackCreated()
		rollbackAfter()
		return describeFileError("writing", prepared.Block.FilePath, prepared.TargetPath, err)
	}
	return nil
}

func describeExt(relPath string) string {
	if ext := filepath.Ext(relPath); ext != "" {
		return ext
	}
	return "files with no extension"
}

// ResolveSafeTargetPath resolves relPath against realWorkspaceRoot (already
// itself resolved through symlinks) and refuses anything not confined to the
// workspace, mirroring the indexer's ScanWorkspace confinement approach:
// absolute paths and ".." components are rejected outright, and the fully
// resolved (symlinks-followed) path must still land inside
// realWorkspaceRoot. Files the indexer would secret-skip are refused too —
// an edit block is untrusted model output and must never rewrite
// credentials.
//
// Confinement to the workspace is necessary but not sufficient (Fix 3): the
// indexer also prunes whole directories it will never show the model — VCS
// internals, this product's own backups and logs, credential dirs — and the
// writer must refuse to write into the same set, or model output can reach
// state that is executed (.git/hooks/*), trusted (.git/config), or relied on
// for recovery (.codeterminal/backups/.../before/*). See ProtectedDirNames.
//
// That check runs TWICE, deliberately. Once on the path as written, so the
// refusal is clear and reason-bearing whether or not the target exists; and
// once on the fully resolved path, so an in-tree symlink (innocent/ -> .git/)
// cannot launder the write. The resolved check is the load-bearing one; the
// first only improves the message.
func ResolveSafeTargetPath(realWorkspaceRoot, relPath string) (string, error) {
	path, exists, err := resolveSafeTarget(realWorkspaceRoot, relPath)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("resolving %s: no such file", relPath)
	}
	return path, nil
}

// resolveSafeTarget is ResolveSafeTargetPath's implementation, reporting
// separately whether the target exists rather than treating absence as a
// failure (Fix 7). Every gate below applies identically to a path that is about
// to be CREATED — a model must not be able to create a git hook or a
// credentials file any more than it can overwrite one — so the only thing the
// two cases differ in is how the leaf is resolved.
func resolveSafeTarget(realWorkspaceRoot, relPath string) (path string, exists bool, err error) {
	if filepath.IsAbs(relPath) {
		return "", false, fmt.Errorf("path %q is absolute; edits must target workspace-relative paths", relPath)
	}

	cleaned := filepath.Clean(relPath)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("path %q escapes the workspace root", relPath)
	}

	// Before the protected and secret gates, because every hazard it refuses is
	// a way of spelling a name those gates would not recognise.
	if err := RejectPathHazards(cleaned); err != nil {
		return "", false, err
	}

	if component := ProtectedDirComponent(cleaned); component != "" {
		return "", false, refuseProtectedDir(relPath, component)
	}
	if MatchesSecretName(filepath.Base(cleaned)) {
		return "", false, fmt.Errorf("path %q matches the indexer's secret-file rules; refusing to edit it", relPath)
	}

	full := filepath.Join(realWorkspaceRoot, cleaned)
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", false, fmt.Errorf("resolving %s: %w", relPath, err)
		}
		// Not there yet: confine via the deepest existing ancestor instead.
		newPath, nerr := resolveSafeNewPath(realWorkspaceRoot, cleaned)
		if nerr != nil {
			return "", false, nerr
		}
		return newPath, false, nil
	}

	rel, err := filepath.Rel(realWorkspaceRoot, realFull)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("path %q resolves outside the workspace root", relPath)
	}

	if component := ProtectedDirComponent(rel); component != "" {
		return "", false, refuseProtectedDir(relPath, component)
	}

	if MatchesSecretName(filepath.Base(realFull)) {
		return "", false, fmt.Errorf("path %q matches the indexer's secret-file rules; refusing to edit it", relPath)
	}

	return realFull, true, nil
}
