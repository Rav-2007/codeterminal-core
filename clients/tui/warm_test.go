package tui

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// countingDaemon answers handshakes and counts them.
func countingDaemon(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	addr := testAddress(t)
	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	data, _ := json.Marshal(protocol.NewLockFile(addr, os.Getpid()))
	lockPath := filepath.Join(t.TempDir(), "daemon.lock")
	if err := os.WriteFile(lockPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				var hs protocol.HandshakeRequest
				if json.NewDecoder(conn).Decode(&hs) != nil {
					return
				}
				n.Add(1)
				_ = json.NewEncoder(conn).Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true})
			}(conn)
		}
	}()
	t.Cleanup(setLockPathForTest(t, lockPath))
	return &n
}

func resetWarm(t *testing.T) {
	t.Helper()
	saved := lastWarm.Load()
	lastWarm.Store(0)
	t.Cleanup(func() { lastWarm.Store(saved) })
}

func TestAWarmUpHandshakesAtMostOnceEveryTwentySeconds(t *testing.T) {
	resetWarm(t)
	handshakes := countingDaemon(t)
	now := time.Now()

	w := warmDaemon(now)
	if w == nil {
		t.Fatal("the first warm-up was not started")
	}
	w()
	if got := handshakes.Load(); got != 1 {
		t.Fatalf("the warm-up made %d handshakes, want 1", got)
	}
	if warmDaemon(now.Add(warmEvery-time.Second)) != nil {
		t.Error("a second warm-up started within warmEvery of the first")
	}
	if warmDaemon(now.Add(warmEvery+time.Second)) == nil {
		t.Error("no warm-up after warmEvery had passed")
	}
}

func typeKey(m chatModel, r rune) chatModel {
	m.input, _ = m.updateInput(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return m
}

// The first key of a question warms; the keys after it, and a client built
// alone, do not.
func TestTheFirstKeyOfAQuestionWarmsTheProvider(t *testing.T) {
	resetWarm(t)
	oldOne := InOneBinary
	t.Cleanup(func() { InOneBinary = oldOne })

	InOneBinary = false
	m := newChatModel("test-client", "/workspace", "/workspace", nil)
	m.input.Focus()
	m = typeKey(m, 'h')
	if lastWarm.Load() != 0 {
		t.Fatal("a client built alone started a warm-up")
	}

	InOneBinary = true
	m = newChatModel("test-client", "/workspace", "/workspace", nil)
	m.input.Focus()
	m = typeKey(m, 'h')
	first := lastWarm.Load()
	if first == 0 {
		t.Fatalf("the first key of a question (prompt %q) started no warm-up", m.input.Value())
	}
	lastWarm.Store(1) // as if long ago: only the empty-to-typed key may start one
	m = typeKey(m, 'i')
	if lastWarm.Load() != 1 {
		t.Error("a key after the first started a warm-up")
	}
}
