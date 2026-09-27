package main

import (
	"mochiii/protocol"
)

// specGrant is one command the user answered "yes while this spec is active"
// to: the grant the daemon offered (an opaque name for that exact command),
// and what to call it on screen.
//
// THE CLIENT IS THE ONLY PLACE IT LIVES. The daemon keeps nothing between turns,
// so these are sent with each turn of the spec they were given under -- and
// they are held in memory only: never written to disk, forgotten when the spec
// is switched off or changed, and gone when the TUI quits. A grant that
// survived the session would be an "always allow", which belongs in
// models.json where the user writes it and can read it back.
type specGrant struct {
	digest string
	label  string
}

// maxClientSpecGrants matches what the daemon accepts in one turn; past it the
// oldest are dropped rather than sending ones the daemon would ignore.
const maxClientSpecGrants = 32

// canGrantForSpec reports whether the "yes while this spec is active" answer
// may be given to req: the daemon offered it, and a spec is active to hold it.
func (m chatModel) canGrantForSpec(req protocol.ToolApprovalRequest) bool {
	return req.SpecGrant != "" && m.activeSpec != ""
}

// rememberSpecGrant keeps req's grant for the active spec.
func (m *chatModel) rememberSpecGrant(req protocol.ToolApprovalRequest) {
	for _, g := range m.specGrants {
		if g.digest == req.SpecGrant {
			return
		}
	}
	label := sanitizeText(req.Server + "__" + req.Tool + " " + clipRunes(req.Arguments, 120))
	m.specGrants = append(m.specGrants, specGrant{digest: req.SpecGrant, label: label})
	if len(m.specGrants) > maxClientSpecGrants {
		m.specGrants = m.specGrants[len(m.specGrants)-maxClientSpecGrants:]
	}
}

// specGrantDigests is what goes on the wire with the next turn.
func (m chatModel) specGrantDigests() []string {
	if m.activeSpec == "" || len(m.specGrants) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.specGrants))
	for _, g := range m.specGrants {
		out = append(out, g.digest)
	}
	return out
}

// grantsFor sends grants only alongside the spec they belong to.
func grantsFor(spec string, grants []string) []string {
	if spec == "" {
		return nil
	}
	return grants
}

// clipRunes shortens s to at most n runes, marking the cut.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
