// The repository map: a bounded, deterministic answer to "what is in this
// project", built for a model that has never seen it.
//
// WHY IT EXISTS, MEASURED. A turn's context is retrieved chunks -- forty-line
// windows selected by similarity -- plus history. Nothing in it describes the
// SHAPE of the workspace, so a model that needs a file it was not handed has
// two options: search for it, or guess. The specialist that cannot search
// guessed: the tool-less Planner invented `src/agent/agent.ts` and
// `src/agent/tools.ts` -- TypeScript paths, in a Go repository -- and the later
// phases carried them into the answer as findings. Both of the four-phase
// pipeline's losses in the §14 A/B were attributed by the judge to exactly
// those invented paths (see orchestrator.go's handoff comment).
//
// A HEURISTIC SCAN, NOT THE LANGUAGE SERVER, and that is a deliberate trade.
// This daemon already drives gopls, typescript-language-server and
// pyright-langserver for query_compiler_definition and propose_ast_edit, where
// being exactly right about one symbol is the whole point. A map wants the
// opposite balance: it is for orientation, it covers every file in the tree,
// and it has to be cheap enough to build on demand. An LSP-backed map would be
// more accurate about three languages and would say NOTHING about a repository
// written in any of the others -- a Rust or Ruby project would get a map with
// no symbols at all, which is a worse map than an imperfect one. Precision
// stays where precision is the product; here, coverage is.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// maxRepoMapBytes bounds the rendered map at roughly 3k tokens. Sized against
// a real repository rather than guessed: this project is 518 files in 45
// directories, and 12 KiB fits every directory, almost every file name, and
// declarations for one file per directory. Everything omitted is COUNTED and
// reported in the map itself -- a map that quietly stops is one a model will
// read as complete and conclude a file does not exist.
const maxRepoMapBytes = 12 << 10

// declReserve is the share of the budget the path listing may NOT spend.
//
// Without it the declaration pass is dead code on any repository large enough
// to need a map: measured here, the file names alone consumed all 8 KiB of the
// first budget and not one declaration was emitted. A pass that never runs is
// worse than no pass, because it reads as a feature.
const declReserve = 4 << 10

// headerReserve is the room set aside for the one-line summary, which is
// written last because it reports what the passes below decided.
//
// Sized against the LONGEST header, not a typical one. Every clause is
// conditional, so the worst case is all of them firing at once -- truncated
// names, dropped directories and the file cap together -- which measures around
// 330 bytes. At 256 the map could overrun the cap it spent the whole render
// enforcing, in exactly the case where it has the most to apologise for.
// TestTheHeaderFitsItsReserveInTheWorstCase holds this honest.
const headerReserve = 512

// maxSymbolsPerFile caps how much any one file can spend of the shared budget.
// A generated 4,000-line API surface must not push every other file out of the
// map; twelve declarations is enough to say what a file is.
const maxSymbolsPerFile = 12

// maxDeclScanLines bounds the work per file. Declarations are spread through a
// file, so this is not "read the head" -- it is a ceiling on effort for a file
// that is enormous, and it is far above the length of anything hand-written.
const maxDeclScanLines = 4000

// maxDeclsCollected bounds what one file may hold in memory before the
// per-file cap is applied. Selection needs to see more than it keeps (see
// pickSymbols), but a generated file with ten thousand top-level constants
// must not be able to allocate a slice of them.
const maxDeclsCollected = 256

// repoFile is one file's contribution to the map.
//
// Symbols are NOT filled by the walk. Measured on this repository, extracting
// declarations from all 518 files cost 62ms of a 77ms build and 33 of those
// files' declarations ever reached the rendered map -- 407 whole-file reads
// thrown away. Render asks for them one file at a time, in the order it will
// print them, and stops asking when the budget is gone.
type repoFile struct {
	Path    string   // workspace-relative, slash-separated
	Symbols []string // top-level declarations, in source order; see loaded
	Class   FileClass
	loaded  bool // Symbols has been extracted (nil is a real answer: no decls)
}

// RepoMap is the built map, before rendering.
type RepoMap struct {
	Files      []repoFile
	Scanned    int // files the walk accepted
	Skipped    int // files the indexer's own rules excluded
	Truncated  bool
	NotShown   int
	WorkspaceR string

	// LimitHit records that the walk stopped at maxFilesScanned with tree left
	// unvisited. It is reported in the rendered header for the same reason
	// NotShown is: a map that stops without saying so is read as complete.
	LimitHit bool
}

// buildRepoMap walks the workspace and extracts top-level declarations.
//
// THE WALK IS THE INDEXER'S WALK, deliberately: the same gitignore layers, the
// same pruned directories, the same secret-name and noise-file rules, the same
// symlink refusal (shouldSkipFile). A map built on its own rules would be a
// SECOND, WEAKER exclusion surface -- and the first thing it would do wrong is
// name a .env file that the indexer has always refused to read. Sharing the
// predicate means a fix to one is a fix to both, which is the same argument
// isPrunedDir makes for sharing editapply.ProtectedDirNames with the writer.
func buildRepoMap(ctx context.Context, root string) (*RepoMap, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root %s: %w", root, err)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	ignore := newGitignoreMatcher(realRoot)
	m := &RepoMap{WorkspaceR: realRoot}
	seen := 0

	walkErr := filepath.WalkDir(realRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// CANCELLABLE, because this walk is inside a turn the user can stop.
		// A turn interrupted while the map is building on a large or slow tree
		// would otherwise keep a goroutine walking the filesystem for a turn
		// that already ended. Checked per batch of entries rather than per
		// entry: ctx.Err() takes a lock, and a stat-bound walk should not pay
		// for it 500 times.
		if seen++; seen%256 == 0 {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
		}
		if path == realRoot {
			return nil
		}
		relPath, relErr := filepath.Rel(realRoot, path)
		if relErr != nil || strings.HasPrefix(relPath, "..") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if isPrunedDir(d.Name()) || ignore.matchDir(relPath) {
				return fs.SkipDir
			}
			return nil
		}
		if _, skip, err := shouldSkipFile(path, relPath, ignore); err != nil || skip {
			m.Skipped++
			return nil //nolint:nilerr // an unreadable file is skipped, never fatal
		}
		if len(m.Files) >= maxFilesScanned {
			// SAY SO. The first version returned SkipAll here and rendered a
			// header claiming the tree it had was the tree -- on a repository
			// past the cap the map would name a subset and read as exhaustive,
			// which is the invented-path failure arriving from the other side.
			m.LimitHit = true
			return fs.SkipAll
		}

		m.Scanned++
		m.Files = append(m.Files, repoFile{
			Path:  filepath.ToSlash(relPath),
			Class: classifyFile(relPath),
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	return m, nil
}

// symbolsFor extracts f's declarations on first request and remembers the
// answer, including the empty one -- a file with no top-level declarations must
// not be re-read on every round of the detail pass.
func (m *RepoMap) symbolsFor(f *repoFile) []string {
	if f.loaded {
		return f.Symbols
	}
	f.loaded = true

	// PROSE IS NOT CODE. declarationsIn matches a column-zero declaration
	// keyword, and English obliges: "let the", "record implied", "function
	// this", "class P0". Measured on this repo before this guard, 2 of the 33
	// files the map described in detail were markdown, and what it said about
	// them was:
	//
	//	docs/ARCHIVE/BACKLOG_2026-07.md: var rows, function this, function
	//	  revoke, let the, record implied
	//	docs/ENTERPRISE_QA_REPORT_2026-08-05.md: class P0
	//
	// Two costs, and the second is the serious one. It spends a scarce byte
	// budget on nothing, and it feeds the planner INVENTED SYMBOLS -- which
	// inverts the entire reason this map exists. roles.go records that reason:
	// the planner was citing files that do not exist, and the fix was to give
	// it the real ones rather than to ask it not to guess. A map that hands it
	// "function revoke" from a changelog is the same failure with extra steps.
	//
	// Docs keep their place in the FILE listing; only the declaration line is
	// suppressed. classRank already ranks them below code, so in practice this
	// also returns budget to files that have real symbols.
	if f.Class == FileClassDoc {
		return nil
	}

	f.Symbols = dedupeSymbols(declarationsIn(filepath.Join(m.WorkspaceR, filepath.FromSlash(f.Path))))
	return f.Symbols
}

// dedupeSymbols drops repeats, keeping first-seen (source) order.
//
// A name can legitimately appear twice at column zero -- an overload in a
// dynamic language, a shell function redefined under a guard, a Go method name
// shared by two receivers. The map has room for one line per file, so printing
// "func print, func print" (measured, scripts/agent-cost-bench.sh) spends a
// slot to say nothing and reads as a bug in the map rather than a fact about
// the file.
func dedupeSymbols(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// declarationsIn extracts a file's top-level declarations.
//
// COLUMN ZERO IS THE WHOLE HEURISTIC. In every language here a top-level
// declaration starts at the left margin, and anything indented is a member, a
// local, or a body -- so one rule covers Go, Python, JavaScript, TypeScript,
// Rust, Ruby and shell without knowing anything about any of them. It is not a
// parser and does not pretend to be: a declaration inside a raw string literal
// at column zero will be listed. That costs a reader one confusing line and
// costs the map nothing else, which is the right side of the trade for a
// document whose job is "look here first".
func declarationsIn(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	// Read-only, so a close error tells nobody anything they can act on.
	defer func() { _ = f.Close() }()

	var found []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for line := 0; scanner.Scan() && line < maxDeclScanLines; line++ {
		if len(found) >= maxDeclsCollected {
			break
		}
		text := scanner.Text()
		if text == "" || text[0] == ' ' || text[0] == '\t' {
			continue // indented: a member or a body, not a top-level declaration
		}
		if name := declarationName(text); name != "" {
			found = append(found, name)
		}
	}
	return pickSymbols(found)
}

// pickSymbols chooses which declarations survive the per-file cap.
//
// SOURCE ORDER IS THE WRONG SELECTOR, measured on this file. Taking the first
// twelve gave repomap.go's own entry five consts, two types and five funcs --
// and dropped Render, the function the file exists for. Go puts its consts and
// vars at the top; TypeScript puts its imported-and-re-exported consts there.
// The first twelve lines of a file are the least informative twelve
// declarations in it.
//
// So: definitions (func, type, class, interface) outrank bindings (const, var,
// let), and source order breaks ties within each group. A reader gets the API
// of the file rather than its preamble.
func pickSymbols(found []string) []string {
	if len(found) <= maxSymbolsPerFile {
		return found
	}
	keep := make([]int, 0, len(found))
	for i := range found {
		keep = append(keep, i)
	}
	sort.SliceStable(keep, func(a, b int) bool {
		return symbolRank(found[keep[a]]) < symbolRank(found[keep[b]])
	})
	keep = keep[:maxSymbolsPerFile]
	sort.Ints(keep) // back into source order
	out := make([]string, 0, maxSymbolsPerFile)
	for _, i := range keep {
		out = append(out, found[i])
	}
	return out
}

// symbolRank is 0 for a declaration that defines something and 1 for one that
// merely binds a value. The rendered form is "<keyword> <name>", so the keyword
// is the prefix.
func symbolRank(sym string) int {
	switch {
	case strings.HasPrefix(sym, "const "), strings.HasPrefix(sym, "var "),
		strings.HasPrefix(sym, "let "), strings.HasPrefix(sym, "export const "):
		return 1
	}
	return 0
}

// declKeywords are the words that begin a top-level declaration, longest first
// so "export function" wins over "export".
var declKeywords = []string{
	"export default function", "export async function", "export function", "export class",
	"export interface", "export type", "export const", "export enum",
	"public class", "public interface", "public record",
	"async function", "func", "function", "class", "interface", "struct", "enum", "trait",
	"impl", "type", "def", "module", "fn", "pub fn", "pub struct", "pub enum", "pub trait",
	"const", "var", "let", "record",
}

// declarationName returns the declared name in line, or "" if the line does not
// declare anything at the top level.
func declarationName(line string) string {
	// trimCR first: this is reached both from repomap's bufio.Scanner (already
	// CR-free) and from the chunker's hand-rolled split (not), and a trailing
	// carriage return would defeat the "{(" trim below.
	trimmed := strings.TrimRight(trimCR(line), " \t{(")

	// A CommonJS import is not a declaration of anything this project owns.
	// `const fs = require("fs")` is written at column zero and begins with a
	// keyword, so it reaches the map as "const fs" -- measured, an entry for one
	// script here read "const fs, const path, const os, const cp, const crypto"
	// before naming a single function it defined. ES imports never had this
	// problem because "import" is not a declaration keyword; this is the same
	// exclusion for the older spelling.
	if strings.Contains(trimmed, "= require(") || strings.Contains(trimmed, "=require(") {
		return ""
	}
	for _, kw := range declKeywords {
		rest, ok := strings.CutPrefix(trimmed, kw+" ")
		if !ok {
			continue
		}
		if name := firstIdentifier(rest); name != "" {
			return kw + " " + name
		}
		return ""
	}
	// A shell function: `name() {`.
	if idx := strings.Index(trimmed, "()"); idx > 0 {
		if name := firstIdentifier(trimmed[:idx]); name == trimmed[:idx] {
			return "func " + name
		}
	}
	return ""
}

// firstIdentifier pulls the declared name out of what follows the keyword,
// stepping over the pieces that sit between a keyword and a name: a Go method
// receiver "(r *T) Name", a pointer or generic sigil, an export list.
func firstIdentifier(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "(") {
		// Go method receiver: skip to the closing paren, then take the name.
		if end := strings.Index(s, ")"); end >= 0 {
			s = strings.TrimSpace(s[end+1:])
		}
	}
	s = strings.TrimLeft(s, "*&")

	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$' {
			b.WriteRune(r)
			continue
		}
		break
	}
	name := b.String()
	// A keyword followed by a STRUCTURAL keyword is not a declaration
	// ("type struct"). Only structural ones, and the narrowing is measured:
	// rejecting every word in declKeywords meant any identifier that happens to
	// be a declaration keyword in some OTHER language was thrown away, and
	// `record` is one -- so `func (l *ledger) record(...)` and
	// `func (s *warnScanDetectorStats) record(...)` were absent from the
	// repository map and invisible as chunk boundaries, purely because C# spells
	// a type that way. `module`, `impl`, `enum` and `trait` are all ordinary
	// identifiers in Go and TypeScript too.
	if name == "" || isStructuralKeyword(name) {
		return ""
	}
	return name
}

// isStructuralKeyword reports whether word names a type or function FORM --
// the only words that can legally follow a declaration keyword and still leave
// the declaration unnamed.
func isStructuralKeyword(word string) bool {
	switch word {
	case "struct", "interface", "enum", "class", "trait", "func", "function", "fn", "def":
		return true
	}
	return false
}

// Render turns the map into the text a model reads.
//
// THE BUDGET POLICY IS FAIRNESS ACROSS DIRECTORIES, and it is the whole design.
// Two earlier versions got this wrong in the same way at two different scales,
// and both were caught by measuring the output rather than by reading it:
//
//   - v1 spent the budget on flat full-path lines and listed 48 of 515 files,
//     every one alphabetically before "daemon/".
//   - v2 grouped by directory and truncated within a directory -- which fixed
//     the file-name half and left the directory half untouched. Measured, it
//     gave daemon/ 4,205 of the 8,192-byte path budget for its 228 test files
//     and then died at "editapply/": helper/, mcp-servers/, protocol/, proxy/
//     and testdata/ were absent from the map, while the header above them said
//     "518 files in 45 directories". A model asked where the wire protocol
//     lives would read that map, find no protocol/, and invent a path -- the
//     precise failure this file exists to prevent, reproduced by the fix for it.
//
// The lesson both times: whoever sorts first spends the budget. So the map is
// laid out in three tiers, cheapest and most valuable first.
//
//	tier 1  every directory, with its file count. Unconditional. ~30 bytes each,
//	        and it is what makes "protocol/ (6)" a fact rather than a guess.
//	tier 2  file names, ROUND-ROBIN across directories -- one name each, then
//	        another, until the path budget is gone. A 228-file directory can no
//	        longer starve a 6-file one; it just stops earlier in its own list.
//	tier 3  declarations, round-robin the same way, drawn lazily.
//
// Every tier reports what it dropped, in the header, in files rather than bytes.
func (m *RepoMap) Render() string {
	if m == nil || len(m.Files) == 0 {
		return "repository map: no readable files in this workspace"
	}

	dirs, byDir := m.groupByDir()

	// TIER 1: the skeleton. Charged against the budget up front, including the
	// worst-case "+N more" suffix each line might need, so tiers 2 and 3 spend
	// only what is genuinely left.
	const pathBudgetTotal = maxRepoMapBytes - declReserve - headerReserve
	pathBudget := pathBudgetTotal
	dirsShown := 0
	for _, dir := range dirs {
		g := byDir[dir]
		cost := len(dir) + len(fmt.Sprintf("/ (%d): +%d more\n", len(g.files), len(g.files)))
		if pathBudget-cost < 0 {
			break // a tree with thousands of directories; reported below
		}
		pathBudget -= cost
		dirsShown++
	}
	dirs = dirs[:dirsShown]

	// TIER 2: names, one per directory per round.
	for round := 0; ; round++ {
		progressed := false
		for _, dir := range dirs {
			g := byDir[dir]
			if g.shown >= len(g.files) {
				continue
			}
			cost := len(path.Base(g.files[g.shown].Path)) + 1
			if pathBudget-cost < 0 {
				continue
			}
			pathBudget -= cost
			g.shown++
			progressed = true
		}
		if !progressed {
			break
		}
	}

	var b strings.Builder
	omittedFiles := 0
	for _, dir := range dirs {
		g := byDir[dir]
		names := make([]string, 0, g.shown)
		for _, f := range g.files[:g.shown] {
			names = append(names, path.Base(f.Path))
		}
		dropped := len(g.files) - g.shown
		omittedFiles += dropped
		fmt.Fprintf(&b, "%s/ (%d):", dir, len(g.files))
		if len(names) > 0 {
			b.WriteString(" " + strings.Join(names, " "))
		}
		if dropped > 0 {
			fmt.Fprintf(&b, " +%d more", dropped)
		}
		b.WriteString("\n")
	}

	// TIER 3: declarations. The candidate order per directory is fixed once
	// (code before config before docs before tests), and files are read only as
	// this loop reaches them -- see repoFile.
	// Everything tiers 1 and 2 did not spend of their own allowance is still
	// available here, on top of declReserve.
	// declSeparator is charged even when no detail is written: it is 15 bytes,
	// and a cap that the map can step over by the width of its own section
	// heading is not a cap.
	const declSeparator = len("\ndeclarations:\n")
	used := headerReserve + declSeparator + (pathBudgetTotal - pathBudget)
	var detail strings.Builder
	detailed, misses := 0, 0
	for round := 0; round < maxSymbolsPerFile && misses < 3; round++ {
		progressed := false
		for _, dir := range dirs {
			if misses >= 3 {
				// Reading files is the expensive half of this pass, so stop
				// asking for them the moment the budget is provably gone rather
				// than finishing the round and discarding what it read.
				break
			}
			g := byDir[dir]
			cand, syms := g.nextDetail(m)
			if cand == nil {
				continue
			}
			progressed = true
			line := cand.Path + ": " + strings.Join(syms, ", ") + "\n"
			if used+len(line) > maxRepoMapBytes {
				// Not fatal on its own -- one file with twelve long symbol
				// names is not proof the budget is gone -- but three in a row is.
				misses++
				continue
			}
			misses = 0
			used += len(line)
			detail.WriteString(line)
			detailed++
		}
		if !progressed {
			break
		}
	}

	m.Truncated = omittedFiles > 0 || len(dirs) < len(byDir)
	m.NotShown = omittedFiles

	head := m.renderHeader(len(dirs), len(byDir), detailed, omittedFiles)
	if detail.Len() > 0 {
		return head + b.String() + "\ndeclarations:\n" + detail.String()
	}
	return head + b.String()
}

// renderHeader states, in one line, exactly how complete the map below it is.
//
// SAID, NOT SILENTLY DONE. A model that reads a truncated map as complete will
// conclude the files it cannot see do not exist, which is the exact failure
// this whole file was written to stop. Every omission this map makes is counted
// somewhere in this sentence.
func (m *RepoMap) renderHeader(dirsShown, dirsTotal, detailed, omittedFiles int) string {
	var head strings.Builder
	fmt.Fprintf(&head, "repository map — %d files in %d directories", len(m.Files), dirsTotal)
	if detailed > 0 {
		fmt.Fprintf(&head, "; declarations shown for %d of them", detailed)
	}
	if omittedFiles > 0 {
		fmt.Fprintf(&head, "; %d file names omitted for space (use list_directory on a directory to see the rest)", omittedFiles)
	}
	if dirsShown < dirsTotal {
		fmt.Fprintf(&head, "; %d directories omitted entirely", dirsTotal-dirsShown)
	}
	if m.LimitHit {
		fmt.Fprintf(&head, "; the workspace exceeds %d files and the scan stopped early, so this map is INCOMPLETE and paths outside it may still exist", maxFilesScanned)
	}
	head.WriteString("\n")
	return head.String()
}

// dirGroup is one directory's slice of the map, plus the two cursors the tiers
// advance: how many names have been shown, and how far the detail pass has got.
type dirGroup struct {
	dir    string
	files  []*repoFile // class-ranked, then by name; see groupByDir
	shown  int         // tier 2 cursor
	cursor int         // tier 3 cursor
}

// nextDetail returns the next file in this directory worth spending declaration
// budget on, extracting declarations as it goes and stepping over files that
// turn out to have none. A file with no top-level declarations is never a
// candidate: the line would cost bytes and say nothing tier 2 did not.
func (g *dirGroup) nextDetail(m *RepoMap) (*repoFile, []string) {
	for g.cursor < len(g.files) {
		f := g.files[g.cursor]
		g.cursor++
		if syms := m.symbolsFor(f); len(syms) > 0 {
			return f, syms
		}
	}
	return nil, nil
}

// groupByDir buckets the files by directory, in sorted directory order.
func (m *RepoMap) groupByDir() ([]string, map[string]*dirGroup) {
	byDir := map[string]*dirGroup{}
	var dirs []string
	for i := range m.Files {
		f := &m.Files[i]
		dir := path.Dir(f.Path)
		if dir == "." {
			dir = "(root)"
		}
		g, ok := byDir[dir]
		if !ok {
			g = &dirGroup{dir: dir}
			byDir[dir] = g
			dirs = append(dirs, dir)
		}
		g.files = append(g.files, f)
	}
	sort.Strings(dirs)

	// WITHIN a directory, code before config before docs before tests, then by
	// name. The alphabet is not a priority order, and letting it act as one is
	// the same bug this file has now had at three scales: measured before this,
	// daemon/'s 63 visible names ran from "addrinuse_unix.go" to
	// "gate7_scrub_test.go" -- forty test files shown, and server.go,
	// orchestrator.go and roles.go hidden behind them. Tier 3 walks this order
	// too, so the sort is done once here rather than per directory per round.
	for _, g := range byDir {
		sort.SliceStable(g.files, func(i, j int) bool {
			if ri, rj := classRank(g.files[i].Class), classRank(g.files[j].Class); ri != rj {
				return ri < rj
			}
			return g.files[i].Path < g.files[j].Path
		})
	}
	return dirs, byDir
}

// classRank orders the budget: real code first, then docs and config, then
// tests, then everything else.
func classRank(c FileClass) int {
	switch c {
	case FileClassCode:
		return 0
	case FileClassConfig:
		return 1
	case FileClassDoc:
		return 2
	case FileClassTest:
		return 3
	default:
		return 4
	}
}
