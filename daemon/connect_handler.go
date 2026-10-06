package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"mochiii/protocol"
)

// The daemon side of `/connect`: take a key over the socket, prove it, store it,
// and START USING IT -- without a restart.
//
// WHY THE DAEMON DOES THE WORK AND NOT THE CLIENT. The daemon is the process that
// holds the credential and talks to the provider, so verification and storage
// live here, once, rather than once per client. A client collects the key and
// renders the answer; it never learns how a credential is checked or where it is
// written, and a second client cannot get either subtly wrong.
//
// WHAT IS NEVER DONE HERE: the key is not logged, not echoed, and not returned.
// Everything that goes back out is masked, and that masking happens in the daemon
// so no client is trusted to do it.

// isConnectRequest recognises the request by its discriminator, the same way
// every other typed request on this socket is told apart.
func isConnectRequest(raw json.RawMessage) bool {
	fields, ok := requestFields(raw)
	if !ok {
		return false
	}
	return hasBoolKey(fields, "connect")
}

// credentials returns the key and base to use for one request.
func (s *Server) credentials() (key, base string) {
	s.credMu.RLock()
	defer s.credMu.RUnlock()
	return s.apiKey, s.apiBase
}

// needsAPIKey reports whether a prompt sent now would go out with no credential
// and be refused by the provider.
//
// See protocol.HandshakeResponse.NeedsAPIKey for why the daemon decides this and
// not each client: the three inputs live here. Read fresh on every handshake, so
// a client that reconnects after `connect` sees the new answer.
func (s *Server) needsAPIKey() bool {
	// PROXY MODE AUTHENTICATES WITH A DIFFERENT CREDENTIAL. main.go already
	// refuses to start in proxy mode without an address, and the proxy key is not
	// interchangeable with a provider key -- asking for one here would be asking
	// for the wrong secret.
	if os.Getenv("MOCHIII_USE_PROXY") == "true" {
		return false
	}
	key, base := s.credentials()
	if strings.TrimSpace(key) != "" {
		return false
	}
	// A LOOPBACK BASE IS A SUPPORTED KEYLESS SETUP -- a local OpenAI-compatible
	// server that wants no Authorization header, which .env.example documents as
	// legitimate. Asking that user for a provider key would be nagging them for
	// something they deliberately do not have.
	return !isLoopbackBase(base)
}

// envKeyWins reports whether this daemon's credential comes from its
// environment -- MOCHIII_API_KEY, or proxy mode -- which outranks any key stored
// through Connect.
func envKeyWins() bool {
	return strings.TrimSpace(os.Getenv("MOCHIII_API_KEY")) != "" ||
		os.Getenv("MOCHIII_USE_PROXY") == "true"
}

// keyReplaceable reports whether a failure of this class is the key's, AND a key
// given through Connect would be used from the next request on. See
// protocol.TokenResponse.KeyReplaceable.
func keyReplaceable(class ModelErrorClass) bool {
	if class != ClassAuth && class != ClassQuotaExceeded {
		return false
	}
	return !envKeyWins()
}

// isLoopbackBase reports whether an api_base addresses this machine. An
// unparseable or host-less base is NOT treated as loopback: the safe default is
// to assume a remote provider that will want a credential.
func isLoopbackBase(base string) bool {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return false
	}
	host := u.Hostname()
	switch {
	case host == "":
		return false
	case strings.EqualFold(host, "localhost"):
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// handleConnect answers a ConnectRequest.
func (s *Server) handleConnect(ctx context.Context, enc *json.Encoder, req protocol.ConnectRequest) {
	resp := s.connectResult(ctx, req)
	resp.ProtocolVersion = protocol.ProtocolVersion
	if err := enc.Encode(resp); err != nil {
		s.logger.Printf("connect response encode error: %v", err)
	}
}

// connectResult is the whole decision, separated from the encoding so every
// branch of it is testable without a socket.
func (s *Server) connectResult(ctx context.Context, req protocol.ConnectRequest) protocol.ConnectResponse {
	path, err := credentialsPath()
	if err != nil {
		return protocol.ConnectResponse{Error: err.Error()}
	}

	// A key in the daemon's ENVIRONMENT beats a stored one, and it did so before
	// this request arrived. Saying so is the difference between a user seeing
	// "connected" and then watching the old key still be used, and a user being
	// told why.
	envOverride := envKeyWins()

	switch {
	case req.Forget:
		removed, err := forgetCredential(path)
		if err != nil {
			return protocol.ConnectResponse{Error: err.Error()}
		}
		// The in-memory key is deliberately NOT cleared: this daemon is serving
		// turns, and silently cutting its credential mid-session would turn a
		// tidy-up into an outage. The removal takes effect at the next start.
		detail := "the stored key was removed; this daemon keeps the key it is already running with until it restarts"
		if !removed {
			detail = "no key was stored, so there was nothing to remove"
		}
		return protocol.ConnectResponse{
			Ok: true, Outcome: protocol.ConnectRemoved, EnvOverride: envOverride,
			Detail: detail,
		}

	case req.Show:
		stored, warn, loadErr := loadCredential(path)
		if loadErr != nil {
			return protocol.ConnectResponse{Error: loadErr.Error()}
		}
		detail := "no key is stored"
		if stored.configured() {
			detail = "stored, and not verified when it was saved"
			if stored.Verified {
				detail = "stored, and the provider accepted it when it was saved"
			}
		}
		if warn != "" {
			detail += "; " + warn
		}
		return protocol.ConnectResponse{
			Ok: true, Outcome: protocol.ConnectShown, Detail: detail,
			MaskedKey: maskKey(stored.APIKey), APIBase: stored.APIBase,
			InUse: stored.configured() && !envOverride, EnvOverride: envOverride,
		}
	}

	key := strings.TrimSpace(req.APIKey)
	if err := validateKeyShape(key); err != nil {
		return protocol.ConnectResponse{Error: err.Error() + "; nothing was saved"}
	}

	// WHERE THE KEY GOES. An address the client named wins; otherwise the key's
	// own prefix says whose it is (resolveConnectBase), and failing that it is
	// for the provider this daemon is talking to.
	//
	// "The provider in use" is the base IN FORCE, not the stored credential's.
	// The stored one used to come first, which is the CLI's order and is right
	// there -- no daemon is running. Here one is, and it can be on a different
	// provider than the stored credential names (FOUND 2026-10-05).
	//
	// Not when the environment wins: the base in force may then be the managed
	// proxy's, and a provider key must never be sent there to be "verified".
	inUse := ""
	if !envOverride {
		_, inUse = s.credentials()
	}
	// NOR IS THE PROXY'S ADDRESS A PROVIDER'S. In proxy mode MOCHIII_API_BASE is
	// where the proxy lives, and with nothing stored it used to be the address a
	// pasted provider key was "verified" against -- a provider credential sent
	// to a host that did not issue it. It is left out, so the key goes to the
	// provider its prefix names, or to the default, and never to the proxy.
	envBase := os.Getenv("MOCHIII_API_BASE")
	if os.Getenv("MOCHIII_USE_PROXY") == "true" {
		envBase = ""
	}
	stored, _, _ := loadCredential(path)
	target := resolveConnectBase(req.APIBase, key, inUse, stored, envBase)
	if len(target.Candidates) > 0 {
		ids := make([]string, 0, len(target.Candidates))
		names := make([]string, 0, len(target.Candidates))
		for _, p := range target.Candidates {
			ids, names = append(ids, p.ID), append(names, p.Name)
		}
		return protocol.ConnectResponse{
			Ok: false, Outcome: protocol.ConnectNeedsProvider, Candidates: ids, EnvOverride: envOverride,
			Detail: "keys that start with \"sk-\" are issued by " + strings.Join(names, ", ") +
				", and nothing in this one says which",
		}
	}
	base := target.Base
	if err := validateConnectBase(base); err != nil {
		return protocol.ConnectResponse{Error: err.Error()}
	}

	setup := providerSetup{Check: verification{Outcome: verifyInconclusive, Detail: "not checked, because the client asked for no verification"}}
	if !req.NoVerify {
		// BOUNDED, as the CLI's check is: a provider that took the connection and
		// never answered held /connect open for as long as the client waited
		// (FOUND 2026-10-01 -- the CLI had the timeout, this path did not). A
		// provider whose models have to be found and tried gets the longer bound.
		timeout := connectVerifyTimeout
		if !usesRoutingDialect(base) {
			timeout = providerSetupTimeout
		}
		vctx, cancel := context.WithTimeout(ctx, timeout)
		setup = setUpProvider(vctx, http.DefaultClient, base, key, s.logger.Printf)
		cancel()
	}
	result := setup.Check

	// A REFUSED KEY IS NOT STORED AND NOT ADOPTED. The daemon keeps serving with
	// whatever it had; replacing a working key with one the provider has just
	// rejected would break the next turn for the sake of an instruction that was
	// already known to be wrong.
	if result.Outcome == verifyRejected {
		return protocol.ConnectResponse{
			Ok: false, Outcome: protocol.ConnectRejected, Detail: result.Detail,
			APIBase: base, EnvOverride: envOverride,
		}
	}

	if err := saveCredential(path, storedCredential{
		APIBase: base, APIKey: key, Verified: result.proven(),
		DefaultModel: setup.DefaultModel, Models: setup.Models, QuietModels: setup.Quiet,
	}); err != nil {
		return protocol.ConnectResponse{Error: err.Error()}
	}

	// LIVE. This is what makes /connect worth having over the CLI: the key, the
	// address and the provider's own models are in force for the next turn, with
	// no restart. Skipped when the environment wins, because adopting it then
	// would make the daemon disagree with what a restart would do -- and a
	// credential that changes depending on whether you restarted is worse than
	// one that is merely inconvenient.
	outcome := protocol.ConnectUnverified
	if result.proven() {
		outcome = protocol.ConnectAccepted
	}
	resp := protocol.ConnectResponse{
		Ok: true, Outcome: outcome, Detail: result.Detail,
		MaskedKey: maskKey(key), APIBase: base,
		InUse: !envOverride, EnvOverride: envOverride,
		Model: setup.DefaultModel, ModelCount: len(setup.Models), ModelTested: setup.Tested,
	}
	if !envOverride {
		switched := strings.TrimSpace(inUse) != "" && !sameAPIBase(inUse, base)
		resp.Notes = connectNotes(base, setup, switched)
		s.setProvider(key, base, providerTierConfig(s.cfg, providerName(base), setup.Models, setup.Quiet, setup.DefaultModel))
	}
	return resp
}

// setProvider puts a key, its address and the tiers that go with them into
// force together, under the lock that guards them. tiers is nil for a provider
// whose models are the configured ones.
//
// The swap has to be guarded because prompts read all three from other
// goroutines: the agent loop and the single-shot path both take the credential
// per request, so an unsynchronised assignment here is a data race the race
// detector would find and a torn read a user would not.
func (s *Server) setProvider(key, base string, tiers *Config) {
	s.credMu.Lock()
	defer s.credMu.Unlock()
	s.apiKey = key
	if strings.TrimSpace(base) != "" {
		s.apiBase = base
	}
	s.tierCfg = tiers
}

// tierConfig is the config whose tiers are in force: the provider's own models
// when the provider in use has them (see Server.tierCfg), otherwise cfg.
func (s *Server) tierConfig() *Config {
	s.credMu.RLock()
	defer s.credMu.RUnlock()
	if s.tierCfg != nil {
		return s.tierCfg
	}
	return s.cfg
}
