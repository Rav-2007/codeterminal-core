//go:build latency

package main

// THE LATENCY HARNESS (1.2.0): how long the person at the keyboard waits, from
// typing `mochiii` to the question reaching the provider, measured on the real
// binary against a stand-in provider on this machine.
//
//	go test -tags latency -count=1 -v -run TestLatency .
//	MOCHIII_LATENCY_BIN=/path/to/mochiii go test -tags latency ...  (another build)
//
// THE STAND-IN CHARGES FOR A NEW CONNECTION. A provider's first answer waits on
// DNS, TCP and TLS -- 0.23-0.39 s measured from a laptop on 2026-10-19, and one
// cold DNS lookup took 5 s -- while a reused connection costs nothing. On
// loopback all of that is free, so the stand-in speaks TLS and holds every new
// connection's handshake for MOCHIII_LATENCY_CONNECT_DELAY (300ms by default).
// The charge sits INSIDE the handshake, where a real one is: a client still
// dialling can then be handed a connection that frees up meanwhile, which a
// charge after the handshake would hide (the first version of this harness did,
// and showed no gain at a cold start that a real provider gives). The binary
// trusts the stand-in's certificate through SSL_CERT_FILE, which Go reads on
// Linux; elsewhere the harness speaks plain HTTP and charges at the first read.
// The stand-in listens on this machine's network address, not 127.0.0.1: the
// daemon does not warm a provider on loopback, where there is nothing to save.
// What the harness shows is when Mochiii pays for a connection, not how fast
// this machine's loopback is.
//
// Rows, each the median of MOCHIII_LATENCY_RUNS runs (5):
//
//	version          `mochiii --version`: process start.
//	cold             no background service: start it, ask, answer.
//	warm, new conn   the service is up, its provider connection is gone, and the
//	                 question comes at once (`mochiii --prompt`).
//	warm, typed      the same, but the app opened first and the question came a
//	                 second later, as a person types it.
//	warm, open conn  straight after another question.
//	cold, indexed    a cold start in an indexed project, which starts the
//	                 code-search helper and loads its model: only when this
//	                 machine has the model (~/.mochiii/models), which the
//	                 throwaway home then links to, read-only.
//
// "to provider" is from starting `mochiii` to the stand-in receiving the first
// request; "to answer" is to the first byte of the answer on stdout. The table
// goes to the log and, in CI, to the job summary. Nothing here is a pass/fail
// threshold: the test fails only when a run does not answer.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mochiii/protocol"
)

const latencyAnswer = "LATENCY-ANSWER-OK"

// latencyProvider is the stand-in: an OpenAI-compatible chat endpoint that
// streams one short answer, records when each completion request arrived, and
// charges delay for every new connection.
type latencyProvider struct {
	url   string
	delay time.Duration
	srv   *http.Server

	mu       sync.Mutex
	arrivals []time.Time
	conns    map[net.Conn]struct{}
}

type chargedListener struct {
	net.Listener
	p *latencyProvider
}

func (l chargedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.p.mu.Lock()
	l.p.conns[c] = struct{}{}
	l.p.mu.Unlock()
	return &chargedConn{Conn: c, delay: l.p.delay}, nil
}

// chargedConn waits delay before its first read: the handshake a real
// provider's new connection costs.
type chargedConn struct {
	net.Conn
	delay time.Duration
	once  sync.Once
}

func (c *chargedConn) Read(b []byte) (int, error) {
	c.once.Do(func() { time.Sleep(c.delay) })
	return c.Conn.Read(b)
}

// startLatencyProvider starts the stand-in. With TLS (on Linux) it also
// returns the PEM file of its certificate, for SSL_CERT_FILE.
func startLatencyProvider(t *testing.T, delay time.Duration) (*latencyProvider, string) {
	t.Helper()
	p := &latencyProvider{delay: delay, conns: map[net.Conn]struct{}{}}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(p.serve))
	ip := remoteLookingIP(t)
	ln, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	_ = srv.Listener.Close()
	srv.Listener = chargedListener{Listener: ln, p: p}
	t.Cleanup(srv.Close)
	if runtime.GOOS != "linux" {
		srv.Start()
		p.url = srv.URL + "/v1"
		return p, ""
	}
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{certificateFor(t, ip)}}
	srv.StartTLS() // tls.NewListener over the charged one: the handshake's first read waits
	p.url = srv.URL + "/v1"
	certFile := filepath.Join(t.TempDir(), "stand-in.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(certFile, pemBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, certFile
}

// remoteLookingIP is an IPv4 address of this machine that is not loopback.
func remoteLookingIP(t *testing.T) net.IP {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			return n.IP.To4()
		}
	}
	t.Skip("this machine has no non-loopback IPv4 address, and the daemon warms no provider on loopback")
	return nil
}

// certificateFor is a self-signed certificate for ip, valid for an hour.
func certificateFor(t *testing.T, ip net.IP) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mochiii latency stand-in"},
		IPAddresses:           []net.IP{ip},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func (p *latencyProvider) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"message":"not found"}}`)
		return
	}
	p.mu.Lock()
	p.arrivals = append(p.arrivals, time.Now())
	p.mu.Unlock()
	var req struct {
		Stream bool `json:"stream"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if !req.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "lat", "object": "chat.completion", "model": "latency-model",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": latencyAnswer}, "finish_reason": "stop"}},
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range []map[string]any{
		{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": latencyAnswer}}}},
		{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}},
	} {
		c["id"], c["object"], c["model"] = "lat", "chat.completion.chunk", "latency-model"
		b, _ := json.Marshal(c)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

// arrivalsSince is how many completion requests had arrived, and the first
// one after n.
func (p *latencyProvider) firstArrivalAfter(n int) (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.arrivals) <= n {
		return time.Time{}, false
	}
	return p.arrivals[n], true
}

func (p *latencyProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.arrivals)
}

// dropConnections closes every connection Mochiii holds to the stand-in, as a
// provider's idle timeout eventually does.
func (p *latencyProvider) dropConnections() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.conns {
		_ = c.Close()
	}
	p.conns = map[net.Conn]struct{}{}
}

type latencyRun struct {
	toProvider, toAnswer, total time.Duration
}

// latencyRig is one built binary in a throwaway home, a project and the
// stand-in provider.
type latencyRig struct {
	t       *testing.T
	bin     string
	project string
	env     []string
	p       *latencyProvider
}

func (r *latencyRig) command(args ...string) *exec.Cmd {
	cmd := exec.Command(r.bin, args...)
	cmd.Env = r.env
	return cmd
}

// ask runs `mochiii --prompt` and times it.
func (r *latencyRig) ask(question string) latencyRun {
	r.t.Helper()
	before := r.p.count()
	cmd := r.command("--workspace", r.project, "--prompt", question)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	var out strings.Builder
	var firstByte time.Time
	buf := make([]byte, 4096)
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 && firstByte.IsZero() {
			firstByte = time.Now()
		}
		out.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		r.t.Fatalf("mochiii --prompt failed: %v\nstdout: %s\nstderr: %s", err, out.String(), stderr.String())
	}
	end := time.Now()
	if !strings.Contains(out.String(), latencyAnswer) {
		r.t.Fatalf("mochiii --prompt did not answer through the stand-in:\nstdout: %s\nstderr: %s", out.String(), stderr.String())
	}
	arrived, ok := r.p.firstArrivalAfter(before)
	if !ok {
		r.t.Fatal("the stand-in provider received no request")
	}
	return latencyRun{toProvider: arrived.Sub(start), toAnswer: firstByte.Sub(start), total: end.Sub(start)}
}

func (r *latencyRig) stop() {
	_ = r.command("--workspace", r.project, "--stop").Run()
}

// openApp does what the terminal app does when it opens: a handshake with the
// service, then nothing until the person has typed a question.
func (r *latencyRig) openApp() {
	r.t.Helper()
	data, err := os.ReadFile(protocol.LockPathFor(r.project))
	if err != nil {
		r.t.Fatalf("no lockfile for the running service: %v", err)
	}
	var lock protocol.LockFile
	if err := json.Unmarshal(data, &lock); err != nil {
		r.t.Fatal(err)
	}
	conn, err := protocol.Dial(protocol.AddressFromLock(lock))
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := json.NewEncoder(conn).Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "latency-harness"}); err != nil {
		r.t.Fatal(err)
	}
	var resp protocol.HandshakeResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil || !resp.Ok {
		r.t.Fatalf("handshake: %v %+v", err, resp)
	}
}

func median(d []time.Duration) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

func ms(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) + " ms" }

func TestLatency(t *testing.T) {
	runs := 5
	if v, err := strconv.Atoi(os.Getenv("MOCHIII_LATENCY_RUNS")); err == nil && v > 0 {
		runs = v
	}
	delay := 300 * time.Millisecond
	if v, err := time.ParseDuration(os.Getenv("MOCHIII_LATENCY_CONNECT_DELAY")); err == nil {
		delay = v
	}

	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	bin := os.Getenv("MOCHIII_LATENCY_BIN")
	label := bin
	if bin == "" {
		bin = filepath.Join(t.TempDir(), "mochiii"+exe)
		build := exec.Command("go", "build", "-o", bin, ".")
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("building mochiii: %v\n%s", err, out)
		}
		label = "this tree"
	}

	// A short runtime directory: a Unix socket path must fit in about 100 bytes.
	runDir, err := os.MkdirTemp("", "mlat")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	home, project := t.TempDir(), t.TempDir()
	if project, err = filepath.EvalSymlinks(project); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, certFile := startLatencyProvider(t, delay)
	t.Setenv("XDG_RUNTIME_DIR", runDir) // for openApp's lockfile path, the same as the service's
	env := []string{"HOME=" + home, "USERPROFILE=" + home, "LOCALAPPDATA=" + filepath.Join(home, "local"),
		"XDG_RUNTIME_DIR=" + runDir, "PATH=" + os.Getenv("PATH"), "NO_COLOR=1", "MOCHIII_NO_INDEX=1",
		"MOCHIII_API_BASE=" + p.url, "MOCHIII_API_KEY=latency-test-key", "SystemRoot=" + os.Getenv("SystemRoot")}
	if certFile != "" {
		env = append(env, "SSL_CERT_FILE="+certFile)
	}
	r := &latencyRig{t: t, bin: bin, project: project, env: env, p: p}
	t.Cleanup(r.stop)

	var indexed *latencyRig
	if userHome, err := os.UserHomeDir(); err == nil {
		models := filepath.Join(userHome, ".mochiii", "models")
		if info, err := os.Stat(models); err == nil && info.IsDir() {
			if err := os.MkdirAll(filepath.Join(home, ".mochiii"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(models, filepath.Join(home, ".mochiii", "models")); err != nil {
				t.Fatal(err)
			}
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 20; i++ {
				src := fmt.Sprintf("package main\n\n// quokka%d counts burrow number %d.\nfunc quokka%d() int { return %d }\n", i, i, i, i)
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("q%d.go", i)), []byte(src), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			indexed = &latencyRig{t: t, bin: bin, project: dir, env: env, p: p}
			if out, err := indexed.command("index", dir).CombinedOutput(); err != nil {
				t.Fatalf("indexing the second project: %v\n%s", err, out)
			}
			t.Cleanup(indexed.stop)
		}
	}

	var version []time.Duration
	for i := 0; i < runs; i++ {
		start := time.Now()
		if out, err := r.command("--version").CombinedOutput(); err != nil {
			t.Fatalf("mochiii --version: %v\n%s", err, out)
		}
		version = append(version, time.Since(start))
	}

	type row struct {
		name string
		runs []latencyRun
	}
	rows := []*row{{name: "cold"}, {name: "warm, new conn"}, {name: "warm, typed"}, {name: "warm, open conn"}}
	if indexed != nil {
		rows = append(rows, &row{name: "cold, indexed"})
	}
	for i := 0; i < runs; i++ {
		r.stop()
		p.dropConnections()
		rows[0].runs = append(rows[0].runs, r.ask("cold question"))

		p.dropConnections()
		rows[1].runs = append(rows[1].runs, r.ask("question at once"))

		p.dropConnections()
		time.Sleep(50 * time.Millisecond) // for the client to see the close, as it has long since after a real idle timeout
		r.openApp()
		time.Sleep(time.Second)
		rows[2].runs = append(rows[2].runs, r.ask("question after typing"))

		rows[3].runs = append(rows[3].runs, r.ask("next question"))

		if indexed != nil {
			indexed.stop()
			p.dropConnections()
			rows[4].runs = append(rows[4].runs, indexed.ask("which function counts burrow number seven"))
		}
	}

	var b strings.Builder
	scheme := "TLS"
	if certFile == "" {
		scheme = "plain HTTP"
	}
	fmt.Fprintf(&b, "Latency (%s, %s/%s, a new provider connection (%s) costs %s, median of %d)\n\n", label, runtime.GOOS, runtime.GOARCH, scheme, delay, runs)
	fmt.Fprintf(&b, "| | to provider | to answer | total |\n|---|---|---|---|\n")
	fmt.Fprintf(&b, "| version | | | %s |\n", ms(median(version)))
	for _, rw := range rows {
		var tp, ta, tt []time.Duration
		for _, x := range rw.runs {
			tp, ta, tt = append(tp, x.toProvider), append(ta, x.toAnswer), append(tt, x.total)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", rw.name, ms(median(tp)), ms(median(ta)), ms(median(tt)))
	}
	t.Log("\n" + b.String())
	if f := os.Getenv("GITHUB_STEP_SUMMARY"); f != "" {
		if fh, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0); err == nil {
			_, _ = fh.WriteString(b.String() + "\n")
			_ = fh.Close()
		}
	}
}
