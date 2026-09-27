package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// "YES WHILE THIS SPEC IS ACTIVE" -- a grant for one exact command that
// outlives the turn, and why it is safe to have one.
//
// A /spec build is several turns of "write the tests, run them, fix what fails,
// run them again". A grant that dies with the turn (approve_for_turn) makes the
// user approve the same `go test ./...` at the start of every one of them. This
// grant covers that command for as long as the spec stays active -- and is
// bounded on every side, because the audit finding it must not reopen (F-02) is
// a grant that authorises what the user never saw:
//
//   - ONE COMMAND. It covers exactly the command the user approved: the same
//     tool with the same arguments, however they are spaced or ordered
//     (specGrantDigest). A different command asks again.
//   - ONLY WHERE THE COMMAND IS CONTAINED. It is offered, and honoured, only for
//     a command-running tool that is really sandboxed on this host, in a turn
//     whose commands run in the private working copy -- so what it runs cannot
//     write to the user's files. (It can reach the network: the build tools
//     need it. The approval prompt says so, from the same sandbox computation.)
//   - ONLY WHILE THE SPEC IS ACTIVE. It is honoured only in a turn that names a
//     spec, and the clients send only the grants given under the spec that is
//     active now.
//   - THE DAEMON KEEPS NOTHING. The client holds the grants in memory and sends
//     them with each turn; switching the spec off or changing it, or quitting,
//     forgets them. Nothing is written to disk anywhere.
//   - NEVER A LAUNCH OR AN OUTSIDE PATH, the same two things a turn grant never
//     covers (see resolveExecutable).

// maxSpecGrants bounds what a turn accepts. A spec's builds run a handful of
// distinct commands; more than this is not a workflow.
const maxSpecGrants = 32

// specGrantSet is one turn's spec grants: what the client sent, plus any the
// user gives during the turn.
type specGrantSet struct {
	digests map[string]bool
}

func (g *specGrantSet) has(digest string) bool { return g != nil && g.digests[digest] }

func (g *specGrantSet) add(digest string) {
	if g != nil && digest != "" {
		g.digests[digest] = true
	}
}

type specGrantsCtxKey struct{}

// withSpecGrants marks ctx as a turn with an active spec, carrying the grants
// the client sent that are well-formed. Only such a turn may offer or honour a
// spec grant.
func withSpecGrants(ctx context.Context, sent []string) context.Context {
	set := &specGrantSet{digests: map[string]bool{}}
	for _, d := range sent {
		if len(set.digests) >= maxSpecGrants {
			break
		}
		if isSHA256Hex(d) {
			set.digests[strings.ToLower(d)] = true
		}
	}
	return context.WithValue(ctx, specGrantsCtxKey{}, set)
}

// specGrantsFrom returns the turn's grants, or nil when no spec is active.
func specGrantsFrom(ctx context.Context) *specGrantSet {
	set, _ := ctx.Value(specGrantsCtxKey{}).(*specGrantSet)
	return set
}

// specGrantDigest names one exact command: the tool and its arguments in a
// canonical form (object keys sorted, spacing removed), so the same command
// written differently in a later turn is still the same grant. Arguments that
// are not a JSON object cannot be named, so they get no grant.
func specGrantDigest(qualified, arguments string) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(arguments))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return "", false
	}
	canonical, err := json.Marshal(v) // map keys are marshalled sorted
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(append([]byte(qualified+"\x00"), canonical...))
	return hex.EncodeToString(sum[:]), true
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && !bytes.ContainsAny([]byte(s), " \t")
}
