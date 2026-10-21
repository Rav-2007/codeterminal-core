package main

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// THE PROVIDER CONNECTION, OPENED BEFORE THE QUESTION (1.2.0).
//
// A question's first byte to the provider waits for DNS, TCP and TLS on a new
// connection: 0.23-0.39 s from a laptop to NVIDIA and OpenRouter on 2026-10-19,
// and one cold DNS lookup of a provider took 5.0 s. Every model request used to
// go through http.DefaultClient, whose idle connections close after 90 s, so
// the first question of a session, and the first after a pause, paid all of it
// while the person watched.
//
// Now a client's handshake -- which the terminal app makes when it opens, and
// before every question -- also opens a connection to the provider in the
// background: a HEAD of the configured base URL, with no key and no content.
// The connection lands in providerHTTP's pool, and the question that follows
// reuses it. The daemon does the same the moment it holds its address, beside
// the rest of its startup. Only the host every question already goes to is
// contacted, through the same proxy settings.
//
// MEASURED with latency_test.go, a new connection costing 300 ms inside its TLS
// handshake, median of 9 (2026-10-20, this laptop, with the 1.2.0 start-up
// fixes beside it): a question typed a second after the app opened reached the
// provider in 16 ms instead of 327; a cold start's in 325 ms instead of 481, and
// in an indexed project 378 instead of 791, the connection opening while the
// code-search helper loads. A question sent the instant the app starts (a
// script's --prompt) races the warm-up's own handshake: 310 vs 324.

// providerHTTP is the client every model request uses. Its own transport, so
// its pool holds provider connections only, and providerConns can count them;
// TLS sessions are cached, so a connection reopened after an idle close resumes
// rather than repeating the full handshake.
var providerHTTP = &http.Client{Transport: newProviderTransport(providerConns)}

func newProviderTransport(conns *liveConns) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	dial := (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return conns.track(addr, c), nil
	}
	t.TLSClientConfig = &tls.Config{ClientSessionCache: tls.NewLRUClientSessionCache(16)}
	return t
}

// liveConns counts the open connections per dialled address. A connection the
// provider closes is closed by the transport when it notices, so the count
// falls without anyone asking.
type liveConns struct {
	mu sync.Mutex
	n  map[string]int
}

var providerConns = &liveConns{n: map[string]int{}}

func (l *liveConns) track(addr string, c net.Conn) net.Conn {
	l.mu.Lock()
	l.n[addr]++
	l.mu.Unlock()
	return &trackedConn{Conn: c, done: func() {
		l.mu.Lock()
		l.n[addr]--
		l.mu.Unlock()
	}}
}

func (l *liveConns) open(addr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n[addr] > 0
}

type trackedConn struct {
	net.Conn
	once sync.Once
	done func()
}

func (c *trackedConn) Close() error {
	c.once.Do(c.done)
	return c.Conn.Close()
}

// providerWarmer opens a provider connection ahead of a question, unless one to
// that address is already open or being opened: a client that handshakes before
// every question must not cost a HEAD each time.
type providerWarmer struct {
	client *http.Client
	conns  *liveConns

	mu       sync.Mutex
	inFlight map[string]bool
}

var warmer = &providerWarmer{client: providerHTTP, conns: providerConns, inFlight: map[string]bool{}}

// warm opens a connection to base's host in the background and returns at
// once. A base that is not an http(s) URL is ignored.
func (w *providerWarmer) warm(base string) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return
	}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), map[string]string{"http": "80", "https": "443"}[u.Scheme])
	}
	if w.conns.open(addr) {
		return
	}
	w.mu.Lock()
	if w.inFlight[addr] {
		w.mu.Unlock()
		return
	}
	w.inFlight[addr] = true
	w.mu.Unlock()
	go func() {
		defer func() {
			w.mu.Lock()
			delete(w.inFlight, addr)
			w.mu.Unlock()
		}()
		w.open(u.String())
	}()
}

// open sends the HEAD. Its answer -- usually a 404 or 401 -- does not matter:
// the connection it leaves behind does.
func (w *providerWarmer) open(target string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return
	}
	// Read to the end, or the connection cannot go back in the pool.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

// warmProvider opens the provider connection a question is about to need, when
// one could be asked: with no credential, the question would be refused anyway.
// A provider on this machine is left alone: a loopback connection has no DNS or
// TLS to save, and a local server owes a HEAD no particular answer (the daemon's
// own tests' stand-ins take any request for a question).
func (s *Server) warmProvider() {
	_, base := s.credentials()
	if s.needsAPIKey() || isLoopbackBase(base) {
		return
	}
	warmer.warm(base)
}
