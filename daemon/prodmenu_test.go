package main

// THE PRODUCTION TOOL MENU, AS A TURN SENDS IT.
//
// The shared half of the production-menu selection eval
// (prodmenu_eval_test.go), kept untagged so `make check` exercises it with no
// key and no network: the request builder, the cases and their paired
// degradations, the significance test, and the credential resolution.
//
// WHY A THIRD EVAL. toolcall_eval_test.go and toolmenu_eval_test.go build
// their tools by hand, and both for good reasons (see their headers): toolcall
// was written before the built-in tools existed, and toolmenu's size curve
// needs a fixed menu to stay comparable. But it means a change to a real
// tool's schema or description sends them a byte-identical request, so they
// pass for a reason unrelated to the change. This one sends what a turn sends.
//
// WHAT "WHAT A TURN SENDS" MEANS HERE: the first request of an ordinary agent
// turn (no mode, no history, no spec, no task), built by the functions the
// turn itself calls, in its order -- server.go's system prompt, then
// agentturn.go:97-103, then the start of runAgentLoop. TestTheProdMenu-
// BuilderMatchesTheRealDaemonsFirstRequest (prodmenu_e2e_test.go) holds the
// builder to the real binary's request, so a step added to the turn and not
// here fails a test instead of quietly making this a fourth hand-built menu.
//
// Two menus: UNINDEXED (no search_code, and grep's description says so) and
// INDEXED (search_code offered, grep's description names it). The indexed
// menu is an index that retrieved nothing for the prompt: a turn whose prompt
// drew chunks would also carry them in the user message, which this does not
// reproduce.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mochiii/daemon/mcp"
)

type prodMenu string

const (
	menuUnindexed prodMenu = "unindexed"
	menuIndexed   prodMenu = "indexed"
)

// prodMenuNoIndexReason stands in for retrieval.DisabledReason in a project
// with no index. Only its being non-empty matters (searchByMeaning).
const prodMenuNoIndexReason = "no index in this workspace"

// prodMenuSetup is what main resolves before it builds the Server: the config,
// the tier configs the connected credential implies, and the credential.
type prodMenuSetup struct {
	cfg, tierCfg, chosenCfg *Config
	apiBase, apiKey         string
	workspace               string
}

// newProdMenuServer is builtinTestServer's shape (mcpruntime_test.go) with the
// fields a turn's first request reads.
func newProdMenuServer(setup prodMenuSetup, menu prodMenu) *Server {
	s := &Server{
		cfg:          setup.cfg,
		tierCfg:      setup.tierCfg,
		apiBase:      setup.apiBase,
		apiKey:       setup.apiKey,
		workspace:    setup.workspace,
		systemPrompt: defaultSystemPrompt,
		logger:       log.New(io.Discard, "", 0),
		serverExtras: serverExtras{chosenCfg: setup.chosenCfg},
	}
	if menu == menuUnindexed {
		s.retrievalDisabledReason = prodMenuNoIndexReason
	}
	return s
}

// prodMenuRequest is one first request: what streamWithRetry is called with.
type prodMenuRequest struct {
	model    string
	routing  providerRouting
	messages []chatMessage
	tools    []toolSpec
}

// prodMenuFirstRequest builds the first request of an ordinary agent turn for
// prompt, as of now. Each step names where the turn does the same thing.
func (s *Server) prodMenuFirstRequest(ctx context.Context, prompt string, now time.Time) (prodMenuRequest, error) {
	// server.go: the tier and its routing, for a turn the router sends to the
	// default tier.
	tiers := s.tierConfig()
	req := prodMenuRequest{model: tiers.ResolvedSlug(), routing: tiers.routingFor(tiers.DefaultTier)}

	// server.go, before the agent fork: the system prompt for this turn and the
	// message list.
	system := systemPromptForTurn(s.systemPrompt, holdsRetrievedContext(nil, prompt), s.hasThirdPartyServer())
	system = s.withUserRules(planModeSystemPrompt(system, ""))
	messages := s.withRulesBesideTheQuestion(buildChatMessages(system, nil, prompt))

	// agentturn.go: the sink, the registry, and the three notes (97-103).
	proposals, messages := s.newTurnSink("", nil, messages)
	defer proposals.discard()
	registry, errs := s.buildRegistry(ctx, s.logger, proposals, "")
	defer func() { _ = registry.Close() }()
	if len(errs) > 0 {
		return req, fmt.Errorf("building the registry: %v", errs)
	}
	messages = s.withMachineReach(ctx, registry, "", messages)
	messages = s.withAgentSetup(ctx, registry, "", req.model, messages)
	messages = compactTurnMessages(ctx, messages)

	// agentloop.go, runAgentLoop before its first call: the menu, find_tools
	// when the cap left anything out, and the date note.
	tools, _, listErrs := s.advertisedToolSpecsFor(ctx, registry, nil, menuQuery(messages))
	if len(listErrs) > 0 {
		return req, fmt.Errorf("listing tools: %v", listErrs)
	}
	for _, t := range tools {
		if server, _, err := mcp.SplitQualifiedName(t.Function.Name); err != nil || server != mcp.BuiltinServerName {
			return req, fmt.Errorf("%s is not a built-in tool; this eval measures the daemon's own menu", t.Function.Name)
		}
	}
	if dropped := registry.DroppedTools(); len(dropped) > 0 {
		if search := newToolSearch(dropped, func(t mcp.Tool) bool { return !modeWithholds("", t) }); search != nil {
			tools = append(tools, search.spec())
		}
	}
	req.messages, _ = applyTurnContext(messages, webToolOffered(tools), now)
	req.tools = tools
	return req, nil
}

// prodMenuDegradation is one deliberate change to one real tool's
// description: old, which must occur in it exactly once, becomes new.
type prodMenuDegradation struct {
	tool, old, new string
}

// bareToolName is a tool's name without its server, as cases name them.
func bareToolName(qualified string) string {
	if _, name, err := mcp.SplitQualifiedName(qualified); err == nil {
		return name
	}
	return qualified
}

// toolOnMenu reports whether a tool by this bare name is on the menu.
func toolOnMenu(tools []toolSpec, name string) bool {
	for _, t := range tools {
		if bareToolName(t.Function.Name) == name {
			return true
		}
	}
	return false
}

// degradeMenu returns a copy of tools with d applied, leaving tools alone, or
// an error naming why d does not apply: its tool is not on the menu, or its
// text is not in the description exactly once. An error is a STALE pairing --
// the description changed under it -- and is never treated as a degradation
// that happened to move nothing.
func degradeMenu(tools []toolSpec, d prodMenuDegradation) ([]toolSpec, error) {
	out := append([]toolSpec(nil), tools...)
	for i, t := range out {
		if bareToolName(t.Function.Name) != d.tool {
			continue
		}
		switch n := strings.Count(t.Function.Description, d.old); n {
		case 1:
			out[i].Function.Description = strings.Replace(t.Function.Description, d.old, d.new, 1)
			return out, nil
		case 0:
			return nil, fmt.Errorf("%s's description no longer contains %q", d.tool, d.old)
		default:
			return nil, fmt.Errorf("%s's description contains %q %d times; a degradation must name one place", d.tool, d.old, n)
		}
	}
	return nil, fmt.Errorf("%s is not on the menu", d.tool)
}

// prodMenuCase is one prompt with one defensible answer, on one menu, and the
// degradation paired with it.
//
// floor is the share of trials the intact menu must get right, DECLARED on
// 2026-10-25 from docs/TOOL_SELECTION_SENSITIVITY_2026-10-25.md before this
// eval first ran, and not to be lowered to pass a run: a case that falls below
// it scored worse than it was measured to. A floor marked "carried" was
// measured on the other menu only.
type prodMenuCase struct {
	name    string
	menu    prodMenu
	prompt  string
	want    []string
	degrade prodMenuDegradation
	floor   float64
	// control marks the case the instrument itself is checked by: the one
	// degradation measured to move the score. A run whose control is not
	// proven sensitive could not have detected anything, and fails as such.
	control bool
	// minTrials is the fewest trials an arm of this case runs, whatever
	// PRODMENU_EVAL_TRIALS says; zero for none.
	minTrials int
}

// prodMenuSensitiveTrials is the trials an arm a case measured to be
// sensitive runs at least. Ten was not enough: the control measured 10/10
// against 2/8 (p=0.0015) in one run and 7/10 against 4/10 (p=0.18) in the
// next, minutes apart; and indexed/pip measured 3/8 in a full run, below its
// floor, then 15/20 at twenty trials. At an intact rate near 75% a short arm
// draws a bad score often. A case that can detect a change is the case that
// cannot be underpowered.
const prodMenuSensitiveTrials = 20

func (c prodMenuCase) id() string { return string(c.menu) + "/" + c.name }

// The degradations, each written against today's description text.
var (
	degradeRunCommandCapabilities = prodMenuDegradation{"run_command",
		"install packages (pip, npm, cargo, brew...), run scripts, use git, start a build. ", ""}
	degradeGrepUnindexed = prodMenuDegradation{"grep",
		"Use it to find where a name is defined or used, where an error message comes from, or a string to change everywhere. ", ""}
	degradeGrepIndexed = prodMenuDegradation{"grep",
		"Use it for every use of a name, where an error message comes from, or a string to change everywhere: " +
			"search_code finds code by meaning, grep finds all of it exactly. ", ""}
	degradeFindFilesVague = prodMenuDegradation{"find_files",
		"Find files in the project by name. A plain word matches any file whose name contains it, ignoring case " +
			"(\"auth\" finds AuthService.ts and auth_test.go). A glob matches the path: * within one folder, ** across " +
			"folders, ? one character (\"**/*.test.ts\", \"cmd/*/main.go\"). Lists project-relative paths; it searches " +
			"your working copy, so it sees files you created.", "Project helper."}
	degradeReadFileVague = prodMenuDegradation{"read_file",
		"Read a UTF-8 text file. A workspace-relative path reads from the workspace. An absolute or ~/ path reads " +
			"anywhere else on the user's machine: the user is asked first, and private keys and credential stores are " +
			"always refused. start_line and end_line (1-based, inclusive) return just those lines, numbered: the way " +
			"to read part of a file, or a file past 64 KB.", "Does things with files."}
	degradeReadFileOutside = prodMenuDegradation{"read_file",
		"An absolute or ~/ path reads anywhere else on the user's machine: the user is asked first, and private keys " +
			"and credential stores are always refused. ", ""}
	degradeListDirectoryOutside = prodMenuDegradation{"list_directory",
		" A workspace-relative path lists the workspace; an absolute or ~/ path (for example ~/Desktop) lists " +
			"anywhere else on the user's machine after the user approves.", ""}
	degradeProposeEditHome = prodMenuDegradation{"propose_edit",
		" A workspace-relative path edits the project; a ~/ path (for example ~/Desktop/notes/todo.md) edits or " +
			"creates a file anywhere in the user's home folder, creating folders as needed -- except hidden files and " +
			"folders, ~/bin and .desktop files, which are always refused.", ""}
	// The working-copy sentence only: it is the same on every host, where the
	// confinement sentence beside it is not (sandboxConfinementSentence).
	degradeSandboxExecCopy = prodMenuDegradation{"sandbox_exec",
		"It runs in a private copy of the project when one can be made: it cannot touch your real files, " +
			"and a change it makes to one of them is offered to you for review, named as the command's. ", ""}
)

// prodMenuCases is the fixed case set. Fixed, so runs are comparable. Each
// comment gives the 2026-10-25 measurement (intact -> degraded, nemotron-3-
// ultra-550b-a55b, transport errors excluded) the floor was set from.
var prodMenuCases = func() []prodMenuCase {
	// Cases that do not depend on search_code run on both menus.
	both := []prodMenuCase{
		// 8/10 -> 1/7: the one degradation measured to move a score.
		{name: "pip", prompt: "Install the Python package requests for this project.",
			want: []string{"run_command"}, degrade: degradeRunCommandCapabilities, floor: 0.5, control: true,
			minTrials: prodMenuSensitiveTrials},
		// 9/9 -> 4/4.
		{name: "git_log", prompt: "Show me the last three commits in this repository.",
			want: []string{"run_command"}, degrade: degradeRunCommandCapabilities, floor: 0.75},
		// 5/5 -> 5/5, with the whole description replaced.
		{name: "file_by_name", prompt: "Where is the file called ratelimit.go in this project?",
			want: []string{"find_files"}, degrade: degradeFindFilesVague, floor: 0.75},
		// 5/5 -> 4/4.
		{name: "glob", prompt: "Find all files matching *_test.go anywhere under the proxy folder.",
			want: []string{"find_files"}, degrade: degradeFindFilesVague, floor: 0.75},
		// 5/5 -> 4/4.
		{name: "read_range", prompt: "Show me lines 120 to 160 of daemon/agentloop.go.",
			want: []string{"read_file"}, degrade: degradeReadFileVague, floor: 0.75},
		// 5/5 -> 5/5 (the preview also removed the confinement sentence).
		{name: "sandboxed_tests", prompt: "Run the Go tests for ./editapply in a confined copy, so nothing they do can touch my real project.",
			want: []string{"sandbox_exec"}, degrade: degradeSandboxExecCopy, floor: 0.75},
		// 5/5 -> 5/5.
		{name: "outside_read", prompt: "What does ~/Documents/meeting-notes.txt say?",
			want: []string{"read_file"}, degrade: degradeReadFileOutside, floor: 0.75},
		// 5/5 -> 4/4.
		{name: "outside_list", prompt: "What's in my ~/Downloads folder?",
			want: []string{"list_directory"}, degrade: degradeListDirectoryOutside, floor: 0.75},
		// 4/4 -> 5/5.
		{name: "home_create", prompt: "Create a file ~/Desktop/todo.md containing the line: buy milk",
			want: []string{"propose_edit"}, degrade: degradeProposeEditHome, floor: 0.75},
	}
	var cases []prodMenuCase
	for _, menu := range []prodMenu{menuUnindexed, menuIndexed} {
		for _, c := range both {
			c.menu = menu
			// The control is the menu it was measured on.
			c.control = c.control && menu == menuUnindexed
			if c.name == "pip" && menu == menuIndexed {
				// MEASURED ON THIS MENU 2026-10-25, 20 trials an arm: intact
				// 15/20, degraded 3/20, one-sided Fisher p=0.0002 -- proven
				// sensitive. (A full run's 3/8 here, below the 50% floor then
				// carried from the unindexed menu, was a short arm's bad draw.)
				// The floor is that measurement's one-sided 95% lower
				// confidence bound, 0.544, rounded down: the measured 75%
				// itself would fail 38% of runs with nothing changed.
				c.floor = 0.54
			}
			cases = append(cases, c)
		}
	}
	return append(cases,
		// 5/5 -> 5/5 and 5/5 -> 5/5 (three prompts, 15/15 -> 15/15).
		prodMenuCase{name: "error_origin", menu: menuUnindexed, prompt: "Where does the error message \"workspace lock held\" come from?",
			want: []string{"grep"}, degrade: degradeGrepUnindexed, floor: 0.75},
		prodMenuCase{name: "type_defined", menu: menuUnindexed, prompt: "Which file defines the type named Inventory?",
			want: []string{"grep"}, degrade: degradeGrepUnindexed, floor: 0.75},
		// 5/5 -> 4/4: the brief's own example, the line that tells grep from
		// search_code.
		prodMenuCase{name: "every_use", menu: menuIndexed, prompt: "Find every use of the identifier resolvedMaxToolResultBytes across the project.",
			want: []string{"grep"}, degrade: degradeGrepIndexed, floor: 0.75},
		// 5/5 -> 5/5.
		prodMenuCase{name: "rename_prep", menu: menuIndexed, prompt: "I want to rename the function fooBar everywhere. First list every line that mentions it.",
			want: []string{"grep"}, degrade: degradeGrepIndexed, floor: 0.75},
		// 4/5 -> 2/4.
		prodMenuCase{name: "by_meaning", menu: menuIndexed, prompt: "Where is the code that decides how fast a client may send requests?",
			want: []string{"search_code"}, degrade: degradeGrepIndexed, floor: 0.5},
	)
}()

// prodMenuWorkspace is the project the eval's turns are about: small, and with
// no path a refusal rule would catch.
var prodMenuWorkspace = map[string]string{
	"go.mod":    "module shop\n\ngo 1.22\n",
	"main.go":   "package main\n\nfunc main() { println(Total(3, 250)) }\n",
	"price.go":  "package main\n\n// Total is qty units at unit cents.\nfunc Total(qty, unit int) int { return qty * unit }\n",
	"README.md": "# shop\n\nA tiny example project.\n",
}

// sensitivityAlpha is the one-sided significance a degradation must reach to
// count as PROVEN to drop a case's score. Declared before the first run.
const sensitivityAlpha = 0.05

// fisherOneSidedLess is the one-sided Fisher exact p-value that the degraded
// arm's success rate is lower than the intact arm's: the probability, with
// both arms' totals fixed, of the degraded arm getting degOK or fewer right.
func fisherOneSidedLess(intactOK, intactN, degOK, degN int) float64 {
	total, successes := intactN+degN, intactOK+degOK
	logChoose := func(n, k int) float64 {
		a, _ := math.Lgamma(float64(n + 1))
		b, _ := math.Lgamma(float64(k + 1))
		c, _ := math.Lgamma(float64(n - k + 1))
		return a - b - c
	}
	p := 0.0
	for x := max(0, successes-intactN); x <= degOK; x++ {
		p += math.Exp(logChoose(successes, x) + logChoose(total-successes, degN-x) - logChoose(total, degN))
	}
	return math.Min(p, 1)
}

// prodMenuCredential is the endpoint, key and model the eval calls, resolved
// the way main resolves them, or skip saying what is missing.
type prodMenuCredential struct {
	apiBase, apiKey     string
	tierCfg, chosenCfg  *Config
	skip                string
	modelSource, keyVia string
}

// resolveProdMenuCredential mirrors main.go's resolution (146-286) with the
// same helpers: the environment first, then the key `connect` stored; the
// provider's own models when models.json was written for another; the model
// the user chose. modelOverride, when set, wins as MOCHIII_MODEL does.
func resolveProdMenuCredential(cfg *Config, getenv func(string) string, credPath, modelOverride string) prodMenuCredential {
	useProxy := getenv("MOCHIII_USE_PROXY") == "true"
	c := prodMenuCredential{apiBase: getenv("MOCHIII_API_BASE"), apiKey: getenv("MOCHIII_API_KEY"), keyVia: "MOCHIII_API_KEY"}
	if useProxy {
		c.apiKey, c.keyVia = getenv("MOCHIII_PROXY_KEY"), "MOCHIII_PROXY_KEY"
	}
	var connected storedCredential
	if !useProxy && (c.apiKey == "" || c.apiBase == "") && credPath != "" {
		stored, _, _ := loadCredential(credPath)
		envKey := c.apiKey
		c.apiKey, c.apiBase = fillFromStored(c.apiKey, c.apiBase, stored, func(string, ...any) {})
		if envKey == "" && stored.inForce(c.apiKey, c.apiBase) {
			connected, c.keyVia = stored, "the key stored by connect"
		}
	}
	if c.apiBase == "" {
		if useProxy {
			c.skip = "MOCHIII_USE_PROXY is set but MOCHIII_API_BASE is empty; the proxy's address cannot be guessed"
			return c
		}
		c.apiBase = defaultAPIBase
	}
	if c.apiKey == "" && !connected.keylessLocal() {
		c.skip = fmt.Sprintf("no model key: MOCHIII_API_KEY is unset and %s holds no key for %s -- set "+
			"MOCHIII_API_KEY (and MOCHIII_API_BASE), or run `mochiii-daemon connect`. This eval makes real "+
			"billed calls and will not run without one.", credPath, c.apiBase)
		return c
	}
	c.modelSource = "models.json"
	modelNamed := modelOverride != ""
	if modelNamed {
		cfg.UseOnlyModel(modelOverride, "PRODMENU_EVAL_MODEL")
		c.modelSource = "PRODMENU_EVAL_MODEL"
	}
	effective := cfg
	if !modelNamed && (!usesRoutingDialect(c.apiBase) || connected.keylessLocal()) {
		if c.tierCfg = providerTierConfig(cfg, providerName(c.apiBase), connected.Models, connected.QuietModels, connected.DefaultModel); c.tierCfg != nil {
			effective, c.modelSource = c.tierCfg, "the provider's models, as listed when the key was connected"
		}
	}
	if chosen := strings.TrimSpace(connected.ChosenModel); chosen != "" && !modelNamed {
		if c.chosenCfg = withDefaultTier(effective, chosen); c.chosenCfg != nil {
			c.modelSource = "the model chosen with /model"
		}
	}
	return c
}

// ---------------------------------------------------------------------------
// Offline checks: no key, no network, run by make check.
// ---------------------------------------------------------------------------

// prodMenuTestSetup is a Server setup on a fresh workspace, with the state and
// config folders a turn reads moved somewhere private.
func prodMenuTestSetup(t *testing.T, cfg *Config, apiBase string) prodMenuSetup {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	ws := realTempDir(t)
	for rel, content := range prodMenuWorkspace {
		writeTempFile(t, ws, rel, content)
	}
	return prodMenuSetup{cfg: cfg, apiBase: apiBase, workspace: ws}
}

func loadShippedConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := LoadConfig("../models.json")
	if err != nil {
		t.Fatalf("loading models.json: %v", err)
	}
	if !cfg.MCP.Enabled {
		t.Fatal("models.json does not enable agent mode, so a turn would send no tools at all")
	}
	return cfg
}

// Every case's degradation applies to today's description, exactly once, and
// every answer it accepts is on its menu -- checked here, offline, so a
// description edit that strands a pairing fails make check rather than
// surfacing as a quiet "not proven" in a billed run.
func TestEveryProdMenuCaseIsPairedWithTextThatExists(t *testing.T) {
	cfg := loadShippedConfig(t)
	setup := prodMenuTestSetup(t, cfg, "https://example.invalid/v1")
	menus := map[prodMenu]prodMenuRequest{}
	for _, menu := range []prodMenu{menuUnindexed, menuIndexed} {
		req, err := newProdMenuServer(setup, menu).prodMenuFirstRequest(context.Background(), "hello there, a question", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		menus[menu] = req
	}
	// The two menus are the two the eval claims: search_code on one only.
	if toolOnMenu(menus[menuUnindexed].tools, "search_code") || !toolOnMenu(menus[menuIndexed].tools, "search_code") {
		t.Fatal("search_code should be on the indexed menu and only there")
	}

	controls := 0
	for _, c := range prodMenuCases {
		if c.control {
			controls++
		}
		tools := menus[c.menu].tools
		for _, w := range c.want {
			if !toolOnMenu(tools, w) {
				t.Errorf("%s: the answer %s is not on the %s menu", c.id(), w, c.menu)
			}
		}
		degraded, err := degradeMenu(tools, c.degrade)
		if err != nil {
			t.Errorf("%s: stale pairing: %v", c.id(), err)
			continue
		}
		if string(toolsJSON(t, degraded)) == string(toolsJSON(t, tools)) {
			t.Errorf("%s: the degradation changed nothing", c.id())
		}
		// A greeting is answered without tools at all (smalltalk.go), so a
		// case worded as one would measure the greeting path.
		if isGreetingOnly(c.prompt) {
			t.Errorf("%s: the prompt is answered as a greeting, with no tools", c.id())
		}
		if c.floor <= 0 || c.floor > 1 {
			t.Errorf("%s: floor %v is not a share", c.id(), c.floor)
		}
	}
	if controls != 1 {
		t.Errorf("%d control case(s); the instrument is checked by exactly one", controls)
	}
	for _, c := range prodMenuCases {
		if c.control && c.minTrials < prodMenuSensitiveTrials {
			t.Errorf("%s: the control runs %d trials an arm; it needs at least %d", c.id(), c.minTrials, prodMenuSensitiveTrials)
		}
	}

	// Positive controls for the checks above: a stale pairing, an absent tool
	// and a greeting are each caught.
	if _, err := degradeMenu(menus[menuUnindexed].tools, prodMenuDegradation{"read_file", "no such sentence anywhere", ""}); err == nil {
		t.Error("degradeMenu accepted text that is not in the description")
	}
	if _, err := degradeMenu(menus[menuUnindexed].tools, prodMenuDegradation{"search_code", "Search", ""}); err == nil {
		t.Error("degradeMenu accepted a tool that is not on the menu")
	}
	if !isGreetingOnly("thanks!") {
		t.Error("the greeting check does not catch a greeting")
	}
}

// degradeMenu changes the copy and only the named description.
func TestDegradeMenuLeavesTheOriginalAlone(t *testing.T) {
	tools := []toolSpec{
		{Type: "function", Function: toolSpecFunction{Name: "builtin__run_command", Description: "a b c"}},
		{Type: "function", Function: toolSpecFunction{Name: "builtin__grep", Description: "a b c"}},
	}
	got, err := degradeMenu(tools, prodMenuDegradation{"run_command", "b ", ""})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Function.Description != "a c" || got[1].Function.Description != "a b c" {
		t.Errorf("degraded = %q, %q; want only run_command changed", got[0].Function.Description, got[1].Function.Description)
	}
	if tools[0].Function.Description != "a b c" {
		t.Error("degradeMenu changed the menu it was given")
	}
	if _, err := degradeMenu(tools, prodMenuDegradation{"grep", "a", ""}); err != nil {
		t.Errorf("a single occurrence was refused: %v", err)
	}
	if _, err := degradeMenu(append(tools, toolSpec{Function: toolSpecFunction{Name: "builtin__x", Description: "q q"}}),
		prodMenuDegradation{"x", "q", ""}); err == nil {
		t.Error("text occurring twice was accepted")
	}
}

// The significance test against values worked by hand.
func TestFisherOneSidedLess(t *testing.T) {
	for _, tc := range []struct {
		a, n1, c, n2 int
		want         float64
	}{
		{5, 5, 0, 5, 1.0 / 252},      // the most extreme table of 10: C(5,5)C(5,0)/C(10,5)
		{8, 10, 1, 7, 260.0 / 19448}, // the pip measurement: (C(9,0)C(8,7) + C(9,1)C(8,6)) / C(17,7)
		{5, 5, 5, 5, 1},              // no difference
		{3, 5, 3, 5, 186.0 / 252},    // equal rates: (6 + 60 + 120) / C(10,5)
		{0, 5, 5, 5, 1},              // degraded better: nothing to prove
	} {
		got := fisherOneSidedLess(tc.a, tc.n1, tc.c, tc.n2)
		if math.Abs(got-tc.want) > 0.0005 {
			t.Errorf("fisher(%d/%d vs %d/%d) = %.5f, want %.5f", tc.a, tc.n1, tc.c, tc.n2, got, tc.want)
		}
	}
	// What the alpha means at the eval's default of 8 trials an arm: 6/8
	// against 1/8 is proven, 6/8 against 4/8 is not.
	if p := fisherOneSidedLess(6, 8, 1, 8); p >= sensitivityAlpha {
		t.Errorf("6/8 vs 1/8: p=%.4f, want below %.2f", p, sensitivityAlpha)
	}
	if p := fisherOneSidedLess(6, 8, 4, 8); p < sensitivityAlpha {
		t.Errorf("6/8 vs 4/8: p=%.4f, want at or above %.2f", p, sensitivityAlpha)
	}
}

// With no key anywhere the eval is told what is missing; with one stored by
// connect it resolves that key's address and the model chosen for it.
func TestProdMenuCredentialSkipsWithoutAKeyAndResolvesAStoredOne(t *testing.T) {
	noEnv := func(string) string { return "" }
	missing := filepath.Join(t.TempDir(), "absent.json")
	c := resolveProdMenuCredential(loadShippedConfig(t), noEnv, missing, "")
	if c.skip == "" || !strings.Contains(c.skip, "MOCHIII_API_KEY") || !strings.Contains(c.skip, missing) {
		t.Errorf("skip = %q; want it to name MOCHIII_API_KEY and %s", c.skip, missing)
	}

	// Positive control: a stored key for a provider that is not OpenRouter,
	// with a chosen model, is the key, address and model used.
	saved := filepath.Join(t.TempDir(), "saved.json")
	raw, _ := json.Marshal(storedCredential{
		APIBase: "https://integrate.api.nvidia.com/v1", APIKey: "nvapi-test-value",
		DefaultModel: "vendor/model-a", Models: []string{"vendor/model-a", "vendor/model-b"},
		ChosenModel: "vendor/model-b",
	})
	if err := os.WriteFile(saved, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := loadShippedConfig(t)
	c = resolveProdMenuCredential(cfg, noEnv, saved, "")
	if c.skip != "" {
		t.Fatalf("a stored key was skipped: %s", c.skip)
	}
	s := newProdMenuServer(prodMenuSetup{cfg: cfg, tierCfg: c.tierCfg, chosenCfg: c.chosenCfg}, menuUnindexed)
	if c.apiBase != "https://integrate.api.nvidia.com/v1" || c.apiKey != "nvapi-test-value" {
		t.Errorf("resolved %s with key %q; want the stored address and key", c.apiBase, c.apiKey)
	}
	if got := s.tierConfig().ResolvedSlug(); got != "vendor/model-b" {
		t.Errorf("model = %s, want vendor/model-b, the one chosen with /model", got)
	}
	// And an override wins, as MOCHIII_MODEL does.
	cfg = loadShippedConfig(t)
	c = resolveProdMenuCredential(cfg, noEnv, saved, "vendor/override")
	s = newProdMenuServer(prodMenuSetup{cfg: cfg, tierCfg: c.tierCfg, chosenCfg: c.chosenCfg}, menuUnindexed)
	if got := s.tierConfig().ResolvedSlug(); got != "vendor/override" {
		t.Errorf("model = %s with an override; want vendor/override", got)
	}
}

func toolsJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
