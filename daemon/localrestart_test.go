package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// A MODEL ON THIS COMPUTER IS STILL THE ONE ASKED AFTER A RESTART.
//
// 1.3.0 connected a local server and answered, and the first daemon after a
// restart asked that server for models.json's default instead (localconnect.go
// says why). The credential on disk was right all along, so a test of what was
// saved passed (TestAKeylessLocalServerIsInForceAfterARestart); this one starts
// the real binary on that credential, the way the terminal app does, and reads
// the model off the request the server receives.
//
// Neuter check: drop `|| connected.keylessLocal()` from main.go's tier
// condition and both cases ask for models.json's default.
func TestAModelOnThisComputerIsStillTheOneAskedAfterARestart(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real binary")
	}
	bin := filepath.Join(t.TempDir(), exeName("mochiii"))
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building mochiii: %v\n%s", err, out)
	}
	server := startLocalStandIn(t)
	listed := []string{"coder-small:3b", "coder-big:7b"}

	for _, tc := range []struct {
		name, chosen, want string
	}{
		{"the server's first model, as connected", "", "coder-small:3b"},
		{"the model chosen with /model", "coder-big:7b", "coder-big:7b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			// What connectLocal saves, plus what /model adds.
			if err := saveCredential(filepath.Join(home, ".mochiii", "credentials.json"), storedCredential{
				APIBase: server.base, Verified: true, DefaultModel: listed[0], Models: listed, ChosenModel: tc.chosen,
			}); err != nil {
				t.Fatal(err)
			}
			project, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(project, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// Built from nothing, so no key or address in the developer's shell
			// reaches the daemon: the stored credential is all it has.
			env := []string{"HOME=" + home, "USERPROFILE=" + home, "LOCALAPPDATA=" + filepath.Join(home, "local"),
				"XDG_RUNTIME_DIR=" + shortTempDir(t), "XDG_STATE_HOME=" + filepath.Join(home, "state"),
				"PATH=" + os.Getenv("PATH"), "NO_COLOR=1", "MOCHIII_NO_INDEX=1", "SystemRoot=" + os.Getenv("SystemRoot")}
			run := func(ctx context.Context, args ...string) *exec.Cmd {
				cmd := exec.CommandContext(ctx, bin, append([]string{"--workspace", project}, args...)...)
				cmd.Env = env
				cmd.WaitDelay = 5 * time.Second
				return cmd
			}
			t.Cleanup(func() { _ = run(context.Background(), "--stop").Run() })

			before := server.count()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			cmd := run(ctx, "--prompt", "which model answers here?")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			asked := server.modelsAfter(before)
			if err != nil || len(asked) == 0 || asked[0] != tc.want || !strings.Contains(stdout.String(), localStandInAnswer) {
				t.Fatalf("after a restart the local server was asked for %q, want %q (err %v)\nstdout: %s\nstderr: %s\n%s",
					asked, tc.want, err, stdout.String(), stderr.String(), daemonLogsUnder(home))
			}
		})
	}
}

const localStandInAnswer = "answered by the model on this computer"

// localStandIn is a model server on this machine that serves two models and
// remembers which one each question asked for.
type localStandIn struct {
	base   string
	mu     sync.Mutex
	models []string
}

func startLocalStandIn(t *testing.T) *localStandIn {
	t.Helper()
	s := &localStandIn{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"coder-small:3b"},{"id":"coder-big:7b"}]}`)
		case "/v1/chat/completions":
			var req struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			s.mu.Lock()
			s.models = append(s.models, req.Model)
			s.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			for _, line := range textSSE(localStandInAnswer) {
				_, _ = fmt.Fprintf(w, "%s\n\n", line)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	s.base = srv.URL + "/v1"
	return s
}

func (s *localStandIn) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.models)
}

// modelsAfter is the model named by each question since the first n.
func (s *localStandIn) modelsAfter(n int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.models[n:]...)
}

// daemonLogsUnder returns every daemon log under home, for a failure message.
func daemonLogsUnder(home string) string {
	var out strings.Builder
	_ = filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), "daemon") && strings.HasSuffix(d.Name(), ".log") {
			data, _ := os.ReadFile(path)
			fmt.Fprintf(&out, "--- %s ---\n%s\n", path, data)
		}
		return nil
	})
	if out.Len() == 0 {
		return "(no daemon log under " + home + ", " + runtime.GOOS + ")"
	}
	return out.String()
}
