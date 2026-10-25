//go:build eval

// Does a change to a REAL tool's description or schema change which tool the
// model picks?
//
//	go test -tags eval -run TestProductionMenuSelection -v -timeout 90m ./daemon
//
// WHAT THIS CAN AND CANNOT TELL YOU -- read this before reading a green run.
//
//   - It CAN show that a change broke a case PROVEN sensitive: a case whose
//     paired degradation measurably drops its score in the same run. If such a
//     case falls below its floor after your change, your change made the model
//     choose worse, and the run fails.
//   - It CANNOT distinguish "this change is safe" from "this eval is blind to
//     it". A case that is not proven sensitive scores the same whether its
//     tool's description is good, vague or gone, so its staying green says
//     nothing about your change. MEASURED 2026-10-25 (docs/
//     TOOL_SELECTION_SENSITIVITY_2026-10-25.md): the tool's NAME decides the
//     choice almost everywhere. Replacing find_files's whole description with
//     "Project helper." moved nothing. Only a description stating a capability
//     the name does not imply -- run_command's "install packages (pip, npm,
//     cargo, brew...)" -- moved a score. So most cases here are NOT proven
//     sensitive, and a green run is NOT a safety proof for a change to them.
//
// The run prints, per case, whether its sensitivity was proven THIS RUN. Judge
// a change only by the cases marked PROVEN that touch the tool you changed.
//
// WHAT IT SENDS: what an ordinary agent turn's first model call sends -- the
// real menu (builtinTools -> builtinRegistry -> AdvertisedFor, so deny,
// sorting and the cap apply) and the real system message, built by the turn's
// own functions (prodmenu_test.go). prodmenu_e2e_test.go holds that builder to
// the real binary's request byte for byte. Both menus: unindexed and indexed.
// Through the production client (streamWithRetry), so the provider's dialect
// and retries are the daemon's.
//
// WHICH MODEL: the connected credential's, resolved as main resolves it -- the
// environment, else the key `connect` stored, with the model chosen for it. No
// key: the run SKIPS and says what is missing. It never passes without calls.
//
// THE SENSITIVITY RULE, declared before the first run: a case is PROVEN
// sensitive when its degraded arm scores lower than its intact arm at one-sided
// Fisher exact p < 0.05 (sensitivityAlpha), with at least prodMenuMinArm
// completed trials in each arm. The CONTROL case (the pip prompt, the one
// degradation measured to move a score) must be proven whenever degraded arms
// run: if it is not, the instrument could not detect anything this run, and it
// fails saying so.
//
// Settings: PRODMENU_EVAL_TRIALS (per arm, default 8; a case measured to be
// sensitive runs at least 20, see prodMenuSensitiveTrials), PRODMENU_EVAL_ARMS
// ("both", default, or "intact" for half the cost and no sensitivity claim),
// PRODMENU_EVAL_CASES (a regexp over case ids, e.g. "unindexed/pip"),
// PRODMENU_EVAL_PACE_MS (pause before each call, default 1000),
// PRODMENU_EVAL_MODEL (overrides the model, as MOCHIII_MODEL does).
//
// Kept beside, not instead of, toolcall_eval_test.go and toolmenu_eval_test.go:
// they measure tool-calling and the menu-size curve against fixed menus, which
// is what makes them comparable across months. This measures the menu that
// ships, which is what makes it see a change.
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	prodMenuDefaultTrials = 8
	// prodMenuMinArm is the fewest completed trials an arm needs before its
	// score may prove or disprove anything.
	prodMenuMinArm = 4
	// prodMenuRetries is how many times one trial is retried after a transport
	// failure, on top of streamWithRetry's own, each after prodMenuBackoff more
	// than the last. Sized for a provider that rate-limits: the first run lost
	// 2 of 10 degraded-arm trials to 429s with 2 retries 5s apart, and the
	// biased-sample guard rightly voided it.
	prodMenuRetries        = 4
	prodMenuBackoff        = 10 * time.Second
	prodMenuRequestTimeout = 120 * time.Second
)

type prodMenuArm struct {
	ran       bool
	correct   int
	attempted int
	transport int
	picks     map[string]int
}

func (a prodMenuArm) rate() float64 { return float64(a.correct) / float64(a.attempted) }

func (a prodMenuArm) score() string {
	if !a.ran {
		return "not run"
	}
	return fmt.Sprintf("%d/%d", a.correct, a.attempted)
}

func TestProductionMenuSelection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live model eval in -short mode")
	}

	trials := prodMenuDefaultTrials
	if v := os.Getenv("PRODMENU_EVAL_TRIALS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < prodMenuMinArm {
			t.Fatalf("PRODMENU_EVAL_TRIALS=%q: want an integer of at least %d", v, prodMenuMinArm)
		}
		trials = n
	}
	degradedArms := true
	switch v := os.Getenv("PRODMENU_EVAL_ARMS"); v {
	case "", "both":
	case "intact":
		degradedArms = false
	default:
		t.Fatalf("PRODMENU_EVAL_ARMS=%q: want both or intact", v)
	}
	filter := regexp.MustCompile("")
	if v := os.Getenv("PRODMENU_EVAL_CASES"); v != "" {
		filter = regexp.MustCompile(v)
	}
	pace := time.Second
	if v := os.Getenv("PRODMENU_EVAL_PACE_MS"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil || ms < 0 {
			t.Fatalf("PRODMENU_EVAL_PACE_MS=%q is not a number of milliseconds", v)
		}
		pace = time.Duration(ms) * time.Millisecond
	}

	// The credential is resolved BEFORE the state and config folders are moved
	// aside below, because the stored key lives under the real home.
	cfg := loadShippedConfig(t)
	credPath, err := credentialsPath()
	if err != nil {
		credPath = ""
	}
	cred := resolveProdMenuCredential(cfg, os.Getenv, credPath, strings.TrimSpace(os.Getenv("PRODMENU_EVAL_MODEL")))
	if cred.skip != "" {
		t.Skip(cred.skip)
	}
	setup := prodMenuTestSetup(t, cfg, cred.apiBase)
	setup.apiKey, setup.tierCfg, setup.chosenCfg = cred.apiKey, cred.tierCfg, cred.chosenCfg
	host := cred.apiBase
	if u, err := url.Parse(cred.apiBase); err == nil && u.Host != "" {
		host = u.Host
	}

	var cases []prodMenuCase
	for _, c := range prodMenuCases {
		if filter.MatchString(c.id()) {
			cases = append(cases, c)
		}
	}
	if len(cases) == 0 {
		t.Fatalf("PRODMENU_EVAL_CASES=%q matches no case", os.Getenv("PRODMENU_EVAL_CASES"))
	}

	quiet := log.New(io.Discard, "", 0)
	now := time.Now()
	logged := map[prodMenu]bool{}
	type outcome struct {
		c                prodMenuCase
		intact, degraded prodMenuArm
		stale            string
		p                float64
		proven           bool
	}
	var results []outcome
	var model string

	for _, c := range cases {
		req, err := newProdMenuServer(setup, c.menu).prodMenuFirstRequest(context.Background(), c.prompt, now)
		if err != nil {
			t.Fatalf("%s: building the request: %v", c.id(), err)
		}
		model = req.model
		if !logged[c.menu] {
			logged[c.menu] = true
			names := make([]string, 0, len(req.tools))
			for _, tool := range req.tools {
				names = append(names, bareToolName(tool.Function.Name))
			}
			t.Logf("%s menu: %d tools, digest %x, system message %d bytes: %s", c.menu, len(req.tools),
				sha256.Sum256(toolsJSON(t, req.tools)), len(req.messages[0].Content), strings.Join(names, " "))
		}
		if len(results) == 0 {
			t.Logf("model=%s (%s) via %s, key from %s: %d case(s) x %d trial(s) (cases measured sensitive %d) x %d arm(s)",
				req.model, cred.modelSource, host, cred.keyVia, len(cases), trials, max(trials, prodMenuSensitiveTrials),
				map[bool]int{true: 2, false: 1}[degradedArms])
		}

		caseTrials := max(trials, c.minTrials)
		run := func(tools []toolSpec) prodMenuArm {
			arm := prodMenuArm{ran: true, picks: map[string]int{}}
			for trial := 0; trial < caseTrials; trial++ {
				var calls []toolCall
				var err error
				for attempt := 0; attempt <= prodMenuRetries; attempt++ {
					time.Sleep(pace + time.Duration(attempt)*prodMenuBackoff)
					ctx, cancel := context.WithTimeout(context.Background(), prodMenuRequestTimeout)
					calls, err = streamWithRetry(ctx, cred.apiBase, cred.apiKey, req.model, req.messages, tools, req.routing,
						func(string) error { return nil }, nil, nil, nil, quiet)
					cancel()
					if err == nil {
						break
					}
					t.Logf("%s trial %d attempt %d: %v", c.id(), trial+1, attempt+1, err)
				}
				if err != nil {
					arm.transport++
					continue
				}
				arm.attempted++
				got := "(no tool)"
				if len(calls) > 0 {
					got = bareToolName(calls[0].Function.Name)
				}
				arm.picks[got]++
				for _, w := range c.want {
					if got == w {
						arm.correct++
						break
					}
				}
			}
			return arm
		}

		o := outcome{c: c, p: 1}
		o.intact = run(req.tools)
		degraded, staleErr := degradeMenu(req.tools, c.degrade)
		switch {
		case staleErr != nil:
			o.stale = staleErr.Error()
		case degradedArms:
			o.degraded = run(degraded)
		}
		if o.degraded.ran && o.intact.attempted >= prodMenuMinArm && o.degraded.attempted >= prodMenuMinArm {
			o.p = fisherOneSidedLess(o.intact.correct, o.intact.attempted, o.degraded.correct, o.degraded.attempted)
			o.proven = o.p < sensitivityAlpha
		}
		t.Logf("%-26s intact %-7s degraded %-7s p=%.4f  intact picks %v  degraded picks %v",
			c.id(), o.intact.score(), o.degraded.score(), o.p, o.intact.picks, o.degraded.picks)
		results = append(results, o)
	}

	// The report, then the verdicts: every failure is listed, not just the first.
	t.Log("")
	t.Logf("=== production-menu selection: model %s via %s, %d trial(s) per arm (cases measured sensitive %d) ===",
		model, host, trials, max(trials, prodMenuSensitiveTrials))
	t.Logf("%-26s %-8s %-8s %-8s %-11s %-6s %s", "case", "intact", "degraded", "p", "sensitivity", "floor", "verdict")
	var failures []string
	controlRan, controlProven := false, false
	baseline := map[prodMenu]*prodMenuArm{menuUnindexed: {}, menuIndexed: {}}
	var proven []string
	for _, o := range results {
		sens := "NOT PROVEN"
		switch {
		case o.stale != "":
			sens = "STALE"
		case !o.degraded.ran:
			sens = "NOT RUN"
		case o.proven:
			sens = "PROVEN"
			proven = append(proven, o.c.id())
		}
		verdict := "ok"
		switch {
		case o.intact.attempted == 0:
			verdict = "MEASURED NOTHING"
			failures = append(failures, fmt.Sprintf("%s: every trial failed in transport; the run measured nothing", o.c.id()))
		case float64(o.intact.transport)/float64(o.intact.transport+o.intact.attempted) > maxTransportErrorRate,
			o.degraded.ran && o.degraded.attempted+o.degraded.transport > 0 &&
				float64(o.degraded.transport)/float64(o.degraded.transport+o.degraded.attempted) > maxTransportErrorRate:
			verdict = "BIASED SAMPLE"
			failures = append(failures, fmt.Sprintf("%s: transport errors above %.0f%% of an arm's trials; the survivors are a biased sample, not a measurement",
				o.c.id(), maxTransportErrorRate*100))
		case o.intact.rate() < o.c.floor:
			verdict = "BELOW FLOOR"
			failures = append(failures, fmt.Sprintf("%s: intact menu scored %s, below its declared floor of %.0f%% -- the model now picks %v for %q",
				o.c.id(), o.intact.score(), o.c.floor*100, o.intact.picks, o.c.prompt))
		}
		if o.stale != "" {
			failures = append(failures, fmt.Sprintf("%s: its paired degradation no longer applies (%s), so its sensitivity cannot be tested -- the description changed; re-pair the case and re-measure",
				o.c.id(), o.stale))
		}
		if o.c.control {
			controlRan = true
			controlProven = o.proven
			if degradedArms && !o.proven && o.stale == "" {
				failures = append(failures, fmt.Sprintf("%s: the CONTROL was not proven sensitive (intact %s, degraded %s, p=%.4f). The one degradation measured to move a score did not move it, so this run could not have detected anything",
					o.c.id(), o.intact.score(), o.degraded.score(), o.p))
			}
		}
		b := baseline[o.c.menu]
		b.correct += o.intact.correct
		b.attempted += o.intact.attempted
		t.Logf("%-26s %-8s %-8s %-8s %-11s %-6s %s", o.c.id(), o.intact.score(), o.degraded.score(),
			strconv.FormatFloat(o.p, 'f', 4, 64), sens, fmt.Sprintf("%.0f%%", o.c.floor*100), verdict)
	}
	t.Log("")
	for _, menu := range []prodMenu{menuUnindexed, menuIndexed} {
		if b := baseline[menu]; b.attempted > 0 {
			t.Logf("baseline, %s menu (intact arms): %d/%d (%.1f%%)", menu, b.correct, b.attempted, 100*b.rate())
		}
	}
	sort.Strings(proven)
	t.Logf("proven sensitive this run (%d): %s", len(proven), strings.Join(proven, " "))
	t.Log("A case not marked PROVEN cannot tell a safe change from one this eval is blind to. Green is not a safety proof.")
	switch {
	case !controlRan:
		t.Log("THE CONTROL CASE DID NOT RUN (filtered out): nothing in this run checks that the instrument can detect a change.")
	case !degradedArms:
		t.Log("Degraded arms were not run (PRODMENU_EVAL_ARMS=intact): no case's sensitivity was tested, so only the floors were checked.")
	case controlProven:
		t.Log("The control was proven sensitive: this run could detect a change to the cases marked PROVEN.")
	}
	for _, f := range failures {
		t.Error(f)
	}
}
