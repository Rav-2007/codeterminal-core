package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// nvidiaInForce is the daemon's answer to "what are you sending to" for the
// state the owner's machine is in: an NVIDIA key, stored and in use, and a
// model that was chosen and saved.
func nvidiaInForce(t *testing.T) protocol.ConnectResponse {
	t.Helper()
	base := mustProvider(t, "nvidia").APIBase
	return protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectShown,
		Detail:    "stored, and the provider accepted it when it was saved",
		MaskedKey: "...3f2a (73 characters)", APIBase: base, InUse: true,
		Model: "z-ai/glm-5.3", ModelCount: 52, ModelSaved: true,
		ConnectInForce: protocol.ConnectInForce{
			ActiveAPIBase: base, KeySource: protocol.KeySourceStored, ActiveMaskedKey: "...3f2a (73 characters)",
		},
	}
}

// connectedModel is the chat with the daemon's answer already in.
func connectedModel(t *testing.T, resp protocol.ConnectResponse) chatModel {
	t.Helper()
	updated, _ := newTestModel().Update(connInfoMsg{resp: resp})
	return updated.(chatModel)
}

// enterSlash types a command at the prompt and presses Enter.
func enterSlash(m chatModel, command string) (chatModel, tea.Cmd) {
	return pressEnter(typeText(m, command))
}

// THE REPORT THIS ANSWERS (OWNER, 2026-10-12): "I can't even select the model".
// /model printed two hundred models as text, each name twice, to be typed back
// in full. On its own it now opens the list: the model in use is marked and
// highlighted, and Enter selects.
//
// Neuter check: delete the `if arg == "" { return m.beginModelPicker() }` in
// handleModelCommand -- /model prints text again and the state stays idle.
func TestModelOnItsOwnOpensTheListToChooseFrom(t *testing.T) {
	fakeDaemonServingTiers(t, tiersForSetup())
	m, cmd := enterSlash(connectedModel(t, nvidiaInForce(t)), "/model")
	if m.state != stateSetup || m.setup.step != setupModel || !m.setup.loading {
		t.Fatalf("state %v, step %v, loading %v; want the model list being fetched", m.state, m.setup.step, m.setup.loading)
	}
	for _, tr := range m.turns {
		if strings.Contains(tr.text, "models (") {
			t.Fatal("/model still prints the models as text")
		}
	}
	tiers, ok := findMsg[setupTiersMsg](cmd)
	if !ok {
		t.Fatal("/model did not ask the daemon for its models")
	}
	updated, _ := m.Update(tiers)
	// A window with room for the sentence above the list; a 24-row one keeps
	// its rows for the list itself (TestTheSetupFitsTheTerminal).
	updated, _ = updated.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(chatModel)

	screen := plainScreen(m)
	for _, want := range []string{setupModelTitle, setupModelSearch, "NVIDIA offers 4 models here",
		"gamma/tested ● (current)", "beta/second", "zeta/third", setupEscKeepModel} {
		if !strings.Contains(screen, want) {
			t.Errorf("the model list does not show %q:\n%s", want, screen)
		}
	}
	// Nothing was connected to get here, and the screen must not say it was.
	if strings.Contains(screen, "Connected to") {
		t.Errorf("/model says a provider was just connected:\n%s", screen)
	}
	if strings.Contains(screen, "omega/off") {
		t.Error("a model that is switched off is offered")
	}
	if got, _ := m.setup.picker.selected(); got.ID != "gamma/tested" {
		t.Errorf("the list opens on %q, want the model in use", got.ID)
	}
}

// Choosing from the list is /model <name>: this client's prompts name the
// model, and the daemon is asked to remember it. Esc changes nothing.
func TestChoosingFromTheModelListSetsAndRemembersIt(t *testing.T) {
	daemon := startRequestRecorder(t, []string{protocol.FeatureModelChoice}, func(raw json.RawMessage) any {
		if strings.Contains(string(raw), `"status"`) {
			return protocol.StatusResponse{AvailableTiers: tiersForSetup()}
		}
		return protocol.ConnectResponse{Ok: true, Outcome: protocol.ConnectModelChosen, Model: "zeta/third", ModelSaved: true}
	})
	m, cmd := enterSlash(newTestModel(), "/model")
	tiers, _ := findMsg[setupTiersMsg](cmd)
	updated, _ := m.Update(tiers)
	m = typeText(updated.(chatModel), "zeta")
	m, cmd = pressEnter(m)

	if m.state != stateIdle || m.setupActive() {
		t.Fatalf("after choosing: state %v, step %v; want the prompt", m.state, m.setup.step)
	}
	if m.preferredTier != "zeta/third" {
		t.Errorf("this client's prompts go to %q, want zeta/third", m.preferredTier)
	}
	if _, ok := findMsg[modelChoiceMsg](cmd); !ok {
		t.Fatal("the daemon was not asked to remember the model")
	}
	var chose []string
	for _, req := range daemon.connectRequests(t) {
		if req.ChooseModel != "" {
			chose = append(chose, req.ChooseModel)
		}
		if req.APIKey != "" {
			t.Error("choosing a model sent a key")
		}
	}
	if len(chose) != 1 || chose[0] != "zeta/third" {
		t.Errorf("the daemon was asked to remember %v, want zeta/third once", chose)
	}

	// Esc on the same list sets nothing.
	kept, cmd := enterSlash(newTestModel(), "/model")
	tiers, _ = findMsg[setupTiersMsg](cmd)
	updated, _ = kept.Update(tiers)
	kept = press(updated.(chatModel), tea.KeyEsc)
	if kept.state != stateIdle || kept.preferredTier != "" {
		t.Errorf("esc: state %v, model %q; want the prompt and no model set", kept.state, kept.preferredTier)
	}
}

// ASKED FOR BY NAME, SO ITS FAILURE IS SAID. As a step of connecting a key, a
// model list that does not arrive closes without a word -- the connect already
// reported. Opened by /model there is nothing else on screen, and a list that
// silently closes reads as a command that does nothing.
func TestTheModelListSaysWhenItCannotBeRead(t *testing.T) {
	restore := setLockPathForTest(t, filepath.Join(t.TempDir(), "absent.lock"))
	defer restore()

	m, cmd := enterSlash(newTestModel(), "/model")
	tiers, ok := findMsg[setupTiersMsg](cmd)
	if !ok || tiers.err == nil {
		t.Fatalf("an unreachable daemon gave no error (ok=%v)", ok)
	}
	updated, _ := m.Update(tiers)
	m = updated.(chatModel)
	if m.state != stateIdle || m.setupActive() {
		t.Fatalf("state %v, step %v; want the prompt back", m.state, m.setup.step)
	}
	if last := m.turns[len(m.turns)-1].text; !strings.Contains(last, setupModelsFailed) {
		t.Errorf("the failure was not said:\n%s", last)
	}

	// One model is still a list here: it answers "which model am I on".
	one, _ := enterSlash(newTestModel(), "/model")
	updated, _ = one.Update(setupTiersMsg{tiers: []protocol.StatusTier{{Name: "only", Slug: "only", Active: true, Default: true}}})
	one = updated.(chatModel)
	if !one.setupActive() || !strings.Contains(plainScreen(one), "only ● (current)") {
		t.Errorf("a single model is not shown:\n%s", plainScreen(one))
	}
	// And none at all says so.
	none, _ := enterSlash(newTestModel(), "/model")
	updated, _ = none.Update(setupTiersMsg{})
	none = updated.(chatModel)
	if none.setupActive() || !strings.Contains(none.turns[len(none.turns)-1].text, setupNoModels) {
		t.Error("an empty model list was not reported")
	}
}

// The mark follows what THIS client sends: a model chosen here outranks the
// daemon's default, because it is named on every prompt.
func TestTheCurrentMarkFollowsThisClientsChoice(t *testing.T) {
	rows, inForce := modelRows(tiersForSetup(), "zeta/third")
	if inForce != "zeta/third" {
		t.Fatalf("in force = %q, want this client's choice", inForce)
	}
	for _, row := range rows {
		if row.Current != (row.ID == "zeta/third") {
			t.Errorf("row %q current=%v", row.ID, row.Current)
		}
	}
	// A choice that is no longer offered falls back to the default.
	if _, inForce := modelRows(tiersForSetup(), "gone/model"); inForce != "gamma/tested" {
		t.Errorf("a vanished choice left %q in force, want the daemon's default", inForce)
	}
}

// A MODEL'S ID IS ITS ONLY NAME. With no hint column to make room for, the
// label has the whole row -- at a 34-column cap two of NVIDIA's models were cut
// to the same text.
func TestALongModelIDIsShownWhole(t *testing.T) {
	long := "nvidia/nemotron-3-nano-omni-30b-a3b-reasoning"
	p := newListPicker(setupModelTitle, []pickerRow{{ID: long, Label: long}, {ID: "b", Label: "nvidia/nemotron-3-nano-omni-30b-a3b"}})
	if view := ansi.Strip(p.view(70, 5)); !strings.Contains(view, long) {
		t.Errorf("the id was cut:\n%s", view)
	}
	// One row with a note of its own does not cut the names of the rows that
	// have none -- the list as the real app showed it, with a model that "did
	// not answer" at its foot.
	mixed := newListPicker(setupModelTitle, []pickerRow{{ID: long, Label: long},
		{ID: "q", Label: "adept/fuyu-8b", Hint: "did not answer when the key was connected", Dim: true}})
	if view := ansi.Strip(mixed.view(70, 5)); !strings.Contains(view, long) || !strings.Contains(view, "did not answer when the key was connected") {
		t.Errorf("a hinted row cut an unhinted one, or lost its own hint:\n%s", view)
	}
	// With hints, the cap still protects the hint column.
	hinted := newListPicker("", []pickerRow{{ID: "a", Label: strings.Repeat("x", 60), Hint: "the hint"}})
	if view := ansi.Strip(hinted.view(70, 5)); !strings.Contains(view, "the hint") {
		t.Errorf("a long label pushed the hint off:\n%s", view)
	}
}

// "/model list" prints a name once. It printed "name  slug", which for a
// provider's own models is the same text twice on every row.
func TestTheTextListWritesANameOnce(t *testing.T) {
	text := modelListText([]protocol.StatusTier{
		{Name: "z-ai/glm-5.3", Slug: "z-ai/glm-5.3", Active: true, Default: true},
		{Name: "fast", Slug: "anthropic/claude-haiku-4.5", Active: true},
	}, "")
	if strings.Count(text, "z-ai/glm-5.3") != 1 {
		t.Errorf("a model whose id is its name is written more than once:\n%s", text)
	}
	if !strings.Contains(text, "* z-ai/glm-5.3") || !strings.Contains(text, "fast  anthropic/claude-haiku-4.5") {
		t.Errorf("the mark or a tier's own id is missing:\n%s", text)
	}
}

// THE OTHER HALF OF THE REPORT: "I can't select the provider". /provider opens
// the list /connect always did, on the provider in use, marked.
//
// Neuter check: take "provider" out of the `case "connect", "provider":` in
// handleLocalSlash -- the command answers "unknown local command".
func TestProviderOpensTheListOnTheProviderInUse(t *testing.T) {
	m, _ := enterSlash(connectedModel(t, nvidiaInForce(t)), "/provider")
	if m.state != stateSetup || m.setup.step != setupProvider {
		t.Fatalf("state %v, step %v; want the provider list", m.state, m.setup.step)
	}
	screen := plainScreen(m)
	for _, want := range []string{setupProviderTitle, setupProviderSearch, "NVIDIA ● (current)", setupEscBack} {
		if !strings.Contains(screen, want) {
			t.Errorf("the provider list does not show %q:\n%s", want, screen)
		}
	}
	if got, _ := m.setup.picker.selected(); got.ID != "nvidia" {
		t.Errorf("the list opens on %q, want the provider in use", got.ID)
	}
	if strings.Count(screen, "(current)") != 1 {
		t.Errorf("more than one provider is marked current:\n%s", screen)
	}

	// With no key in force nothing is marked: the daemon always has SOME
	// address, and calling it the current provider would name one that refuses.
	none := nvidiaInForce(t)
	none.KeySource, none.InUse, none.ActiveMaskedKey = protocol.KeySourceNone, false, ""
	unconnected, _ := enterSlash(connectedModel(t, none), "/provider")
	if strings.Contains(plainScreen(unconnected), "(current)") {
		t.Error("a provider is marked current with no key in force")
	}

	// /provider <name> goes straight to that provider's key, like /connect <name>.
	named, _ := enterSlash(newTestModel(), "/provider groq")
	if named.state != stateConnect || named.setup.step != setupKey || !strings.Contains(plainScreen(named), "Groq") {
		t.Errorf("/provider groq: state %v, step %v; want Groq's key step", named.state, named.setup.step)
	}
}

// Picking the provider you are already on, to change something that is not the
// key, must not demand the key again: an empty field keeps it and moves on to
// the models. For any OTHER provider an empty field is still a slip.
func TestAnEmptyKeyKeepsTheKeyInUseForTheSameProvider(t *testing.T) {
	daemon := startRequestRecorder(t, nil, func(json.RawMessage) any {
		return protocol.StatusResponse{AvailableTiers: tiersForSetup()}
	})
	m, _ := enterSlash(connectedModel(t, nvidiaInForce(t)), "/provider")
	m, _ = pressEnter(m) // the list opened on NVIDIA
	if m.state != stateConnect || m.setup.step != setupKey {
		t.Fatalf("state %v, step %v; want NVIDIA's key step", m.state, m.setup.step)
	}
	if s := plainScreen(m); !strings.Contains(s, "Leave it empty and press Enter to keep the key in use") || !strings.Contains(s, "...3f2a") {
		t.Errorf("the key step does not offer to keep the key in use:\n%s", s)
	}
	m, cmd := pressEnter(m)
	if m.state != stateSetup || m.setup.step != setupModel || !m.setup.loading {
		t.Fatalf("state %v, step %v; want the model list being fetched", m.state, m.setup.step)
	}
	if _, ok := findMsg[setupTiersMsg](cmd); !ok {
		t.Fatal("keeping the key did not go on to the models")
	}
	for _, req := range daemon.connectRequests(t) {
		if req.APIKey != "" || req.Connect {
			t.Errorf("keeping the key sent a connect request: %+v", req.Connect)
		}
	}

	// Another provider: empty is refused, as before.
	other, _ := enterSlash(connectedModel(t, nvidiaInForce(t)), "/provider")
	other = typeText(other, "groq")
	other, _ = pressEnter(other)
	if strings.Contains(plainScreen(other), "keep the key in use") {
		t.Error("another provider's key step offers to keep a key that is not its own")
	}
	other, _ = pressEnter(other)
	if other.setup.step != setupKey || other.setup.notice != setupNoKeyNote {
		t.Errorf("an empty key for another provider: step %v, notice %q; want it refused", other.setup.step, other.setup.notice)
	}
}

// The new look, pinned where it carries meaning: the search field is a field,
// the highlighted row is one bar the width of the list, and the help line is
// inside the box, under what it is about.
func TestTheListLooksLikeTheScreenItWasAskedToLookLike(t *testing.T) {
	m, _ := enterSlash(connectedModel(t, nvidiaInForce(t)), "/provider")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(chatModel)
	lines := strings.Split(ansi.Strip(m.View()), "\n")

	var field, help, bottom, bar int = -1, -1, -1, -1
	for i, line := range lines {
		switch {
		case strings.Contains(line, setupProviderSearch):
			field = i
		case strings.Contains(line, setupEscBack):
			help = i
		case strings.Contains(line, "❯ NVIDIA"):
			bar = i
		}
		if strings.Contains(line, "╰") && strings.Contains(line, "╯") {
			bottom = i // the last one is the box's own
		}
	}
	if field < 1 || !strings.Contains(lines[field-1], "╭") || !strings.Contains(lines[field+1], "╰") {
		t.Errorf("the search text is not in a bordered field of its own:\n%s", strings.Join(lines, "\n"))
	}
	if help < 0 || help > bottom {
		t.Errorf("the help line (%d) is not inside the box, which closes at line %d", help, bottom)
	}
	if bar < 0 {
		t.Fatalf("no highlighted row with the ❯ marker:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[bar], "❯ NVIDIA ● (current)") {
		t.Errorf("the highlighted row lost its current mark: %q", lines[bar])
	}
	// The bar reaches the far side of the list: the highlighted row is drawn
	// the whole width it was given, where every other row ends with its text.
	const width = 48
	var full int
	for _, line := range strings.Split(m.setup.picker.view(width, 6), "\n") {
		switch {
		case strings.Contains(ansi.Strip(line), "❯ "):
			if w := lipgloss.Width(line); w != width {
				t.Errorf("the highlighted row is %d columns wide, want the whole %d", w, width)
			}
			full++
		case strings.Contains(ansi.Strip(line), "OpenRouter"):
			if w := lipgloss.Width(line); w >= width {
				t.Errorf("an ordinary row is padded to the full width (%d)", w)
			}
		}
	}
	if full != 1 {
		t.Errorf("%d highlighted rows in the list, want one", full)
	}
}

// IN A SMALL TERMINAL THE WAY OUT IS STILL ON SCREEN. The help line is one line
// there, and cut from the right it lost "Esc to …" before anything else.
func TestASmallTerminalStillSaysHowToLeave(t *testing.T) {
	open := map[string]string{"/provider": setupEscBack, "/model": setupEscKeepModel}
	for command, esc := range open {
		m, _ := enterSlash(connectedModel(t, nvidiaInForce(t)), command)
		updated, _ := m.Update(setupTiersMsg{tiers: tiersForSetup()})
		updated, _ = updated.Update(tea.WindowSizeMsg{Width: 60, Height: 16})
		if screen := plainScreen(updated.(chatModel)); !strings.Contains(screen, esc) {
			t.Errorf("%s at 60x16 does not say %q:\n%s", command, esc, screen)
		}
	}
}
