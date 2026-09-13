package main

import (
	"strings"
	"testing"

	"codeterminal/protocol"
)

// ITEM 1a -- the prompt is PERSISTED SCRUBBED.
//
// Decided by the daemon/ owner on 2026-09-13 (memo item 1, FIXED).
//
// Until this landed, persistTurn wrote promptReq.Prompt -- the RAW string --
// while the wire got cleanPrompt, so a secret the daemon had just told the user
// it redacted was stored verbatim, handed back at the next handshake, and sent
// in full on turn 2. Found by execution 2026-09-04, re-derived independently as
// R1.22 (09-08) and F-1 (09-12): the same defect three times, because the
// daemon/ owner was an unassigned ROLE rather than an unresponsive person.
//
// THE SENTINEL IS AKIA-SHAPED ON PURPOSE, following sentinel_secrets_test.go:
// a canary that scrub() does not recognise would make this pass for the wrong
// reason -- "no hit" would mean "nothing was ever supposed to be redacted".
func TestPersistTurn_StoresTheScrubbedPrompt(t *testing.T) {
	memStore, _ := openTestMemoryStore(t)
	const ws = "/workspace/scrub-persist"
	srv := &Server{logger: discardLogger(), workspace: ws, memory: memStore, cfg: &Config{}}

	const secret = "AKIAIOSFODNN7EXAMPLE"
	srv.persistTurn("deploy with "+secret+" please", "done", nil)

	// READ BACK WITH SCRUBBING DISABLED, which is what isolates 1a from 1b.
	//
	// This test first read with scrubDisabled=false and PASSED WITH 1a
	// NEUTERED: prepareHistory (1b) now scrubs on the hydration path too, so it
	// redacted on the way out what persistTurn had stored raw, and the
	// assertion could not tell the two controls apart. Reading with scrubbing
	// off returns exactly the bytes on disk, so this asserts the AT-REST
	// property and nothing else.
	//
	// Two overlapping controls are the point -- a secret should have to get
	// past both -- but a test that cannot distinguish them proves only that at
	// least one works, and would have gone green if 1a were removed tomorrow.
	turns, err := memStore.LoadRecentTurns(t.Context(), ws, 10, true)
	if err != nil {
		t.Fatalf("LoadRecentTurns: %v", err)
	}
	// Vacuity floor: nothing persisted would satisfy every assertion below.
	if len(turns) == 0 {
		t.Fatal("vacuity floor: nothing was persisted, so this proves nothing")
	}
	var userTurn string
	for _, turn := range turns {
		if turn.Role == "user" {
			userTurn = turn.Content
		}
	}
	if userTurn == "" {
		t.Fatal("vacuity floor: no user turn was persisted")
	}

	if strings.Contains(userTurn, secret) {
		t.Errorf("the RAW secret is in the persisted user turn: %q\n"+
			"persistTurn must store the scrubbed prompt, not promptReq.Prompt -- otherwise the "+
			"redaction notice the user was shown is false from turn 2 onward.", userTurn)
	}
	// The other direction, so a persistTurn that stored nothing, or stored an
	// empty string, cannot pass: the redaction must be VISIBLE.
	if !strings.Contains(userTurn, "[REDACTED:aws_access_key]") {
		t.Errorf("the persisted turn carries no redaction placeholder: %q\n"+
			"a clean result with no placeholder means scrub() never ran, not that it worked.", userTurn)
	}
}

// --no-scrub MUST STILL BE HONOURED at the persistence site, or the escape
// hatch silently stops being one. Kept beside the test above so the pair cannot
// drift: one asserts the control fires, the other asserts it can be turned off.
func TestPersistTurn_NoScrubStoresTheRawPrompt(t *testing.T) {
	memStore, _ := openTestMemoryStore(t)
	const ws = "/workspace/scrub-persist-off"
	srv := &Server{logger: discardLogger(), workspace: ws, memory: memStore, cfg: &Config{NoScrub: true}}

	const secret = "AKIAIOSFODNN7EXAMPLE"
	srv.persistTurn("deploy with "+secret, "done", nil)

	// scrubDisabled=true on the READ too, matching what server.go passes
	// (s.noScrub()) for this configuration. Both halves of the escape hatch or
	// neither: reading back with scrubbing ENABLED would redact on the way out
	// what --no-scrub deliberately stored raw, which is the inconsistency the
	// threaded flag exists to prevent.
	turns, err := memStore.LoadRecentTurns(t.Context(), ws, 10, true)
	if err != nil {
		t.Fatalf("LoadRecentTurns: %v", err)
	}
	if len(turns) == 0 {
		t.Fatal("vacuity floor: nothing was persisted")
	}
	var found bool
	for _, turn := range turns {
		if turn.Role == "user" && strings.Contains(turn.Content, secret) {
			found = true
		}
	}
	if !found {
		t.Error("--no-scrub did not reach the persistence path: the prompt was redacted anyway. " +
			"The escape hatch must apply everywhere scrub() does, or it is not an escape hatch.")
	}
}

// A NIL CONFIG MUST SCRUB, not skip.
//
// The first draft of the persistTurn scrub read `s.cfg == nil || s.cfg.NoScrub`
// as its "disabled" argument, which inverts the polarity: no config would have
// meant no scrubbing. It was caught before landing, and this is what stops it
// coming back. s.noScrub() already encodes the safe direction; the bug was in
// re-deriving the condition rather than reusing the accessor.
//
// This is the same question register row 6.4 leaves open -- three call sites
// read s.cfg.NoScrub directly and would panic where s.noScrub() returns false.
// That row is untouched here; this only pins the persistence path's polarity.
func TestPersistTurn_NilConfigScrubsRatherThanSkips(t *testing.T) {
	memStore, _ := openTestMemoryStore(t)
	const ws = "/workspace/scrub-persist-nilcfg"
	srv := &Server{logger: discardLogger(), workspace: ws, memory: memStore} // cfg deliberately nil

	const secret = "AKIAIOSFODNN7EXAMPLE"
	srv.persistTurn("key "+secret, "done", nil)

	turns, err := memStore.LoadRecentTurns(t.Context(), ws, 10, false)
	if err != nil {
		t.Fatalf("LoadRecentTurns: %v", err)
	}
	if len(turns) == 0 {
		t.Fatal("vacuity floor: nothing was persisted")
	}
	for _, turn := range turns {
		if turn.Role == "user" && strings.Contains(turn.Content, secret) {
			t.Errorf("a nil config disabled scrubbing: %q\n"+
				"An absent config must fail CLOSED. Use s.noScrub(), not a re-derived condition.",
				turn.Content)
		}
	}
}

// ITEM 1b -- CLIENT-SUPPLIED history is scrubbed before it reaches the model.
//
// prepareHistory is the daemon's server-side enforcement point for history, and
// its own doc comment says so: "it never trusts the client to have already
// capped or sanitized this list". It validated roles and capped size and did
// not scrub, so a secret in a client-sent turn went to the provider verbatim.
//
// ONE CHOKE POINT, TWO CALLERS. prepareHistory is reached from server.go (the
// client-supplied list) and from memory.go's LoadRecentTurns (the hydration
// path). Putting the scrub inside it covers both, which is why the flag is
// threaded rather than the scrub being applied at one call site.
//
// USER TURNS ONLY. Scrubbing ASSISTANT turns is item 1c, DECLINED by the
// daemon/ owner on 2026-09-13: a prior answer that legitimately contained a
// key-shaped string would come back redacted and the model would be confused
// about its own earlier reply. The assertion below pins that boundary, so
// "scrub history" cannot quietly grow into 1c without this test failing.
func TestPrepareHistory_ScrubsUserTurnsNotAssistantTurns(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	out := prepareHistory([]protocol.Turn{
		{Role: "user", Content: "my key is " + secret},
		{Role: "assistant", Content: "I saw " + secret + " in your message"},
	}, false)

	if len(out.Messages) != 2 {
		t.Fatalf("vacuity floor: got %d message(s), want 2 -- nothing was validated", len(out.Messages))
	}
	var user, assistant string
	for _, m := range out.Messages {
		switch m.Role {
		case "user":
			user = m.Content
		case "assistant":
			assistant = m.Content
		}
	}

	if strings.Contains(user, secret) {
		t.Errorf("the client-supplied USER turn reached the model unscrubbed: %q", user)
	}
	if !strings.Contains(user, "[REDACTED:aws_access_key]") {
		t.Errorf("vacuity floor: the user turn carries no placeholder (%q), so a clean "+
			"result would not prove scrub() ran", user)
	}
	if !strings.Contains(assistant, secret) {
		t.Errorf("the ASSISTANT turn was scrubbed: %q\n"+
			"That is item 1c, which was DECLINED. If it is being taken deliberately, change "+
			"this test and say so -- do not let it drift in.", assistant)
	}
}

// --no-scrub reaches the history path too, for the reason it reaches every
// other: an escape hatch with a gap is not an escape hatch.
func TestPrepareHistory_NoScrubLeavesUserTurnsAlone(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE"
	out := prepareHistory([]protocol.Turn{
		{Role: "user", Content: "my key is " + secret},
	}, true)

	if len(out.Messages) != 1 {
		t.Fatalf("vacuity floor: got %d message(s), want 1", len(out.Messages))
	}
	if !strings.Contains(out.Messages[0].Content, secret) {
		t.Errorf("--no-scrub did not reach prepareHistory: %q", out.Messages[0].Content)
	}
}
