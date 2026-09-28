package main

import (
	"bufio"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"mochiii/protocol"
)

// CHAT HISTORY: the conversations ctrl+n closed, so /history can find work
// that was left half done and bring it back.
//
// Until this existed ctrl+n DELETED the conversation (ClearWorkspace), so a
// project had exactly one chat and every earlier one was gone. Now ctrl+n saves
// it here first.
//
// ONE GZIPPED JSONL FILE PER CHAT, under StateDir()/history/<workspace key>/ --
// beside memory.db and never inside the project, so it cannot be committed.
// Line 1 is a small header (title, when, how many turns, the spec, whether the
// last answer was cut off); every later line is one turn. Listing reads only
// the header line, so a folder of hundreds of chats lists without loading any
// of them.
//
// BOUNDED, so the folder stays small however long it is used: at most
// maxArchivesPerWorkspace chats per project and maxSavedChatBytes for the whole
// folder, oldest dropped first. No age limit, for memory.go's reason: "I came
// back after a month and my chat was gone" is worse than a bounded folder.
//
// DISK IS UNTRUSTED on the way back in, exactly as memory.db is: reads are
// bounded after decompression (a planted gzip bomb cannot exhaust memory), a
// chat id must match the file-name pattern (it can never name a path), symlinks
// are refused, and loaded turns are re-validated with validTurn.

const (
	historyDirName = "history"
	archiveSuffix  = ".jsonl.gz"
	archiveVersion = 1

	// maxArchivesPerWorkspace and maxSavedChatBytes bound the folder. MEASURED
	// on a real memory.db: 72 turns were 48 KB of text and 19 KB gzipped, so
	// 20 MiB holds about a thousand chats of that size.
	maxArchivesPerWorkspace = 50
	maxSavedChatBytes       = 20 << 20

	// maxArchiveBytes bounds one chat DECOMPRESSED. memory.db keeps at most
	// maxTurnsPerWorkspace turns, so a real chat is far below this; a file
	// that inflates past it was not written here.
	maxArchiveBytes = 8 << 20

	// maxArchiveHeaderBytes bounds the header line a list reads per file.
	maxArchiveHeaderBytes = 16 << 10

	// archiveTitleRunes clips the title and last prompt a list shows.
	archiveTitleRunes = 80

	// staleTempAge is how old a leftover temp file (a save interrupted by a
	// crash) must be before pruning removes it.
	staleTempAge = time.Hour
)

// archiveIDPattern is the whole of a chat id: the UTC time it was saved and 8
// random hex digits. Time first, so ids sort oldest to newest.
var archiveIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)

const archiveIDTimeLayout = "20060102T150405Z"

var errArchiveNotFound = errors.New("no saved chat with that id")

// archiveHeader is line 1 of a saved chat.
type archiveHeader struct {
	V          int    `json:"v"`
	Title      string `json:"title"`
	LastPrompt string `json:"last_prompt,omitempty"`
	Started    string `json:"started,omitempty"`
	Ended      string `json:"ended"`
	Turns      int    `json:"turns"`
	Bytes      int    `json:"bytes"`
	Spec       string `json:"spec,omitempty"`
	Incomplete string `json:"incomplete,omitempty"`
}

// archiveLine is every later line: one turn.
type archiveLine struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	At      string `json:"at,omitempty"`
}

// savedChat is one listed chat: its id and its header.
type savedChat struct {
	ID     string
	Header archiveHeader
}

// chatArchive is one workspace's saved chats.
type chatArchive struct {
	root string // StateDir()/history
	dir  string // root/<workspace key>
}

// historyRoot is the folder every workspace's saved chats live under.
func historyRoot() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, historyDirName), nil
}

// workspaceKey names a workspace's folder without putting its path on disk
// as a file name.
func workspaceKey(workspace string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(workspace)))
	return hex.EncodeToString(sum[:8])
}

func newChatArchive(root, workspace string) *chatArchive {
	return &chatArchive{root: root, dir: filepath.Join(root, workspaceKey(workspace))}
}

// ensureDir makes root and dir, owner-only, and refuses either one if it is a
// symlink -- the same refusal OpenMemoryStore makes for memory.db, for the
// same reason: a planted link would send every chat somewhere else.
func (a *chatArchive) ensureDir() error {
	for _, d := range []string{a.root, a.dir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("creating history folder: %w", err)
		}
		fi, err := os.Lstat(d)
		if err != nil {
			return fmt.Errorf("checking history folder: %w", err)
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("history folder %s is not a plain directory; refusing to use it", d)
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return fmt.Errorf("restricting history folder: %w", err)
		}
	}
	return nil
}

// dirUsable reports whether the workspace folder exists and is a plain
// directory under a plain root. A missing folder means no saved chats.
func (a *chatArchive) dirUsable() (bool, error) {
	for _, d := range []string{a.root, a.dir} {
		fi, err := os.Lstat(d)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return false, fmt.Errorf("history folder %s is not a plain directory; refusing to use it", d)
		}
	}
	return true, nil
}

// save writes turns as one saved chat and returns its id. A conversation with
// no complete exchange saves nothing and returns "".
func (a *chatArchive) save(turns []storedTurn, spec string, now time.Time) (string, error) {
	h, ok := headerFor(turns, spec, now)
	if !ok {
		return "", nil
	}
	if err := a.ensureDir(); err != nil {
		return "", err
	}
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("naming the saved chat: %w", err)
	}
	id := now.UTC().Format(archiveIDTimeLayout) + "-" + hex.EncodeToString(suffix[:])

	tmp, err := os.CreateTemp(a.dir, ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("saving chat: %w", err)
	}
	tmpName := tmp.Name()
	fail := func(err error) (string, error) {
		_ = tmp.Close()        // the write already failed; this only releases the file
		_ = os.Remove(tmpName) // best effort; pruning removes a stale temp file anyway
		return "", fmt.Errorf("saving chat: %w", err)
	}
	gz, err := gzip.NewWriterLevel(tmp, gzip.BestCompression)
	if err != nil {
		return fail(err)
	}
	enc := json.NewEncoder(gz)
	if err := enc.Encode(h); err != nil {
		return fail(err)
	}
	for _, t := range turns {
		if err := enc.Encode(archiveLine{Role: t.Role, Content: t.Content, At: t.CreatedAt}); err != nil {
			return fail(err)
		}
	}
	if err := gz.Close(); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmpName, filepath.Join(a.dir, id+archiveSuffix)); err != nil {
		_ = os.Remove(tmpName) // best effort, as in fail
		return "", fmt.Errorf("saving chat: %w", err)
	}
	return id, nil
}

// headerFor describes turns. ok is false when there is nothing worth saving:
// no user prompt with an answer after it.
func headerFor(turns []storedTurn, spec string, now time.Time) (archiveHeader, bool) {
	h := archiveHeader{V: archiveVersion, Ended: now.UTC().Format(time.RFC3339)}
	answered := false
	for i, t := range turns {
		h.Bytes += len(t.Content)
		switch t.Role {
		case "user":
			if h.Title == "" {
				h.Title = clipRunes(oneLine(t.Content), archiveTitleRunes)
				h.Started = t.CreatedAt
			}
			h.LastPrompt = clipRunes(oneLine(t.Content), archiveTitleRunes)
		case "assistant":
			if h.Title != "" {
				answered = true
			}
			if i == len(turns)-1 {
				h.Incomplete = incompleteReasonOf(t.Content)
			}
		}
	}
	h.Turns = len(turns)
	if rel, ok := specRelPath(spec); ok {
		h.Spec = rel
	}
	return h, answered
}

// incompleteReasonOf recovers why an answer was cut off from the note
// persistTurn appended to it (incompleteHistoryNote), or "" for a finished one.
func incompleteReasonOf(content string) string {
	for _, reason := range []string{
		protocol.IncompleteLength, protocol.IncompleteContentFilter, protocol.IncompleteBudgetExceeded,
		protocol.IncompleteAgentBudget, protocol.IncompleteUserCancelled, protocol.IncompleteProviderError,
	} {
		if note := incompleteHistoryNote(reason); note != "" && strings.HasSuffix(content, note) {
			return reason
		}
	}
	return ""
}

// list returns the saved chats, newest first. A file that cannot be read is
// skipped rather than failing the list: one damaged chat must not hide the
// rest.
func (a *chatArchive) list() ([]savedChat, error) {
	ok, err := a.dirUsable()
	if err != nil || !ok {
		return nil, err
	}
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return nil, fmt.Errorf("reading history folder: %w", err)
	}
	var out []savedChat
	for _, e := range entries {
		id, ok := archiveIDOf(e.Name())
		if !ok || !e.Type().IsRegular() {
			continue
		}
		h, err := readArchiveHeader(filepath.Join(a.dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, savedChat{ID: id, Header: h})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// archiveIDOf returns the chat id a file name carries, if it is one.
func archiveIDOf(name string) (string, bool) {
	id, found := strings.CutSuffix(name, archiveSuffix)
	return id, found && archiveIDPattern.MatchString(id)
}

// openArchive opens a saved chat for reading: a regular file, never a link.
func openArchive(path string) (*os.File, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errArchiveNotFound
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("saved chat is not a regular file; refusing to read it")
	}
	return os.Open(path)
}

// readArchiveHeader reads line 1 and nothing more.
func readArchiveHeader(path string) (archiveHeader, error) {
	f, err := openArchive(path)
	if err != nil {
		return archiveHeader{}, err
	}
	defer func() { _ = f.Close() }() // read-only
	gz, err := gzip.NewReader(f)
	if err != nil {
		return archiveHeader{}, err
	}
	line, err := bufio.NewReader(io.LimitReader(gz, maxArchiveHeaderBytes)).ReadBytes('\n')
	if err != nil {
		return archiveHeader{}, fmt.Errorf("reading saved chat header: %w", err)
	}
	var h archiveHeader
	if err := json.Unmarshal(line, &h); err != nil {
		return archiveHeader{}, fmt.Errorf("reading saved chat header: %w", err)
	}
	if h.V != archiveVersion {
		return archiveHeader{}, fmt.Errorf("saved chat has format version %d; this daemon reads %d", h.V, archiveVersion)
	}
	return h, nil
}

// load reads one saved chat: its header and its turns, oldest first. Turns
// that fail validTurn are dropped, as LoadRecentTurns drops them from
// memory.db.
func (a *chatArchive) load(id string) (archiveHeader, []protocol.Turn, error) {
	path, err := a.pathFor(id)
	if err != nil {
		return archiveHeader{}, nil, err
	}
	if ok, err := a.dirUsable(); err != nil || !ok {
		if err == nil {
			err = errArchiveNotFound
		}
		return archiveHeader{}, nil, err
	}
	f, err := openArchive(path)
	if err != nil {
		return archiveHeader{}, nil, err
	}
	defer func() { _ = f.Close() }() // read-only
	gz, err := gzip.NewReader(f)
	if err != nil {
		return archiveHeader{}, nil, fmt.Errorf("reading saved chat: %w", err)
	}
	limited := &io.LimitedReader{R: gz, N: maxArchiveBytes + 1}
	dec := json.NewDecoder(limited)
	var h archiveHeader
	if err := dec.Decode(&h); err != nil {
		return archiveHeader{}, nil, fmt.Errorf("reading saved chat: %w", err)
	}
	if h.V != archiveVersion {
		return archiveHeader{}, nil, fmt.Errorf("saved chat has format version %d; this daemon reads %d", h.V, archiveVersion)
	}
	var turns []protocol.Turn
	for {
		var l archiveLine
		err := dec.Decode(&l)
		if errors.Is(err, io.EOF) {
			break
		}
		if limited.N <= 0 {
			return archiveHeader{}, nil, fmt.Errorf("saved chat is larger than %d bytes; refusing to read it", maxArchiveBytes)
		}
		if err != nil {
			return archiveHeader{}, nil, fmt.Errorf("reading saved chat: %w", err)
		}
		if t := (protocol.Turn{Role: l.Role, Content: l.Content}); validTurn(t) {
			turns = append(turns, t)
		}
	}
	if limited.N <= 0 {
		return archiveHeader{}, nil, fmt.Errorf("saved chat is larger than %d bytes; refusing to read it", maxArchiveBytes)
	}
	return h, turns, nil
}

// remove deletes one saved chat.
func (a *chatArchive) remove(id string) error {
	path, err := a.pathFor(id)
	if err != nil {
		return err
	}
	if ok, err := a.dirUsable(); err != nil || !ok {
		if err == nil {
			err = errArchiveNotFound
		}
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errArchiveNotFound
		}
		return fmt.Errorf("deleting saved chat: %w", err)
	}
	return nil
}

// pathFor turns an id into its file, refusing anything that is not an id.
func (a *chatArchive) pathFor(id string) (string, error) {
	if !archiveIDPattern.MatchString(id) {
		return "", errArchiveNotFound
	}
	return filepath.Join(a.dir, id+archiveSuffix), nil
}

// pruneHistory keeps the whole history folder bounded: at most keep chats
// per workspace, then at most maxBytes across every workspace, oldest first
// either way. It also removes temp files a crash left behind. Best effort:
// a file that cannot be removed is left for the next prune.
func pruneHistory(root string, keep int, maxBytes int64, now time.Time) {
	fi, err := os.Lstat(root)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return
	}
	dirs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type file struct {
		id, path string
		size     int64
	}
	var all []file
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(root, d.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var files []file
		for _, e := range entries {
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			path := filepath.Join(dir, e.Name())
			if strings.HasPrefix(e.Name(), ".tmp-") {
				if now.Sub(info.ModTime()) > staleTempAge {
					_ = os.Remove(path) // best effort
				}
				continue
			}
			if id, ok := archiveIDOf(e.Name()); ok {
				files = append(files, file{id: id, path: path, size: info.Size()})
			}
		}
		sort.Slice(files, func(i, j int) bool { return files[i].id > files[j].id })
		for i, f := range files {
			if i >= keep {
				_ = os.Remove(f.path) // best effort
				continue
			}
			all = append(all, f)
		}
	}
	var total int64
	for _, f := range all {
		total += f.size
	}
	// Oldest first across workspaces: ids start with the time they were saved.
	sort.Slice(all, func(i, j int) bool { return all[i].id < all[j].id })
	for _, f := range all {
		if total <= maxBytes {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}

// oneLine collapses whitespace runs, newlines included, to single spaces.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clipRunes shortens s to at most n runes, marking the cut.
func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
