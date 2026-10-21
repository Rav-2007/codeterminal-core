package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// warmTarget is a provider that counts its new connections and records each
// request's method and whether it carried a credential.
type warmTarget struct {
	srv *httptest.Server

	mu       sync.Mutex
	newConns int
	requests []string // "HEAD", "POST", with " key" when an Authorization header came
}

func newWarmTarget(t *testing.T) *warmTarget {
	t.Helper()
	w := &warmTarget{}
	w.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		entry := r.Method
		if r.Header.Get("Authorization") != "" {
			entry += " key"
		}
		w.mu.Lock()
		w.requests = append(w.requests, entry)
		w.mu.Unlock()
		rw.WriteHeader(http.StatusNotFound)
	}))
	w.srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			w.mu.Lock()
			w.newConns++
			w.mu.Unlock()
		}
	}
	w.srv.Start()
	t.Cleanup(w.srv.Close)
	return w
}

func (w *warmTarget) seen() (conns int, requests []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.newConns, append([]string(nil), w.requests...)
}

func newTestWarmer() (*providerWarmer, *liveConns) {
	conns := &liveConns{n: map[string]int{}}
	client := &http.Client{Transport: newProviderTransport(conns)}
	return &providerWarmer{client: client, conns: conns, inFlight: map[string]bool{}}, conns
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// THE POINT OF IT: the question after a warm-up goes over the connection the
// warm-up opened, and the warm-up itself carries no key.
func TestTheQuestionReusesTheWarmedConnection(t *testing.T) {
	target := newWarmTarget(t)
	w, conns := newTestWarmer()
	addr := strings.TrimPrefix(target.srv.URL, "http://")

	w.warm(target.srv.URL + "/v1")
	waitUntil(t, "the warm-up's connection", func() bool { _, reqs := target.seen(); return len(reqs) == 1 && conns.open(addr) })

	req, _ := http.NewRequest(http.MethodPost, target.srv.URL+"/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer k")
	resp, err := w.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	newConns, reqs := target.seen()
	if newConns != 1 {
		t.Errorf("the provider saw %d new connections; the question should have reused the warm-up's one", newConns)
	}
	if len(reqs) != 2 || reqs[0] != "HEAD" || reqs[1] != "POST key" {
		t.Errorf("requests = %q, want [HEAD \"POST key\"]: the warm-up must carry no key", reqs)
	}
}

// A connection already open, or one being opened, is not opened again: a client
// handshakes before every question.
func TestAWarmUpIsNotRepeatedWhileAConnectionIsOpen(t *testing.T) {
	target := newWarmTarget(t)
	w, conns := newTestWarmer()
	addr := strings.TrimPrefix(target.srv.URL, "http://")
	base := target.srv.URL + "/v1"

	w.warm(base)
	w.warm(base) // in flight: nothing more
	waitUntil(t, "the warm-up's connection", func() bool { _, reqs := target.seen(); return len(reqs) >= 1 && conns.open(addr) })
	w.warm(base) // open: nothing more
	time.Sleep(50 * time.Millisecond)
	if newConns, reqs := target.seen(); newConns != 1 || len(reqs) != 1 {
		t.Errorf("after three warm-ups: %d connections, requests %q; want one of each", newConns, reqs)
	}

	// The provider closes it, as an idle timeout does: the next warm-up opens one.
	target.srv.CloseClientConnections()
	waitUntil(t, "the client to see the close", func() bool { return !conns.open(addr) })
	w.warm(base)
	waitUntil(t, "a second warm-up", func() bool { _, reqs := target.seen(); return len(reqs) == 2 })
}

func TestAWarmUpIgnoresABaseThatIsNoHTTPURL(t *testing.T) {
	w, _ := newTestWarmer()
	for _, base := range []string{"", "   ", "not a url", "ftp://example.com/v1", "://", "unix:///tmp/x.sock"} {
		w.warm(base)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.inFlight) != 0 {
		t.Errorf("warm-ups started for bases that are not http(s): %v", w.inFlight)
	}
}

// roundTripFunc records where the server's warm-up would have gone.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// With no credential a question would be refused, so no connection is opened
// for one, nor for a provider on this machine; with a key, the configured
// provider's host is the one contacted.
func TestTheServerWarmsOnlyAProviderItCanAsk(t *testing.T) {
	var mu sync.Mutex
	var hosts []string
	saved := warmer
	t.Cleanup(func() { warmer = saved })
	warmer = &providerWarmer{
		client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			hosts = append(hosts, r.Method+" "+r.URL.Host)
			mu.Unlock()
			return nil, errors.New("not dialled in a test")
		})},
		conns:    &liveConns{n: map[string]int{}},
		inFlight: map[string]bool{},
	}

	(&Server{apiBase: "https://api.provider.example/v1"}).warmProvider()
	(&Server{apiBase: "http://127.0.0.1:9/v1", apiKey: "k"}).warmProvider() // loopback: nothing to save
	(&Server{apiBase: "https://keyed.provider.example/v1", apiKey: "k"}).warmProvider()
	waitUntil(t, "the keyed warm-up", func() bool { mu.Lock(); defer mu.Unlock(); return len(hosts) == 1 })
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(hosts) != 1 || hosts[0] != "HEAD keyed.provider.example" {
		t.Errorf("warm-ups went to %q; want only HEAD keyed.provider.example", hosts)
	}
}

// The warm-up is worth something only if questions use the pool it fills.
func TestModelRequestsGoThroughTheWarmedPool(t *testing.T) {
	if warmer.client != providerHTTP {
		t.Fatal("the warm-up does not use providerHTTP, so its connections are not the ones questions use")
	}
	saved := providerHTTP
	t.Cleanup(func() { providerHTTP = saved })
	var got string
	providerHTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r.Method + " " + r.URL.String()
		return nil, errors.New("not dialled in a test")
	})}
	if resp, err := openCompletion(context.Background(), "https://p.example/v1", "k", "m", nil, nil, providerRouting{}, func(err error) error { return err }); err == nil {
		_ = resp.Body.Close()
	}
	if got != "POST https://p.example/v1/chat/completions" {
		t.Errorf("openCompletion's request through providerHTTP = %q; it must use the warmed pool", got)
	}
}
