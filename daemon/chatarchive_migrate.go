package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// THE SAVED-CHAT FOLDER, BEFORE AND AFTER THE SPLIT (chatscope.go).
//
// Before schema v6 a workspace's saved chats sat directly under
// history/<key>/, with one current.json beside them. They now sit under
// history/<key>/<surface>/. What was saved before the split was saved from a
// shared chat, so nobody can say whose it is, and the owner's decision
// (2026-11-14) is that nothing anyone saved disappears from either client:
//
//   - every legacy saved chat is COPIED into both the terminal's and the
//     editor's folders;
//   - the legacy current.json -- which saved chat the live chat is -- goes to the
//     editor's folder only, because the live chat went there (memory.go,
//     migrateToScopedTurns) and the link describes the live chat.
//
// IDEMPOTENT BY CONSTRUCTION, with no marker file. What is legacy is whatever
// still sits directly under history/<key>/ as a saved chat, and the last step
// removes it, so: copies are atomic (a temp file, then a rename) and skip a name
// that is already there; originals are removed only after every copy; and a run
// that finds nothing does nothing. A crash at any point leaves a state the next
// run finishes. It is also self-healing: a saved chat an OLDER daemon writes
// into the old layout later (the two can share a state folder) is moved the next
// time this daemon starts. The design note proposed a marker; there is none,
// because a marker is a second thing that can disagree with the folder.
//
// Disk use for saved chats roughly doubles at worst, and each folder is within
// its own bounds, because it received exactly the set the one folder held.

// legacyArchiveSurfaces are the folders a legacy saved chat is copied into.
var legacyArchiveSurfaces = []Surface{SurfaceTerminal, SurfaceVSCode}

// archiveFor is the saved-chat folder for this connection's chat, after making
// sure the folder layout is the current one (once per daemon process).
func (s *Server) archiveFor(ctx context.Context) (*chatArchive, error) {
	return s.archiveForScope(s.scope(ctx))
}

func (s *Server) archiveForScope(sc chatScope) (*chatArchive, error) {
	root, err := historyRoot()
	if err != nil {
		return nil, err
	}
	s.archiveOnce.Do(func() {
		if err := migrateLegacyArchives(root, s.workspace); err != nil {
			// Not fatal: the new folders work without it, and the next start
			// tries again. The user's saved chats are still where they were.
			s.logger.Printf("history: moving saved chats into per-client folders: %v", err)
		}
	})
	return newChatArchive(root, sc), nil
}

// legacyFiles lists what sits directly under keyDir as a saved chat or a link:
// regular files only (a symlink is never followed), named like an archive or
// current.json. A missing folder is nothing to migrate.
func legacyFiles(keyDir string) (archives []string, link bool, stale []string, err error) {
	entries, err := os.ReadDir(keyDir)
	if os.IsNotExist(err) {
		return nil, false, nil, nil
	}
	if err != nil {
		return nil, false, nil, err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		switch {
		case name == currentLinkName:
			link = true
		case strings.HasSuffix(name, archiveSuffix) &&
			archiveIDPattern.MatchString(strings.TrimSuffix(name, archiveSuffix)):
			archives = append(archives, name)
		case strings.HasPrefix(name, ".tmp-"):
			stale = append(stale, name) // a save that died part-way, from the old layout
		}
	}
	return archives, link, stale, nil
}

// migrateLegacyArchives moves one workspace's pre-split saved chats into the
// per-surface folders. See the note above for what, and why it is safe to run
// at any time and any number of times.
func migrateLegacyArchives(root, workspace string) error {
	keyDir := filepath.Join(root, workspaceKey(workspace))
	archives, link, stale, err := legacyFiles(keyDir)
	if err != nil {
		return err
	}
	if len(archives) == 0 && !link && len(stale) == 0 {
		return nil
	}

	// The folders are made, and refused if a symlink, by the archive's own
	// ensureDir -- the same check every save makes.
	dirs := map[Surface]*chatArchive{}
	for _, surface := range legacyArchiveSurfaces {
		a := newChatArchive(root, chatScope{Workspace: workspace, Surface: surface})
		if err := a.ensureDir(); err != nil {
			return err
		}
		dirs[surface] = a
	}

	for _, name := range archives {
		for _, surface := range legacyArchiveSurfaces {
			if err := copyIfAbsent(filepath.Join(keyDir, name), filepath.Join(dirs[surface].dir, name)); err != nil {
				return err
			}
		}
	}
	if link {
		if err := copyIfAbsent(filepath.Join(keyDir, currentLinkName),
			filepath.Join(dirs[SurfaceVSCode].dir, currentLinkName)); err != nil {
			return err
		}
	}

	// Originals last, and only now: every copy above has been renamed into place.
	// A removal that fails leaves a file the next run copies (skipping) and tries
	// to remove again.
	var firstErr error
	remove := func(name string) {
		if err := os.Remove(filepath.Join(keyDir, name)); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	for _, name := range archives {
		remove(name)
	}
	if link {
		remove(currentLinkName)
	}
	for _, name := range stale {
		remove(name)
	}
	return firstErr
}

// copyIfAbsent copies the regular file src to dst atomically (a temp file in
// dst's folder, then a rename), unless dst already exists. src is read through
// a bounded reader and must be a regular file: a planted symlink is refused,
// not followed.
func copyIfAbsent(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; refusing to copy it", src)
	}
	if fi.Size() > maxSavedChatBytes {
		return fmt.Errorf("%s is larger than a saved chat can be; refusing to copy it", src)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }() // read-only
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, io.LimitReader(in, maxSavedChatBytes+1)); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName) // best effort; pruning removes stale temp files
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
