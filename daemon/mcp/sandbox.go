package mcp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
)

// SandboxConfig specifies the runtime confinement rules for launching an MCP server.
type SandboxConfig struct {
	Mode              SandboxMode
	WorkspaceRoot     string
	ReadOnlyWorkspace bool
	AllowNetwork      bool
	MemoryLimitMB     int
	CPULimit          float64
}

var lookPath = exec.LookPath
var getUID = os.Getuid
var getGID = os.Getgid

// WrapCommand inspects cfg and wraps the specified executable and arguments
// into a sandboxed command specification if a supported sandbox mode is active
// and available on the host system.
func WrapCommand(command string, args []string, cfg SandboxConfig) (string, []string, error) {
	mode := cfg.Mode
	if mode == SandboxAuto || mode == "" {
		if cfg.WorkspaceRoot == "" {
			mode = SandboxNone
		} else if _, err := lookPath("bwrap"); err == nil {
			mode = SandboxBubblewrap
		} else if _, err := lookPath("docker"); err == nil {
			mode = SandboxDocker
		} else {
			mode = SandboxNone
		}
	}

	switch mode {
	case SandboxNone:
		return command, args, nil

	case SandboxBubblewrap:
		if _, err := lookPath("bwrap"); err != nil {
			return "", nil, fmt.Errorf("bubblewrap sandbox requested (bwrap), but 'bwrap' executable is not installed on PATH: %w", err)
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
		systemMounts := []string{
			"/usr", "/lib", "/lib64", "/bin", "/sbin",
			"/etc/ssl", "/etc/ca-certificates", "/etc/pki", "/etc/resolv.conf",
		}
		for _, m := range systemMounts {
			if _, err := os.Stat(m); err == nil {
				bwrapArgs = append(bwrapArgs, "--ro-bind", m, m)
			}
		}

		// Workspace bind mount confinement
		if cfg.ReadOnlyWorkspace {
			bwrapArgs = append(bwrapArgs, "--ro-bind", cleanWs, cleanWs)
		} else {
			bwrapArgs = append(bwrapArgs, "--bind", cleanWs, cleanWs)
		}

		// Network namespace isolation
		if !cfg.AllowNetwork {
			bwrapArgs = append(bwrapArgs, "--unshare-net")
		}

		bwrapArgs = append(bwrapArgs, "--chdir", cleanWs)
		bwrapArgs = append(bwrapArgs, "--", command)
		bwrapArgs = append(bwrapArgs, args...)

		return "bwrap", bwrapArgs, nil

	case SandboxDocker:
		if _, err := lookPath("docker"); err != nil {
			return "", nil, fmt.Errorf("docker sandbox requested, but 'docker' executable is not installed on PATH: %w", err)
		}
		if cfg.WorkspaceRoot == "" {
			return "", nil, fmt.Errorf("docker sandbox requires a non-empty WorkspaceRoot")
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

		if !cfg.AllowNetwork {
			dockerArgs = append(dockerArgs, "--network", "none")
		}

		dockerArgs = append(dockerArgs, command)
		dockerArgs = append(dockerArgs, args...)

		return "docker", dockerArgs, nil

	default:
		return "", nil, fmt.Errorf("unknown sandbox mode %q", cfg.Mode)
	}
}
