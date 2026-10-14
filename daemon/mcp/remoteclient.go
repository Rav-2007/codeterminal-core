package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// A REMOTE MCP SERVER: one reached over HTTP at a URL (Notion's, Sentry's,
// GitHub's hosted server), not started on this machine.
//
// IT IS STILL LANE B. Nothing about it is ours: the remote end decides what its
// tools do, and what it returns is a stranger's text. So its tools are
// translated exactly as a subprocess's are (session.go) -- Confined:false,
// third party, every call asked -- and Tool.Remote names the host so the
// approval prompt says the arguments are SENT there rather than run here.
//
// THE HTTP CLIENT IS OURS, because the SDK's transports take an *http.Client
// and nothing else: no header field, no size bound. So:
//   - the headers the user gave (a token) are added here, and only to requests
//     for the server's own origin;
//   - a request to any other origin is refused, and so is a redirect that
//     leaves the origin or drops from https to http -- a token must not follow
//     a server's redirect to a host the user never named;
//   - one message is bounded the way a subprocess's line is (messageLimitReader),
//     so a server cannot make this daemon allocate without limit;
//   - TLS and the wait for response headers are bounded.

// RemoteConfig is everything needed to reach one remote server.
type RemoteConfig struct {
	Name string
	// URL is the endpoint: https, or http to this machine only.
	URL string
	// Transport is "http" (streamable HTTP, the current spec) or "sse" (the
	// older one). "" is http.
	Transport string
	// Headers are sent with every request to the server's origin.
	Headers map[string]string
	// OAuth, when set, signs requests in and answers a 401 (the sign-in flow).
	OAuth auth.OAuthHandler
	// Logf receives this daemon's diagnostics about the server.
	Logf func(format string, args ...any)
	// MaxMessageBytes bounds one message; 0 means DefaultMaxMessageBytes.
	MaxMessageBytes int
	// ConnectTimeout bounds the handshake; 0 means DefaultConnectTimeout.
	ConnectTimeout time.Duration
	// HTTPClient replaces the default transport underneath the guards (tests).
	HTTPClient *http.Client
}

// RemoteClient is a connected remote server.
type RemoteClient struct {
	name string
	host string
	logf func(format string, args ...any)

	mu      sync.Mutex
	session *sdk.ClientSession
	closed  bool
	// end cancels the session's context (ConnectRemote).
	end context.CancelFunc
}

// ValidateRemoteURL reports why raw cannot be a remote server's address, or
// nil. https only, except http to this machine, and never a user:password@ in
// it (that is a credential in a string that is stored and shown).
func ValidateRemoteURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, errors.New("is not a URL (it needs https:// and a host)")
	}
	if u.User != nil {
		return nil, errors.New("carries a user name or password; give a token as a header instead, so it is kept as a key")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return nil, errors.New("is http; a remote server must be https, so what is sent to it cannot be read on the way (http is allowed for this machine only)")
		}
	default:
		return nil, fmt.Errorf("has scheme %q; it must be https", u.Scheme)
	}
	return u, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ValidateHeader reports why a header cannot be sent, or nil. The value is
// never repeated in the error.
func ValidateHeader(name, value string) error {
	if name == "" || strings.IndexFunc(name, func(r rune) bool {
		return r <= ' ' || r >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", r)
	}) >= 0 {
		return fmt.Errorf("%q is not a header name", name)
	}
	switch strings.ToLower(name) {
	case "host", "content-length", "connection", "transfer-encoding", "upgrade", "te", "trailer", "mcp-session-id":
		return fmt.Errorf("the %s header is set by the connection itself, not by a server's settings", name)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("the value of %s contains a line break", name)
	}
	return nil
}

// guardedTransport is the RoundTripper every remote request goes through.
type guardedTransport struct {
	origin  *url.URL
	headers map[string]string
	limit   int
	server  string
	next    http.RoundTripper
	// life is the session's context. The SDK sends each request on a context
	// that is never cancelled (a write is not interrupted halfway), so without
	// this a server that accepts the connection and never answers held the
	// handshake past its timeout, and a closed session's request open, until
	// the transport's own header timeout.
	life context.Context
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func (g *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !sameOrigin(req.URL, g.origin) {
		// The SDK follows URLs the server gives it (the SSE transport's
		// endpoint event). One that leaves the origin is not followed: the
		// user's token was given for this host.
		return nil, fmt.Errorf("server %s pointed this daemon at %s, which is not the origin it was added with (%s); refused",
			g.server, req.URL.Host, g.origin.Host)
	}
	ctx, cancel := context.WithCancel(req.Context())
	release := cancel
	if g.life != nil {
		stop := context.AfterFunc(g.life, cancel)
		release = func() { stop(); cancel() }
	}
	req = req.Clone(ctx)
	for k, v := range g.headers {
		req.Header.Set(k, v)
	}
	resp, err := g.next.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		release()
		return resp, err
	}
	resp.Body = &limitedBody{r: &messageLimitReader{r: resp.Body, max: g.limit, server: g.server}, c: resp.Body, done: release}
	return resp, nil
}

type limitedBody struct {
	r    io.Reader
	c    io.Closer
	done func()
}

func (b *limitedBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *limitedBody) Close() error {
	err := b.c.Close()
	b.done()
	return err
}

// remoteHTTPClient is the guarded client for one server.
func remoteHTTPClient(cfg RemoteConfig, origin *url.URL, life context.Context) *http.Client {
	next := http.RoundTripper(&http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   2,
		ForceAttemptHTTP2:     true,
	})
	if cfg.HTTPClient != nil && cfg.HTTPClient.Transport != nil {
		next = cfg.HTTPClient.Transport
	}
	limit := cfg.MaxMessageBytes
	if limit <= 0 {
		limit = DefaultMaxMessageBytes
	}
	return &http.Client{
		Transport: &guardedTransport{origin: origin, headers: cfg.Headers, limit: limit, server: cfg.Name, next: next, life: life},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !sameOrigin(req.URL, origin) {
				return fmt.Errorf("server %s redirected to %s, another origin; not followed", cfg.Name, req.URL.Host)
			}
			return nil
		},
	}
}

// ConnectRemote reaches a remote server and completes the MCP handshake.
func ConnectRemote(ctx context.Context, cfg RemoteConfig) (*RemoteClient, error) {
	if err := ValidateServerName(cfg.Name); err != nil {
		return nil, err
	}
	origin, err := ValidateRemoteURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("server %s: its URL %v", cfg.Name, err)
	}
	for k, v := range cfg.Headers {
		if err := ValidateHeader(k, v); err != nil {
			return nil, fmt.Errorf("server %s: %v", cfg.Name, err)
		}
	}
	// THE SESSION OUTLIVES THE HANDSHAKE. The SSE transport keeps its event
	// stream on the context Connect is given, so a context cancelled when the
	// handshake's time is up ended the stream the moment this returned and
	// every later call read EOF. The session gets a context of its own, ended
	// by Close (or by the caller's, for a turn that ends); the handshake alone
	// is timed, by cancelling it if the deadline passes first. Every request
	// ends with it too (guardedTransport.life).
	sessionCtx, endSession := context.WithCancel(ctx)
	httpClient := remoteHTTPClient(cfg, origin, sessionCtx)

	var transport sdk.Transport
	switch strings.ToLower(cfg.Transport) {
	case "", "http", "streamable-http":
		transport = &sdk.StreamableClientTransport{
			Endpoint:   origin.String(),
			HTTPClient: httpClient,
			// No standalone event stream: this daemon never takes a request the
			// server starts, and a GET held open per turn is a connection the
			// user pays for and gets nothing from.
			DisableStandaloneSSE: true,
			OAuthHandler:         cfg.OAuth,
			MaxRetries:           1,
		}
	case "sse":
		transport = &sdk.SSEClientTransport{Endpoint: origin.String(), HTTPClient: httpClient}
	default:
		endSession()
		return nil, fmt.Errorf("server %s: transport %q is not http or sse", cfg.Name, cfg.Transport)
	}

	handshake := cfg.ConnectTimeout
	if handshake <= 0 {
		handshake = DefaultConnectTimeout
	}
	timer := time.AfterFunc(handshake, endSession)
	client := sdk.NewClient(&sdk.Implementation{Name: "mochiii", Version: "v1"}, nil)
	session, err := client.Connect(sessionCtx, transport, nil)
	timedOut := !timer.Stop()
	if err != nil || timedOut {
		endSession()
		if session != nil {
			_ = session.Close()
		}
		if err == nil || timedOut {
			err = fmt.Errorf("no answer within %s: %w", handshake, context.DeadlineExceeded)
		}
		return nil, fmt.Errorf("%w: server %s could not be reached at %s: %v", ErrServerUnavailable, cfg.Name, origin.Host, err)
	}
	return &RemoteClient{name: cfg.Name, host: origin.Host, logf: cfg.Logf, session: session, end: endSession}, nil
}

func (c *RemoteClient) logfSafe(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

// ListTools returns the server's tools, each naming the host it is reached at.
func (c *RemoteClient) ListTools(ctx context.Context) ([]Tool, error) {
	c.mu.Lock()
	session, closed := c.session, c.closed
	c.mu.Unlock()
	if closed || session == nil {
		return nil, fmt.Errorf("%w: server %s is not connected", ErrServerUnavailable, c.name)
	}
	return listSessionTools(ctx, session, c.name, c.host, c.logfSafe)
}

// CallTool invokes one tool on the remote server.
func (c *RemoteClient) CallTool(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	c.mu.Lock()
	session, closed := c.session, c.closed
	c.mu.Unlock()
	if closed || session == nil {
		return Result{}, fmt.Errorf("%w: server %s is not connected", ErrServerUnavailable, c.name)
	}
	return callSessionTool(ctx, session, c.name, name, args)
}

// Close ends the session (the streamable transport tells the server so).
// Idempotent.
func (c *RemoteClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	session, end := c.session, c.end
	c.mu.Unlock()
	var err error
	if session != nil {
		err = session.Close()
	}
	if end != nil {
		end()
	}
	return err
}
