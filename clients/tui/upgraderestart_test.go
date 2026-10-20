package tui

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

func TestClassifyServing(t *testing.T) {
	self := filepath.Join("opt", "npm", "mochiii", "bin", "mochiii")
	for _, tc := range []struct {
		name                   string
		serving, program, mine string
		goos                   string
		want                   servingKind
	}{
		{"same version", "1.2.0", self, "1.2.0", "linux", servingUsable},
		{"this file, upgraded", "1.2.0", self, "1.2.1", "linux", servingUpgraded},
		{"this file, downgraded", "1.2.1", self, "1.2.0", "linux", servingUpgraded},
		{"another file", "1.2.0", filepath.Join("home", "u", ".mochiii", "bin", "mochiii"), "1.2.1", "linux", servingForeign},
		{"no program: before 1.2.0", "1.1.0", "", "1.2.0", "linux", servingForeign},
		{"daemon says no version", "", self, "1.2.0", "linux", servingUsable},
		{"a dev build compares nothing", "1.2.0", self, "dev", "linux", servingUsable},
		{"case differs on Windows", "1.2.0", strings.ToUpper(self), "1.2.1", "windows", servingUpgraded},
		{"case differs elsewhere", "1.2.0", strings.ToUpper(self), "1.2.1", "darwin", servingForeign},
	} {
		if got := classifyServing(tc.serving, tc.program, tc.mine, self, tc.goos); got != tc.want {
			t.Errorf("%s: classifyServing = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// fakeVersionedDaemon answers handshakes as a daemon of version, with a
// lockfile naming program, the way daemon/main.go writes one.
func fakeVersionedDaemon(t *testing.T, version, program string) {
	t.Helper()
	addr := testAddress(t)
	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatalf("listening on fake daemon socket: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	lock := protocol.NewLockFile(addr, os.Getpid())
	lock.Executable = program
	data, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(t.TempDir(), "daemon.lock")
	if err := os.WriteFile(lockPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				var hs protocol.HandshakeRequest
				if json.NewDecoder(conn).Decode(&hs) != nil {
					return
				}
				_ = json.NewEncoder(conn).Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true, DaemonVersion: version})
			}(conn)
		}
	}()
	t.Cleanup(setLockPathForTest(t, lockPath))
}

// asOneBinary sets the client up as the one binary at version for one test.
func asOneBinary(t *testing.T, version string) {
	t.Helper()
	oldOne, oldVersion := InOneBinary, Version
	InOneBinary, Version = true, version
	t.Cleanup(func() { InOneBinary, Version = oldOne, oldVersion })
}

// THE NPM UPGRADE: the file was replaced under its own running daemon.
func TestAnUpgradedDaemonOfThisInstallIsRestarted(t *testing.T) {
	self := filepath.Join(t.TempDir(), "mochiii")
	fakeVersionedDaemon(t, "1.2.0", self)
	asOneBinary(t, "1.2.1")
	var out bytes.Buffer
	stops := 0
	if useRunningDaemon(self, &out, func() error { stops++; return nil }) {
		t.Fatal("useRunningDaemon kept a 1.2.0 daemon of this very file; a 1.2.1 client must replace it")
	}
	if stops != 1 {
		t.Fatalf("stop was called %d times, want 1", stops)
	}
	if !strings.Contains(out.String(), "upgraded from 1.2.0 to 1.2.1") {
		t.Errorf("the client did not say why it restarted the service: %q", out.String())
	}
}

func TestAnotherInstallsDaemonIsLeftRunning(t *testing.T) {
	self := filepath.Join(t.TempDir(), "mochiii")
	fakeVersionedDaemon(t, "1.1.0", filepath.Join(t.TempDir(), "mochiii-daemon"))
	asOneBinary(t, "1.2.0")
	var out bytes.Buffer
	if !useRunningDaemon(self, &out, func() error { t.Fatal("stopped another install's daemon"); return nil }) {
		t.Fatal("useRunningDaemon refused another install's daemon")
	}
	if !strings.Contains(out.String(), "Mochiii 1.1.0, from another install") {
		t.Errorf("the client did not name the version answering: %q", out.String())
	}
}

func TestTheSameVersionIsUsedQuietly(t *testing.T) {
	self := filepath.Join(t.TempDir(), "mochiii")
	fakeVersionedDaemon(t, "1.2.0", self)
	asOneBinary(t, "1.2.0")
	var out bytes.Buffer
	if !useRunningDaemon(self, &out, func() error { t.Fatal("stopped a current daemon"); return nil }) {
		t.Fatal("useRunningDaemon refused a daemon of this version")
	}
	if out.Len() != 0 {
		t.Errorf("said something about a current daemon: %q", out.String())
	}
}

// A client built alone never claims a daemon, whatever its version.
func TestAClientBuiltAloneRestartsNothing(t *testing.T) {
	self := filepath.Join(t.TempDir(), "mochiii")
	fakeVersionedDaemon(t, "1.2.0", self)
	oldOne, oldVersion := InOneBinary, Version
	InOneBinary, Version = false, "1.2.1"
	t.Cleanup(func() { InOneBinary, Version = oldOne, oldVersion })
	if !useRunningDaemon(self, &bytes.Buffer{}, func() error { t.Fatal("a client built alone stopped a daemon"); return nil }) {
		t.Fatal("useRunningDaemon refused a running daemon")
	}
}

func TestNoDaemonMeansStartOne(t *testing.T) {
	t.Cleanup(setLockPathForTest(t, filepath.Join(t.TempDir(), "absent.lock")))
	asOneBinary(t, "1.2.0")
	if useRunningDaemon("mochiii", &bytes.Buffer{}, func() error { t.Fatal("stopped a daemon that is not there"); return nil }) {
		t.Fatal("useRunningDaemon said to use a daemon when none answers")
	}
}
