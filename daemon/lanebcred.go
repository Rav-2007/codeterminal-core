package main

import (
	"fmt"
	"strings"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// A CREDENTIAL THIS MACHINE HOLDS IS NEVER HANDED TO A THIRD-PARTY TOOL.
//
// mcpargscrub.go redacts secrets by SHAPE, and a shape is a guess: a bare
// `sk-...` DeepSeek key, a novel format, or any encoding of a key passes it.
// The daemon does not have to guess about the credentials it holds: the
// provider key, MCP servers' stored keys, the GitHub token
// (credentialValuesLocked). credscrub.go matches those exact values and their
// common encodings (raw, base64, hex, url-escaped, reversed, a long fragment)
// with no false positives.
//
// web_search and web_fetch have refused a call carrying one since the literal
// scrubber landed (refuseOutboundCredential). A third-party MCP tool never
// was checked, so the model could hand the user's own key to any server the
// user added, and the approval prompt was the only thing between them
// (found 2026-11-19; owner decision 2026-11-20). Now it is refused the same
// way, and for the same reason it is REFUSED rather than stripped and sent:
// the call itself was the leak's carrier.
//
// Refused before anyone is asked: there is no answer to "may this send your
// key to that server?" that the daemon should act on. Like credRedact, this is
// not turned off by --no-scrub, which governs the heuristic.
func (s *Server) laneBCredentialRefusal(spec mcp.Tool, arguments string) (toolDecision, bool) {
	if spec.Lane != protocol.LaneThirdParty {
		return toolDecision{}, false
	}
	hit, labels := s.currentCredScrubber().scan(arguments)
	if !hit {
		return toolDecision{}, false
	}
	s.logger.Printf("agent: refusing %s/%s: its arguments carry one of this machine's own credentials (matched form: %s)",
		spec.Server, spec.Name, strings.Join(labels, ", "))
	return toolDecision{
		tool:   spec,
		policy: mcp.PolicyDeny,
		source: auditDeniedCredential,
		cause:  "credential_in_outbound_request[" + strings.Join(labels, ",") + "]",
		reason: fmt.Sprintf("refused: this call to the %q server contains one of this machine's own credentials "+
			"(matched form: %s), so it was not sent and nobody was asked. Nothing left the machine. Do not put "+
			"credentials, or anything derived from them, in a tool's arguments: a server that needs a key reads "+
			"the one stored for it.", spec.Server, strings.Join(labels, ", ")),
	}, true
}
