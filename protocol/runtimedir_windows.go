//go:build windows

package protocol

import "os"

// %LOCALAPPDATA% if set, otherwise the OS temp dir.
//
// LOCAL, not Roaming. os.UserConfigDir() would give Roaming AppData, which
// syncs across machines in a domain environment — and a lockfile naming a pipe
// on a DIFFERENT machine is worse than no lockfile at all.
//
// Derived from the environment variable rather than os.UserCacheDir() so that
// the TypeScript client can derive the identical path: the two happen to agree
// today, but "happen to agree" is exactly the drift protocol.go's header warns
// about. daemonClient.ts reads process.env.LOCALAPPDATA with the same fallback.
func runtimeDir() string {
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return d
	}
	return os.TempDir()
}
