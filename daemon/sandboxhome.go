package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"time"

	"mochiii/protocol"
)

// RECLAIMING SANDBOX HOMES (register item 37).
//
// sandbox_exec gives each workspace its own HOME under the user cache directory
// (sandboxExecHome), and nothing ever removed one: open ten projects and there
// were ten folders, forever, and deleting a project left its folder behind.
// Measured before this existed: 1,712 folders and 232 MB, 1,107 of them empty.
//
// The contrast that made it a defect rather than a trade was in the same
// codebase: conversation history is bounded, and pruned at thirty days
// (maxTurnAge in memory.go). This is the same bound, for the same reason.
//
// THIS FILE DELETES THINGS, so what it will and will not touch is stated as
// rules and each rule has a test:
//
//   - The root must be absolute, named sandbox-home, with a parent named
//     mochiii. Anything else is refused outright and nothing is deleted.
//   - Only entries whose name is exactly a workspace tag (16 lowercase hex) are
//     candidates. Everything else under the root is left alone.
//   - A candidate must Lstat as a real directory. A symlink is never followed
//     and never removed.
//   - This daemon's own workspace is never a candidate.
//   - A folder goes when its project no longer exists (per its record), or when
//     nothing has used it for sandboxHomeMaxAge.
//   - Removal renames first, then deletes, so two daemons reclaiming at once
//     cannot both act on one folder, and an interrupted run leaves a name the
//     next run recognises and finishes.

// sandboxHomeMaxAge is how long a sandbox home may go unused before it is
// reclaimed. Thirty days, matching maxTurnAge: this is a cache, and the cost of
// reclaiming one still wanted is a re-download.
const sandboxHomeMaxAge = 30 * 24 * time.Hour

var (
	sandboxTagName       = regexp.MustCompile(`^[0-9a-f]{16}$`)
	sandboxRecordName    = regexp.MustCompile(`^[0-9a-f]{16}\.json$`)
	sandboxReclaimingDir = regexp.MustCompile(`^[0-9a-f]{16}\.reclaiming-[0-9]+-[0-9]+$`)
	reclaimSeq           atomic.Int64
)

// maxSandboxRecordBytes bounds the read of a record. A record is one short
// JSON object; anything larger is not one this daemon wrote.
const maxSandboxRecordBytes = 64 * 1024

// sandboxHomeRecord says whose sandbox home a folder is.
//
// The folder's name is protocol.WorkspaceTag -- a one-way hash -- so without
// this nothing can tell which project a folder belongs to, or whether that
// project still exists.
type sandboxHomeRecord struct {
	Workspace string `json:"workspace"`
}

// sandboxHomeRoot is the directory every workspace's sandbox home lives under.
func sandboxHomeRoot() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "mochiii", "sandbox-home"), nil
}

// recordSandboxHomeUse writes <home>.json beside the home folder, naming the
// workspace it belongs to, and so refreshes the record's mtime as "last used".
//
// BESIDE THE FOLDER, NOT INSIDE IT. The folder is bound in as a sandboxed
// command's HOME, so a record inside it could be rewritten by the very build
// script whose cache it describes -- to keep its folder forever, or to point
// the "does the project still exist" check somewhere else. The sidecar is
// outside that bind.
//
// Written to a temporary name and renamed, so a reader never sees half of one.
func recordSandboxHomeUse(home, workspace string) error {
	data, err := json.Marshal(sandboxHomeRecord{Workspace: workspace})
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.json.tmp-%d-%d", home, os.Getpid(), reclaimSeq.Add(1))
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, home+".json"); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// sandboxReclaimReport is what one reclaim pass did.
type sandboxReclaimReport struct {
	Refused   string // non-empty if the root failed its checks; nothing was touched
	Reclaimed int
	Kept      int
	Bytes     int64
	Errors    []error
}

// reclaimSandboxHomes removes the sandbox homes under root that are due, and
// never keepTag's. now and maxAge are parameters so the rules can be tested
// without waiting a month.
func reclaimSandboxHomes(root, keepTag string, now time.Time, maxAge time.Duration) sandboxReclaimReport {
	var rep sandboxReclaimReport

	if !filepath.IsAbs(root) || filepath.Clean(root) != root ||
		filepath.Base(root) != "sandbox-home" || filepath.Base(filepath.Dir(root)) != "mochiii" {
		rep.Refused = fmt.Sprintf("%q is not a mochiii/sandbox-home directory; refusing to delete anything under it", root)
		return rep
	}

	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return rep // nothing has ever been created: nothing to do
	}
	if err != nil {
		rep.Errors = append(rep.Errors, err)
		return rep
	}

	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(root, name)

		switch {
		case sandboxReclaimingDir.MatchString(name):
			// Left by a pass that was interrupted between rename and delete.
			if isRealDir(path) {
				rep.Bytes += dirBytes(path)
				if err := os.RemoveAll(path); err != nil {
					rep.Errors = append(rep.Errors, err)
				}
			}

		case sandboxRecordName.MatchString(name):
			// A record whose folder is gone is removed; its folder's own
			// reclaim removes it otherwise.
			if _, err := os.Lstat(filepath.Join(root, name[:len(name)-len(".json")])); errors.Is(err, fs.ErrNotExist) {
				if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
					rep.Errors = append(rep.Errors, err)
				}
			}

		case sandboxTagName.MatchString(name):
			if name == keepTag || !isRealDir(path) || !sandboxHomeDue(path, now, maxAge) {
				rep.Kept++
				continue
			}
			bytes, err := removeSandboxHome(path, now, maxAge)
			if err != nil {
				rep.Errors = append(rep.Errors, err)
				continue
			}
			if bytes >= 0 {
				rep.Reclaimed++
				rep.Bytes += bytes
			} else {
				rep.Kept++ // became un-due between the check and the removal
			}
		}
	}
	return rep
}

// sandboxHomeDue reports whether the home at dir should be reclaimed at now.
func sandboxHomeDue(dir string, now time.Time, maxAge time.Duration) bool {
	info, err := os.Lstat(dir)
	if err != nil {
		return false
	}
	lastUsed := info.ModTime()

	if rec, err := os.Lstat(dir + ".json"); err == nil && rec.Mode().IsRegular() {
		lastUsed = rec.ModTime()
		if ws, ok := readSandboxRecord(dir + ".json"); ok {
			// Gone means ENOENT and nothing else: a permission error, or a
			// drive that is merely slow, says nothing about whether the
			// project still exists.
			if _, err := os.Stat(ws); errors.Is(err, fs.ErrNotExist) {
				return true
			}
		}
	}
	// A future mtime counts as now, which errs toward keeping.
	if lastUsed.After(now) {
		lastUsed = now
	}
	return now.Sub(lastUsed) >= maxAge
}

func readSandboxRecord(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxSandboxRecordBytes))
	if err != nil {
		return "", false
	}
	var rec sandboxHomeRecord
	if json.Unmarshal(data, &rec) != nil || !filepath.IsAbs(rec.Workspace) {
		return "", false
	}
	return rec.Workspace, true
}

// removeSandboxHome re-checks that dir is still due, renames it out of the way
// and deletes it. It returns the bytes freed, or -1 if dir stopped being due
// before it could be moved.
func removeSandboxHome(dir string, now time.Time, maxAge time.Duration) (int64, error) {
	// Re-checked immediately before acting, which narrows the window in which
	// another daemon starts using the folder again to a few instructions.
	if !isRealDir(dir) || !sandboxHomeDue(dir, now, maxAge) {
		return -1, nil
	}
	moved := fmt.Sprintf("%s.reclaiming-%d-%d", dir, os.Getpid(), reclaimSeq.Add(1))
	if err := os.Rename(dir, moved); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return -1, nil // another daemon got there first
		}
		return 0, err
	}
	bytes := dirBytes(moved)
	if err := os.RemoveAll(moved); err != nil {
		return 0, err
	}
	if err := os.Remove(dir + ".json"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return bytes, err
	}
	return bytes, nil
}

// isRealDir reports whether path is a directory itself -- not a symlink to one.
func isRealDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

// dirBytes sums the regular files under root. WalkDir does not follow links.
func dirBytes(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// reclaimSandboxHomes runs one reclaim pass for this daemon, from startup, in
// the background. It never fails the daemon: this is housekeeping for a cache.
func (s *Server) reclaimSandboxHomes() {
	root, err := sandboxHomeRoot()
	if err != nil {
		return
	}
	rep := reclaimSandboxHomes(root, protocol.WorkspaceTag(s.workspace), time.Now(), sandboxHomeMaxAge)
	switch {
	case rep.Refused != "":
		s.logger.Printf("sandbox-home: %s", rep.Refused)
	case rep.Reclaimed > 0:
		s.logger.Printf("sandbox-home: reclaimed %d folder(s), %s (unused 30+ days, or their project is gone); kept %d",
			rep.Reclaimed, humanBytes(rep.Bytes), rep.Kept)
	}
	for _, err := range rep.Errors {
		s.logger.Printf("sandbox-home: %v", err)
	}
}

// humanBytes renders a size a reader can use. Most reclaimed homes are empty or
// nearly so -- 1,107 of the 1,712 first measured were -- and "0.0 MB" for a
// few kilobytes is true and tells the reader nothing.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
