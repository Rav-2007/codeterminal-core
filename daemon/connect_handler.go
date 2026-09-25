package main

import (
	"context"
	"encoding/json"
	"net/http"
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

// setAPIKey swaps the key the daemon sends, under the lock that guards it.
//
// The swap has to be guarded because prompts read it from other goroutines: the
// agent loop and the single-shot path both take s.apiKey per request, so an
// unsynchronised assignment here is a data race the race detector would find and
// a torn read a user would not.
func (s *Server) setAPIKey(key, base string) {
	s.credMu.Lock()
	defer s.credMu.Unlock()
	s.apiKey = key
	if strings.TrimSpace(base) != "" {
		s.apiBase = base
	}
}

// credentials returns the key and base to use for one request.
func (s *Server) credentials() (key, base string) {
	s.credMu.RLock()
	defer s.credMu.RUnlock()
	return s.apiKey, s.apiBase
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
	envOverride := strings.TrimSpace(os.Getenv("MOCHIII_API_KEY")) != "" ||
		os.Getenv("MOCHIII_USE_PROXY") == "true"

	switch {
	case req.Forget:
		if err := forgetCredential(path); err != nil {
			return protocol.ConnectResponse{Error: err.Error()}
		}
		// The in-memory key is deliberately NOT cleared: this daemon is serving
		// turns, and silently cutting its credential mid-session would turn a
		// tidy-up into an outage. The removal takes effect at the next start.
		return protocol.ConnectResponse{
			Ok: true, Outcome: protocol.ConnectRemoved, EnvOverride: envOverride,
			Detail: "the stored key was removed; this daemon keeps the key it is already running with until it restarts",
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

	stored, _, _ := loadCredential(path)
	base := firstNonEmpty(req.APIBase, stored.APIBase, os.Getenv("MOCHIII_API_BASE"), defaultAPIBase)
	if err := validateAPIBase(base); err != nil {
		return protocol.ConnectResponse{Error: err.Error()}
	}

	result := verification{Outcome: verifyInconclusive, Detail: "not checked, because the client asked for no verification"}
	if !req.NoVerify {
		result = verifyKey(ctx, http.DefaultClient, base, key)
	}

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

	if err := saveCredential(path, storedCredential{APIBase: base, APIKey: key, Verified: result.proven()}); err != nil {
		return protocol.ConnectResponse{Error: err.Error()}
	}

	// LIVE. This is what makes /connect worth having over the CLI: the key is in
	// force for the next turn, with no restart. Skipped when the environment wins,
	// because adopting it then would make the daemon disagree with what a restart
	// would do -- and a credential that changes depending on whether you restarted
	// is worse than one that is merely inconvenient.
	outcome := protocol.ConnectUnverified
	if result.proven() {
		outcome = protocol.ConnectAccepted
	}
	if !envOverride {
		s.setAPIKey(key, base)
	}
	return protocol.ConnectResponse{
		Ok: true, Outcome: outcome, Detail: result.Detail,
		MaskedKey: maskKey(key), APIBase: base,
		InUse: !envOverride, EnvOverride: envOverride,
	}
}
