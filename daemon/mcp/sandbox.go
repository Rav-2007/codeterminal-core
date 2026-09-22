package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SandboxMode specifies the level of OS-level containment applied to a
// third-party MCP subprocess (Lane B).
type SandboxMode string

const (
	// SandboxAuto automatically selects the best available sandbox mechanism on
	// the host system (Bubblewrap if bwrap exists, Docker if docker exists, or fallback to host mode).
	SandboxAuto SandboxMode = "auto"

	// SandboxNone runs the MCP process under the host user's UID with process
	// group containment and strict env scrubbing, but without path/net namespaces.
	SandboxNone SandboxMode = "none"

	// SandboxBubblewrap runs the process inside a Bubblewrap (bwrap) unprivileged
	// namespace container, restricting filesystem access strictly to the workspace
	// directory and isolated /tmp.
	SandboxBubblewrap SandboxMode = "bubblewrap"

	// SandboxDocker runs the process inside a transient Docker container.
	SandboxDocker SandboxMode = "docker"

	// SandboxLandlock confines the process with Landlock and a seccomp filter,
	// both unprivileged and neither needing a user namespace -- so it works on
	// hosts where bwrap is installed and forbidden (see BwrapUsable). It is
	// weaker than bubblewrap in stated ways (see LandlockLimits) and is only
	// ever chosen automatically for a caller that sets LandlockFallback.
	SandboxLandlock SandboxMode = "landlock"
)

// SandboxConfig specifies the runtime confinement rules for launching an MCP server.
type SandboxConfig struct {
	Mode              SandboxMode
	WorkspaceRoot     string
	ReadOnlyWorkspace bool
	AllowNetwork      bool
	MemoryLimitMB     int
	CPULimit          float64

	// PidsLimit caps the number of tasks the command may create. Zero means no
	// pids limit, which is the thing that makes a fork bomb possible: no memory
	// cap stops `:(){ :|:& };:` either, because each shell is small.
	PidsLimit int

	// HomeDir is a host directory bound in as the command's HOME.
	//
	// EMPTY MEANS THE DEFAULT, and the default is bad. bwrap auto-creates the
	// parent directories of a bind mount, so with the workspace bound at
	// /home/<user>/... the path in HOME exists inside the namespace and is
	// writable -- on bwrap's INTERNAL TMPFS, which is RAM and which is
	// destroyed when the command exits.
	//
	// That is worse than it sounds once MemoryLimitMB is set. A cold
	// GOMODCACHE or npm cache is re-downloaded on every single run, into a
	// filesystem whose pages are charged to the very cgroup the memory limit
	// is enforcing -- so a limit sized for a compiler gets spent on a package
	// download, and an ordinary `go build` starts getting OOM-killed for
	// reasons the user cannot see. Binding a real directory here fixes the
	// repeated download and keeps the cache off the memory budget at once.
	//
	// Bound at the SAME PATH inside as out, exactly as the workspace is, so
	// HOME has one value in every mode and no caller has to ask which sandbox
	// it got.
	HomeDir string

	// Image is the container image the docker backend runs in. There is no
	// sane default: the image has to carry whatever toolchain the command
	// needs, which only the caller knows.
	//
	// Empty means the docker backend is UNAVAILABLE, and that is why this field
	// exists. Without it the backend appended the command directly after the
	// docker flags, so `docker run --rm -i ... make leak` asked Docker for an
	// image literally named "make":
	//
	//	Unable to find image 'make:latest' locally
	//	docker: pull access denied for make
	//
	// The docker backend had therefore never worked. Nothing noticed because
	// SandboxAuto only reached it when bwrap was absent, and bwrap is installed
	// nearly everywhere -- so the branch was effectively dead until the bwrap
	// capability probe started routing real traffic to it.
	Image string

	// LandlockFallback lets SandboxAuto choose the landlock backend when
	// neither bwrap nor docker can run.
	//
	// OPT-IN, AND ONLY sandbox_exec OPTS IN. The landlock policy grants the
	// system directories, the command's toolchain, the workspace and HomeDir --
	// and deliberately not the user's real home. A third-party MCP server
	// (stdioclient.go) usually lives in exactly that real home: an npx cache
	// under ~/.npm, a venv under ~/.local. Choosing landlock for it
	// automatically would break servers that run today, on precisely the
	// hosts where they have always run unconfined. Lane B keeps its behaviour
	// until that is a decision someone makes on purpose.
	LandlockFallback bool

	// ScopeUnit names the transient systemd scope the limiter puts the command
	// in, so it can be torn down by name afterwards.
	//
	// EMPTY MEANS TODAY'S BEHAVIOUR: an auto-generated unit name and no
	// teardown, which is what bwrap, docker and the none backend keep. Only the
	// landlock backend sets it, because only landlock lacks bwrap's PID
	// namespace -- where a process the command backgrounds is killed when the
	// command (pid 1 in the namespace) exits. With no namespace, a named scope
	// plus `systemctl --user kill` on it is how the same processes get reaped.
	// See ScopeTeardownArgs.
	ScopeUnit string
}

// sandboxSystemPaths is what every confining backend lets a command read: the
// system binaries and libraries, and what TLS and DNS need. ONE LIST, read by
// both the bwrap binds and the landlock policy, so the two backends cannot
// quietly come to disagree about what "the system" is.
var sandboxSystemPaths = []string{
	"/usr", "/lib", "/lib64", "/bin", "/sbin",
	"/etc/ssl", "/etc/ca-certificates", "/etc/pki", "/etc/resolv.conf",
}

// DockerUsable reports whether the docker backend can actually run cfg.
//
// Presence of the binary is not enough, for the third time in this file: a
// docker with no image to run is as useless as a bwrap that cannot unshare.
func DockerUsable(cfg SandboxConfig) bool {
	if strings.TrimSpace(cfg.Image) == "" {
		return false
	}
	_, err := lookPath("docker")
	return err == nil
}

var lookPath = exec.LookPath
var getUID = os.Getuid
var getGID = os.Getgid

// BwrapUsable reports whether bwrap can actually create a user namespace on
// this host, by running one.
//
// PRESENCE IS NOT CAPABILITY, and assuming otherwise is the mistake this file
// has now made twice. The first was --nosuid, a flag that looked right and made
// every invocation fail. This is the second: selecting the bubblewrap backend
// because the BINARY IS ON PATH.
//
// Ubuntu 24.04 and later ship kernel.apparmor_restrict_unprivileged_userns=1 by
// default. Under it /usr/bin/bwrap exists, is executable, and fails every time:
//
//	bwrap: setting up uid map: Permission denied
//
// So on a very common desktop Linux, lookPath said yes, the sandbox was
// selected, and the tool could never run — while a working Docker backend sat
// unused two branches below, because the bwrap branch had already matched.
//
// The probe is a real bwrap invocation because that is the only thing that
// answers the question; reading the sysctl would be a proxy, and proxies are
// what got us here. It runs once per process (sync.OnceValue) and costs one
// fork of /bin/true against a read-only bind of /.
var BwrapUsable = sync.OnceValue(func() bool {
	if _, err := lookPath("bwrap"); err != nil {
		return false
	}
	truePath, err := lookPath("true")
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, "bwrap",
		"--ro-bind", "/", "/",
		"--unshare-user",
		"--die-with-parent",
		"--", truePath)
	probe.Env = ServerEnv(nil)
	return probe.Run() == nil
})

// limiterRuntimeEnvNames are the variables systemd-run needs to reach the user
// manager, and that NOTHING BEHIND IT MAY KEEP.
//
// XDG_RUNTIME_DIR is the path to the session bus. A command that can reach that
// bus can ask systemd to start a unit -- outside the sandbox, without limits,
// as the user. Handing it to a confined build command would be a hole big
// enough to walk the whole sandbox through, so the limiter prefix strips both
// names again before the command it is wrapping ever starts.
//
// MEASURED: under the scrubbed environment ServerEnv produces (HOME and PATH
// only, on Linux), systemd-run fails with "Failed to connect to bus: No medium
// found". So the variable genuinely has to be added for the limiter and
// genuinely has to be removed for the payload.
var limiterRuntimeEnvNames = []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"}

// LimiterEnv returns the environment a limited command must be launched with:
// the same scrub every subprocess gets, plus only what systemd-run needs.
//
// Callers must use this rather than ServerEnv when LimitsApply is true, and the
// two are different functions rather than one with a flag so that a caller who
// forgets gets a limiter that does not run -- which LimiterUsable's probe, run
// under this exact environment, will already have reported as unavailable.
func LimiterEnv(allow []string) []string {
	return ServerEnv(append(append([]string{}, allow...), limiterRuntimeEnvNames...))
}

// probeMemoryMaxBytes is the limit the usability probe asks for. Small enough
// to be unmistakable when read back, and never applied to anything real.
const probeMemoryMaxBytes = 64 * 1024 * 1024

// LimiterUsable reports whether a systemd user scope will actually BOUND what
// it is asked to bound here, by creating one and reading the limit back out of
// the kernel.
//
// PRESENCE IS NOT CAPABILITY, FOR THE FOURTH TIME IN THIS FILE, and the fourth
// was this function itself. The first was --nosuid, a flag that looked right and
// broke every invocation. The second was selecting bwrap because the binary was
// on PATH. The third is the two ways a scope can look creatable and not be, both
// listed below. The fourth is subtler and cost a green CI run to find:
//
//	systemd-run ACCEPTS -p MemoryMax= and creates the scope even when the memory
//	controller is not delegated to the user manager. The property is recorded and
//	silently never enforced.
//
// The old probe ran /bin/true inside such a scope and returned whether it
// exited zero -- which it does either way. So on a host with no delegated
// memory controller this function returned true, LimitsApply returned true, and
// the approval prompt told the user their command was capped at N MiB while it
// allocated 4 GiB. Measured exactly that way on a CI runner: the cap held on a
// workstation whose user manager has `cpu memory pids` delegated, and did
// nothing on a runner that does not.
//
// So the probe now reads back what the kernel actually applied. It runs, inside
// the scope, the smallest thing that can answer the question: find this
// process's own cgroup, and print that cgroup's memory.max. A delegated
// controller prints the byte count that was asked for; an undelegated one prints
// "max", which is the kernel saying "unbounded" in as many words.
//
// The two older traps this still has to clear, unchanged:
//
//   - No user systemd (a container, a minimal image, an init that is not
//     systemd), or no cgroup v2 delegation of the controllers we set.
//   - No session bus reachable from the environment the command will ACTUALLY
//     run with. This one is the trap: probing with the ambient environment
//     passes, and the real invocation -- scrubbed to HOME and PATH by
//     ServerEnv -- then fails every time. So the probe runs under LimiterEnv,
//     which is precisely what the real call gets.
//
// CGROUP V1 READS AS UNUSABLE, deliberately. /proc/self/cgroup there names a
// per-controller hierarchy and there is no memory.max to read, so this cannot
// prove enforcement -- and an unprovable bound must be reported as absent. That
// is the whole lesson of the four paragraphs above; a host that loses the
// limiter this way is told so honestly by the prompt rather than reassured
// falsely.
//
// One scope, once per process.
var LimiterUsable = sync.OnceValue(func() bool {
	if _, err := lookPath("systemd-run"); err != nil {
		return false
	}
	// sh for the two builtins that locate the cgroup, cat to read the one file.
	// Both are as ordinary as the /bin/true this used to run, and unlike it they
	// can answer the question that matters.
	shPath, err := lookPath("sh")
	if err != nil {
		return false
	}
	if _, err := lookPath("cat"); err != nil {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read INSIDE the scope, not after it. A transient scope is collected as
	// soon as its process exits, so a probe that printed its cgroup path and
	// left the daemon to read memory.max would be racing the cleanup for a file
	// that is usually already gone.
	// Both files, in one cat: if either is missing -- a kernel with no swap
	// accounting, a controller that is not delegated -- cat exits non-zero and
	// the probe reports unusable, which is the honest answer for a bound this
	// cannot prove.
	const readBackOwnMemoryLimits = `IFS=: read -r _ _ p < /proc/self/cgroup; exec cat "/sys/fs/cgroup$p/memory.max" "/sys/fs/cgroup$p/memory.swap.max"`

	// The same controllers the real invocation sets: a host that delegates
	// none of them must not report the limiter as usable.
	probe := exec.CommandContext(ctx, "systemd-run",
		"--user", "--scope", "--quiet",
		"-p", fmt.Sprintf("MemoryMax=%d", probeMemoryMaxBytes),
		"-p", "MemorySwapMax=0",
		"-p", "TasksMax=16", "-p", "CPUQuota=100%",
		"--", shPath, "-c", readBackOwnMemoryLimits)
	probe.Env = LimiterEnv(nil)

	out, err := probe.Output()
	if err != nil {
		return false
	}
	return memoryLimitsAreEnforced(string(out), probeMemoryMaxBytes)
})

// memoryLimitsAreEnforced reads back a scope's memory.max and memory.swap.max
// and reports whether the pair is a real bound at or below want.
//
// BOTH LINES MATTER, and the second is the one that was missing. "max" is the
// kernel's word for unbounded and is exactly what an undelegated memory
// controller reports, so it can never be accepted for memory.max. And
// memory.swap.max must be 0, because any other value lets the cgroup exceed
// memory.max by that much again through swap -- which is not a smaller bound,
// it is a different one, and not the one the approval prompt quoted.
//
// A memory.max ABOVE what was asked for is refused too: it means something
// other than our property decided the limit, and a bound we did not set is not
// a bound we can promise.
func memoryLimitsAreEnforced(readBack string, wantMax int64) bool {
	fields := strings.Fields(readBack)
	if len(fields) != 2 {
		return false
	}
	max, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || max <= 0 || max > wantMax {
		return false
	}
	swap, err := strconv.ParseInt(fields[1], 10, 64)
	return err == nil && swap == 0
}

// wantsLimits reports whether cfg asked for any resource bound at all.
func wantsLimits(cfg SandboxConfig) bool {
	return cfg.MemoryLimitMB > 0 || cfg.CPULimit > 0 || cfg.PidsLimit > 0
}

// LimitsApply answers, for THIS host and right now, whether the bounds cfg asks
// for will actually be enforced.
//
// SEPARATE FROM Confines, because they are separate properties and conflating
// them is how a prompt lies. A command can be inside a namespace and still able
// to exhaust the host's memory: bubblewrap has no memory, CPU or pids flag at
// all, and never had. It can also be bounded and unconfined -- host execution
// inside a scope -- which is strictly better than host execution without one.
//
// The docker backend sets its own --memory/--cpus/--pids-limit, so it is
// bounded by its own flags and needs no scope.
func LimitsApply(cfg SandboxConfig) bool {
	if !wantsLimits(cfg) {
		return false
	}
	if ResolveMode(cfg) == SandboxDocker {
		return true
	}
	return LimiterUsable()
}

// limiterPrefix builds the argv that puts a command in a bounded cgroup.
//
// It wraps the OUTSIDE of the sandbox rather than the inside: the scope must
// contain bwrap and everything bwrap starts, or a fork bomb inside the
// namespace is outside the accounting. --scope (not --service) so the command
// stays a child of this daemon and its exit status is still ours to read;
// --quiet so "Running as unit: ..." does not land in the tool output the model
// reads as the command's own.
func limiterPrefix(cfg SandboxConfig) []string {
	args := []string{"--user", "--scope", "--quiet", "--collect"}
	if cfg.ScopeUnit != "" {
		// A name so the scope's whole cgroup can be killed afterwards by that
		// name (ScopeTeardownArgs). Empty for every caller but landlock, so the
		// other backends' argv is byte-for-byte what it was before this field
		// existed.
		args = append(args, "--unit="+cfg.ScopeUnit)
	}
	if cfg.MemoryLimitMB > 0 {
		// BOTH, ALWAYS. MemoryMax alone is memory.max, which caps RESIDENT
		// memory and lets the cgroup push the rest to swap: on any host with
		// swap a "2048 MiB cap" is a 2048 MiB + all-of-swap cap, and a runaway
		// build thrashes the machine instead of dying. Measured on a CI runner
		// with ~4 GiB of swap: a 4 GiB allocation ran to completion under this
		// exact cap. It passed on a workstation only because that box has
		// SwapTotal=0, which turns memory.max into the hard bound it looked like.
		//
		// MemoryLimitMB therefore means TOTAL memory, and memory.swap.max=0 is
		// what makes the number mean that.
		args = append(args, "-p", fmt.Sprintf("MemoryMax=%dM", cfg.MemoryLimitMB))
		args = append(args, "-p", "MemorySwapMax=0")
	}
	if cfg.PidsLimit > 0 {
		args = append(args, "-p", fmt.Sprintf("TasksMax=%d", cfg.PidsLimit))
	}
	if cfg.CPULimit > 0 {
		args = append(args, "-p", fmt.Sprintf("CPUQuota=%d%%", int(cfg.CPULimit*100)))
	}
	// env(1) strips the bus back out before the payload starts. See
	// limiterRuntimeEnvNames.
	args = append(args, "--", "env")
	for _, name := range limiterRuntimeEnvNames {
		args = append(args, "-u", name)
	}
	return args
}

// ScopeTeardownArgs is the `systemctl` argv that reaps everything still alive in
// the named transient scope: build servers, watchers, anything a command left
// running in the background.
//
// WHY KILL, NOT STOP. `systemctl --user stop` sends SIGTERM and then waits up to
// the unit's TimeoutStopSec (90 s by default) for SIGKILL -- so a process that
// ignores SIGTERM would hang the tool call for a minute and a half. `kill
// --signal=KILL` sends SIGKILL to the whole cgroup at once and returns without
// waiting. `--kill-whom` defaults to `all`, so it needs no version-specific
// flag. On the common path there is nothing left to kill and the unit is already
// collected, so this fails with "unit not loaded" and the caller ignores it.
//
// This is landlock's stand-in for bwrap's PID-namespace reaping and
// --die-with-parent: the same cgroup-wide kill, asked for explicitly because
// there is no namespace to do it for us.
func ScopeTeardownArgs(unit string) []string {
	return []string{"--user", "kill", "--signal=KILL", unit}
}

// ResolveMode reports which backend WrapCommand would actually select for cfg.
//
// EXTRACTED SO THE CLAIM AND THE BEHAVIOUR ARE ONE COMPUTATION. The approval
// prompt has to tell the user whether this call will really be confined, and the
// only honest source for that is the selection WrapCommand is about to make.
// Answering it separately -- "is bwrap on PATH?", "is docker installed?" -- is
// how a prompt starts describing a sandbox the command does not get: the two
// would agree on the day they were written and drift on the first change to
// either. There is now one ladder, and both callers walk it.
//
// An explicitly requested mode is returned unchanged, INCLUDING one that cannot
// run here. Availability of an explicit mode is WrapCommand's business: it
// refuses with a diagnostic naming the sysctl, which is far more useful than
// silently degrading to a backend the user did not ask for.
func ResolveMode(cfg SandboxConfig) SandboxMode {
	if cfg.Mode != SandboxAuto && cfg.Mode != "" {
		return cfg.Mode
	}
	switch {
	case cfg.WorkspaceRoot == "":
		return SandboxNone
	case BwrapUsable():
		// Deliberately BwrapUsable() rather than lookPath("bwrap"): see its
		// comment. A host where bwrap is installed but forbidden from creating
		// user namespaces must fall through to Docker, not select a backend
		// that cannot run.
		return SandboxBubblewrap
	case DockerUsable(cfg):
		return SandboxDocker
	case cfg.LandlockFallback && cfg.AllowNetwork && LandlockUsable():
		// LAST OF THE THREE, so every host that gets bwrap or docker today
		// still does. And only with the network allowed: Landlock can refuse
		// TCP but not UDP, so it cannot keep a "no network" promise, and a
		// backend that cannot keep the caller's promise must not be chosen
		// for it.
		return SandboxLandlock
	default:
		return SandboxNone
	}
}

// PassedOver says why SandboxAuto did not choose each backend ranked above the
// one it did choose, in ResolveMode's order -- from the same probes ResolveMode
// just used, so the explanation cannot describe a different host from the one
// the command runs on. Nil for an explicit mode, and for bwrap, which is first.
func PassedOver(cfg SandboxConfig) []string {
	if (cfg.Mode != SandboxAuto && cfg.Mode != "") || cfg.WorkspaceRoot == "" {
		return nil
	}
	mode := ResolveMode(cfg)
	if mode == SandboxBubblewrap {
		return nil
	}
	var reasons []string
	if _, err := lookPath("bwrap"); err != nil {
		reasons = append(reasons, "bwrap is not installed")
	} else {
		reasons = append(reasons, "bwrap is installed but cannot start a sandbox here")
	}
	if mode == SandboxDocker {
		return reasons
	}
	if _, err := lookPath("docker"); err != nil {
		reasons = append(reasons, "Docker is not installed")
	} else {
		reasons = append(reasons, "Docker has no image configured for this")
	}
	if mode == SandboxLandlock || !cfg.LandlockFallback {
		return reasons
	}
	if !cfg.AllowNetwork {
		return append(reasons, "Landlock cannot keep this command off the network")
	}
	return append(reasons, "Landlock is unavailable")
}

// Confines reports whether cfg would actually put a command inside something.
//
// This is what an approval prompt must be built from. SandboxNone is not a
// sandbox: it is host execution with the user's full privileges, and a UI that
// calls it confined is obtaining consent for something other than what happens.
func Confines(cfg SandboxConfig) bool { return ResolveMode(cfg) != SandboxNone }

// WrapCommand inspects cfg and wraps the specified executable and arguments
// into a sandboxed command specification if a supported sandbox mode is active
// and available on the host system.
func WrapCommand(command string, args []string, cfg SandboxConfig) (string, []string, error) {
	mode := ResolveMode(cfg)

	switch mode {
	case SandboxNone:
		// UNCONFINED, BUT NOT NECESSARILY UNBOUNDED. Nothing here creates a
		// namespace, so the approval prompt still says "your full privileges"
		// -- but a scope still stops a runaway from taking the host down with
		// it, and this is the mode where that matters most.
		return withLimiter(command, args, cfg)

	case SandboxBubblewrap:
		if _, err := lookPath("bwrap"); err != nil {
			return "", nil, fmt.Errorf("bubblewrap sandbox requested (bwrap), but 'bwrap' executable is not installed on PATH: %w", err)
		}
		// Explicitly requested, and installed, but unable to run. Say why and
		// what to do, rather than letting the caller discover it as
		// "bwrap: setting up uid map: Permission denied".
		if !BwrapUsable() {
			return "", nil, fmt.Errorf("bubblewrap sandbox requested and 'bwrap' is installed, but this host " +
				"forbids it from creating a user namespace (typically Ubuntu 24.04+ with " +
				"kernel.apparmor_restrict_unprivileged_userns=1). Allow it with " +
				"`sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0`, install Docker so the " +
				"docker backend can be used instead, or set the sandbox mode explicitly")
		}
		if cfg.WorkspaceRoot == "" {
			return "", nil, fmt.Errorf("bubblewrap sandbox requires a non-empty WorkspaceRoot")
		}

		cleanWs := filepath.Clean(cfg.WorkspaceRoot)
		bwrapArgs := []string{
			// Hardened Security & Isolation Flags
			"--new-session",     // Prevent TIOCSTI terminal ioctl injection attacks
			"--die-with-parent", // Terminate immediately if host daemon dies
			"--cap-drop", "ALL", // Drop all Linux POSIX capabilities
			// NOTE: there is deliberately no --nosuid here. It was present and
			// bwrap REJECTED THE WHOLE INVOCATION with "Unknown option
			// --nosuid" -- it is a mount(2) option, not a bwrap flag -- so
			// every sandboxed command failed before running. Nothing is lost by
			// its absence: bubblewrap mounts its binds MS_NOSUID inherently,
			// which is the property the flag was reaching for.
			"--unshare-pid", // PID namespace isolation (cannot see host processes)
			"--unshare-uts", // Hostname/domain isolation
			"--unshare-ipc", // Shared memory / SysV IPC isolation
			"--proc", "/proc",
			"--dev", "/dev",
			"--tmpfs", "/tmp",
		}

		// Read-only system binary and SSL certificate mounts
		systemMounts := sandboxSystemPaths
		for _, m := range systemMounts {
			if _, err := os.Stat(m); err == nil {
				bwrapArgs = append(bwrapArgs, "--ro-bind", m, m)
			}
		}

		// The toolchain itself, read-only, when it lives outside the system
		// directories above. Added BEFORE the workspace bind: bwrap applies
		// binds in order, so a root that happens to contain the workspace must
		// not be mounted over it afterwards.
		if root := toolchainRoot(command); root != "" && !coveredBy(root, systemMounts) && !coveredBy(cleanWs, []string{root}) {
			bwrapArgs = append(bwrapArgs, "--ro-bind", root, root)
		}

		// Workspace bind mount confinement
		if cfg.ReadOnlyWorkspace {
			bwrapArgs = append(bwrapArgs, "--ro-bind", cleanWs, cleanWs)
		} else {
			bwrapArgs = append(bwrapArgs, "--bind", cleanWs, cleanWs)
		}

		// A REAL HOME, bound at the same path inside as out. Without it HOME
		// resolves to a directory bwrap auto-created on its internal tmpfs:
		// writable, in RAM, and gone at exit. See SandboxConfig.HomeDir.
		if cfg.HomeDir != "" {
			cleanHome := filepath.Clean(cfg.HomeDir)
			bwrapArgs = append(bwrapArgs, "--bind", cleanHome, cleanHome)
		}

		// Network namespace isolation
		if !cfg.AllowNetwork {
			bwrapArgs = append(bwrapArgs, "--unshare-net")
		}

		bwrapArgs = append(bwrapArgs, "--chdir", cleanWs)
		bwrapArgs = append(bwrapArgs, "--", command)
		bwrapArgs = append(bwrapArgs, args...)

		return withLimiter("bwrap", bwrapArgs, cfg)

	case SandboxLandlock:
		if !LandlockUsable() {
			return "", nil, fmt.Errorf("landlock sandbox requested, but this host cannot enforce it " +
				"(needs Linux with Landlock enabled, on amd64 or arm64)")
		}
		if cfg.WorkspaceRoot == "" {
			return "", nil, fmt.Errorf("landlock sandbox requires a non-empty WorkspaceRoot")
		}
		// The limiter wraps the OUTSIDE, exactly as for bwrap: the scope holds
		// the helper and everything the command it becomes goes on to start.
		helperArgs := append(landlockPolicyFor(command, cfg).args(), "--", command)
		return withLimiter(selfExecutable(), append([]string{SandboxHelperArg}, append(helperArgs, args...)...), cfg)

	case SandboxDocker:
		if _, err := lookPath("docker"); err != nil {
			return "", nil, fmt.Errorf("docker sandbox requested, but 'docker' executable is not installed on PATH: %w", err)
		}
		if cfg.WorkspaceRoot == "" {
			return "", nil, fmt.Errorf("docker sandbox requires a non-empty WorkspaceRoot")
		}
		if strings.TrimSpace(cfg.Image) == "" {
			return "", nil, fmt.Errorf("docker sandbox requires an Image: without one the command name is " +
				"passed where docker expects an image, and `docker run ... make` asks for an image called \"make\"")
		}

		cleanWs := filepath.Clean(cfg.WorkspaceRoot)
		uid, gid := getUID(), getGID()
		dockerArgs := []string{
			"run", "--rm", "-i",
			"--security-opt=no-new-privileges:true", // Block privilege escalation
			"--cap-drop=ALL",                        // Drop capabilities
			"--pids-limit=200",                      // Fork-bomb protection
			"--tmpfs", "/tmp:rw,noexec,nosuid,size=64m",
			"-v", fmt.Sprintf("%s:%s", cleanWs, cleanWs),
			"-w", cleanWs,
		}

		if uid >= 0 && gid >= 0 {
			dockerArgs = append(dockerArgs, "--user", fmt.Sprintf("%d:%d", uid, gid))
		}

		// Resource exhaustion / DoS protections
		memLimit := cfg.MemoryLimitMB
		if memLimit <= 0 {
			memLimit = 1024 // Default 1GB RAM cap
		}
		dockerArgs = append(dockerArgs, fmt.Sprintf("--memory=%dm", memLimit))

		if cfg.CPULimit > 0 {
			dockerArgs = append(dockerArgs, fmt.Sprintf("--cpus=%.2f", cfg.CPULimit))
		}

		if cfg.HomeDir != "" {
			cleanHome := filepath.Clean(cfg.HomeDir)
			dockerArgs = append(dockerArgs, "-v", fmt.Sprintf("%s:%s", cleanHome, cleanHome),
				"-e", "HOME="+cleanHome)
		}

		if !cfg.AllowNetwork {
			dockerArgs = append(dockerArgs, "--network", "none")
		}

		if cfg.PidsLimit > 0 {
			dockerArgs = append(dockerArgs, fmt.Sprintf("--pids-limit=%d", cfg.PidsLimit))
		}

		// The image goes BEFORE the command. This line is the bug fix: without
		// it docker read the command as the image name.
		dockerArgs = append(dockerArgs, cfg.Image, command)
		dockerArgs = append(dockerArgs, args...)

		return "docker", dockerArgs, nil

	default:
		return "", nil, fmt.Errorf("unknown sandbox mode %q", cfg.Mode)
	}
}

// toolchainRoot returns the directory that must be readable inside the
// namespace for `command` to run at all, or "" if it cannot be determined.
//
// FOUND BY RUNNING THE TOOL, not by reading it. sandbox_exec accepts exactly
// go, npm, make and cargo, and the only one of those that reliably lives in a
// system directory is make. The others are normally installed per-user:
//
//	go     golang.org tarball -> ~/.local/go or /usr/local/go
//	npm    nvm                -> ~/.nvm/versions/node/<v>
//	cargo  rustup             -> ~/.cargo
//
// None of those paths is bound, and HOME is not bound either, so bwrap answered
// every one of them with "execvp go: No such file or directory" -- the sandbox
// could run precisely the one command of the four that needed it least. The end
// to end test did not catch it because it used `make`.
//
// The rule is the toolchain layout every one of these follows: the binary sits
// in <root>/bin, and <root> is what has to be readable (GOROOT's lib and pkg,
// nvm's lib/node_modules). A binary NOT in a bin directory gets its own
// directory instead. Symlinks are resolved first, because ~/.local/bin/go
// pointing into ~/.local/go would otherwise bind the wrong tree.
func toolchainRoot(command string) string {
	resolved, err := lookPath(command)
	if err != nil {
		return ""
	}
	if abs, err := filepath.Abs(resolved); err == nil {
		resolved = abs
	}
	if real, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = real
	}
	dir := filepath.Dir(resolved)
	if filepath.Base(dir) == "bin" {
		dir = filepath.Dir(dir)
	}
	return dir
}

// coveredBy reports whether path is already inside one of the given mounts, so
// a toolchain in /usr/bin does not get a second, redundant bind.
func coveredBy(path string, mounts []string) bool {
	for _, m := range mounts {
		if path == m || strings.HasPrefix(path, m+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// withLimiter puts an already-built command inside a bounded cgroup, or returns
// it untouched when it asked for no bounds or the host cannot provide them.
//
// DEGRADES RATHER THAN FAILS, deliberately and in one direction only. A host
// with no user systemd is not a host where a build command should stop working;
// it is a host where the command is less contained, and the honest response is
// to run it and SAY SO. LimitsApply is what the approval prompt reads, and it
// is this function's own condition rather than a second opinion about it --
// the same one-computation rule ResolveMode exists for.
func withLimiter(command string, args []string, cfg SandboxConfig) (string, []string, error) {
	if !wantsLimits(cfg) || !LimiterUsable() {
		return command, args, nil
	}
	full := append(limiterPrefix(cfg), command)
	return "systemd-run", append(full, args...), nil
}
