package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"encoding/json"

	"mochiii/editapply"
)

// THE CONVERGENCE JUDGE. A long task fixes, checks, and fixes again; the judge
// compares every check with the ones before it and says what the last attempt
// did: made the code better, made it worse, changed nothing, or went back to
// something it had already left. It is the comparator a self-correcting loop
// needs and the task did not have -- before it, an attempt that broke the
// build and one that fixed it both read as "an edit", so a task flipping
// between two failing fixes looked like progress.
//
// WHERE THE RULES COME FROM. CyberCorrect (arXiv 2605.17305) frames self-
// correction as a control loop: removing its convergence judge doubled
// overshoot (8.2% -> 17.5%) and nearly tripled oscillation (3.6% -> 10.3%).
// Reflexion's ablation is why the judge reads only GROUNDED signals -- a check
// that ran -- and never the model's opinion of its work: reflection without
// tests scored BELOW doing nothing (0.52 against 0.60, HumanEval-Rust), the
// agent "performing harmful edits" it could not tell were harmful.
//
// WHAT IT READS: the check's exit status and parsed output (checkparse.go) and
// a hash of the code it ran on. Nothing the model wrote about its own work.
//
// WHAT IT DOES depends on mcp.correction.mode. In "shadow" -- the default --
// it only records: its verdicts go to the task's ledger and the log, and
// neither the model nor the files see anything of it. In "on" the model sees
// what it measured: each failed check comes back with how it compares with the
// last run and where to repair, and every segment opens with the last failed
// checks and what the task already tried (Reflexion's memory: telling the
// model WHY an attempt failed beat replaying the attempt by 8% absolute); and
// the judge acts: it puts back the best code when attempts get worse, climbs
// the ladder when a failure keeps coming back, and runs a stale check again.
// Shadow is the default on purpose: the rules were tuned on reasoning
// benchmarks, not code, and a rule that reverts files is only worth having
// once its verdicts have been seen to be right on real tasks -- shape routing
// was falsified twice for acting on signals no one had checked.

// MCPCorrectionConfig is mcp.correction: whether the judge only watches or
// acts, how often a failure may come back, and the tier a task may move to.
type MCPCorrectionConfig struct {
	// Mode is "off", "shadow" or "on". Empty means "shadow": every check a
	// long task runs is read and judged, and the verdicts are kept in the
	// task's ledger and the log -- the model and the files see nothing of
	// them. "on" acts on them: a note under each failed check, the task's
	// memory of its failures, the best code put back, the ladder, re-checks.
	Mode string `json:"mode,omitempty"`
	// Attempts is how often the same failure may come back before the judge
	// steps in: at that count it asks the model to find out why, one more
	// starts the segment fresh, and two more stop the task. 0 means 3; 2-5.
	Attempts int `json:"attempts,omitempty"`
	// EscalateTo names a tier of the provider in use -- a stronger model --
	// that a long task moves to when a failure keeps coming back, or when the
	// provider keeps failing. Empty, the default: never.
	EscalateTo string `json:"escalate_to,omitempty"`
}

// resolvedAttempts is how often a failure may come back: 3 when unset, and
// never outside 2-5.
func (c MCPCorrectionConfig) resolvedAttempts() int {
	switch {
	case c.Attempts <= 0:
		return correctionRecurrence
	case c.Attempts < minCorrectionAttempts:
		return minCorrectionAttempts
	case c.Attempts > maxCorrectionAttempts:
		return maxCorrectionAttempts
	}
	return c.Attempts
}

// The bounds on mcp.correction.attempts: once is not a pattern, and Reflexion
// and CyberCorrect both stopped iterating by the fifth time.
const (
	minCorrectionAttempts = 2
	maxCorrectionAttempts = 5
)

// The correction modes.
const (
	correctionOff    = "off"
	correctionShadow = "shadow"
	correctionOn     = "on"
)

// resolvedMode is the mode in force: the default for an empty value, and for
// one the config check has already warned about.
func (c MCPCorrectionConfig) resolvedMode() string {
	switch c.Mode {
	case correctionOff, correctionShadow, correctionOn:
		return c.Mode
	}
	return correctionShadow
}

// Verdicts: what one check says about the attempt before it.
const (
	verdictFirst       = "first"       // the first result of its command: what the rest are compared with
	verdictConverged   = "converged"   // it passed
	verdictImproved    = "improved"    // better than the check before it
	verdictSame        = "same"        // the same failure as before
	verdictChanged     = "changed"     // as bad as before, but a different failure
	verdictOvershoot   = "overshoot"   // worse than the check before it
	verdictOscillation = "oscillation" // back to code, or a failure, it had moved away from
	verdictRecurrence  = "recurrence"  // the same failure for the correctionRecurrence'th time
	verdictFlaky       = "flaky"       // the same code and command gave a different result
	verdictUnknown     = "unknown"     // a timeout, or output nothing could read
)

const (
	// correctionRecurrence is when a failure that keeps coming back becomes a
	// pattern: Reflexion reflected after an action and its result repeated
	// more than three times; the third time is where this one speaks.
	correctionRecurrence = 3
	// maxCorrectionEvents bounds the verdicts a ledger keeps, newest last.
	maxCorrectionEvents = 40
	// maxTestFileBytes bounds reading a test file to see who wrote a test.
	maxTestFileBytes = 4 << 20
	// The task's memory of its failures, rendered into every segment's task
	// state: Reflexion bounded its memory to 1-3 experiences, because more
	// of them crowd the context the next attempt has to think in.
	maxCorrectionFrames = 3
	maxTriedNotes       = 20
	maxReflectionRunes  = 400
	maxTriedRunes       = 200
)

// taskCorrection is the judge's record in a task's ledger.
type taskCorrection struct {
	Mode   string            `json:"mode"`
	Events []correctionEvent `json:"events,omitempty"`
	Counts map[string]int    `json:"counts,omitempty"`
	// Frames are the last failed checks, newest last; Tried is everything the
	// task tried that did not work -- failed checks, refused edits, refused
	// finishes -- one line each, newest last.
	Frames []errorFrame `json:"frames,omitempty"`
	Tried  []string     `json:"tried,omitempty"`
}

// errorFrame is one failed check as the next attempt needs it: what ran, what
// failed and where, how it compares with the run before, where to repair --
// and, when the model said one, why it thinks it failed.
type errorFrame struct {
	Segment    int    `json:"segment"`
	Check      int    `json:"check"`
	Command    string `json:"command"`
	Summary    string `json:"summary"`
	Delta      string `json:"delta,omitempty"`
	Repair     string `json:"repair,omitempty"`
	Excerpt    string `json:"excerpt,omitempty"`
	Reflection string `json:"reflection,omitempty"`
}

// correctionEvent is one verdict about one check.
type correctionEvent struct {
	Segment int    `json:"segment"`
	Check   int    `json:"check"`
	Command string `json:"command"`
	Verdict string `json:"verdict"`
	Detail  string `json:"detail,omitempty"`
}

// checkScore is how bad a check's result is. Code that does not build is worse
// than any code that does; after that, the more failing tests the worse.
type checkScore struct {
	broken  int // build or vet errors
	failing int // failing tests; a panic counts at least one
}

func (a checkScore) worse(b checkScore) bool {
	if a.broken != b.broken {
		return a.broken > b.broken
	}
	return a.failing > b.failing
}

func (a checkScore) String() string {
	switch {
	case a.broken > 0:
		return plural(a.broken, "build error")
	case a.failing > 0:
		return plural(a.failing, "failing test")
	}
	return "passing"
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// score is an outcome's score, when it has one: a timeout or an output nothing
// recognised says the check failed, not how badly.
func (o checkOutcome) score() (checkScore, bool) {
	switch o.Kind {
	case checkPass:
		return checkScore{}, true
	case checkBuild, checkVet:
		return checkScore{broken: max(1, o.Errors)}, true
	case checkTest, checkPanic:
		return checkScore{failing: max(1, o.Tests)}, true
	}
	return checkScore{}, false
}

// commandTrack is the judge's memory of one command: results are compared
// only with results of the same command, never `go vet` with `go test`.
type commandTrack struct {
	have        bool
	last        checkScore
	lastFailing []string
	// sigs are the failing results' signatures, in order, for a failure that
	// comes back after another one.
	sigs  []string
	flaky bool
	// said is what each state of the code gave: one state, two answers is a
	// flaky command, whose score says nothing about an attempt.
	said map[string]string
	// best is the best this command has scored, at which check, on what code
	// -- and that code itself (bestSnap, taken by control), which is what a
	// restore puts back. worse counts the checks in a row worse than best.
	best      checkScore
	haveBest  bool
	bestCheck int
	bestState string
	bestSnap  *stageCheckpoint
	worse     int
}

// controlDecision is what the judge decided about one check, for control to
// do -- in mode "on" only.
type controlDecision struct {
	// newBest names the command whose best this check became: its code is
	// snapshotted, to be put back if later attempts make it worse.
	newBest string
	// restore is why to put that command's best code back, and command is
	// which; cut is why to end the segment; stop is why to stop the task.
	restore, command, cut, stop string
	// ladder is the step a failure that keeps coming back has reached.
	ladder int
}

// correctionJudge is one run's judge.
type correctionJudge struct {
	mode    string
	segment int
	checks  int
	tracks  map[string]*commandTrack
	// states is the state of the code at every check, in order.
	states   []string
	sigCount map[string]int
	// events are the verdicts not yet moved into the ledger; frames and tried
	// are the memory not yet moved there.
	events []correctionEvent
	frames []errorFrame
	tried  []string
	// note is what the last check's verdicts say to the model, when the
	// judge speaks to it at all (correcting).
	note string
	// awaiting is set while the newest frame waits for the model's own word
	// on why it failed: the text of its next reply (said).
	awaiting bool
	// attempts is how often a failure may come back before the ladder starts
	// (mcp.correction.attempts); decision is what the last check asks of
	// control.
	attempts int
	decision controlDecision
	// weakened is what edits since the last check did to the tests: a test
	// removed, a skip added -- the way a check can pass without a fix.
	weakened []string
	// ends is every state of the code a segment ended on, for progress.
	ends map[string]bool
	// wantReflection is the ladder asking the model outright why a failure
	// keeps coming back, at the segment's end (reflect).
	wantReflection bool
}

func newCorrectionJudge(mode string) *correctionJudge {
	return &correctionJudge{mode: mode, tracks: map[string]*commandTrack{}, sigCount: map[string]int{},
		attempts: correctionRecurrence, ends: map[string]bool{}}
}

// observe judges one check. restored is whether the model restored one of its
// own checkpoints since the last check; ownTest reports a failing test the
// model itself wrote.
func (j *correctionJudge) observe(c ranCheck, restored bool, ownTest func(string) bool) {
	j.checks++
	key := commandKey(c.command)
	tr := j.tracks[key]
	if tr == nil {
		tr = &commandTrack{said: map[string]string{}}
		j.tracks[key] = tr
	}
	add := func(verdict, detail string) {
		j.events = append(j.events, correctionEvent{Segment: j.segment, Check: j.checks, Command: key,
			Verdict: verdict, Detail: detail})
	}
	o := c.outcome

	// ONE STATE, ONE COMMAND, TWO ANSWERS: the command is flaky, and from
	// here on its score is not evidence about any attempt.
	answer := o.Kind + ":" + o.Signature
	if prior, ok := tr.said[c.stateHash]; ok && prior != answer && !tr.flaky {
		tr.flaky = true
		add(verdictFlaky, "the same code gave two different results")
	}
	tr.said[c.stateHash] = answer

	// BACK TO CODE IT HAD LEFT -- not by restoring a checkpoint, which is the
	// model's own way to abandon a hypothesis, and not to the untouched
	// project, which is starting over; a change that is undone and made again
	// is the circle.
	oscillated := false
	if n := len(j.states); n > 0 && j.states[n-1] != c.stateHash && !restored && c.stateHash != pristineState {
		for k := n - 2; k >= 0; k-- {
			if j.states[k] == c.stateHash {
				add(verdictOscillation, fmt.Sprintf("the code is back to how it was at check %d", k+1))
				oscillated = true
				break
			}
		}
	}
	j.states = append(j.states, c.stateHash)

	prevSig := lastOf(tr.sigs)
	score, known := o.score()
	if o.Kind != checkPass && o.Signature != "" {
		// A FAILURE THAT COMES BACK after a different one: X, then Y, then X
		// -- unless the code went back on purpose, by a checkpoint or to the
		// untouched project, where the old failure is what going back means.
		// Only failures that say what failed are in the trail: a timeout
		// between two of the same failure is not a different one.
		if known {
			if n := len(tr.sigs); n >= 2 && tr.sigs[n-2] == o.Signature && tr.sigs[n-1] != o.Signature &&
				!oscillated && !restored && c.stateHash != pristineState {
				add(verdictOscillation, "the failure is the one from two checks ago")
			}
			tr.sigs = append(tr.sigs, o.Signature)
		}
		j.sigCount[o.Signature]++
		if n := j.sigCount[o.Signature]; n == j.attempts {
			add(verdictRecurrence, fmt.Sprintf("the same failure for the %d%s time", n, ordinal(n)))
		}
	}

	j.decision = controlDecision{}
	if tr.flaky {
		j.remember(c, key, j.events[len(j.events)-countSince(j.events, j.checks):])
		return
	}
	judged := score
	switch {
	case !known:
		add(verdictUnknown, o.Kind)
	case o.Kind == checkPass:
		add(verdictConverged, "")
	case !tr.have:
		add(verdictFirst, score.String())
	default:
		// A test the model has just written, failing, is its reproduction of
		// the bug -- the first step of a fix, not a regression.
		own := 0
		for _, name := range newNames(o.Failing, tr.lastFailing) {
			if ownTest != nil && ownTest(name) {
				own++
			}
		}
		judged.failing = max(0, judged.failing-own)
		detail := tr.last.String() + " -> " + score.String()
		if own > 0 {
			detail += fmt.Sprintf(" (%d of them new tests of its own)", own)
		}
		switch {
		case tr.last.worse(judged):
			add(verdictImproved, detail)
		case judged.worse(tr.last):
			add(verdictOvershoot, detail)
		case o.Signature == prevSig:
			add(verdictSame, detail)
		default:
			add(verdictChanged, detail)
		}
	}
	j.decideControl(c, key, tr, score, judged, known, oscillated)
	if known {
		tr.have, tr.last, tr.lastFailing = true, score, o.Failing
	}
	j.remember(c, key, j.events[len(j.events)-countSince(j.events, j.checks):])
}

// decideControl is the control law: CyberCorrect's, for code. A new best is kept. A
// circle -- code it had left, a failure it had left -- goes back to the best
// and starts the segment fresh. Getting worse goes back to the best only on the
// SECOND check in a row worse than it, and only if that second is not better
// than the first: a refactor or a new signature breaks the build on its way
// and then mends it (5 errors, 3, 0), and a judge that reverted the first
// break would never let it through. A failure that keeps coming back climbs a
// ladder: think, then start fresh, then stop.
func (j *correctionJudge) decideControl(c ranCheck, key string, tr *commandTrack, score, judged checkScore, known, oscillated bool) {
	d := &j.decision
	if known {
		improving := tr.have && tr.last.worse(judged)
		if !tr.haveBest || tr.best.worse(score) {
			tr.best, tr.haveBest, tr.bestCheck, tr.bestState = score, true, j.checks, c.stateHash
			d.newBest = key
		}
		if judged.worse(tr.best) {
			tr.worse++
		} else {
			tr.worse = 0
		}
		away := tr.bestState != c.stateHash
		switch {
		case oscillated:
			d.cut = "the attempts went round in a circle"
			if away {
				d.restore = d.cut
			}
		case tr.worse >= 2 && !improving && away:
			d.restore = fmt.Sprintf("two checks in a row were worse than the best so far (%s, at check %d) and not getting better",
				tr.best, tr.bestCheck)
			d.cut = d.restore
		}
		if d.restore != "" {
			d.command = key
		}
	}
	if o := c.outcome; o.Kind != checkPass && o.Signature != "" {
		switch n := j.sigCount[o.Signature]; {
		case n >= j.attempts+2:
			d.stop = fmt.Sprintf("the same failure (%s) came back %d times", outcomeSummary(o), n)
		case n == j.attempts+1:
			d.ladder = 2
			if d.cut == "" {
				d.cut = fmt.Sprintf("the same failure came back %d times", n)
			}
		case n == j.attempts:
			d.ladder = 1
		}
	}
}

// countSince is how many of the newest events are about check n.
func countSince(events []correctionEvent, n int) int {
	k := 0
	for i := len(events) - 1; i >= 0 && events[i].Check == n; i-- {
		k++
	}
	return k
}

// remember turns one judged check into the task's memory: a frame and a line
// of "tried" when it failed, and the note the model reads under its result.
func (j *correctionJudge) remember(c ranCheck, key string, verdicts []correctionEvent) {
	j.note, j.awaiting = "", false
	weakened := strings.Join(j.weakened, "; ")
	j.weakened = nil
	o := c.outcome
	if o.Kind == checkPass {
		j.tried = append(j.tried, fmt.Sprintf("check %d: `%s` passed on code %s", j.checks, key, shortHash(c.stateHash)))
		if weakened != "" {
			// A PASS THAT COMES FROM A TEST NO LONGER RUNNING is the score
			// gamed, not the bug fixed: said, so it is not mistaken for one.
			j.note = "[Mochiii: this run passed, but since the last check " + weakened +
				". A pass that comes from a test no longer running is not a fix.]"
		}
		return
	}
	var said []string
	for _, ev := range verdicts {
		if phrase := verdictPhrase(ev); phrase != "" {
			said = append(said, phrase)
		}
	}
	f := errorFrame{Segment: j.segment, Check: j.checks, Command: key, Summary: outcomeSummary(o),
		Delta: strings.Join(said, "; "), Repair: repairFor(o), Excerpt: c.tail}
	j.frames = append(j.frames, f)
	if len(j.frames) > maxCorrectionFrames {
		j.frames = j.frames[len(j.frames)-maxCorrectionFrames:]
	}
	line := fmt.Sprintf("check %d: `%s` on code %s: %s", j.checks, key, shortHash(c.stateHash), f.Summary)
	if f.Delta != "" {
		line += " -- " + f.Delta
	}
	j.tried = append(j.tried, line)
	j.awaiting = true

	j.note = "[Mochiii measured this run"
	if f.Delta != "" {
		j.note += ": " + f.Delta
	}
	j.note += ".]"
	if f.Repair != "" {
		j.note += "\n[Next: " + f.Repair + "]"
	}
	if weakened != "" {
		j.note += "\n[Since the last check " + weakened + ".]"
	}
	j.note += "\n[Before your next edit, say in one line why it failed and what you will change.]"
}

// edited hears every edit the task makes in its working copy: one that takes
// a test out, or skips one, is remembered and said at the next check.
func (j *correctionJudge) edited(path, search, replace string) {
	if j == nil {
		return
	}
	if w := testsWeakened(search, replace); w != "" {
		what := "an edit to " + path + " " + w
		j.weakened = append(j.weakened, what)
		j.tried = append(j.tried, truncateRunes(what, maxTriedRunes))
	}
}

// testDecl is a Go test's declaration.
var testDecl = regexp.MustCompile(`func (Test[A-Za-z0-9_]*)\(`)

// testsWeakened says what an edit did to the tests it touched: removed a Go
// test, added a skip (Go or pytest) -- or "" for neither. Read from the edit's
// own text: what a test file is, is not asked.
func testsWeakened(search, replace string) string {
	had := map[string]bool{}
	for _, m := range testDecl.FindAllStringSubmatch(search, -1) {
		had[m[1]] = true
	}
	for _, m := range testDecl.FindAllStringSubmatch(replace, -1) {
		delete(had, m[1])
	}
	var parts []string
	if len(had) > 0 {
		parts = append(parts, "removed "+strings.Join(sortedSet(had), ", "))
	}
	skips := func(s string) int {
		return strings.Count(s, ".Skip") + strings.Count(s, "mark.skip") + strings.Count(s, "pytest.skip")
	}
	if skips(replace) > skips(search) {
		parts = append(parts, "added a skip")
	}
	return strings.Join(parts, " and ")
}

// noteTried records an attempt that did not work and was not a check: an edit
// the edit gates refused, a finish the finish gate refused.
func (j *correctionJudge) noteTried(what string) {
	if j != nil {
		j.tried = append(j.tried, truncateRunes(what, maxTriedRunes))
	}
}

// said is the text of the model's next reply after a failed check: its own
// word on why the check failed, kept with that check's frame.
func (j *correctionJudge) said(text string) {
	if j == nil || !j.awaiting || len(j.frames) == 0 {
		return
	}
	if text = strings.TrimSpace(text); text != "" {
		j.frames[len(j.frames)-1].Reflection = truncateRunes(text, maxReflectionRunes)
		j.awaiting = false
	}
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// outcomeSummary says what a failed check's output says failed, and where.
func outcomeSummary(o checkOutcome) string {
	var b strings.Builder
	if score, ok := o.score(); ok {
		b.WriteString(score.String())
	} else {
		b.WriteString(map[string]string{checkTimeout: "it timed out", checkOther: "it failed"}[o.Kind])
	}
	if len(o.Failing) > 0 {
		names := o.Failing
		if len(names) > 5 {
			names = names[:5]
		}
		b.WriteString(" (" + strings.Join(names, ", "))
		if o.Tests > len(names) {
			fmt.Fprintf(&b, " and %d more", o.Tests-len(names))
		}
		b.WriteString(")")
	}
	if len(o.Locations) > 0 {
		locs := o.Locations
		if len(locs) > 3 {
			locs = locs[:3]
		}
		b.WriteString(" at " + strings.Join(locs, ", "))
	}
	return b.String()
}

// verdictPhrase is one verdict as the model reads it.
func verdictPhrase(ev correctionEvent) string {
	switch ev.Verdict {
	case verdictFirst:
		return "the first run of this command in the task"
	case verdictImproved:
		return "better than the run before it (" + ev.Detail + ")"
	case verdictOvershoot:
		return "WORSE than the run before it (" + ev.Detail + ")"
	case verdictSame:
		return "the same failure as the run before it"
	case verdictChanged:
		return "as many failures as the run before it, but different ones (" + ev.Detail + ")"
	case verdictOscillation, verdictRecurrence:
		return ev.Detail
	case verdictFlaky:
		return "the same code gave two different results: this command is flaky, so its results are no longer compared"
	case verdictUnknown:
		return "a result that says it failed but not how"
	}
	return ""
}

// repairFor is CyberCorrect's type-directed repair, adapted to code: what a
// failure of this kind asks the next attempt to change, aimed at where the
// output says it is. The same outcome always gets the same words.
func repairFor(o checkOutcome) string {
	at := ""
	if len(o.Locations) > 0 {
		at = o.Locations[0]
	}
	name := ""
	if len(o.Failing) > 0 {
		name = o.Failing[0]
	}
	switch o.Kind {
	case checkBuild:
		if at != "" {
			return "it does not build: fix the error at " + at + " first, and change only that block"
		}
		return "it does not build: fix the first compile error before anything else"
	case checkVet:
		if at != "" {
			return "the build is fine, a check of it is not: fix what it reports at " + at
		}
		return "the build is fine, a check of it is not: fix the first thing it reports"
	case checkTest:
		switch {
		case name != "" && at != "":
			return name + " fails at " + at + ": fix the code it tests, not the test -- unless it is the test you are writing"
		case name != "":
			return name + " fails: read what it asserts, then fix the code it tests"
		}
		return "a test fails: find which one in the output, then fix the code it tests"
	case checkPanic:
		if at != "" {
			return "it panics at " + at + ": fix the cause there -- a nil, an index, a closed channel -- before anything else"
		}
		return "it panics: find the first frame in your code in the output and fix the cause there"
	case checkTimeout:
		return "the check timed out: look for a loop or a blocking call in what you changed, or run a narrower test"
	}
	return ""
}

// render writes the judge's memory into a segment's task state: the last
// failed checks -- what failed, how it compared, the model's own word on why,
// where to repair the newest -- and what the task already tried. A nil record
// writes nothing. The newest check's output is the "Last check" section's, so
// no frame repeats it.
func (c *taskCorrection) render(s *strings.Builder) {
	if c == nil {
		return
	}
	if len(c.Frames) > 0 {
		s.WriteString("\nYour last failed checks, as the daemon measured them, newest last:\n")
		for i, f := range c.Frames {
			fmt.Fprintf(s, "- check %d (segment %d), `%s`: %s", f.Check, f.Segment,
				neutralizeDelimiters(f.Command), neutralizeDelimiters(f.Summary))
			if f.Delta != "" {
				s.WriteString(" -- " + neutralizeDelimiters(f.Delta))
			}
			s.WriteString(".\n")
			if f.Reflection != "" {
				s.WriteString("  You said then: " + neutralizeDelimiters(f.Reflection) + "\n")
			}
			if i == len(c.Frames)-1 && f.Repair != "" {
				s.WriteString("  Next: " + neutralizeDelimiters(f.Repair) + ".\n")
			}
		}
	}
	if len(c.Tried) > 0 {
		s.WriteString("\nWhat this task has tried, newest last -- do not repeat what failed:\n")
		for _, t := range c.Tried {
			s.WriteString("- " + neutralizeDelimiters(t) + "\n")
		}
	}
}

// progress is the judge's part of a segment's progress mark: how many states
// of the code segments have ended on, and the best each command has scored.
// Going back to code seen before adds nothing -- which is what makes a task
// that alternates between two failing attempts read as going in circles,
// where counting its edits read as progress.
func (j *correctionJudge) progress(state string) string {
	j.ends[state] = true
	keys := make([]string, 0, len(j.tracks))
	for k := range j.tracks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "|n%d", len(j.ends))
	for _, k := range keys {
		if tr := j.tracks[k]; tr.haveBest {
			fmt.Fprintf(&b, "|b%s=%s", k, tr.best)
		}
	}
	return b.String()
}

// pristineState is the hash of no changes at all: the project as it is.
var pristineState = hex.EncodeToString(sha256.New().Sum(nil))[:16]

func lastOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}

func ordinal(n int) string {
	switch n {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	}
	return "th"
}

// newNames are the names in now that were not in before.
func newNames(now, before []string) []string {
	had := map[string]bool{}
	for _, n := range before {
		had[n] = true
	}
	var out []string
	for _, n := range now {
		if !had[n] {
			out = append(out, n)
		}
	}
	return out
}

// judging reports whether this run's checks are judged: a long task with a
// judge, other than a /hunt -- whose failing runs are the proof of what it
// found. Nothing else spends the time to read a check (recordCheck).
func (r *taskRun) judging() bool {
	return r != nil && r.judge != nil && r.ledger != nil && r.ledger.Mode != modeHunt
}

// judgedRun is what the daemon read of a check's run itself, for the judge:
// the exact arguments it was called with, its exit status, what its output
// says failed, the code it ran on, and the end of what the model was shown
// (for the check's frame).
type judgedRun struct {
	args      json.RawMessage
	exit      int
	outcome   checkOutcome
	stateHash string
	tail      string
}

// newCheck is the record of one command's run in the working copy -- read and
// hashed for the judge when the task is judged.
//
// PASSED IS THE EXIT STATUS, not the words the result starts with: those words
// sit at the head of the output, and a program can print them itself.
//
// A FAILING RUN IS NOT AN ERROR RESULT, and must never become one: a result
// marked IsError is not recorded at all, so a failure would vanish and an
// earlier pass would satisfy the finish gate (finishRefusal) -- the one
// outcome a check exists to prevent.
func (p *proposalSink) newCheck(command string, raw json.RawMessage, shown string, out execOutcome) ranCheck {
	c := ranCheck{command: command, passed: out.ran && !out.timedOut && out.exit == 0, edits: p.editCount(),
		judgedRun: judgedRun{args: append(json.RawMessage(nil), raw...), exit: out.exit,
			tail: lastNLines(ciANSI.ReplaceAllString(shown, ""), 12)}}
	if p.stage != nil && p.task.judging() {
		c.outcome = parseCheck(command, out, p.stage.toReal(out.output), p.stage.real)
		c.stateHash = p.stage.stateHash()
	}
	return c
}

// judgeRecorded hands a recorded check to the judge and lets it act. restored
// -- the model put back one of its own checkpoints since the last check, so
// returning to code it had is its method, not the circle the judge looks for
// -- is cleared before control, which may restore code and set it again.
func (p *proposalSink) judgeRecorded(c ranCheck) {
	judged := p.stage != nil && p.task.judging()
	if judged {
		p.task.judgeCheck(c, p.restored, p.stage.definesTest)
	}
	p.restored = false
	if judged {
		p.control()
	}
}

// editRefused and editApplied tell the judge about an edit to the working
// copy: a refused one joins what the task already tried, an applied one is
// weighed (a test removed or skipped is flagged). Nothing outside a judged task.
func (r *taskRun) editRefused(rel, why string) {
	if r.judging() {
		r.judge.noteTried("an edit to " + rel + " was refused: " + why)
	}
}

func (r *taskRun) editApplied(rel, search, replace string) {
	if r.judging() {
		r.judge.edited(rel, search, replace)
	}
}

// control does what the judge decided about the check just recorded: keeps a
// new best, puts the best back, ends the segment, climbs the ladder, stops the
// task. Mode "on" only -- and a restore only where the checkpoint tool may run
// unasked (mayRestore): the same consent a restore by the model would need.
// What it did is said under the check's result, so the model is never left
// working on files that changed beneath it without being told.
func (p *proposalSink) control() {
	run := p.task
	if !run.correcting() || p.stage == nil {
		return
	}
	j := run.judge
	d := j.decision
	j.decision = controlDecision{}
	restoresBefore := run.restores
	if d.newBest != "" {
		// A snapshot too large to take leaves the last one: a restore then
		// goes further back, never to code that was not the best.
		if snap, err := p.stage.snapshot("best"); err == nil {
			j.tracks[d.newBest].bestSnap = snap
		}
	}
	if d.restore != "" {
		tr := j.tracks[d.command]
		switch {
		case run.ledger.Mode == modeRefactor:
			// A refactor breaks code on its way to mending it; it is told,
			// not reverted.
			j.note += fmt.Sprintf("\n[The best so far was check %d (%s). A refactor breaks code on its way, so nothing "+
				"was put back; if this is not on its way back, checkpoint restore to where it last built.]", tr.bestCheck, tr.best)
		case !run.mayRestore || tr.bestSnap == nil || tr.bestSnap.reshaped != p.stage.reshaped:
			// No consent to revert, or the best was before files were deleted
			// or moved in the project: told, not reverted.
			j.note += fmt.Sprintf("\n[The best so far was check %d (%s). Go back to it if this is not on its way there.]",
				tr.bestCheck, tr.best)
		default:
			if before, err := p.stage.snapshot(autoRestoreName); err == nil {
				_, _ = p.stage.keepCheckpoint(before) // the way back from the judge
			}
			res, err := p.stage.restoreSnapshot(tr.bestSnap)
			if err != nil || res.IsError {
				break
			}
			p.restored = true
			j.noteTried(fmt.Sprintf("the judge put back the code of check %d (%s): %s", tr.bestCheck, tr.best, d.restore))
			j.note += fmt.Sprintf("\n[Mochiii put back the code of check %d, the best so far (%s), because %s. What you had "+
				"is saved: checkpoint restore %q brings it back.]", tr.bestCheck, tr.best, d.restore, autoRestoreName)
			run.restores++
		}
		// A segment is cut because the code changed beneath it. Nothing put
		// back, nothing changed: the model goes on, told where the best was.
		if run.restores == restoresBefore && d.cut == d.restore {
			d.cut = ""
		}
	}
	switch d.ladder {
	case 1:
		j.note += "\n[This failure has now come back " + plural(j.attempts, "time") + ". Before another edit, find out why: " +
			"call investigate with the question of why it keeps failing, then change approach.]"
		j.wantReflection = true
	case 2:
		if run.escalate != nil && run.escalate() {
			j.note += "\n[The next segment runs on a stronger model.]"
		}
		j.wantReflection = true
	}
	if d.stop != "" {
		run.stopWhy = d.stop
		j.note += "\n[The task stops here: " + d.stop + ".]"
		return
	}
	if d.cut != "" && run.cut == "" {
		run.cut = d.cut
		j.note += "\n[This segment ends here; the next starts from your task state, which records what failed.]"
	}
}

// correcting reports whether the judge speaks to the model: mode "on".
func (r *taskRun) correcting() bool {
	return r.judging() && r.judge.mode == correctionOn
}

// checkNote is what the model reads under a check it just ran, when the judge
// speaks to it: "" otherwise.
func (r *taskRun) checkNote() string {
	if !r.correcting() {
		return ""
	}
	return r.judge.note
}

// judgeCheck hands one recorded check to the run's judge, when it judges.
func (r *taskRun) judgeCheck(c ranCheck, restored bool, ownTest func(string) bool) {
	if r.judging() {
		r.judge.observe(c, restored, ownTest)
	}
}

// flushCorrection moves the verdicts and memory gathered since the last flush
// into the ledger and returns the verdicts, for the log. clean is what every
// command's output goes through before the ledger may hold it: it is sent to
// the model again in every later segment.
func (r *taskRun) flushCorrection(clean func(string) string) []correctionEvent {
	if r.judge == nil || len(r.judge.events)+len(r.judge.tried) == 0 {
		return nil
	}
	fresh := r.judge.events
	r.judge.events = nil
	c := r.ledger.Correction
	if c == nil {
		c = &taskCorrection{}
		r.ledger.Correction = c
	}
	if len(r.judge.frames) > 0 {
		// The newest frames replace the oldest, whichever run made them.
		for _, f := range r.judge.frames {
			f.Excerpt = clean(f.Excerpt)
			f.Reflection = clean(f.Reflection)
			c.Frames = append(c.Frames, f)
		}
		if over := len(c.Frames) - maxCorrectionFrames; over > 0 {
			c.Frames = append([]errorFrame(nil), c.Frames[over:]...)
		}
		r.judge.frames = nil
	}
	for _, t := range r.judge.tried {
		c.Tried = append(c.Tried, clean(t))
	}
	r.judge.tried = nil
	if over := len(c.Tried) - maxTriedNotes; over > 0 {
		c.Tried = append([]string(nil), c.Tried[over:]...)
	}
	c.Mode = r.judge.mode
	if c.Counts == nil {
		c.Counts = map[string]int{}
	}
	for _, ev := range fresh {
		c.Counts[ev.Verdict]++
	}
	c.Events = append(c.Events, fresh...)
	if over := len(c.Events) - maxCorrectionEvents; over > 0 {
		c.Events = append([]correctionEvent(nil), c.Events[over:]...)
	}
	return fresh
}

// stateHash names the code a check ran on: every file an edit or a command
// changed, as it is now -- leaving out any that is back to what the project
// holds, so code that is undone is the code it was. Two checks with the same
// hash ran on the same code.
func (st *stagedWorkspace) stateHash() string {
	paths := st.changedPaths()
	h := sha256.New()
	if st.reshaped > 0 {
		// A delete or a move the copy followed is a change no edited file
		// shows (stage_fileops.go): without it, the next check on the same
		// edits read as the same code giving another result -- flaky.
		_, _ = fmt.Fprintf(h, "reshaped\x00%d\x00", st.reshaped)
	}
	copyRoot, err := os.OpenRoot(st.root)
	if err != nil {
		return hex.EncodeToString(h.Sum(nil))[:16]
	}
	defer func() { _ = copyRoot.Close() }() // read-only
	realRoot, err := os.OpenRoot(st.real)
	if err != nil {
		realRoot = nil
	} else {
		defer func() { _ = realRoot.Close() }() // read-only
	}
	for _, rel := range paths {
		now, err := fileDigest(copyRoot, rel)
		if err != nil {
			now = "?" + err.Error()
		}
		if realRoot != nil {
			if was, err := fileDigest(realRoot, rel); err == nil && was == now {
				continue // back to the project's own
			}
		}
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00", filepath.ToSlash(rel), now)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// fileDigest is the sha256 of one file under root, or "" when it does not
// exist.
func fileDigest(root *os.Root, rel string) (string, error) {
	f, err := root.Open(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }() // read-only
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// definesTest reports whether a failing test is one the task wrote itself: a
// Go test whose func is in a Go file it changed, or a pytest test in a file it
// changed. Which files are Go is editapply.LanguageOf's to say; a TestX func
// outside a _test.go file is not a test, but nor can it be one that fails.
func (st *stagedWorkspace) definesTest(name string) bool {
	if path, _, ok := strings.Cut(name, "::"); ok {
		return st.touched[filepath.FromSlash(path)]
	}
	top, _, _ := strings.Cut(name, "/")
	if top == "" {
		return false
	}
	decl := "func " + top + "("
	for rel := range st.touched {
		if editapply.LanguageOf(rel) != editapply.LangGo {
			continue
		}
		data, exists, err := st.readCopyFile(rel, maxTestFileBytes)
		if err == nil && exists && strings.Contains(string(data), decl) {
			return true
		}
	}
	return false
}
