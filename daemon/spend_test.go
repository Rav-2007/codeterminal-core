package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mochiii/protocol"
)

// A ledger with small, round limits and a clock the test owns.
func testLedger(t *testing.T, dir, key string) (*spendLedger, *time.Time) {
	t.Helper()
	l := newSpendLedger(MCPSpendConfig{SessionTokens: 1000, SessionUSD: 1, DayTokens: 5000, DayUSD: 5}, dir, key, discardLogger())
	now := time.Date(2026, 10, 8, 14, 0, 0, 0, time.Local)
	l.now = func() time.Time { return now }
	l.own.Day = l.today()
	return l, &now
}

func TestASessionIsRefusedAtItsLimitAndSaysHowToGoOn(t *testing.T) {
	l, _ := testLedger(t, "", "a")
	l.add("s1", 600, 0)
	if stop := l.reached("s1", 0); stop != nil {
		t.Fatalf("600 of 1,000 tokens was refused: %s", stop.refusal())
	}
	l.add("s1", 400, 0)
	stop := l.reached("s1", 0)
	if stop == nil {
		t.Fatal("1,000 of 1,000 tokens was not refused")
	}
	msg := stop.refusal()
	for _, want := range []string{"this session's spending limit", "1,000 of 1,000 tokens", "/budget more", "another 1,000 tokens", "has not sent this question"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, msg)
		}
	}
	// Another session is its own count; the day holds them both.
	if stop := l.reached("s2", 0); stop != nil {
		t.Errorf("a second session was refused on the first one's spend: %s", stop.refusal())
	}
	if st := l.status("s2"); st.Day.Tokens != 1000 || st.Session.Tokens != 0 || st.Reached {
		t.Errorf("the second session's status is %+v; want nothing spent by it, 1,000 today, not reached", st)
	}
}

// Room is kept back while a turn runs: for its next step and for the call that
// ends a stopped turn. The stop comes before the number, not two calls after.
func TestARunningTurnIsStoppedWithRoomForItsLastTwoCalls(t *testing.T) {
	l, _ := testLedger(t, "", "a")
	l.add("s", 500, 0)
	if l.reached("s", 2*200) != nil {
		t.Fatal("500 used with two 200-token calls to come is 900 of 1,000: under, and stopped")
	}
	if l.reached("s", 2*250) == nil {
		t.Fatal("500 used with two 250-token calls to come is the limit, and was not stopped")
	}
}

// A STOP HOLDS. The room kept back stops a turn below the number, so without
// this the next question would be admitted, billed, stopped again -- each one a
// call and a wrap-up past the limit.
func TestALimitThatStoppedATurnRefusesTheNextQuestionUntilRaised(t *testing.T) {
	l, _ := testLedger(t, "", "a")
	l.add("s", 500, 0)
	if l.reached("s", 600) == nil {
		t.Fatal("the turn was not stopped")
	}
	if l.reached("s", 0) == nil {
		t.Fatal("after a limit stopped a turn the next question was admitted, at 500 of 1,000")
	}
	if st := l.status("s"); !st.Reached {
		t.Error("the status does not say the limit is reached")
	}
	// It is raised although the count is under the warning mark: a limit that
	// is holding must always be one that can be raised.
	got := l.more("s")
	if !strings.Contains(got.Notice, "another 1,000 tokens and $1.00 for this session") || got.Reached {
		t.Errorf("more answered %+v; want the session raised and no longer held", got)
	}
	if got.Session.LimitTokens != 2000 {
		t.Errorf("the session's limit is %d after one more allotment, want 2,000", got.Session.LimitTokens)
	}
	if stop := l.reached("s", 0); stop != nil {
		t.Errorf("the next question is still refused after the limit was raised: %s", stop.refusal())
	}
}

// Looking is not stopping: a status report must not make a limit hold.
func TestAskingForTheStatusDoesNotHoldALimit(t *testing.T) {
	l, _ := testLedger(t, "", "a")
	l.add("s", 1000, 0)
	l.status("s")
	l.mu.Lock()
	held := l.sessions["s"].held
	l.mu.Unlock()
	if held != 0 {
		t.Error("a status report recorded a stop")
	}
}

func TestMoreRaisesNothingThatIsNotNear(t *testing.T) {
	l, _ := testLedger(t, "", "a")
	l.add("s", 790, 0)
	got := l.more("s")
	if !strings.HasPrefix(got.Notice, "Nothing was raised") || !strings.Contains(got.Notice, "this session 79%") ||
		got.Session.LimitTokens != 1000 || got.Day.LimitTokens != 5000 {
		t.Errorf("more at 79%% answered %+v; want nothing raised", got)
	}
	l.add("s", 10, 0)
	if got := l.more("s"); got.Session.LimitTokens != 2000 || got.Day.LimitTokens != 5000 {
		t.Errorf("more at 80%% of the session left the limits at %d and %d; want the session's raised and the day's alone",
			got.Session.LimitTokens, got.Day.LimitTokens)
	}
}

func TestTheWarningIsSaidOnceForEachAllotment(t *testing.T) {
	l, _ := testLedger(t, "", "a")
	l.add("s", 700, 0)
	if n := l.notice("s"); n != "" {
		t.Fatalf("a warning at 70%%: %q", n)
	}
	l.add("s", 150, 0)
	n := l.notice("s")
	for _, want := range []string{"This session has used 850 of 1,000 tokens (85%)", "/budget shows the numbers"} {
		if !strings.Contains(n, want) {
			t.Errorf("the warning does not say %q: %q", want, n)
		}
	}
	if again := l.notice("s"); again != "" {
		t.Errorf("the warning was said twice: %q", again)
	}
	l.more("s")
	l.add("s", 800, 0) // 1,650 of 2,000: past the mark of the second allotment
	if n := l.notice("s"); !strings.Contains(n, "1,650 of 2,000 tokens (82%)") {
		t.Errorf("no warning for the second allotment: %q", n)
	}
}

// A client that names no session has none: only the day holds it, and what it
// is told does not send it to a command it does not have.
func TestAClientWithNoSessionIsHeldByTheDayAndToldWhatItCanDo(t *testing.T) {
	l, _ := testLedger(t, "", "a")
	l.add("", 4999, 0)
	if l.reached("", 0) != nil {
		t.Fatal("4,999 of 5,000 was refused")
	}
	l.add("", 1, 0)
	stop := l.reached("", 0)
	if stop == nil || stop.which != "day" {
		t.Fatalf("the day's limit did not stop a client with no session: %+v", stop)
	}
	msg := stop.refusal()
	if strings.Contains(msg, "/budget") {
		t.Errorf("a client that sent no session was told to type /budget, which it has shown no sign of having:\n%s", msg)
	}
	for _, want := range []string{"today's spending limit", "5,000 of 5,000 tokens", "all projects", "mcp.budget.spend.day_tokens", "tomorrow"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, msg)
		}
	}
	if st := l.status(""); st.Session != (protocol.SpendMeter{}) {
		t.Errorf("a client with no session has a session meter: %+v", st.Session)
	}
}

func TestDollarsStopASessionWhereTheyAreBilled(t *testing.T) {
	l, _ := testLedger(t, "", "a")
	l.add("s", 10, 0.99)
	if l.reached("s", 0) != nil {
		t.Fatal("$0.99 of $1.00 was refused")
	}
	l.add("s", 10, 0.02)
	stop := l.reached("s", 0)
	if stop == nil || !strings.Contains(stop.refusal(), "$1.01 of $1.00") {
		t.Fatalf("$1.01 of $1.00 did not stop the session: %+v", stop)
	}
}

// THE DAY IS EVERY PROJECT'S, and outlives a daemon. Two daemons (two
// workspaces) add to one total, and a daemon started again carries on from
// what it had spent instead of from nothing.
func TestTheDayIsSharedBetweenDaemonsAndSurvivesARestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spend")
	a, _ := testLedger(t, dir, "project-a")
	b, _ := testLedger(t, dir, "project-b")
	a.add("sa", 3000, 1.5)
	b.add("sb", 1500, 0.5)

	for name, l := range map[string]*spendLedger{"a": a, "b": b} {
		if st := l.status(""); st.Day.Tokens != 4500 || st.Day.USD != 2.0 {
			t.Errorf("daemon %s sees %d tokens and $%.2f today; want both daemons' 4,500 and $2.00", name, st.Day.Tokens, st.Day.USD)
		}
	}
	b.add("sb", 500, 0)
	if a.reached("", 0) == nil {
		t.Error("daemon a was not stopped by a day's limit that daemon b's spend completed")
	}

	// A restart of a: a new ledger, the same key.
	again, _ := testLedger(t, dir, "project-a")
	if st := again.status(""); st.Day.Tokens != 5000 {
		t.Errorf("after a restart the day is %d tokens, want the 5,000 already spent", st.Day.Tokens)
	}
	// Raised from one daemon, the day is raised for both.
	if got := again.more("x"); got.Day.LimitTokens != 10000 {
		t.Fatalf("the day's limit is %d after one more allotment, want 10,000: %+v", got.Day.LimitTokens, got)
	}
	if st := b.status(""); st.Day.LimitTokens != 10000 || st.Reached {
		t.Errorf("daemon b still sees the old limit after a raised it: %+v", st)
	}

	// Stored owner-only, one file a daemon.
	entries, err := os.ReadDir(filepath.Join(dir, a.own.Day))
	if err != nil || len(entries) != 2 {
		t.Fatalf("the day's folder holds %d files (%v), want one a daemon", len(entries), err)
	}
	for _, e := range entries {
		if fi, _ := e.Info(); fi.Mode().Perm()&0o077 != 0 && runtime.GOOS != "windows" {
			t.Errorf("%s is readable by others: %v", e.Name(), fi.Mode().Perm())
		}
	}
}

func TestANewDayStartsFromNothingAndASessionDoesNot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spend")
	l, now := testLedger(t, dir, "a")
	l.add("s", 900, 0)
	l.add("", 4100, 0)
	if l.reached("", 0) == nil {
		t.Fatal("the day's limit was not reached")
	}
	*now = now.AddDate(0, 0, 1)
	if stop := l.reached("", 0); stop != nil {
		t.Errorf("yesterday's spend stopped today: %s", stop.refusal())
	}
	st := l.status("s")
	if st.Day.Tokens != 0 || st.Session.Tokens != 900 {
		t.Errorf("after midnight the day is %d and the session %d; want 0 and the session's own 900", st.Day.Tokens, st.Session.Tokens)
	}
}

func TestDaysOlderThanAWeekAreRemovedAndNothingElseIs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spend")
	for _, name := range []string{"2026-09-20", "2026-10-05", "not-a-day", "2020-01-01.txt"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	l, _ := testLedger(t, dir, "a")
	l.mu.Lock()
	l.pruneOldDays()
	l.mu.Unlock()
	for name, want := range map[string]bool{"2026-09-20": false, "2026-10-05": true, "not-a-day": true, "2020-01-01.txt": true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != want {
			t.Errorf("%s: kept=%t, want kept=%t", name, err == nil, want)
		}
	}
}

// A link planted where the count is kept is not followed, in either
// direction: nothing is written through it and nothing is read from it.
func TestAPlantedLinkIsNeitherWrittenThroughNorReadFrom(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("making a symbolic link needs a privilege the Windows runner does not have")
	}
	base := t.TempDir()
	elsewhere := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "spend")
	if err := os.Symlink(elsewhere, dir); err != nil {
		t.Fatal(err)
	}
	l, _ := testLedger(t, dir, "a")
	l.add("s", 100, 0)
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("the count was written through a planted link: %v", entries)
	}
	if st := l.status("s"); st.Day.Tokens != 100 {
		t.Errorf("the day is %d; a folder that cannot be used leaves the count in memory, not lost", st.Day.Tokens)
	}

	// A share that is itself a link to a file claiming a huge spend is not read.
	real := filepath.Join(base, "real")
	day := filepath.Join(real, l.own.Day)
	if err := os.MkdirAll(day, 0o700); err != nil {
		t.Fatal(err)
	}
	forged := filepath.Join(base, "forged.json")
	if err := os.WriteFile(forged, []byte(`{"day":"`+l.own.Day+`","tokens":999999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(forged, filepath.Join(day, "other.json")); err != nil {
		t.Fatal(err)
	}
	m, _ := testLedger(t, real, "a")
	if st := m.status(""); st.Day.Tokens != 0 {
		t.Errorf("a linked share was counted: %d tokens", st.Day.Tokens)
	}
}

// What is read for the count is bounded where it is read: a share is a
// hundred bytes, and a file in its place that is not is not counted -- however
// well it parses.
func TestAShareTooLargeToBeOneIsNotCounted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spend")
	l, _ := testLedger(t, dir, "a")
	l.add("s", 100, 0)
	day := filepath.Join(dir, l.own.Day)
	huge := `{"day":"` + l.own.Day + `","tokens":700}` + strings.Repeat(" ", maxSpendShareBytes)
	if err := os.WriteFile(filepath.Join(day, "padded.json"), []byte(huge), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(day, "honest.json"), []byte(`{"day":"`+l.own.Day+`","tokens":30}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Not a share at all, and not for today: neither is counted.
	if err := os.WriteFile(filepath.Join(day, "notes.txt"), []byte(`{"day":"`+l.own.Day+`","tokens":5000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(day, "stale.json"), []byte(`{"day":"2001-01-01","tokens":5000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := l.status("s").Day.Tokens; got != 130 {
		t.Errorf("the day is %d tokens; want this daemon's 100 and the one honest share's 30", got)
	}
}

func TestOnlyTheMostRecentSessionsAreRemembered(t *testing.T) {
	l, now := testLedger(t, "", "a")
	for i := range maxSpendSessions + 5 {
		*now = now.Add(time.Second)
		l.add("s"+string(rune('A'+i%26))+string(rune('a'+i/26)), 10, 0)
	}
	l.mu.Lock()
	n := len(l.sessions)
	_, firstKept := l.sessions["sAa"]
	l.mu.Unlock()
	if n != maxSpendSessions {
		t.Errorf("%d sessions are remembered, want the bound of %d", n, maxSpendSessions)
	}
	if firstKept {
		t.Error("the session used longest ago was kept past the bound")
	}
}

// A Server built without a ledger -- every in-process test -- counts nothing
// and is limited by nothing.
func TestNoLedgerMeansNoLimit(t *testing.T) {
	var l *spendLedger
	l.add("s", 1<<30, 1e9)
	if l.reached("s", 0) != nil || l.status("s") != nil || l.more("s") != nil || l.notice("s") != "" {
		t.Error("a nil ledger limited or reported something")
	}
	var tally *usageTally
	if tally.spendReached() != nil || tally.spendReport() != nil {
		t.Error("a nil tally reported a limit")
	}
}

func TestTheSpendDefaultsAndWhatAConfigChanges(t *testing.T) {
	got := MCPBudgetConfig{}.resolvedSpend()
	if got != (MCPSpendConfig{SessionTokens: 3_000_000, SessionUSD: 1, DayTokens: 15_000_000, DayUSD: 5}) {
		t.Errorf("the defaults are %+v; decided 2026-10-08: 3M tokens or $1 a session, 15M or $5 a day", got)
	}
	got = MCPBudgetConfig{Spend: &MCPSpendConfig{SessionTokens: 500_000, DayUSD: 2}}.resolvedSpend()
	if got != (MCPSpendConfig{SessionTokens: 500_000, SessionUSD: 1, DayTokens: 15_000_000, DayUSD: 2}) {
		t.Errorf("a config that sets two of the four gave %+v; the other two keep their defaults", got)
	}
}

func TestASessionNameIsBounded(t *testing.T) {
	if got := sessionID("  abc  "); got != "abc" {
		t.Errorf("sessionID trimmed to %q", got)
	}
	if got := sessionID(strings.Repeat("x", 500)); len(got) != protocol.MaxSessionIDBytes {
		t.Errorf("a 500-byte session name is kept at %d bytes, want %d", len(got), protocol.MaxSessionIDBytes)
	}
}

// A client learns from the handshake whether /budget will be understood: a
// daemon that keeps no limits does not claim to.
func TestOnlyADaemonThatKeepsLimitsSaysSo(t *testing.T) {
	has := func(features []string) bool {
		for _, f := range features {
			if f == protocol.FeatureSpendLimits {
				return true
			}
		}
		return false
	}
	if has((&Server{}).features()) {
		t.Error("a daemon with no ledger announced spending limits")
	}
	l, _ := testLedger(t, "", "a")
	if !has((&Server{spend: l}).features()) {
		t.Error("a daemon that keeps the limits did not announce them, so a client would refuse to ask")
	}
}

// With no ledger a budget request is still answered as one -- refused, in
// words -- and never falls through to be read as an empty question.
func TestABudgetRequestToADaemonWithNoLedgerIsRefusedAsOne(t *testing.T) {
	s := &Server{logger: discardLogger()}
	var buf strings.Builder
	if !s.serveBudgetAction(json.NewEncoder(&buf), protocol.PromptRequest{BudgetAction: protocol.BudgetActionStatus}) {
		t.Fatal("a budget request was not taken as one")
	}
	if !strings.Contains(buf.String(), "keeps no spending limits") {
		t.Errorf("answered %s", buf.String())
	}
	if s.serveBudgetAction(json.NewEncoder(&buf), protocol.PromptRequest{Prompt: "hello"}) {
		t.Error("an ordinary question was taken for a budget request")
	}
}
