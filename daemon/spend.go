package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mochiii/protocol"
)

// A SESSION'S AND A DAY'S SPEND, HELD TO A LIMIT.
//
// FOUND 2026-10-06 and still true on 2026-10-08: every ceiling this daemon had
// was about ONE turn or ONE task -- calls, tool bytes, seconds, and since
// today tokens -- and nothing added turns up. The bill was tallied per turn
// (usage.go) and forgotten at Done; the session's total existed only in the
// terminal client, for display. So the twentieth expensive question in a row
// went out exactly as the first had, and a dollar limit existed only for long
// tasks, where a provider that reports no cost counts as $0.
//
// This is the missing sum. Every model call's bill is added here as it
// arrives, to two counts:
//
//   - THE SESSION: one run of one client (protocol.PromptRequest.Session),
//     kept in memory. A client that names no session has none, and only the
//     day's limit holds it.
//   - THE DAY: every project on this machine, by the local date, kept on disk
//     so a restart does not forget it and two daemons add to the same total.
//
// Both are counted from the provider's own report, never estimated: tokens
// from every provider, dollars where one reports them. With a provider that
// reports no cost the token limits are the ones that hold.
//
// AT A LIMIT THE DAEMON STOPS AND ASKS; IT DOES NOT LOCK. A turn in flight
// ends the way every limit ends one -- a last call that answers from what was
// read -- and the next question is refused before any model call, with the
// way on: /budget more allows one more allotment. The person typing that is
// the consent. A warning is given once at 80%, so the stop is not the first
// they hear of it.
//
// NO LOCK FILE. Each daemon writes only its OWN share of a day
// (spend/<day>/<workspace key>.json, replaced whole by rename) and the day's
// total is the sum of the shares, read from disk when it is asked for. One
// workspace has one daemon (the workspace lock), so each file has one writer
// and no update can be lost between two of them.

const (
	spendDirName = "spend"
	// spendWarnPercent is the share of a limit at which the user is told, and
	// from which /budget more will raise it.
	spendWarnPercent = 80
	// maxSpendSessions bounds the sessions remembered; the one used longest
	// ago goes first. A session forgotten and then used again starts from
	// nothing, and the day's count -- which forgets nothing -- still holds it.
	maxSpendSessions = 64
	// spendDaysKept is how many days' folders stay on disk. Only today's is
	// ever read; the rest are kept a week for a person to look at.
	spendDaysKept = 7
	spendDayStamp = "2006-01-02"
	// maxSpendShareBytes bounds one share as READ, and maxSpendDirEntries one
	// listing of the folder: both at the source, so what this daemon allocates
	// for the count is decided here and not by whatever is in the folder. A
	// share is under a hundred bytes; a day's folder holds one file a project.
	maxSpendShareBytes = 4096
	maxSpendDirEntries = 1024
)

// spendShare is one daemon's share of one day, as stored.
type spendShare struct {
	Day    string  `json:"day"`
	Tokens int     `json:"tokens"`
	USD    float64 `json:"usd"`
	// Extra is how many further allotments of the day's limit were allowed
	// from this daemon (/budget more). The day's limit is the configured one
	// times one plus every share's Extra.
	Extra int `json:"extra,omitempty"`
}

type sessionSpend struct {
	tokens int
	usd    float64
	extra  int // further allotments allowed with /budget more
	warned int // 1 + the allotment already warned about; 0 is none yet
	// held is 1 + the allotment a turn was stopped at; while it names the
	// current allotment the session takes no new question (see reached).
	held int
	used time.Time // for forgetting the oldest
}

// spendLedger is the daemon's count of both. A nil ledger counts nothing and
// limits nothing, which is what a Server built without one (a test) gets.
type spendLedger struct {
	mu       sync.Mutex
	limits   MCPSpendConfig
	dir      string // StateDir()/spend; "" keeps the day in memory only
	key      string // this daemon's file name inside a day's folder
	now      func() time.Time
	logger   *log.Logger
	sessions map[string]*sessionSpend
	own      spendShare
	// dayWarned is 1 + the day allotment this daemon already warned about, and
	// dayHeld 1 + the one it stopped a turn at (see sessionSpend.held).
	dayWarned, dayHeld int
	// diskFailed is set after one logged failure to write the share, so a
	// read-only state folder is reported once and not on every model call.
	diskFailed bool
}

// newSpendLedger opens the ledger for one daemon, on the clock now. dir may be
// "": the day is then in memory, and this daemon's alone. A share on disk for
// today is picked up, so a restarted daemon carries on from what it had spent.
func newSpendLedger(limits MCPSpendConfig, dir, key string, logger *log.Logger, now func() time.Time) *spendLedger {
	l := &spendLedger{
		limits: limits, dir: dir, key: key, now: now, logger: logger,
		sessions: map[string]*sessionSpend{},
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.own.Day = l.today()
	if share, ok := l.readShare(l.sharePath(l.own.Day)); ok && share.Day == l.own.Day {
		l.own = share
	}
	l.pruneOldDays()
	return l
}

func (l *spendLedger) today() string { return l.now().Format(spendDayStamp) }

func (l *spendLedger) sharePath(day string) string {
	if l.dir == "" {
		return ""
	}
	return filepath.Join(l.dir, day, l.key+".json")
}

// readShare reads one share. A file that is missing, unreadable, too large or
// holds something else is no share: a count that cannot be read is not guessed.
func (l *spendLedger) readShare(path string) (spendShare, bool) {
	if path == "" {
		return spendShare{}, false
	}
	// Lstat first: a share is a plain file this daemon wrote. Not a link, and
	// not a pipe or a device either -- opening one of those to read can wait
	// for ever, and this is asked before every model call.
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		return spendShare{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return spendShare{}, false
	}
	defer func() { _ = f.Close() }() // read-only; nothing is lost by a failed close
	data, err := io.ReadAll(io.LimitReader(f, maxSpendShareBytes+1))
	if err != nil || len(data) > maxSpendShareBytes {
		return spendShare{}, false
	}
	var share spendShare
	if json.Unmarshal(data, &share) != nil || share.Tokens < 0 || share.USD < 0 || share.Extra < 0 {
		return spendShare{}, false
	}
	return share, true
}

// listSpendDir lists dir, at most maxSpendDirEntries of it. ReadDir on the
// handle with a count: os.ReadDir reads every entry before it returns.
func listSpendDir(dir string) []os.DirEntry {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }() // read-only
	entries, err := f.ReadDir(maxSpendDirEntries)
	if err != nil && err != io.EOF {
		return nil
	}
	return entries
}

// rollDay starts a new day's share when the date has changed. Called with mu
// held, before anything reads or adds to the day.
func (l *spendLedger) rollDay() {
	if day := l.today(); day != l.own.Day {
		l.own = spendShare{Day: day}
		l.dayWarned, l.dayHeld = 0, 0
		l.pruneOldDays()
	}
}

// writeShare stores this daemon's share, whole, by rename. Best effort: a
// state folder that cannot be written makes the day's count this daemon's own
// and in memory, which is said once in the log and is still a count.
func (l *spendLedger) writeShare() {
	path := l.sharePath(l.own.Day)
	if path == "" {
		return
	}
	fail := func(err error) {
		if !l.diskFailed && l.logger != nil {
			l.logger.Printf("spend: cannot store today's count (%v); it is kept in memory and other daemons will not see it", err)
		}
		l.diskFailed = true
	}
	for _, d := range []string{l.dir, filepath.Dir(path)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			fail(err)
			return
		}
		// A planted link would send the count somewhere else, or read one
		// from there: refused, as the history folder refuses it.
		if fi, err := os.Lstat(d); err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			fail(fmt.Errorf("%s is not a folder of this daemon's own", d))
			return
		}
	}
	data, err := json.Marshal(l.own)
	if err != nil {
		fail(err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		fail(err)
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name()) // best effort; a stale temp file is pruned with its day
		fail(werr)
		return
	}
	l.diskFailed = false
}

// pruneOldDays removes day folders older than spendDaysKept. Only names that
// are a date are touched, so nothing else under the folder can be removed.
func (l *spendLedger) pruneOldDays() {
	if l.dir == "" {
		return
	}
	cutoff := l.now().AddDate(0, 0, -spendDaysKept).Format(spendDayStamp)
	for _, e := range listSpendDir(l.dir) {
		if _, err := time.Parse(spendDayStamp, e.Name()); err != nil || !e.IsDir() {
			continue
		}
		if e.Name() < cutoff {
			_ = os.RemoveAll(filepath.Join(l.dir, e.Name())) // best effort; tried again tomorrow
		}
	}
}

// dayTotal is today across every daemon: this one's share from memory, the
// others' from disk. Called with mu held, after rollDay.
func (l *spendLedger) dayTotal() spendShare {
	total := l.own
	if l.dir == "" {
		return total
	}
	for _, e := range listSpendDir(filepath.Join(l.dir, l.own.Day)) {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || name == l.key+".json" {
			continue
		}
		if share, ok := l.readShare(filepath.Join(l.dir, l.own.Day, name)); ok && share.Day == l.own.Day {
			total.Tokens += share.Tokens
			total.USD += share.USD
			total.Extra += share.Extra
		}
	}
	return total
}

// session returns id's count, making it. Called with mu held; id is not empty.
func (l *spendLedger) session(id string) *sessionSpend {
	s, ok := l.sessions[id]
	if !ok {
		if len(l.sessions) >= maxSpendSessions {
			oldest := ""
			for k, v := range l.sessions {
				if oldest == "" || v.used.Before(l.sessions[oldest].used) {
					oldest = k
				}
			}
			delete(l.sessions, oldest)
		}
		s = &sessionSpend{}
		l.sessions[id] = s
	}
	s.used = l.now()
	return s
}

// sessionID is a client's session name as the ledger keys it: bounded, and
// empty for a client that sent none.
func sessionID(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) > protocol.MaxSessionIDBytes {
		raw = raw[:protocol.MaxSessionIDBytes]
	}
	return raw
}

// add counts one model call's bill.
func (l *spendLedger) add(session string, tokens int, usd float64) {
	if l == nil || (tokens <= 0 && usd <= 0) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollDay()
	l.own.Tokens += max(tokens, 0)
	l.own.USD += max(usd, 0)
	if session != "" {
		s := l.session(session)
		s.tokens += max(tokens, 0)
		s.usd += max(usd, 0)
	}
	l.writeShare()
}

// meters is where both counts stand against their limits. Called with mu held,
// after rollDay.
func (l *spendLedger) meters(session string) (sess, day protocol.SpendMeter) {
	total := l.dayTotal()
	day = protocol.SpendMeter{
		Tokens: total.Tokens, USD: total.USD,
		LimitTokens: l.limits.DayTokens * (1 + total.Extra),
		LimitUSD:    l.limits.DayUSD * float64(1+total.Extra),
	}
	if session != "" {
		s := l.session(session)
		sess = protocol.SpendMeter{
			Tokens: s.tokens, USD: s.usd,
			LimitTokens: l.limits.SessionTokens * (1 + s.extra),
			LimitUSD:    l.limits.SessionUSD * float64(1+s.extra),
		}
	}
	return sess, day
}

// status is the two meters, for /budget and for a turn's last message.
func (l *spendLedger) status(session string) *protocol.SpendStatus {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollDay()
	sess, day := l.meters(session)
	return &protocol.SpendStatus{Session: sess, Day: day, Reached: l.stopAt(session, 0, false) != nil}
}

// meterReached reports whether m is at its limit, keeping reserve tokens back
// for calls that are about to be made.
func meterReached(m protocol.SpendMeter, reserve int) bool {
	if m.LimitTokens > 0 && m.Tokens+reserve >= m.LimitTokens {
		return true
	}
	return m.LimitUSD > 0 && m.USD >= m.LimitUSD
}

// meterPercent is how far along its limits m is: the further of the two.
func meterPercent(m protocol.SpendMeter) int {
	pct := 0.0
	if m.LimitTokens > 0 {
		pct = 100 * float64(m.Tokens) / float64(m.LimitTokens)
	}
	if m.LimitUSD > 0 {
		pct = max(pct, 100*m.USD/m.LimitUSD)
	}
	return int(pct)
}

// meterWords says a meter the way a person reads it: the tokens always, and
// the dollars when any were billed.
func meterWords(m protocol.SpendMeter) string {
	words := fmt.Sprintf("%s of %s tokens", groupThousands(m.Tokens), groupThousands(m.LimitTokens))
	if m.USD > 0 && m.LimitUSD > 0 {
		words += fmt.Sprintf(", $%.2f of $%.2f", m.USD, m.LimitUSD)
	}
	return words
}

// spendStop says which limit is reached and how to go on.
type spendStop struct {
	// which is "session" or "day".
	which string
	meter protocol.SpendMeter
	// canAsk reports whether the client can run /budget: it named its session,
	// which only a client that knows about budgets does.
	canAsk bool
	limits MCPSpendConfig
}

// reached returns the limit that is met, or nil. reserve is the tokens to
// keep back: zero when a new question is being admitted, twice the turn's
// last request while one is running -- room for its next step and for the
// call that ends a stopped turn, so the limit is what gets billed and not
// what was billed two calls ago (tokensSpent keeps the same room for a turn).
//
// A LIMIT THAT STOPPED A TURN HOLDS UNTIL IT IS RAISED. The room kept back
// means a turn is stopped a little BELOW the number, and a question asked next
// would be under it again: admitted, billed one call, stopped, and so on, each
// one a call and a wrap-up past the limit. So the stop is remembered, and the
// next question is refused on it whatever the count says -- "stops and asks"
// would otherwise be "stops, and stops again".
func (l *spendLedger) reached(session string, reserve int) *spendStop {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollDay()
	return l.stopAt(session, reserve, true)
}

// stopAt is reached with mu held. remember records a stop so that it holds;
// a status report asks without it, since looking is not stopping.
func (l *spendLedger) stopAt(session string, reserve int, remember bool) *spendStop {
	sess, day := l.meters(session)
	if session != "" {
		s := l.session(session)
		if s.held == s.extra+1 || meterReached(sess, reserve) {
			if remember {
				s.held = s.extra + 1
			}
			return &spendStop{which: "session", meter: sess, canAsk: true, limits: l.limits}
		}
	}
	if level := l.dayTotal().Extra + 1; l.dayHeld == level || meterReached(day, reserve) {
		if remember {
			l.dayHeld = level
		}
		return &spendStop{which: "day", meter: day, canAsk: session != "", limits: l.limits}
	}
	return nil
}

// what names the limit: "this session's spending limit (… of … tokens)".
func (st *spendStop) what() string {
	if st.which == "session" {
		return "this session's spending limit (" + meterWords(st.meter) + ")"
	}
	return "today's spending limit (" + meterWords(st.meter) + ", all projects)"
}

// wayOn says how to continue, in the words of what this client can do.
func (st *spendStop) wayOn() string {
	allot, setting := st.limits.SessionTokens, "mcp.budget.spend.session_tokens"
	if st.which == "day" {
		allot, setting = st.limits.DayTokens, "mcp.budget.spend.day_tokens"
	}
	if st.canAsk {
		way := fmt.Sprintf("Type /budget more to allow another %s tokens", groupThousands(allot))
		if st.which == "day" {
			return way + " today, or wait until tomorrow."
		}
		return way + "."
	}
	return fmt.Sprintf("It starts again tomorrow; to go on now, raise %s in the daemon's configuration and restart it.", setting)
}

// refusal is what a new question is told when a limit is already met.
func (st *spendStop) refusal() string {
	return "Mochiii has reached " + st.what() + " and has not sent this question to the model. " + st.wayOn()
}

// incomplete is how a turn in flight ends at a limit.
func (st *spendStop) incomplete() *protocol.IncompleteInfo {
	return &protocol.IncompleteInfo{
		Reason: protocol.IncompleteAgentBudget,
		Detail: "this task stopped because it reached " + st.what() + " — what you see above is " +
			"everything that was done. " + st.wayOn(),
	}
}

// notice is the one sentence a turn's last message carries when it took a
// count past the warning mark, once for each allotment. Empty otherwise.
func (l *spendLedger) notice(session string) string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollDay()
	sess, day := l.meters(session)
	var parts []string
	if session != "" {
		s := l.session(session)
		if pct := meterPercent(sess); pct >= spendWarnPercent && s.warned != s.extra+1 {
			s.warned = s.extra + 1
			parts = append(parts, fmt.Sprintf("this session has used %s (%d%%)", meterWords(sess), pct))
		}
	}
	if pct := meterPercent(day); pct >= spendWarnPercent {
		if level := l.dayTotal().Extra + 1; l.dayWarned != level {
			l.dayWarned = level
			parts = append(parts, fmt.Sprintf("today has used %s across all projects (%d%%)", meterWords(day), pct))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	text := strings.Join(parts, ", and ")
	text = strings.ToUpper(text[:1]) + text[1:]
	if session != "" {
		return text + ". At the limit Mochiii stops and asks; /budget shows the numbers."
	}
	return text + ". At the limit Mochiii stops until tomorrow, or until mcp.budget.spend is raised."
}

// more allows one more allotment of every limit that is at or past its
// warning mark, or that has stopped a turn, and says what it did. Short of
// that it changes nothing: a limit raised before it is near is a limit nobody
// has.
func (l *spendLedger) more(session string) *protocol.SpendStatus {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollDay()
	sess, day := l.meters(session)
	var raised []string
	if session != "" {
		if s := l.session(session); meterPercent(sess) >= spendWarnPercent || s.held == s.extra+1 {
			s.extra++
			raised = append(raised, fmt.Sprintf("another %s tokens and $%.2f for this session",
				groupThousands(l.limits.SessionTokens), l.limits.SessionUSD))
		}
	}
	if meterPercent(day) >= spendWarnPercent || l.dayHeld == l.dayTotal().Extra+1 {
		l.own.Extra++
		l.writeShare()
		raised = append(raised, fmt.Sprintf("another %s tokens and $%.2f for today",
			groupThousands(l.limits.DayTokens), l.limits.DayUSD))
	}
	note := fmt.Sprintf("Nothing was raised: no limit is at %d%% yet (this session %d%%, today %d%%).",
		spendWarnPercent, meterPercent(sess), meterPercent(day))
	if len(raised) > 0 {
		note = "Allowed " + strings.Join(raised, ", and ") + "."
	}
	sess, day = l.meters(session)
	return &protocol.SpendStatus{Session: sess, Day: day, Notice: note, Reached: l.stopAt(session, 0, false) != nil}
}

// spendDir is where the day's shares live, or "" when the state folder cannot
// be found -- the ledger then keeps the day in memory.
func spendDir() string {
	dir, err := StateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, spendDirName)
}

// serveBudgetAction answers /budget and /budget more: one message with the
// limits as they stand, and no model call. It reports false for a request that
// is not one.
func (s *Server) serveBudgetAction(enc *json.Encoder, req protocol.PromptRequest) bool {
	if req.BudgetAction == "" {
		return false
	}
	resp := protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true}
	switch {
	case s.spend == nil:
		resp.Error, resp.ErrorClass = "this daemon keeps no spending limits", string(ClassInvalidRequest)
	case req.BudgetAction == protocol.BudgetActionStatus:
		resp.Spend = s.spend.status(sessionID(req.Session))
	case req.BudgetAction == protocol.BudgetActionMore:
		resp.Spend = s.spend.more(sessionID(req.Session))
		s.logger.Printf("budget: %s", resp.Spend.Notice)
	default:
		resp.Error = fmt.Sprintf("unknown budget action %q (want %s or %s)", truncateForClient(req.BudgetAction, 32),
			protocol.BudgetActionStatus, protocol.BudgetActionMore)
		resp.ErrorClass = string(ClassInvalidRequest)
	}
	if err := enc.Encode(resp); err != nil {
		s.logger.Printf("budget answer write error: %v", err)
	}
	return true
}

// features is what this daemon can do that an older one cannot, for the
// handshake. The spending limits are named only by a daemon that keeps them.
func (s *Server) features() []string {
	out := []string{protocol.FeatureSavedChats}
	if s.spend != nil {
		out = append(out, protocol.FeatureSpendLimits)
	}
	return out
}
