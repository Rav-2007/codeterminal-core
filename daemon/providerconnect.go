package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"mochiii/protocol"
)

// CONNECTING A KEY: WHICH PROVIDER IT IS FOR, AND WHAT MAKES IT READY.
//
// "Paste the key and it works" needs three things nobody should have to type:
// the provider's address, a model that provider serves, and proof that the
// three of them together answer a prompt. The key itself says whose it is --
// providers put their name in the prefix -- and the provider says the rest.
//
// Shared by the daemon's /connect and by `mochiii-daemon connect`, so the two
// cannot disagree about where a key goes.

// providersForKey is protocol.ProvidersForKey. A var, like providerForBase, so
// a test can have a key's prefix name a loopback server instead of a real
// provider's address; nothing else may reassign it.
var providersForKey = protocol.ProvidersForKey

// connectTarget is where a key is to be checked.
type connectTarget struct {
	// Base is the address to use. Empty when Candidates is set.
	Base string
	// Candidates is set when the key could belong to any of several providers
	// and nothing here may choose between them: the user is asked.
	Candidates []protocol.Provider
}

// resolveConnectBase decides where a pasted key goes, in this order:
//
//  1. An address or provider the user named. Always wins.
//
//  2. A custom address already in use -- a company gateway, a local server.
//     Someone set that deliberately, keys for it look like anything, and a key
//     is never moved off it on the strength of its spelling.
//
//  3. The provider the key's prefix names, when the prefix names exactly one.
//     This is what lets an NVIDIA key pasted into a daemon on OpenRouter go to
//     NVIDIA, instead of being refused by a company that never issued it.
//
//  4. A bare "sk-" key, which several providers use: the provider in use if it
//     is one of them, otherwise NOBODY -- Candidates is returned and the user
//     picks. Trying them in turn would hand a working credential to each.
//
//  5. Otherwise the provider in use.
//
// inUse is the base the running daemon sends to ("" from the CLI, where
// nothing is running), then the stored credential's, the environment's, and
// the built-in default.
func resolveConnectBase(explicit, key, inUse string, stored storedCredential, envBase string) connectTarget {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return connectTarget{Base: secureProviderBase(explicit)}
	}
	current := firstNonEmpty(inUse, stored.APIBase, envBase, defaultAPIBase)
	currentProvider, known := providerForBase(current)
	if !known {
		return connectTarget{Base: current}
	}

	matches, certain := providersForKey(key)
	switch {
	case certain && matches[0].ID == currentProvider.ID:
		return connectTarget{Base: current}
	case certain:
		return connectTarget{Base: matches[0].APIBase}
	case len(matches) > 0:
		for _, p := range matches {
			if p.ID == currentProvider.ID {
				return connectTarget{Base: current}
			}
		}
		return connectTarget{Candidates: matches}
	}
	return connectTarget{Base: current}
}

// secureProviderBase returns base with https in place of http when it names one
// of the providers this product knows: `/connect http://api.openai.com/v1`
// would otherwise send the pasted key, and every prompt after it, across the
// network in the clear to a company that has only ever served https (FOUND
// 2026-10-06). A plain-http address is still accepted for anything else -- a
// model server on the user's own machine or network has no certificate -- and
// connectNotes says what that means.
func secureProviderBase(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || isLoopbackBase(base) {
		return base
	}
	if _, known := providerForBase(base); !known {
		return base
	}
	u.Scheme = "https"
	return u.String()
}

// validateConnectBase is validateAPIBase for an address a key is about to be
// SENT to, with one more refusal: anything before an "@". In
// "https://api.openai.com@evil.example/v1" the host is evil.example and the
// provider's name is a username -- the shape a disguised link takes, and no
// provider's address has one. The terminal client already refused it; a key
// offered by any other client, or by `connect --api-base`, met no such check.
func validateConnectBase(base string) error {
	if err := validateAPIBase(base); err != nil {
		return err
	}
	if u, err := url.Parse(base); err == nil && u.User != nil {
		return fmt.Errorf("the address has something before an \"@\" (%s is the host it really names), which no "+
			"provider's address does; the key was sent nowhere and nothing was saved", u.Hostname())
	}
	return nil
}

// providerSetup is what connecting a key established.
type providerSetup struct {
	// Check is whether the provider accepted the key, and the sentence for it.
	Check verification
	// Models is the provider's own chat models, less the ones it answered "not
	// found" for when they were tried. Nil means "keep the tiers in the model
	// config": OpenRouter, or a custom address that serves them.
	Models []string
	// Quiet are the entries of Models that were tried and sent nothing back.
	Quiet []string
	// DefaultModel is the one prompts go to until /model says otherwise.
	DefaultModel string
	// Tested reports that DefaultModel answered a real request with this key.
	Tested bool
	// ToolsRefused reports that DefaultModel answered only once the request
	// stopped offering a tool -- so it can chat, and cannot run agent mode.
	ToolsRefused bool
	// Listed, Tried and Unavailable count the provider's chat models: how many
	// it lists, how many were sent a test request, and how many of those it
	// said were not there for this account.
	Listed, Tried, Unavailable int
}

var (
	// providerSetupTimeout bounds everything setUpProvider does. Longer than
	// connectVerifyTimeout because it asks models a question.
	providerSetupTimeout = 75 * time.Second
	// probeTimeout bounds one test request: the time to a model's FIRST word.
	// Measured 2026-10-05 on NVIDIA with a real key, over several rounds: the
	// models that answer at all do so in 0.5-7.2 s, and the ones that do not had
	// still sent nothing after 150 s. Waiting longer buys nothing, and it is how
	// long a connect takes whenever one silent model is among those tried. A
	// working model that is slower than this on the day is marked quiet and
	// stays selectable; it is only not chosen as the default.
	probeTimeout = 8 * time.Second
	// probeHeadStart is how long the best candidate is tried on its own before
	// the others are. On a provider whose list is what it serves -- most of
	// them -- that candidate answers inside it and exactly one request is sent.
	probeHeadStart = 2500 * time.Millisecond
	// probeConcurrency is how many test requests are in flight at once after
	// the head start. Silent models are what make this necessary: one at a time,
	// each costs a full probeTimeout before the next is tried.
	probeConcurrency = 12
	// maxProbeCandidates bounds how many models are tried at all.
	maxProbeCandidates = 40
	// maxStoredModels bounds how many of a provider's models are kept as tiers.
	// The list is the provider's to write and was stored whole: one of 5,000
	// entries became 5,000 rows in /model and in credentials.json (FOUND
	// 2026-10-06 against a stand-in provider). The largest real list measured
	// is NVIDIA's 59; a hundred of the best-ranked is every model a person
	// would pick from, and MOCHIII_MODEL still names any other.
	maxStoredModels = 100
)

// candidateOutcome is what one candidate's test request came to.
type candidateOutcome struct {
	tried bool
	err   error
}

func (o candidateOutcome) answered() bool { return o.tried && o.err == nil }

// status is the HTTP status the provider refused with, or 0.
func (o candidateOutcome) status() int {
	var me *ModelError
	if errors.As(o.err, &me) {
		return me.status
	}
	return 0
}

// unavailable reports that the provider said the model is not there for this
// account -- as opposed to refusing the key or the request.
func (o candidateOutcome) unavailable() bool {
	return o.tried && (o.status() == http.StatusNotFound || o.status() == http.StatusGone)
}

// quiet reports that the model was asked and sent nothing back in time.
func (o candidateOutcome) quiet() bool {
	return o.tried && o.err != nil && o.status() == 0 && probeErrorClass(o.err) == ClassUpstreamUnavailable
}

// tryModels sends candidates a test request, best first, until one answers.
//
// LISTED IS NOT THE SAME AS SERVED, and that is why this is not a loop. The
// first version tried the five best-ranked models one after another and gave
// up. On NVIDIA, with a real key (2026-10-05), all five were models the list
// offers and the account cannot use: four accepted the request and never sent a
// byte, one answered 404. The key was good, four other models answered in under
// four seconds, and the user was told nothing worked -- then left on a default
// that hung every prompt for two minutes.
//
// So: the best candidate gets a short head start, alone, which is all a provider
// with an honest list ever needs. If it has not answered by then, the rest are
// tried several at a time, in order, until one answers; whatever is already in
// flight is allowed to finish, so a better-ranked model that was merely slower
// still wins. Nothing new is started once there is an answer.
func tryModels(ctx context.Context, apiBase, apiKey string, candidates []string, routing providerRouting) []candidateOutcome {
	out := make([]candidateOutcome, len(candidates))
	if len(candidates) == 0 {
		return out
	}
	var mu sync.Mutex
	answered := false
	probe := func(i int) {
		err := probeWithin(ctx, apiBase, apiKey, candidates[i], routing, true)
		mu.Lock()
		defer mu.Unlock()
		out[i] = candidateOutcome{tried: true, err: err}
		answered = answered || err == nil
	}
	hasAnswer := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return answered
	}

	first := make(chan struct{})
	go func() {
		defer close(first)
		probe(0)
	}()
	select {
	case <-first:
	case <-time.After(probeHeadStart):
	case <-ctx.Done():
	}

	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup
	for i := 1; i < len(candidates) && !hasAnswer(); i++ {
		acquired := false
		select {
		case sem <- struct{}{}:
			acquired = true
		case <-ctx.Done():
		}
		if !acquired {
			break
		}
		// The slot may have been a long time coming, and an answer may have
		// arrived while it was.
		if hasAnswer() {
			<-sem
			break
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			probe(i)
		}(i)
	}
	wg.Wait()
	<-first

	// Read under the lock one last time: every writer has finished, and the
	// race detector is owed the same discipline the writers kept.
	mu.Lock()
	defer mu.Unlock()
	return append([]candidateOutcome(nil), out...)
}

// setUpProvider checks a key against apiBase and finds out what it can run.
//
// For OpenRouter, and for any address this does not know by name, it is
// verifyKey and nothing else, exactly as before: models.json names the models,
// and OpenRouter's /key endpoint proves a key without spending anything.
//
// For every other named provider it lists the provider's models, orders the
// chat models as candidates (rankModels), and sends them a real, tiny request
// until one answers (tryModels). That answer is the proof -- of the key, of the
// model name, and of the request's wording (providerdialect.go adapts it on the
// way, and remembers what worked).
//
// logf gets one line saying what was tried and what came of it. It never
// receives the key.
func setUpProvider(ctx context.Context, client *http.Client, apiBase, apiKey string, logf func(string, ...any)) providerSetup {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if usesRoutingDialect(apiBase) {
		return providerSetup{Check: verifyKey(ctx, client, apiBase, apiKey)}
	}
	name := providerName(apiBase)

	models, status, body, err := listModels(ctx, client, apiBase, apiKey)
	switch {
	case err != nil:
		return providerSetup{Check: verification{verifyUnreachable, fmt.Sprintf("%s did not answer: %v", apiBase, err)}}
	case refusesKey(status, body):
		return providerSetup{Check: verification{verifyRejected, fmt.Sprintf("the provider refused it (HTTP %d)", status)}}
	}
	ids := chatModelIDs(models)
	if len(ids) == 0 {
		return providerSetup{Check: verification{verifyInconclusive, fmt.Sprintf(
			"%s gave no list of models (HTTP %d), so none could be chosen or tried", name, status)}}
	}

	routing := providerRouting{maxTokens: defaultMaxOutputTokens}
	candidates := rankModels(ids)
	if len(candidates) > maxProbeCandidates {
		candidates = candidates[:maxProbeCandidates]
	}
	outcomes := tryModels(ctx, apiBase, apiKey, candidates, routing)

	// What the attempts say about the LIST, whichever way the key turns out.
	setup := providerSetup{Listed: len(ids)}
	gone := map[string]bool{}
	best, refusedKey, limited, refusedRequest := -1, 0, -1, -1
	var answering []string
	for i, o := range outcomes {
		if !o.tried {
			continue
		}
		setup.Tried++
		switch {
		case o.answered():
			answering = append(answering, candidates[i])
			if best < 0 {
				best = i
			}
		case o.unavailable():
			setup.Unavailable++
			gone[candidates[i]] = true
		case o.quiet():
			setup.Quiet = append(setup.Quiet, candidates[i])
		case probeErrorClass(o.err) == ClassAuth:
			refusedKey++
		case probeErrorClass(o.err) == ClassQuotaExceeded || probeErrorClass(o.err) == ClassRateLimited:
			if limited < 0 {
				limited = i
			}
		case o.status() == http.StatusBadRequest || o.status() == http.StatusUnprocessableEntity:
			if refusedRequest < 0 {
				refusedRequest = i
			}
		}
	}
	// What is kept: every listed model the provider did not disown, up to
	// maxStoredModels of them, best-ranked first when there are more. Kept in
	// the list's own (alphabetical) order, which is the order /model shows.
	keep := map[string]bool{}
	for _, id := range rankModels(ids) {
		if !gone[id] && len(keep) < maxStoredModels {
			keep[id] = true
		}
	}
	for _, id := range ids {
		if keep[id] {
			setup.Models = append(setup.Models, id)
		}
	}
	logf("connect: %s lists %d chat model(s); %d tried: %d answered %v, %d not available to this account, %d sent nothing within %s, %d refused the key, %d failed otherwise",
		name, setup.Listed, setup.Tried, len(answering), answering, setup.Unavailable, len(setup.Quiet), probeTimeout, refusedKey,
		setup.Tried-len(answering)-setup.Unavailable-len(setup.Quiet)-refusedKey)
	for i, o := range outcomes {
		if o.tried && !o.answered() && !o.unavailable() && !o.quiet() && probeErrorClass(o.err) != ClassAuth {
			logf("connect: %s: %s", name, describeProbeFailure(candidates[i], o.err))
		}
	}

	if best >= 0 {
		setup.DefaultModel, setup.Tested = candidates[best], true
		setup.Check = verification{verifyAccepted, fmt.Sprintf("%s answered a test request to %s with this key", name, candidates[best])}
		logf("connect: %s answered on %s", name, candidates[best])
		return setup
	}

	// EVERY MODEL THAT ANSWERED AT ALL SAID THE KEY IS BAD. One model saying so
	// can be about that model -- a provider answers 403 for a model an account
	// may not use -- which is why one refusal ends nothing. All of them saying
	// so is about the key...
	if refusedKey > 0 && refusedKey == setup.Tried {
		// ...UNLESS THE PROVIDER HAS ALREADY SHOWN IT KNOWS THIS KEY. Where the
		// model list is refused to a made-up key and was served to this one, the
		// key is real, and the refusals are models this account may not use --
		// a restricted key, not a wrong one, and refusing to store it would lock
		// out someone whose key works.
		if modelListNeedsKey(ctx, client, apiBase) {
			setup.DefaultModel = candidates[0]
			setup.Check = verification{verifyInconclusive, fmt.Sprintf(
				"%s accepts the key, but refused a test request on each of the %d models tried, so this account "+
					"may not be allowed to use them", name, setup.Tried)}
			return setup
		}
		return providerSetup{Check: verification{verifyRejected, fmt.Sprintf(
			"%s refused it on every one of the %d models tried", name, setup.Tried)}}
	}

	// From here the key is kept, unproven, and the default is the best candidate
	// the provider did not say is missing.
	for _, id := range candidates {
		if !gone[id] {
			setup.DefaultModel = id
			break
		}
	}
	if setup.DefaultModel == "" {
		setup.DefaultModel = candidates[0]
	}

	if limited >= 0 {
		// The provider knew the key well enough to count against it, which a
		// stranger's key does not get. It is not proof a prompt will run.
		setup.DefaultModel = candidates[limited]
		setup.Check = verification{verifyInconclusive, fmt.Sprintf(
			"%s recognised the key but would not run a test request (%s), so nothing here proves a prompt will work",
			name, probeErrorClass(outcomes[limited].err))}
		return setup
	}

	// A MODEL THAT ANSWERS ONLY WHEN NO TOOL IS OFFERED is worth knowing about:
	// it can hold a conversation and cannot run agent mode, and without this the
	// user is told nothing worked. Tried on a model that refused the REQUEST --
	// a silent or missing one will be no different without a tool.
	if refusedRequest >= 0 && ctx.Err() == nil {
		if err := probeWithin(ctx, apiBase, apiKey, candidates[refusedRequest], routing, false); err == nil {
			setup.DefaultModel, setup.Tested, setup.ToolsRefused = candidates[refusedRequest], true, true
			setup.Check = verification{verifyAccepted, fmt.Sprintf("%s answered a test request to %s with this key", name, candidates[refusedRequest])}
			return setup
		}
	}

	setup.Check = verification{verifyInconclusive, fmt.Sprintf(
		"%s lists %d models, but none of the %d tried answered a test request (not available to this account: %d; "+
			"sent nothing back: %d; refused: %d)", name, setup.Listed, setup.Tried, setup.Unavailable, len(setup.Quiet),
		setup.Tried-setup.Unavailable-len(setup.Quiet))}
	return setup
}

// madeUpKey is a key no provider issued, sent to learn whether an endpoint
// checks keys at all.
const madeUpKey = "mochiii-not-a-key-0000000000000000"

// modelListNeedsKey reports whether a provider's model list is refused to a key
// nobody issued. When it is, a list that WAS served is proof the key it was
// served to is real. Measured 2026-10-05: OpenAI, Anthropic, Groq, DeepSeek,
// Mistral and most others refuse; NVIDIA, Hugging Face and SambaNova serve
// their list to anyone, and for those this says nothing.
func modelListNeedsKey(ctx context.Context, client *http.Client, apiBase string) bool {
	_, status, body, err := listModels(ctx, client, apiBase, madeUpKey)
	return err == nil && refusesKey(status, body)
}

func probeWithin(ctx context.Context, apiBase, apiKey, model string, routing providerRouting, withTool bool) error {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	return probeCompletion(pctx, apiBase, apiKey, model, routing, withTool)
}

// refusesKey reports whether a /models answer says the key is not valid.
func refusesKey(status int, body string) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return true
	case http.StatusBadRequest:
		return containsAny(strings.ToLower(body), badKeyPhrases)
	}
	return false
}

func probeErrorClass(err error) ModelErrorClass {
	var me *ModelError
	if errors.As(err, &me) {
		return me.Class
	}
	return ClassUnknown
}

// connectNotes are the consequences of a connect the user would otherwise meet
// later with nothing tying them to it.
func connectNotes(apiBase string, setup providerSetup, switched bool) []string {
	var notes []string
	if u, err := url.Parse(apiBase); err == nil && u.Scheme == "http" && !isLoopbackBase(apiBase) {
		notes = append(notes, fmt.Sprintf("%s is plain http: the key and every prompt travel to it unencrypted. "+
			"That is fine for a model server on your own network and never for one across the internet.", u.Host))
	}
	if setup.ToolsRefused {
		notes = append(notes, fmt.Sprintf("%s answered only once no tool was offered. Agent mode works by tool calls, "+
			"so it will not run on this model; /model lists the others.", setup.DefaultModel))
	}
	switch _, known := providerForBase(apiBase); {
	case setup.Models != nil:
	case !usesRoutingDialect(apiBase):
		// A NAMED PROVIDER WITH NO MODEL CHOSEN IS NOT READY, and saying nothing
		// would leave the next prompt to fail on a model name from models.json
		// that this provider has never heard of.
		notes = append(notes, fmt.Sprintf("No model could be chosen for %s, so it is not ready: prompts would ask "+
			"for the models in the model config, which are another provider's. Run /connect again once it "+
			"answers, or name a model with MOCHIII_MODEL.", providerName(apiBase)))
	case switched && !known:
		notes = append(notes, fmt.Sprintf("Model names are each provider's own. The tiers in the model config "+
			"are still in force, and %s gave no list of its own to replace them with; a prompt that fails "+
			"with an unknown model means the tiers need that address's model names.", apiBase))
	}
	if setup.Models != nil && setup.Listed-setup.Unavailable > len(setup.Models) {
		notes = append(notes, fmt.Sprintf("%s lists %d chat models; /model offers the %d best suited to this work. "+
			"MOCHIII_MODEL names any other.", providerName(apiBase), setup.Listed, len(setup.Models)))
	}
	if dead := setup.Unavailable + len(setup.Quiet); dead > 0 && setup.Models != nil {
		notes = append(notes, fmt.Sprintf("%s lists %d models, and not all of them are served to this key. Of the %d "+
			"tried — not available to this account, and left out of /model: %d; sent nothing back, and marked in /model: %d.",
			providerName(apiBase), setup.Listed, setup.Tried, setup.Unavailable, len(setup.Quiet)))
	}
	if switched && !usesRoutingDialect(apiBase) {
		notes = append(notes, fmt.Sprintf("Zero-data-retention routing is something OpenRouter does. Prompts now go "+
			"straight to %s, under its own data policy.", providerName(apiBase)))
	}
	return notes
}
