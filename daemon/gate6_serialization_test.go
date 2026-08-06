package main

// Regression cover for FAIL-3 Gate 6 (Apply/Undo/prune data-integrity). Each
// test here is the audit's (commit f97dc09) exact concurrent-repro shape,
// re-run at the same loop count, now asserting the failure rate is ZERO after
// the per-workspace serialization lock (see Server.lockWorkspace). The audit's
// measured pre-fix rates are noted per test for the record. These drive the
// real Server.Serve/handleApplyEdit/handleUndo over real unix sockets, exactly
// as production.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codeterminal/protocol"
)

// g6Server runs srv.Serve on a fresh unix socket for workspace ws.
func g6Server(t *testing.T, ws string) protocol.Address {
	t.Helper()
	srv := &Server{logger: discardLogger(), workspace: ws}
	addr := testAddress(t)
	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	return addr
}

// g6Conn dials + handshakes, returning a live connection ready for one request.
func g6Conn(t *testing.T, addr protocol.Address) (net.Conn, *json.Encoder, *json.Decoder) {
	t.Helper()
	c, err := protocol.Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	enc := json.NewEncoder(c)
	dec := json.NewDecoder(c)
	if err := enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "g6"}); err != nil {
		t.Fatalf("handshake send: %v", err)
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil {
		t.Fatalf("handshake recv: %v", err)
	}
	if !hs.Ok {
		t.Fatalf("handshake rejected: %+v", hs)
	}
	c.SetDeadline(time.Now().Add(20 * time.Second))
	return c, enc, dec
}

func g6Seed(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// g6Apply runs one ApplyEditRequest to completion over its own connection.
func g6Apply(t *testing.T, addr protocol.Address, req protocol.ApplyEditRequest) protocol.ApplyEditResponse {
	t.Helper()
	c, e, d := g6Conn(t, addr)
	defer c.Close()
	if err := e.Encode(req); err != nil {
		t.Fatalf("apply send: %v", err)
	}
	var resp protocol.ApplyEditResponse
	if err := d.Decode(&resp); err != nil {
		t.Fatalf("apply recv: %v", err)
	}
	return resp
}

// g6FireTwo pre-handshakes two connections and releases both requests via a
// shared start-gun for tight overlap, returning both raw response bytes.
func g6FireTwo(t *testing.T, addr protocol.Address, reqA, reqB any) (json.RawMessage, json.RawMessage) {
	t.Helper()
	cA, eA, dA := g6Conn(t, addr)
	cB, eB, dB := g6Conn(t, addr)
	defer cA.Close()
	defer cB.Close()
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var rawA, rawB json.RawMessage
	fire := func(enc *json.Encoder, dec *json.Decoder, req any, out *json.RawMessage) {
		defer wg.Done()
		<-start
		_ = enc.Encode(req)
		_ = dec.Decode(out)
	}
	go fire(eA, dA, reqA, &rawA)
	go fire(eB, dB, reqB, &rawB)
	close(start)
	wg.Wait()
	return rawA, rawB
}

// --- Gate 2: Apply/Apply concurrent write (audit: 100% lost update) ---------

func TestGate6_ApplyApply_NoLostUpdate(t *testing.T) {
	const iters = 60
	ws := t.TempDir()
	target := filepath.Join(ws, "conf.txt")
	addr := g6Server(t, ws)

	losses := 0
	for i := 0; i < iters; i++ {
		g6Seed(t, target, "alpha=0\nbeta=0\n")
		reqA := protocol.ApplyEditRequest{ProtocolVersion: protocol.ProtocolVersion,
			Edit: protocol.EditBlockWire{FilePath: "conf.txt", Search: "alpha=0", Replace: "alpha=1"}}
		reqB := protocol.ApplyEditRequest{ProtocolVersion: protocol.ProtocolVersion,
			Edit: protocol.EditBlockWire{FilePath: "conf.txt", Search: "beta=0", Replace: "beta=1"}}
		g6FireTwo(t, addr, reqA, reqB)

		final, _ := os.ReadFile(target)
		if string(final) != "alpha=1\nbeta=1\n" {
			losses++
			t.Errorf("iter %d: expected both edits applied, got %q (an edit was lost)", i, string(final))
		}
	}
	if losses != 0 {
		t.Fatalf("Apply/Apply lost-update rate = %d/%d (audit pre-fix: 100%%); want 0", losses, iters)
	}
	t.Logf("Apply/Apply over %d iters: 0 lost updates (audit pre-fix: 60/60)", iters)
}

// --- Gate 2b: backup-session collapse (audit: ~68% shared session dir) ------

func TestGate6_NoBackupSessionCollapse(t *testing.T) {
	const iters = 60
	ws := t.TempDir()
	target := filepath.Join(ws, "conf.txt")
	backupsRoot := filepath.Join(ws, ".codeterminal", "backups")
	addr := g6Server(t, ws)

	shared := 0
	for i := 0; i < iters; i++ {
		os.RemoveAll(backupsRoot)
		g6Seed(t, target, "alpha=0\nbeta=0\n")
		reqA := protocol.ApplyEditRequest{ProtocolVersion: protocol.ProtocolVersion,
			Edit: protocol.EditBlockWire{FilePath: "conf.txt", Search: "alpha=0", Replace: "alpha=1"}}
		reqB := protocol.ApplyEditRequest{ProtocolVersion: protocol.ProtocolVersion,
			Edit: protocol.EditBlockWire{FilePath: "conf.txt", Search: "beta=0", Replace: "beta=1"}}
		rawA, rawB := g6FireTwo(t, addr, reqA, reqB)
		var ra, rb protocol.ApplyEditResponse
		_ = json.Unmarshal(rawA, &ra)
		_ = json.Unmarshal(rawB, &rb)
		if !ra.Applied || !rb.Applied {
			t.Fatalf("iter %d: both applies should succeed, got %+v / %+v", i, ra, rb)
		}
		if ra.BackupDir == rb.BackupDir {
			shared++
			t.Errorf("iter %d: two applies collapsed into one backup session %q", i, ra.BackupDir)
		}
	}
	if shared != 0 {
		t.Fatalf("backup-session collapse rate = %d/%d (audit pre-fix: ~68%%); want 0", shared, iters)
	}
	t.Logf("Backup-session collapse over %d iters: 0 shared (audit pre-fix: ~41/60)", iters)
}

// --- Gate 3: Apply/Undo guard defeat (audit: ~98%) --------------------------

func TestGate6_ApplyUndo_GuardHolds(t *testing.T) {
	const iters = 60
	ws := t.TempDir()
	target := filepath.Join(ws, "conf.txt")
	addr := g6Server(t, ws)

	defeats := 0
	for i := 0; i < iters; i++ {
		g6Seed(t, target, "v=original\n")
		seed := g6Apply(t, addr, protocol.ApplyEditRequest{ProtocolVersion: protocol.ProtocolVersion,
			Edit: protocol.EditBlockWire{FilePath: "conf.txt", Search: "v=original", Replace: "v=one"}})
		if !seed.Applied {
			t.Fatalf("iter %d: seed apply failed: %+v", i, seed)
		}
		applyReq := protocol.ApplyEditRequest{ProtocolVersion: protocol.ProtocolVersion,
			Edit: protocol.EditBlockWire{FilePath: "conf.txt", Search: "v=one", Replace: "v=two"}}
		undoReq := protocol.UndoRequest{ProtocolVersion: protocol.ProtocolVersion, Undo: true, BackupSessionDir: seed.BackupDir}
		_, rawU := g6FireTwo(t, addr, applyReq, undoReq)
		var ur protocol.UndoResponse
		_ = json.Unmarshal(rawU, &ur)

		final, _ := os.ReadFile(target)
		// The two consistent serialized outcomes: undo-first (file=v=original,
		// undo restored 1, the later apply's search no longer matches) or
		// apply-first (file=v=two, undo guarded it, restored 0). The guard is
		// DEFEATED iff undo claims a restore while the concurrent apply's content
		// is what actually survived.
		if ur.Restored >= 1 && string(final) == "v=two\n" {
			defeats++
			t.Errorf("iter %d: undo reported Restored=%d but file is the concurrent apply's %q (guard defeated)", i, ur.Restored, string(final))
		}
		if s := string(final); s != "v=original\n" && s != "v=two\n" {
			t.Errorf("iter %d: file left in an unexpected/torn state %q", i, s)
		}
	}
	if defeats != 0 {
		t.Fatalf("undo-guard-defeat rate = %d/%d (audit pre-fix: ~98%%); want 0", defeats, iters)
	}
	t.Logf("Apply/Undo over %d iters: 0 guard defeats (audit pre-fix: ~59/60)", iters)
}

// --- Gate 4: Undo/Undo double-restore (audit: ~88%) -------------------------

func TestGate6_UndoUndo_NoDoubleRestore(t *testing.T) {
	const iters = 60
	ws := t.TempDir()
	target := filepath.Join(ws, "conf.txt")
	addr := g6Server(t, ws)

	doubles := 0
	for i := 0; i < iters; i++ {
		g6Seed(t, target, "v=original\n")
		seed := g6Apply(t, addr, protocol.ApplyEditRequest{ProtocolVersion: protocol.ProtocolVersion,
			Edit: protocol.EditBlockWire{FilePath: "conf.txt", Search: "v=original", Replace: "v=one"}})
		if !seed.Applied {
			t.Fatalf("iter %d: seed apply failed: %+v", i, seed)
		}
		undoReq := protocol.UndoRequest{ProtocolVersion: protocol.ProtocolVersion, Undo: true, BackupSessionDir: seed.BackupDir}
		rawX, rawY := g6FireTwo(t, addr, undoReq, undoReq)
		var rx, ry protocol.UndoResponse
		_ = json.Unmarshal(rawX, &rx)
		_ = json.Unmarshal(rawY, &ry)
		if rx.Error != "" || ry.Error != "" {
			t.Errorf("iter %d: undo error(s): %q / %q", i, rx.Error, ry.Error)
		}
		if total := rx.Restored + ry.Restored; total != 1 {
			doubles++
			t.Errorf("iter %d: expected exactly one restore across two undos, got %d (%d + %d)", i, total, rx.Restored, ry.Restored)
		}
		if final, _ := os.ReadFile(target); string(final) != "v=original\n" {
			t.Errorf("iter %d: file should be restored to original, got %q", i, string(final))
		}
	}
	if doubles != 0 {
		t.Fatalf("double-restore rate = %d/%d (audit pre-fix: ~88%%); want 0", doubles, iters)
	}
	t.Logf("Undo/Undo over %d iters: 0 double-restores (audit pre-fix: ~53/60)", iters)
}

// --- Gate 4b: prune-vs-undo atomicity (audit: reproduced) -------------------
//
// A burst of applies (each prunes to keep=5) races an undo of an older session.
// The lock makes prune and undo mutually exclusive, so the undo is ATOMIC: it
// either fully restores (validated session still present) or cleanly refuses
// ("not found", the session was legitimately pruned before the undo took the
// lock) — never a partial restore, and never a mid-walk "reading backup
// session" corruption error from a RemoveAll racing runUndoSession's WalkDir.
func TestGate6_PruneVsUndo_Atomic(t *testing.T) {
	const iters = 40
	for i := 0; i < iters; i++ {
		ws := t.TempDir()
		target := filepath.Join(ws, "conf.txt")
		addr := g6Server(t, ws)
		g6Seed(t, target, "n=0\n")

		// Build 8 sessions; remember an older, prune-eligible one.
		var oldest string
		cur := 0
		for k := 0; k < 8; k++ {
			next := cur + 1
			r := g6Apply(t, addr, protocol.ApplyEditRequest{ProtocolVersion: protocol.ProtocolVersion,
				Edit: protocol.EditBlockWire{FilePath: "conf.txt", Search: fmt.Sprintf("n=%d", cur), Replace: fmt.Sprintf("n=%d", next)}})
			if r.Applied && k == 1 {
				oldest = r.BackupDir
			}
			cur = next
		}

		undoReq := protocol.UndoRequest{ProtocolVersion: protocol.ProtocolVersion, Undo: true, BackupSessionDir: oldest}
		var wg sync.WaitGroup
		start := make(chan struct{})
		var undoResp protocol.UndoResponse
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c, e, d := g6Conn(t, addr)
			defer c.Close()
			_ = e.Encode(undoReq)
			_ = d.Decode(&undoResp)
		}()
		for k := 0; k < 8; k++ {
			wg.Add(1)
			next := cur + 1
			req := protocol.ApplyEditRequest{ProtocolVersion: protocol.ProtocolVersion,
				Edit: protocol.EditBlockWire{FilePath: "conf.txt", Search: fmt.Sprintf("n=%d", cur), Replace: fmt.Sprintf("n=%d", next)}}
			cur = next
			go func(req protocol.ApplyEditRequest) { defer wg.Done(); <-start; _ = g6Apply(t, addr, req) }(req)
		}
		close(start)
		wg.Wait()

		// Atomicity invariant: no partial-restore-then-error, and no mid-walk
		// corruption error. A clean not-found refusal (restored 0) is allowed.
		if undoResp.Error != "" {
			if undoResp.Restored != 0 {
				t.Fatalf("iter %d: partial restore (%d) paired with error %q — undo was not atomic", i, undoResp.Restored, undoResp.Error)
			}
			if strings.Contains(undoResp.Error, "reading backup session") {
				t.Fatalf("iter %d: mid-walk corruption error from prune racing undo: %q", i, undoResp.Error)
			}
		}
	}
	t.Logf("Prune-vs-undo over %d iters: undo always atomic (no partial restore, no mid-walk error)", iters)
}

// --- Lock design properties: keying + no cross-workspace interference -------

func TestGate6_CrossWorkspace_NoInterference(t *testing.T) {
	srv := &Server{logger: discardLogger()}
	releaseA := srv.lockWorkspace("/ws/a")

	// A DIFFERENT workspace's lock must not block on A's lock.
	done := make(chan struct{})
	go func() { rel := srv.lockWorkspace("/ws/b"); rel(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		releaseA()
		t.Fatal("lockWorkspace(/ws/b) blocked while /ws/a held — lock is not per-workspace")
	}

	// The SAME workspace's lock must serialize: a second acquire blocks until
	// the first is released.
	second := make(chan func(), 1)
	go func() { second <- srv.lockWorkspace("/ws/a") }()
	select {
	case rel := <-second:
		rel()
		releaseA()
		t.Fatal("second lockWorkspace(/ws/a) acquired while first still held — not serialized")
	case <-time.After(300 * time.Millisecond):
		// expected: still blocked on A
	}
	releaseA()
	select {
	case rel := <-second:
		rel()
	case <-time.After(2 * time.Second):
		t.Fatal("second lockWorkspace(/ws/a) never acquired after release — stuck")
	}
}

// TestGate6_DifferentWorkspacesRunConcurrently proves end to end that two real
// daemons on two different workspace roots apply concurrently without blocking
// each other, under a timeout that fails loudly on any cross-workspace stall.
func TestGate6_DifferentWorkspacesRunConcurrently(t *testing.T) {
	mk := func() (protocol.Address, string) {
		ws := t.TempDir()
		target := filepath.Join(ws, "f.txt")
		g6Seed(t, target, "x=0\n")
		return g6Server(t, ws), target
	}
	addrA, tgtA := mk()
	addrB, tgtB := mk()

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for _, p := range []protocol.Address{addrA, addrB} {
			wg.Add(1)
			go func(p protocol.Address) {
				defer wg.Done()
				for k := 0; k < 30; k++ {
					_ = g6Apply(t, p, protocol.ApplyEditRequest{ProtocolVersion: protocol.ProtocolVersion,
						Edit: protocol.EditBlockWire{FilePath: "f.txt", Search: fmt.Sprintf("x=%d", k), Replace: fmt.Sprintf("x=%d", k+1)}})
				}
			}(p)
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("two different workspaces stalled each other")
	}
	a, _ := os.ReadFile(tgtA)
	b, _ := os.ReadFile(tgtB)
	if string(a) != "x=30\n" || string(b) != "x=30\n" {
		t.Fatalf("expected both workspaces fully applied, got A=%q B=%q", string(a), string(b))
	}
}

// --- Deadlock / stress: overlapping Apply/Undo/prune on one workspace -------

func TestGate6_Stress_NoDeadlock(t *testing.T) {
	ws := t.TempDir()
	addr := g6Server(t, ws)
	// Each worker owns its own file (deterministic search text) but shares the
	// workspace lock + backups root, so applies, prunes, and undos all contend
	// on the single per-workspace mutex — the real deadlock surface.
	const workers = 24
	for w := 0; w < workers; w++ {
		g6Seed(t, filepath.Join(ws, fmt.Sprintf("f%d.txt", w)), "x=0\n")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				file := fmt.Sprintf("f%d.txt", w)
				for k := 0; k < 4; k++ {
					r := g6Apply(t, addr, protocol.ApplyEditRequest{ProtocolVersion: protocol.ProtocolVersion,
						Edit: protocol.EditBlockWire{FilePath: file, Search: fmt.Sprintf("x=%d", k), Replace: fmt.Sprintf("x=%d", k+1)}})
					if r.Applied {
						// Undo our own session; interleaves restores with others'
						// applies and prunes, all under the one workspace lock.
						c, e, d := g6Conn(t, addr)
						_ = e.Encode(protocol.UndoRequest{ProtocolVersion: protocol.ProtocolVersion, Undo: true, BackupSessionDir: r.BackupDir})
						var ur protocol.UndoResponse
						_ = d.Decode(&ur)
						c.Close()
						// Re-seed our file if the undo reverted it, so the next
						// apply's search text exists again.
						g6Seed(t, filepath.Join(ws, file), fmt.Sprintf("x=%d\n", k))
					}
				}
			}(w)
		}
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("overlapping Apply/Undo/prune load DEADLOCKED / hung on the workspace lock")
	}

	// Daemon still responsive after the barrage.
	g6Seed(t, filepath.Join(ws, "after.txt"), "y=0\n")
	r := g6Apply(t, addr, protocol.ApplyEditRequest{ProtocolVersion: protocol.ProtocolVersion,
		Edit: protocol.EditBlockWire{FilePath: "after.txt", Search: "y=0", Replace: "y=1"}})
	if !r.Applied {
		t.Fatalf("daemon unresponsive after stress: %+v", r)
	}
}
