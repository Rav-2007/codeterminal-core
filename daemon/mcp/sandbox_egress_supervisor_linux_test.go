//go:build linux

package mcp

import (
	"testing"
)

// WHY THERE IS NO IN-PROCESS SUPERVISOR TEST HERE, and why that is the right
// call rather than a gap.
//
// The obvious cheap test -- install the connect-trapping filter on a locked
// thread of THIS process and supervise it from another goroutine -- was written,
// passed on its own, and then hung the whole package under -race for ten
// minutes. The reason is structural, not a flake: a seccomp filter is inherited
// by every thread CLONED from the filtered one, and the Go runtime creates
// threads whenever it likes. So the trap leaks onto threads the test never meant
// to filter, and a connect(2) made there -- by the supervisor's own
// connect-on-behalf, or by an unrelated test later in the same binary -- traps
// with nothing able to answer it and blocks forever.
//
// Production never has that shape. The filter is installed in the HELPER
// process, and the supervisor runs in the DAEMON, which never installs one, so
// no supervisor syscall can be trapped by the filter it is answering. Testing
// the firewall in one process tests an arrangement the product does not have,
// and pays for it with a deadlock.
//
// So the firewall is proven the way it actually runs, across processes:
//
//	EgressFilterUsable (below)          the real helper in its own process: fd
//	                                    handoff, PR_SET_PTRACER, the supervisor's
//	                                    cross-process reads, a DENIED connect and
//	                                    an ALLOWED one (connect-on-behalf).
//	TestAnEgressFilteredCommandStillRuns  (daemon) the listener fd survives
//	                                    systemd-run -> env -> helper.
//	TestMetadataIsRefusedThroughTheRealTool (daemon) curl to 169.254.169.254
//	                                    through the real tool exits 7, not 28.
//
// The decode and deny-set logic, which needs no filter at all, is unit-tested in
// sandbox_egress_linux_test.go.

// THE CROSS-PROCESS PROOF: EgressFilterUsable runs the real helper as a separate
// process through the whole chain -- fd handoff over the socketpair, PR_SET_PTRACER,
// and the supervisor's cross-process reads (process_vm_readv, pidfd_getfd) -- and
// must find the metadata endpoint refused and a loopback listener allowed. On a
// host with Landlock this must succeed; a failure is a real regression, not a skip.
//
// Neuter check: drop the egressDenied branch in egressSupervisor.handle and the
// helper's denied-target connect stops returning EPERM, so the probe reports false.
func TestEgressFilterUsableEnforcesEndToEnd(t *testing.T) {
	if !LandlockUsable() {
		t.Skip("NOT RUN: Landlock cannot be enforced on this host, so egress rides on nothing")
	}
	if !EgressFilterUsable() {
		t.Fatal("EgressFilterUsable is false on a Landlock host: the egress firewall did not enforce end to end")
	}
}
