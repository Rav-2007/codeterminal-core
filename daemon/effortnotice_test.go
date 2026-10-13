package main

import (
	"strings"
	"testing"

	"mochiii/protocol"
)

// THE THINKING-EFFORT PICKER OFFERED THREE SETTINGS AND, ON SOME PROVIDERS,
// PRODUCED ONE BEHAVIOUR.
//
// reasoning_effort is sent as OpenAI's top-level field (providerdialect.go's
// plainChatRequest). A provider that does not know it answers 400 naming it,
// adapt() sets noReasoningEffort, the request is retried without it, and the
// dialect is remembered in dialectMemo for the rest of the daemon's life. That
// recovery is right -- the effort is a preference and the answer is not, so
// losing the turn over it would be worse.
//
// What was wrong is that it was SILENT. From the next turn on, Low / Medium /
// High were three labels on one behaviour, the picker went on offering them,
// and nothing anywhere said the setting had stopped being sent. The honest
// reading available to the user was that the control did nothing; the reading
// most users would reach is that it did something.
//
// effortDegradation is the notice. These tests pin the three things that make
// it worth having: it fires when the latch is set AND an effort was asked for,
// it stays silent in every other case, and it names the model -- because the
// latch is per-model and the client greys its control on that.

func TestEffortDegradationFiresWhenTheLatchIsSet(t *testing.T) {
	forgetDialects(t)
	const base = "https://integrate.api.nvidia.com/v1" // a NAMED provider: plain dialect
	const model = "meta/llama-4-70b"

	// What adapt() does after the provider refuses the field, then what
	// openCompletion stores on the retry's success.
	dialectMemo.Store(dialectKey(base, model), requestDialect{
		streamOptions:     true,
		maxTokensField:    fieldMaxTokens,
		noReasoningEffort: true,
	})

	d, ok := effortDegradation(base, model, "high")
	if !ok {
		t.Fatal("no degradation reported while the latch is set and the turn asked for high effort -- " +
			"this is the silence the notice exists to end")
	}
	if d.Component != protocol.DegradedReasoningEffort {
		t.Errorf("Component = %q, want %q", d.Component, protocol.DegradedReasoningEffort)
	}

	// THE PROVIDER IS NAMED, NOT THE ADDRESS. Every client-facing detail in
	// degraded.go follows the Fix 8 rule: say what is reduced and what it costs,
	// never where anything lives.
	if !strings.Contains(d.Detail, "NVIDIA") {
		t.Errorf("Detail does not name the provider: %q", d.Detail)
	}
	if strings.Contains(d.Detail, "integrate.api.nvidia.com") {
		t.Errorf("Detail leaks the endpoint address, which is not the user's business: %q", d.Detail)
	}

	// The user needs to know what to DO. "Choose another model" is the only
	// action available, so the sentence has to contain it.
	if !strings.Contains(strings.ToLower(d.Detail), "another model") {
		t.Errorf("Detail does not say what the user can do about it: %q", d.Detail)
	}

	// The model rides in metadata because the client greys a per-model control
	// on it and must be able to un-grey it when the model changes.
	if got := d.Metadata["model"]; got != model {
		t.Errorf("Metadata[model] = %v, want %q -- the client cannot scope the greying without it", got, model)
	}
	if got := d.Metadata["requested"]; got != "high" {
		t.Errorf("Metadata[requested] = %v, want \"high\"", got)
	}
}

func TestEffortDegradationSilentWhenNoEffortWasAskedFor(t *testing.T) {
	forgetDialects(t)
	const base = "https://integrate.api.nvidia.com/v1"
	const model = "meta/llama-4-70b"
	dialectMemo.Store(dialectKey(base, model), requestDialect{noReasoningEffort: true})

	// A user who never touched the picker is not owed a notice about a field
	// they did not set. Reporting it would put a warning on every turn with
	// these providers, which is how a warning becomes wallpaper.
	if _, ok := effortDegradation(base, model, ""); ok {
		t.Error("reported a degradation for a turn that asked for no effort at all")
	}
}

func TestEffortDegradationSilentWhenTheProviderAcceptsTheField(t *testing.T) {
	forgetDialects(t)
	const base = "https://integrate.api.nvidia.com/v1"
	const model = "meta/llama-4-70b"

	// No memo entry at all: the field has never been refused, so it is being
	// sent and the control is live.
	if _, ok := effortDegradation(base, model, "low"); ok {
		t.Error("reported a degradation although nothing has ever refused reasoning_effort for this model")
	}

	// And an explicit memo whose latch is clear -- the shape left behind when
	// some OTHER field had to be adapted (max_completion_tokens, stream_options).
	dialectMemo.Store(dialectKey(base, model), requestDialect{
		streamOptions:  false,
		maxTokensField: fieldMaxCompletionTokens,
	})
	if _, ok := effortDegradation(base, model, "low"); ok {
		t.Error("a dialect adapted for a DIFFERENT field must not be read as an effort refusal")
	}
}

// THE LATCH IS PER MODEL, so the notice must be too.
//
// This is the assertion the client's un-greying depends on: a verdict about one
// model says nothing about the next one the user picks, and a control left
// greyed across a model switch is the same lie in the other direction.
func TestEffortDegradationIsScopedToOneModel(t *testing.T) {
	forgetDialects(t)
	const base = "https://integrate.api.nvidia.com/v1"
	dialectMemo.Store(dialectKey(base, "refuses/it"), requestDialect{noReasoningEffort: true})

	if _, ok := effortDegradation(base, "refuses/it", "medium"); !ok {
		t.Error("the model whose provider refused the field reports nothing")
	}
	if _, ok := effortDegradation(base, "accepts/it", "medium"); ok {
		t.Error("a latch set for one model leaked onto another; the client would grey a control that works")
	}
}

// OPENROUTER AND THE PROXY CANNOT REACH THIS STATE, and the notice must not
// claim they can.
//
// Those addresses use the routing dialect, which carries the effort in a
// "reasoning" object. adapt() returns early for a routing dialect -- nothing is
// ever taken out of a request to OpenRouter or the proxy because a response
// said so -- so noReasoningEffort can never latch there and the effort is always
// sent. dialectFor returns a fresh routing dialect for those bases regardless of
// what is in the memo, which is what makes this hold.
func TestEffortDegradationSilentOnTheRoutingDialect(t *testing.T) {
	forgetDialects(t)
	const base = "https://openrouter.ai/api/v1"
	const model = "deepseek/deepseek-v4-pro"

	// Even with a latched entry planted under that key, the routing dialect
	// wins: dialectFor checks usesRoutingDialect BEFORE consulting the memo.
	dialectMemo.Store(dialectKey(base, model), requestDialect{noReasoningEffort: true})

	if _, ok := effortDegradation(base, model, "high"); ok {
		t.Error("reported an effort refusal for an address whose dialect can never refuse one -- " +
			"a notice that fires where the problem cannot occur trains users to ignore it")
	}
}
