//go:build windows

package protocol

// The environment variable runtimeDir consults on this platform. paths_test.go
// asserts the behaviour; this supplies the only thing that differs.
//
// LOCALAPPDATA, not APPDATA: runtimedir_windows.go deliberately uses the LOCAL
// one, because Roaming AppData syncs across machines in a domain and a lockfile
// naming a pipe on a DIFFERENT machine is worse than no lockfile at all. Naming
// the constant here means paths_test.go now pins that choice on every Windows
// run, where before it asserted XDG semantics and failed for reasons of its own.
//
// The owner-only counterpart to paths_unix_test.go's mode assertion is
// ownership rather than permission bits, and lives in socketdir_windows_test.go.
const runtimeDirEnvVar = "LOCALAPPDATA"
