package helperproto

import (
	"codeterminal/protocol"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This package had 0 tests. It is the wire contract between the daemon and the
// embedder subprocess -- the boundary that exists specifically so CGO never
// touches the daemon binary -- so a drift here breaks indexing with no compile
// error on either side.
//
// Everything below is model-independent. The ONNX-dependent paths stay behind
// -tags eval, where a real model is available.

func TestRequestResponseRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		req  Request
	}{
		{"health has no texts", Request{Method: MethodHealth}},
		{"embed one text", Request{Method: MethodEmbed, Texts: []string{"func main() {}"}}},
		{"embed a batch", Request{Method: MethodEmbed, Texts: []string{"a", "b", "c"}}},
		// The daemon batches, so an empty batch is reachable if a caller filters
		// everything out; it must survive the wire rather than becoming garbage.
		{"embed an empty batch", Request{Method: MethodEmbed, Texts: []string{}}},
		// Chunk text is arbitrary source code: quotes, newlines, tabs, and
		// non-ASCII all pass through this socket unescaped by anything else.
		{"text with quotes and newlines", Request{Method: MethodEmbed, Texts: []string{"s := \"a\\nb\"\n\tif x {}\n"}}},
		{"text with non-ASCII", Request{Method: MethodEmbed, Texts: []string{"// café ☕ 日本語"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.req)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got Request
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatalf("unmarshal %s: %v", encoded, err)
			}
			if got.Method != tc.req.Method {
				t.Errorf("Method = %q, want %q", got.Method, tc.req.Method)
			}
			if len(got.Texts) != len(tc.req.Texts) {
				t.Fatalf("Texts length = %d, want %d (wire: %s)", len(got.Texts), len(tc.req.Texts), encoded)
			}
			for i := range tc.req.Texts {
				if got.Texts[i] != tc.req.Texts[i] {
					t.Errorf("Texts[%d] = %q, want %q -- chunk text must survive the socket byte-exact, "+
						"or the vector is computed for something other than the indexed code",
						i, got.Texts[i], tc.req.Texts[i])
				}
			}
		})
	}
}

func TestResponseRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		resp Response
	}{
		{"health ok", Response{OK: true}},
		{"vectors", Response{OK: true, Vectors: [][]float32{{0.1, -0.2, 0}, {1, 2, 3}}}},
		{"error", Response{OK: false, Error: "model not loaded"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got Response
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatalf("unmarshal %s: %v", encoded, err)
			}
			if got.OK != tc.resp.OK || got.Error != tc.resp.Error {
				t.Errorf("OK/Error = %v/%q, want %v/%q", got.OK, got.Error, tc.resp.OK, tc.resp.Error)
			}
			if len(got.Vectors) != len(tc.resp.Vectors) {
				t.Fatalf("Vectors length = %d, want %d", len(got.Vectors), len(tc.resp.Vectors))
			}
			for i := range tc.resp.Vectors {
				if len(got.Vectors[i]) != len(tc.resp.Vectors[i]) {
					t.Fatalf("Vectors[%d] dimension = %d, want %d -- a dimension change silently "+
						"corrupts every similarity comparison in the index",
						i, len(got.Vectors[i]), len(tc.resp.Vectors[i]))
				}
				for j := range tc.resp.Vectors[i] {
					if got.Vectors[i][j] != tc.resp.Vectors[i][j] {
						t.Errorf("Vectors[%d][%d] = %v, want %v", i, j, got.Vectors[i][j], tc.resp.Vectors[i][j])
					}
				}
			}
		})
	}
}

// OK must be false whenever Error is set: the daemon branches on OK, and an
// "ok:true with an error" reply would be treated as a successful embedding of
// nothing.
func TestOKAndErrorAreConsistent(t *testing.T) {
	var resp Response
	if err := json.Unmarshal([]byte(`{"ok":false,"error":"boom"}`), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.OK {
		t.Error("a response carrying an error decoded with OK=true")
	}
	if resp.Error == "" {
		t.Error("the error string was dropped")
	}
}

// Response.Vectors is omitempty, so a health reply carries no vectors key at all
// rather than a null. Pinned because the daemon distinguishes "no vectors" from
// "zero-length vectors" when deciding whether an embed succeeded.
func TestHealthResponseOmitsVectors(t *testing.T) {
	encoded, err := json.Marshal(Response{OK: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "vectors") {
		t.Errorf("health response = %s, want no vectors key", encoded)
	}
}

func TestMethodConstants(t *testing.T) {
	// Values, not names: the daemon sends these strings.
	if MethodHealth != "health" {
		t.Errorf("MethodHealth = %q, want \"health\"", MethodHealth)
	}
	if MethodEmbed != "embed" {
		t.Errorf("MethodEmbed = %q, want \"embed\"", MethodEmbed)
	}
	if MethodHealth == MethodEmbed {
		t.Error("the two methods must be distinguishable")
	}
}

// The endpoint is scoped by the daemon's PID precisely so an endpoint left
// behind by a previous or unrelated daemon can never be dialled by this one.
//
// SPLIT INTO A PORTABLE HALF AND A UNIX HALF when SocketPath became Address.
// The old test asserted a filesystem path and a 0700 directory mode, which are
// not properties of the endpoint -- they are properties of the endpoint ON
// UNIX. Asserting them unconditionally is what made the signature Unix-only in
// the first place, so the portable properties (PID scoping, the PID appearing
// in the name, a usable transport) are now checked everywhere and the
// filesystem ones only where there is a filesystem object to check.
func TestHelperAddressIsScopedByDaemonPID(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmp)

	first, err := Address(4242)
	if err != nil {
		t.Fatalf("Address: %v", err)
	}
	second, err := Address(4243)
	if err != nil {
		t.Fatalf("Address: %v", err)
	}

	if first.Address == second.Address {
		t.Error("two different daemon PIDs produced the same endpoint -- a stale endpoint from " +
			"a dead daemon could be dialled by a new one")
	}
	if !strings.Contains(first.Address, fmt.Sprintf("%d", 4242)) {
		t.Errorf("Address(4242) = %q, want the PID in the name", first.Address)
	}
	// An empty transport is legal and means "this platform's default", but the
	// whole point of this function is that it commits to one, so that the
	// daemon and the helper cannot each resolve the default differently.
	if first.Transport == "" {
		t.Error("Address returned an empty transport; both sides derive from this " +
			"and an unstated transport is how they drift apart")
	}

	if first.Transport != protocol.TransportUnix {
		// Windows: a named pipe. There is no path to join, no directory to
		// stat, and nothing left behind when the owner dies.
		//
		// THIS LITERAL WAS WRONG BY ONE BACKSLASH until 2026-09-20, and the
		// first run of `cross (windows-latest, helper)` -- run 35488437797 on
		// Rav-2007/codeterminal-core, commit be7b982 -- is what found it. It
		// read `\.\pipe\` where the namespace is `\\.\pipe\`, so the
		// assertion could not pass on the only platform that reaches it:
		//
		//   messages_test.go:190: Address(4242) = "\\\\.\\pipe\\codeterminal-...-embedder-helper-4242",
		//                         want a named pipe name
		//
		// The ADDRESS was correct. The expectation was not. A test written for
		// a platform and never executed on it asserts whatever its author
		// believed, and this one was excluded from the Windows matrix as cgo
		// until the commit that added it there.
		//
		// It is a second spelling of protocol's own unexported pipePrefix
		// (protocol/transport_windows.go:38), which is the drift shape this
		// repository keeps finding. Left as a literal rather than exported,
		// because widening protocol's API for one assertion trades a smaller
		// problem for a permanent one -- but recorded here so the next reader
		// knows there are two copies and which one is authoritative.
		if !strings.HasPrefix(first.Address, `\\.\pipe\`) {
			t.Errorf("Address(4242) = %q, want a named pipe name", first.Address)
		}
		return
	}

	if want := filepath.Join(tmp, "codeterminal", "embedder-helper-4242.sock"); first.Address != want {
		t.Errorf("Address(4242) = %q, want %q", first.Address, want)
	}

	// It shares the daemon's own runtime directory, and that directory is
	// created owner-only (protocol.SocketDir) -- the helper socket is an
	// unauthenticated local endpoint, so the directory mode is what confines it.
	info, err := os.Stat(filepath.Dir(first.Address))
	if err != nil {
		t.Fatalf("stat socket dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("helper socket directory mode = %#o, want 0700", perm)
	}
}
