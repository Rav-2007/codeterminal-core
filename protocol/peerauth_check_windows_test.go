//go:build windows

package protocol

import "testing"

// checkPeerSID is the Windows half of the daemon's access control, and it is a
// pure function for the same reason checkPeerUID is: the match/mismatch
// decision must be testable without a real cross-user pipe, which an
// unprivileged test process cannot create.
//
// NOT RUN ON HARDWARE — these compile for windows/amd64 and have never been
// executed. They will run the first time CI has a Windows runner (launch plan
// Stage 2.3), which is the point of writing them now rather than then.

const (
	sidA = "S-1-5-21-1111111111-2222222222-3333333333-1001"
	sidB = "S-1-5-21-1111111111-2222222222-3333333333-1002"
)

func TestCheckPeerSID_SameUserIsAllowed(t *testing.T) {
	if err := checkPeerSID(sidA, sidA); err != nil {
		t.Errorf("a peer running as this daemon's own user was refused: %v", err)
	}
}

func TestCheckPeerSID_DifferentUserIsRefused(t *testing.T) {
	if err := checkPeerSID(sidB, sidA); err == nil {
		t.Error("a peer running as a DIFFERENT user was allowed; this is the check's entire job")
	}
}

// Two unknowns must not compare equal. This is the bug that would silently turn
// the whole check into a rubber stamp: if both sides fail to resolve and the
// code compares "" == "", every peer is admitted.
func TestCheckPeerSID_EmptyIdentitiesNeverMatch(t *testing.T) {
	if err := checkPeerSID("", ""); err == nil {
		t.Error("two unresolved identities compared equal; an unknown peer must be refused, not matched")
	}
	if err := checkPeerSID("", sidA); err == nil {
		t.Error("an unresolved PEER identity was allowed")
	}
	if err := checkPeerSID(sidA, ""); err == nil {
		t.Error("an unresolved DAEMON identity was allowed; it cannot verify anything against nothing")
	}
}

// SIDs are compared exactly. A prefix match would admit a different account in
// the same domain, since SIDs share a long domain prefix and differ only in the
// trailing RID.
func TestCheckPeerSID_ComparesExactlyNotByPrefix(t *testing.T) {
	if err := checkPeerSID(sidA+"0", sidA); err == nil {
		t.Error("a SID sharing this daemon's full SID as a prefix was allowed; SIDs in one domain differ only in the trailing RID")
	}
}
