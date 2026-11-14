package main

import (
	"context"
	"os"
	"regexp"
	"time"
)

// WINDOWS THAT WERE CLOSED LEAVE A CHAT BEHIND, AND IT IS SWEPT.
//
// Each terminal window has a chat of its own (chatscope.go), keyed by an id the
// terminal makes when it starts and never writes down. The daemon is never told a
// window has closed -- a connection lasts one request -- so the chat of a window
// that was closed, killed, or just quit with its chat saved stays in memory.db,
// reachable by nobody: a relaunch is a new window with a new id.
//
// WHAT STOPS THAT FROM GROWING WITHOUT BOUND is a sweep, once an hour (and on the
// first connection a daemon sees): a window whose newest turn is older than
// idleWindowTTL loses its turns and its link file. Seven days, because the cost of
// being wrong is a live chat disappearing under a window that is still open, and a
// window left open for a week with nothing said in it is one nobody is using.
//
// A WINDOW THIS DAEMON HAS HEARD FROM IS NOT SWEPT, whatever its newest turn's age.
// /resume gives a window a chat whose turns keep the time they were first said
// (memory.go, ReplaceWorkspace), so a window that resumed a month-old chat and has
// asked nothing yet would otherwise be idle by the turns' clock while it is the one
// being used. windowSeen is the daemon's own memory of who connected, kept for the
// life of the process; it is the guard for that case, not a record.
//
// The shared chats ("" window: the editor's, the isolated surface's, and the
// terminal's default) are never swept. They are bounded by their own cap
// (maxTurnsPerWorkspace) and are not leaked one per launch.

const (
	idleWindowTTL = 7 * 24 * time.Hour
	sweepEvery    = time.Hour
)

// windowLinkPattern is a window's link file name (chatArchive.linkFile).
var windowLinkPattern = regexp.MustCompile(`^current-([0-9a-f]{8,32})\.json$`)

// noteWindowAndSweep records that this connection's window is alive and, at most
// once an hour, sweeps the idle ones. Called from serveConn once the handshake is
// accepted; cheap when there is nothing to do.
func (s *Server) noteWindowAndSweep(ctx context.Context) {
	if w := windowFrom(ctx); w != "" {
		s.windowSeen.Store(w, time.Now().UnixNano())
	}
	if s.memory == nil {
		return
	}
	now := time.Now()
	last := s.lastSweep.Load()
	if now.UnixNano()-last < int64(sweepEvery) || !s.lastSweep.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	s.sweepIdleWindows(now, idleWindowTTL)
}

// seenSince reports whether this daemon heard from window after cutoff.
func (s *Server) seenSince(window string, cutoff time.Time) bool {
	v, ok := s.windowSeen.Load(window)
	if !ok {
		return false
	}
	at, _ := v.(int64)
	return at > cutoff.UnixNano()
}

// sweepIdleWindows removes the chat of every terminal window idle for longer than
// ttl, and the link files of windows that have no chat left. Failures are logged
// and retried at the next sweep: nothing here is worth failing a connection over.
func (s *Server) sweepIdleWindows(now time.Time, ttl time.Duration) {
	cutoff := now.Add(-ttl)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	removed, err := s.memory.PruneIdleWindows(ctx, s.workspace, cutoff, func(w string) bool { return s.seenSince(w, cutoff) })
	if err != nil {
		s.logger.Printf("sweeping idle terminal windows: %v", err)
		return
	}
	if removed > 0 {
		s.logger.Printf("swept the chats of %d idle terminal window(s)", removed)
	}
	live, err := s.memory.liveWindows(ctx, s.workspace)
	if err != nil {
		s.logger.Printf("listing terminal windows: %v", err)
		return
	}
	archive, err := s.archiveForScope(chatScope{Workspace: s.workspace, Surface: SurfaceTerminal})
	if err != nil {
		return
	}
	archive.sweepLinks(live, func(w string) bool { return s.seenSince(w, cutoff) })
	s.windowSeen.Range(func(k, v any) bool {
		if at, _ := v.(int64); at <= cutoff.UnixNano() {
			s.windowSeen.Delete(k)
		}
		return true
	})
}

// PruneIdleWindows deletes the turns of every terminal window of workspace whose
// newest turn is older than cutoff, except those keep() protects, and returns how
// many windows lost their chat. The shared chats (window "") are not touched.
func (s *MemoryStore) PruneIdleWindows(ctx context.Context, workspace string, cutoff time.Time, keep func(window string) bool) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT window_id FROM turns
		   WHERE workspace = ? AND surface = ? AND window_id != ''
		   GROUP BY window_id HAVING MAX(created_at) < ?`,
		workspace, string(SurfaceTerminal), cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	var idle []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			_ = rows.Close()
			return 0, err
		}
		idle = append(idle, w)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close() // read to the end; the single connection is needed for the deletes below

	removed := 0
	for _, w := range idle {
		if keep != nil && keep(w) {
			continue
		}
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM turns WHERE workspace = ? AND surface = ? AND window_id = ?`,
			workspace, string(SurfaceTerminal), w); err != nil {
			return removed, err
		}
		removed++
	}
	if removed > 0 {
		return removed, reclaimFreePages(ctx, s.db)
	}
	return 0, nil
}

// liveWindows is the set of terminal windows of workspace that have a chat.
func (s *MemoryStore) liveWindows(ctx context.Context, workspace string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT window_id FROM turns WHERE workspace = ? AND surface = ? AND window_id != ''`,
		workspace, string(SurfaceTerminal))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // read-only; rows.Err below reports failures
	live := map[string]bool{}
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, err
		}
		live[w] = true
	}
	return live, rows.Err()
}

// sweepLinks removes the link file of every window that has no chat (not in live)
// and that keep() does not protect. Returns how many it removed.
func (a *chatArchive) sweepLinks(live map[string]bool, keep func(window string) bool) int {
	if ok, err := a.dirUsable(); err != nil || !ok {
		return 0
	}
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		m := windowLinkPattern.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		if live[m[1]] || (keep != nil && keep(m[1])) {
			continue
		}
		if os.Remove(a.dir+string(os.PathSeparator)+e.Name()) == nil {
			removed++
		}
	}
	return removed
}
