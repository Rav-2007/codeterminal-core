package helper

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"mochiii/helper/helperproto"
	"mochiii/protocol"
)

// THE OLD BINDING AND THE NEW ONE, ON ONE CPU, BIT FOR BIT.
//
// golden_vectors.bin pins the new binding to the old one on the machine that
// wrote it. Anywhere else it cannot: ONNX Runtime picks its int8 kernels by
// CPU, and the CI runner's vectors differ from that machine's for reasons that
// have nothing to do with the binding (found 2026-10-19: the golden test failed
// on the runner while every other real-model test passed). So where a helper
// built from the old binding exists -- the retrieval-eval job builds one from
// its base tree -- this compares it with this package's binding on the same
// CPU, text by text, and requires every bit to match.
//
// MOCHIII_OTHER_HELPER names that helper binary. Unset, the test skips.
func TestAnotherHelperBuildComputesTheSameBits(t *testing.T) {
	other := os.Getenv("MOCHIII_OTHER_HELPER")
	if other == "" {
		t.Skip("MOCHIII_OTHER_HELPER is not set")
	}
	e := fullEmbedder(t) // this binding, in process; skips without the model
	home, _ := os.UserHomeDir()
	lib := filepath.Join(home, ".mochiii", "models", "onnxruntime-1.26.0", "libonnxruntime.so")

	dir, err := os.MkdirTemp("", "oh") // short: a socket path has ~100 bytes
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "h.sock")
	cmd := exec.Command(other, "--socket", sock, "--model-dir", modelDirForLeakTest(t), "--onnxruntime-lib", lib)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", other, err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	addr := protocol.Address{Address: sock}
	embed := func(text string) []float32 {
		t.Helper()
		var conn interface {
			Read([]byte) (int, error)
			Write([]byte) (int, error)
			Close() error
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			c, err := protocol.DialTimeout(addr, 2*time.Second)
			if err == nil {
				conn = c
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the other helper never listened on %s: %v", sock, err)
			}
			time.Sleep(100 * time.Millisecond)
		}
		defer conn.Close()
		if err := json.NewEncoder(conn).Encode(helperproto.Request{Method: helperproto.MethodEmbed, Texts: []string{text}}); err != nil {
			t.Fatalf("sending to the other helper: %v", err)
		}
		var resp helperproto.Response
		if err := json.NewDecoder(conn).Decode(&resp); err != nil {
			t.Fatalf("reading the other helper's answer: %v", err)
		}
		if !resp.OK || len(resp.Vectors) != 1 {
			t.Fatalf("the other helper answered OK=%v with %d vectors: %s", resp.OK, len(resp.Vectors), resp.Error)
		}
		return resp.Vectors[0]
	}

	mine, err := e.Embed(goldenTexts)
	if err != nil {
		t.Fatalf("embedding with this binding: %v", err)
	}
	differ, worst := 0, 1.0
	for i, text := range goldenTexts {
		theirs := embed(text)
		if len(theirs) != len(mine[i]) {
			t.Fatalf("text %d: %d components from the other helper, %d here", i, len(theirs), len(mine[i]))
		}
		same := true
		for j := range theirs {
			if math.Float32bits(theirs[j]) != math.Float32bits(mine[i][j]) {
				same = false
				break
			}
		}
		if !same {
			differ++
			if c := cosine(theirs, mine[i]); c < worst {
				worst = c
			}
		}
	}
	t.Logf("%d texts compared with %s: %d differ in any bit (worst cosine %.7f)", len(goldenTexts), other, differ, worst)
	if differ > 0 {
		t.Errorf("%d of %d texts embed differently from the other helper build on this CPU", differ, len(goldenTexts))
	}
}
