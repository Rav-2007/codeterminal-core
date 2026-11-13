//go:build trace

package trace

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The id a client names its turn with is untrusted input from the socket. The
// only test it passes is ValidClientID, and everything it fails is dropped.

var hex8 = regexp.MustCompile(`^[0-9a-f]{8}$`)

func TestOnlyEightLowerCaseHexDigitsAreAClientID(t *testing.T) {
	for _, ok := range []string{"a1b2c3d4", "00000000", "ffffffff", "deadbeef"} {
		if !ValidClientID(ok) {
			t.Errorf("ValidClientID(%q) = false", ok)
		}
	}
	for _, bad := range []string{
		"", "a", "a1b2c3d", "a1b2c3d4e", "A1B2C3D4", "a1b2c3dG", "a1b2c3d ", " a1b2c3d4", "a1b2c3d4\n", "a1b2\x00c3d4",
		"a1b2c3dé", "١٢٣٤٥٦٧٨", `a1b2c3d"`, "../../..", "sk-ant-api03-CANARYdummyAAAAAAAA",
		strings.Repeat("a", 64), strings.Repeat("a", 1<<20), "plainzebra-joincanary-1a2b3c",
	} {
		if ValidClientID(bad) {
			t.Errorf("ValidClientID(%.30q) = true", bad)
		}
	}
	// Agrees with the schema's own pattern for an id, so the two cannot drift.
	re := regexp.MustCompile(loadSchema(t).Patterns["sid"])
	for _, s := range []string{"a1b2c3d4", "A1B2C3D4", "a1b2c3d", "a1b2c3d4e", "g1b2c3d4", ""} {
		if ValidClientID(s) != re.MatchString(s) {
			t.Errorf("ValidClientID(%q) = %v but the schema's 8-hex pattern says %v", s, ValidClientID(s), re.MatchString(s))
		}
	}
}

func TestAValidClientIDIsTheTurnID(t *testing.T) {
	r := enable(t)
	tr := NewTurnWithID("a-session", "a1b2c3d4")
	tr.Emit(Event{Stage: StageRequest, Status: StatusOK, Tool: KindPrompt.Tool()})
	tr.Emit(Event{Stage: StageResult, Status: StatusOK})
	for _, l := range r.lines(t) {
		if !strings.Contains(l, `"turn":"a1b2c3d4"`) {
			t.Errorf("line does not carry the client's id: %s", l)
		}
	}
}

func TestAnInvalidClientIDIsReplacedAndNeverWritten(t *testing.T) {
	r := enable(t)
	hostile := []string{"", "A1B2C3D4", "a1b2c3d", `a1b2c3d4","stage":"result`, "sk-ant-api03-CANARYdummyAAAAAAAA", "plainzebra-joincanary-1a2b3c", strings.Repeat("a", 4096)}
	for _, id := range hostile {
		tr := NewTurnWithID("", id)
		if tr == nil {
			t.Fatalf("NewTurnWithID(%.20q) = nil with the recorder on", id)
		}
		tr.Emit(Event{Stage: StageRequest, Status: StatusOK})
	}
	b, _ := os.ReadFile(r.file())
	for _, id := range hostile {
		if id != "" && bytes.Contains(b, []byte(id)) {
			t.Errorf("the rejected id %.30q is in the trace", id)
		}
	}
	for _, l := range r.lines(t) {
		m := regexp.MustCompile(`"turn":"([^"]*)"`).FindStringSubmatch(l)
		if m == nil || !hex8.MatchString(m[1]) {
			t.Errorf("the turn id is not one the daemon minted: %s", l)
		}
	}
	// And nothing was logged about them: a rejected id has no way into the log.
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.logs {
		for _, id := range hostile {
			if id != "" && strings.Contains(l, id) {
				t.Errorf("the rejected id is in the daemon log: %s", l)
			}
		}
	}
}

// With the recorder off the id is not looked at, and there is no turn.
func TestAClientIDWithTheRecorderOffIsIgnored(t *testing.T) {
	Reset()
	if NewTurnWithID("s", "a1b2c3d4") != nil {
		t.Error("a turn exists with the recorder off")
	}
}
