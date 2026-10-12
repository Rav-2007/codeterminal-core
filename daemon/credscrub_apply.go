package main

import (
	"os"
	"strings"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// WHERE THE LITERAL CREDENTIAL SCRUBBER IS BUILT AND APPLIED. See credscrub.go
// for what it matches and why; this file is the daemon wiring: which values are
// registered, when the matcher is rebuilt, and the four application points.

// credentialValuesLocked returns the distinct secret values the daemon holds in
// memory or its environment, or was handed on stdin at start. Caller holds credMu (read or write): it reads
// s.apiKey directly rather than through credentials(), which would re-lock.
//
// s.apiKey is whatever is in force -- the key `connect` stored, one set live, or
// MOCHIII_PROXY_KEY in proxy mode -- so registering it covers the live
// credential whatever its source. The two environment names are registered too:
// they are the daemon's own, a child or tool that read them would hold the same
// secret, and they can differ from s.apiKey (MOCHIII_API_KEY set while proxy
// mode runs on the proxy key). The stored-but-overridden key on disk is NOT
// here: it is not in model-reachable memory, and the read-tool denylist already
// refuses ~/.mochiii/credentials.json (OPEN_ITEMS item 44).
func (s *Server) credentialValuesLocked() []string {
	var vals []string
	for _, v := range append([]string{
		s.apiKey,
		os.Getenv("MOCHIII_API_KEY"),
		os.Getenv("MOCHIII_PROXY_KEY"),
	}, s.launchSecrets...) {
		if strings.TrimSpace(v) != "" {
			vals = append(vals, v)
		}
	}
	return vals
}

// rebuildCredScrubberLocked recompiles the matcher from the current credentials.
// Caller holds credMu for writing. setProvider calls this after swapping the
// key; main.go calls rebuildCredScrubber once at startup.
func (s *Server) rebuildCredScrubberLocked() {
	s.credScrub = buildCredScrubber(s.credentialValuesLocked())
}

// rebuildCredScrubber takes the write lock and rebuilds. For startup, where no
// other goroutine holds the lock yet.
func (s *Server) rebuildCredScrubber() {
	s.credMu.Lock()
	defer s.credMu.Unlock()
	s.rebuildCredScrubberLocked()
}

// currentCredScrubber returns the immutable matcher in force. The pointer is
// fetched under the lock; the scan that follows needs none, because a rebuild
// replaces the pointer rather than mutating what it points at.
func (s *Server) currentCredScrubber() *credScrubber {
	s.credMu.RLock()
	defer s.credMu.RUnlock()
	return s.credScrub
}

// credRedact replaces every known-credential occurrence in text with the
// placeholder and returns the count. ALWAYS ON: unlike scrub(), it takes no
// scrubDisabled argument, because --no-scrub governs the heuristic and the
// daemon's own live credential is not a heuristic match. A nil matcher (no
// credential held) is a no-op.
func (s *Server) credRedact(text string) (string, int) {
	return s.currentCredScrubber().redact(text)
}

// refuseOutboundCredential checks an outbound web tool's raw arguments for a
// known credential and, if one is present, returns a refusal Result and true.
// The call is REFUSED ENTIRELY -- not stripped and sent -- because a search or
// fetch with the key cut out still tells the other end a request was made, and
// the safe answer to "the model tried to send our key off the box" is to not
// make the call at all. Scanning the whole raw arguments JSON catches the value
// in the query, the URL, or any field a future web tool adds, without this
// function having to track each one. The refusal is recorded to the audit log
// with the form that matched, never the value, and the model is told why so it
// does not retry.
func (s *Server) refuseOutboundCredential(toolName, argsJSON string) (mcp.Result, bool) {
	hit, labels := s.currentCredScrubber().scan(argsJSON)
	if !hit {
		return mcp.Result{}, false
	}
	s.recordCredentialRefusal(toolName, labels)
	res, _ := toolError("refused: this %s call contains one of this machine's own API credentials "+
		"(matched form: %s), so it was not sent. Nothing left the machine. Do not put credentials, or "+
		"anything derived from them, in a web request.", toolName, strings.Join(labels, ", "))
	return res, true
}

// recordCredentialRefusal writes one audit line for a refused outbound call. The
// matched form labels go in DenyCause -- the KIND of match ("raw", "b64",
// "fragment"), never the credential. It is in addition to the ordinary dispatch
// record for the call; this is the line that says WHY it returned an error.
func (s *Server) recordCredentialRefusal(toolName string, labels []string) {
	s.toolAudit.record(toolAuditEvent{
		Server:    mcp.BuiltinServerName,
		Tool:      toolName,
		Lane:      protocol.LaneFirstParty,
		Policy:    string(mcp.PolicyAllow),
		Source:    auditDeniedCredential,
		Outcome:   auditOutcomeRefused,
		DenyCause: "credential_in_outbound_request[" + strings.Join(labels, ",") + "]",
	})
}
