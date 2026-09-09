// B.2, the remaining A.4 rows. sentinel_secrets_test.go drives rows 5, 10 and
// 11; this file drives 1, 3, 4, 6, 7, 8 and 9, so the sweep's coverage claim is
// carried by tests rather than by prose.
//
// THREE OUTCOMES, KEPT APART. A row where the sentinel is absent is a control
// that holds. A row where it is PRESENT BY DESIGN is not a leak and must not be
// filed as one -- a backup that redacted the user's file would corrupt undo, and
// an index that stored redacted text could not match the code it indexes. A row
// where it is present and should not be is the only kind that is a finding.
// Test names here say which of the three they are, because a reader skimming
// green checkmarks cannot otherwise tell a control from a characterisation.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeterminal/daemon/mcp"
	"codeterminal/editapply"
	"codeterminal/protocol"
)

// ROW 1 -- daemon env keys. The API key is held on Server and must reach
// exactly one place: the Authorization header. Not status, not logs.
func TestSentinelRow1_APIKeyNeverLeavesTheAuthorizationHeader(t *testing.T) {
	secret := sentinelFor(1)
	logger, logBuf := bufferLogger()
	s := &Server{apiKey: secret, logger: logger, cfg: &Config{}, workspace: t.TempDir()}

	var statusOut bytes.Buffer
	s.handleStatus(json.NewEncoder(&statusOut))
	blob := statusOut.Bytes()
	if len(blob) == 0 {
		t.Fatal("vacuity floor: handleStatus wrote nothing")
	}
	if bytes.Contains(blob, []byte(secret)) {
		t.Errorf("row 1: the API key is in the status payload:\n%s", blob)
	}
	if strings.Contains(logBuf.String(), secret) {
		t.Errorf("row 1: the API key reached the daemon log:\n%s", logBuf.String())
	}
	// Load-bearing floor: prove the key was actually held, or the two clean
	// greps above are greps over a Server that never had it.
	if s.apiKey != secret {
		t.Fatal("vacuity floor: the server did not hold the sentinel key")
	}
}

// ROW 3 -- the TCP bearer token.
//
// NOT LOAD-BEARING IN THE WAY THE OTHER ROWS ARE, and labelled rather than
// deleted or left to read as verified (B2.2e). It RECONSTRUCTS the rejection
// artefacts instead of driving handleConn, because nothing in the product
// selects protocol.TransportTCP today (server.go:368) -- there is no way to
// reach the bearer path end to end from a test without adding a transport.
// So this pins the SHAPE of the two artefacts, and a change to server.go's
// actual rejection would not fail it. The real assurance for row 3 is
// structural: s.tcpToken is compared and never formatted into either artefact.
func TestSentinelRow3_BearerTokenIsNotInTheRejection(t *testing.T) {
	secret := sentinelFor(3)
	logger, logBuf := bufferLogger()

	// The rejection text and log line as server.go builds them, driven directly:
	// a wrong token, and a client name that is itself hostile.
	logger.Printf("rejecting client %q: missing or invalid bearer token", "evil\x00client")
	resp := protocol.HandshakeResponse{
		ProtocolVersion: protocol.ProtocolVersion,
		Ok:              false,
		Error:           "HTTP 401 Unauthorized: invalid or missing bearer token",
	}
	blob, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshalling handshake response: %v", err)
	}
	if logBuf.Len() == 0 || len(blob) == 0 {
		t.Fatal("vacuity floor: nothing was produced to grep")
	}
	if strings.Contains(logBuf.String(), secret) || bytes.Contains(blob, []byte(secret)) {
		t.Errorf("row 3: the bearer token appears in a rejection artefact")
	}
	if !bytes.Contains(blob, []byte("401")) {
		t.Error("vacuity floor: the rejection response is not the one under test")
	}
}

// ROW 4 -- the user's typed prompt, on the outbound path. This is the control
// that scrub exists to provide, pinned with the same sentinel shape as the rest.
func TestSentinelRow4_TypedPromptIsScrubbedOutbound(t *testing.T) {
	secret := sentinelFor(4)
	prompt := "here is my key " + secret + " please debug it"

	cleaned, redactions := scrub(prompt, false)
	if len(redactions) != 1 {
		t.Fatalf("vacuity floor: scrub made %d redactions, want 1", len(redactions))
	}
	if strings.Contains(cleaned, secret) {
		t.Errorf("row 4: the prompt sentinel survived scrub: %q", cleaned)
	}
	if !strings.Contains(cleaned, "[REDACTED:aws_access_key]") {
		t.Errorf("row 4: no redaction marker: %q", cleaned)
	}
	// The surroundings must survive, or scrub is corrupting the user's text.
	if !strings.Contains(cleaned, "please debug it") {
		t.Errorf("row 4: scrub ate the surrounding prompt: %q", cleaned)
	}
}

// ROW 6 -- MCP server env. The allow-list is the control: a forbidden name is
// dropped whatever its value, and a name that is not allowed never appears.
func TestSentinelRow6_MCPEnvAllowListDropsForbiddenAndUnlisted(t *testing.T) {
	secret := sentinelFor(6)
	t.Setenv("OPENROUTER_API_KEY", secret)
	t.Setenv("SENTINEL_NOT_ALLOWED", secret)
	t.Setenv("SENTINEL_ALLOWED", secret)

	env := mcp.ServerEnv([]string{"OPENROUTER_API_KEY", "SENTINEL_ALLOWED"})
	if len(env) == 0 {
		t.Fatal("vacuity floor: ServerEnv returned nothing")
	}
	joined := strings.Join(env, "\n")

	for _, name := range []string{"OPENROUTER_API_KEY", "SENTINEL_NOT_ALLOWED"} {
		if strings.Contains(joined, name+"=") {
			t.Errorf("row 6: %s reached the subprocess environment:\n%s", name, joined)
		}
	}
	// PRESENT BY DESIGN: an explicitly allowed name is passed through with its
	// value. That is what an allow-list is for; asserting its absence would be
	// asserting the feature is broken.
	if !strings.Contains(joined, "SENTINEL_ALLOWED="+secret) {
		t.Errorf("vacuity floor: the explicitly allowed variable did not pass through, so the two absences above prove nothing:\n%s", joined)
	}
}

// ROW 7 -- persisted prompts. TRIPWIRE, NOT A CONTROL.
//
// This test asserts the CURRENT behaviour: the raw prompt is persisted and
// comes back out. B2.1 measured that this is what returns an unscrubbed secret
// to the provider on turn N+1. It is pinned here so the behaviour cannot change
// silently in either direction.
//
// WHEN THE INGEST-SCRUB DECISION LANDS, THIS TEST GOES RED, AND THAT IS THE
// FIX ARRIVING, NOT A REGRESSION. Whoever sees it red should delete it and say
// so in the commit, not restore the raw persist to make it green.
func TestSentinelRow7_PersistedPromptIsStillRaw_TRIPWIRE(t *testing.T) {
	secret := sentinelFor(7)
	ctx := context.Background()
	store, err := OpenMemoryStore(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("opening memory store: %v", err)
	}
	// Close's error is dropped deliberately: this is a t.TempDir() database that
	// the test framework removes either way, and a close failure here would mask
	// the assertion below rather than tell anyone anything.
	defer func() { _ = store.Close() }()

	ws := "/tmp/sentinel-ws"
	if err := store.AppendTurn(ctx, ws, "user", "my key is "+secret); err != nil {
		t.Fatalf("appending turn: %v", err)
	}
	turns, err := store.LoadRecentTurns(ctx, ws, 10)
	if err != nil {
		t.Fatalf("loading turns: %v", err)
	}
	if len(turns) == 0 {
		t.Fatal("vacuity floor: nothing was persisted, so this pins nothing")
	}
	var found bool
	for _, turn := range turns {
		if strings.Contains(turn.Content, secret) {
			found = true
		}
	}
	if !found {
		t.Errorf("TRIPWIRE FIRED (this is good news): the persisted prompt no longer carries the raw secret. " +
			"The ingest-scrub decision has landed. Delete this test rather than restoring the raw persist.")
	}
}

// ROW 8 -- chunk text at rest. PRESENT BY DESIGN.
//
// The index stores what the file says, because retrieval must match the code
// that is actually there. Redaction happens at RENDER time (renderChunk), which
// is the egress boundary. Pinned so the at-rest/egress split stays deliberate.
func TestSentinelRow8_ChunkTextAtRestIsRawByDesign(t *testing.T) {
	secret := sentinelFor(8)
	c := Chunk{ID: "a.go:1-1", FilePath: "a.go", StartLine: 1, EndLine: 1, Content: "k = " + secret}

	if !strings.Contains(c.Content, secret) {
		t.Fatal("vacuity floor: the chunk does not carry the sentinel")
	}
	rendered := renderChunk(1, c, false)
	if strings.Contains(rendered, secret) {
		t.Errorf("row 8: the EGRESS boundary leaked; at-rest raw is by design, rendered raw is not:\n%s", rendered)
	}
	if !strings.Contains(rendered, "[REDACTED:aws_access_key]") {
		t.Error("vacuity floor: renderChunk did not redact, so the clean result proves nothing")
	}
}

// ROW 9 -- file content in backups. PRESENT BY DESIGN, and necessarily so.
//
// A backup exists to restore the file byte-for-byte. A backup that redacted the
// user's own secret would silently corrupt their file on undo, turning a
// privacy control into data loss. Asserting absence here would be asserting
// that undo should be broken.
func TestSentinelRow9_BackupIsByteExactByDesign(t *testing.T) {
	secret := sentinelFor(9)
	root := t.TempDir()
	backupDir := filepath.Join(root, ".backups")
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		t.Fatalf("creating backup dir: %v", err)
	}
	target := filepath.Join(root, "creds.go")
	original := "package main\n\nconst key = \"" + secret + "\"\n"
	if err := os.WriteFile(target, []byte(original), 0600); err != nil {
		t.Fatalf("writing target: %v", err)
	}

	p := &editapply.PreparedEdit{TargetPath: target, Original: original, FileMode: 0600}
	if err := editapply.BackupOriginal(backupDir, root, p); err != nil {
		t.Fatalf("BackupOriginal: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(backupDir, "before", "creds.go"))
	if err != nil {
		t.Fatalf("reading the backup: %v", err)
	}
	if string(got) != original {
		t.Errorf("row 9: the backup is NOT byte-exact; undo would corrupt the file.\ngot:  %q\nwant: %q", got, original)
	}
}
