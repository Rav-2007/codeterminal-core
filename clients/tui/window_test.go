package tui

import (
	"regexp"
	"testing"
)

// EACH TERMINAL WINDOW HAS ITS OWN CHAT: the daemon can only tell windows apart
// by the id the handshake carries, so every connection this process makes must
// carry the same one (window.go).

func (f *fakeChatDaemon) windowsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.windows...)
}

// Neuter check: drop `WindowID: daemonWindowID` from the handshake in
// connectToDaemon -- every connection arrives with no window, and two terminals
// share a chat again.
func TestWindowID_RidesOnEveryConnection(t *testing.T) {
	f := serveChatDaemon(t, nil)
	old := daemonWindowID
	t.Cleanup(func() { setDaemonWindowID(old) })
	setDaemonWindowID("00ff00ff00ff00ff")

	for i := 0; i < 3; i++ {
		sess, err := connectToDaemon("test-client")
		if err != nil {
			t.Fatalf("connect %d: %v", i, err)
		}
		_ = sess.Close()
	}
	got := f.windowsSeen()
	if len(got) != 3 {
		t.Fatalf("%d handshakes arrived, want 3", len(got))
	}
	for i, w := range got {
		if w != "00ff00ff00ff00ff" {
			t.Errorf("handshake %d carried window %q, want this process's", i, w)
		}
	}
}

// A process that never set one -- the one-shot path, and every test -- sends none,
// so it shares the terminal's default chat exactly as before.
func TestWindowID_IsEmptyUntilRunChatSetsIt(t *testing.T) {
	f := serveChatDaemon(t, nil)
	old := daemonWindowID
	t.Cleanup(func() { setDaemonWindowID(old) })
	setDaemonWindowID("")
	sess, err := connectToDaemon("test-client")
	if err != nil {
		t.Fatal(err)
	}
	_ = sess.Close()
	if got := f.windowsSeen(); len(got) != 1 || got[0] != "" {
		t.Errorf("handshake windows = %q, want one empty id", got)
	}
}

// The form is the one the daemon accepts (8 to 32 lowercase hex digits), and two
// windows do not get the same id.
func TestWindowID_IsRandomHexTheDaemonAccepts(t *testing.T) {
	ok := regexp.MustCompile(`^[0-9a-f]{8,32}$`)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id := newWindowID()
		if !ok.MatchString(id) {
			t.Fatalf("newWindowID() = %q, not 8 to 32 lowercase hex digits", id)
		}
		if seen[id] {
			t.Fatalf("newWindowID() repeated %q: two windows would share a chat", id)
		}
		seen[id] = true
	}
}
