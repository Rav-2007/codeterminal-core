package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// SIGNING IN TO A REMOTE SERVER IN THE BROWSER (Notion, Sentry, Linear): the
// OAuth authorization-code flow with PKCE, the go-sdk's AuthorizationCodeHandler
// doing discovery, client registration, the code exchange and the state and
// issuer checks.
//
// WHO DOES WHAT. The DAEMON listens for the browser's return on 127.0.0.1, on a
// random port and an unguessable path, and keeps the tokens. The CLIENT opens
// the address in the user's browser -- VS Code with openExternal, the terminal
// by printing it -- because the daemon has no window and must not be the thing
// that launches programs on its own. One client request carries one answer
// (sendMCP), so the sign-in is several short ones: auth_start answers with the
// address; auth_wait is repeated until it is done; auth_complete takes the
// address the browser ended on, pasted, for a browser that cannot reach this
// machine's 127.0.0.1 (a remote VS Code, ssh).
//
// A TURN NEVER SIGNS IN. A turn's connection uses the stored token, refreshing
// it when it has expired; a server that still says 401 makes the turn report
// "needs a sign-in" (MCPStateNeedsAuth) at once -- it never waits on a browser
// nobody opened.
//
// THE TOKENS ARE KEYS. They live in mcp-secrets.json beside the server's other
// keys (owner-only, locked, re-read when another daemon refreshed them first),
// bound to the URL they were issued for, and every one joins the scrubber.

// mcpSignInWindow is how long a sign-in stays open for the user to finish.
const mcpSignInWindow = 5 * time.Minute

// mcpOAuthSecret is one server's sign-in, as kept in mcp-secrets.json.
type mcpOAuthSecret struct {
	// Endpoint is the server URL it was issued for: a server whose URL changed
	// does not use it.
	Endpoint     string    `json:"endpoint"`
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret,omitempty"`
	AuthURL      string    `json:"auth_url,omitempty"`
	TokenURL     string    `json:"token_url"`
	AuthStyle    int       `json:"auth_style,omitempty"`
	Scopes       []string  `json:"scopes,omitempty"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
}

func (o *mcpOAuthSecret) config() *oauth2.Config {
	return &oauth2.Config{
		ClientID: o.ClientID, ClientSecret: o.ClientSecret, Scopes: o.Scopes,
		Endpoint: oauth2.Endpoint{AuthURL: o.AuthURL, TokenURL: o.TokenURL, AuthStyle: oauth2.AuthStyle(o.AuthStyle)},
	}
}

func (o *mcpOAuthSecret) token() *oauth2.Token {
	return &oauth2.Token{AccessToken: o.AccessToken, RefreshToken: o.RefreshToken, TokenType: o.TokenType, Expiry: o.Expiry}
}

// oauthOf reads a server's stored sign-in from the key file, if it is for url.
func (st *mcpSecretStore) oauthOf(server, endpoint string) *mcpOAuthSecret {
	if st == nil {
		return nil
	}
	st.refresh()
	st.mu.Lock()
	raw, ok := st.data.Servers[server].Extra["oauth"]
	st.mu.Unlock()
	if !ok {
		return nil
	}
	var o mcpOAuthSecret
	if json.Unmarshal(raw, &o) != nil || o.AccessToken == "" || o.Endpoint != endpoint {
		return nil
	}
	return &o
}

// setOAuth stores (nil: forgets) a server's sign-in.
func (st *mcpSecretStore) setOAuth(server string, o *mcpOAuthSecret) error {
	return st.update(func(f *mcpSecretsFile) {
		if f.Servers == nil {
			f.Servers = map[string]mcpServerSecrets{}
		}
		srv := f.Servers[server]
		if o == nil {
			delete(srv.Extra, "oauth")
		} else {
			raw, _ := json.Marshal(o)
			if srv.Extra == nil {
				srv.Extra = map[string]json.RawMessage{}
			}
			srv.Extra["oauth"] = raw
		}
		f.Servers[server] = srv
	})
}

// errNeedsSignIn is a remote server refusing a turn for want of a sign-in.
var errNeedsSignIn = errors.New("this server needs you to sign in; use Sign in on it in Customize > MCP, " +
	"`mcp auth <name>`, or /mcp-server sign-in <name>")

// oauthHTTPClient is the client every sign-in request goes through: discovery,
// registration, the code exchange and refreshes. Bounded, https only.
func oauthHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" && !isLoopbackHostname(req.URL.Hostname()) {
				return fmt.Errorf("refused a redirect to %s over %s", req.URL.Host, req.URL.Scheme)
			}
			return nil
		},
	}
}

func isLoopbackHostname(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// storedOAuth is the OAuthHandler a turn, a check or a list uses: the stored
// token, refreshed when it has expired, and no sign-in -- a 401 is reported,
// never answered with a browser.
type storedOAuth struct {
	s        *Server
	server   string
	endpoint string
}

func (h *storedOAuth) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	o := h.s.mcpSecrets.oauthOf(h.server, h.endpoint)
	if o == nil {
		return nil, nil
	}
	return &persistingTokenSource{s: h.s, server: h.server, endpoint: h.endpoint, secret: o}, nil
}

func (h *storedOAuth) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	return errNeedsSignIn
}

// persistingTokenSource hands out the stored token, refreshing it when it has
// expired and keeping the new one. Before refreshing it reads the file again:
// another daemon may have refreshed first, and a refresh token is often good
// for one use only.
type persistingTokenSource struct {
	s        *Server
	server   string
	endpoint string

	mu     sync.Mutex
	secret *mcpOAuthSecret
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t := p.secret.token(); t.Valid() {
		return t, nil
	}
	if fresh := p.s.mcpSecrets.oauthOf(p.server, p.endpoint); fresh != nil && fresh.token().Valid() {
		p.secret = fresh
		return fresh.token(), nil
	}
	if p.secret.RefreshToken == "" {
		return nil, errNeedsSignIn
	}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, oauthHTTPClient())
	tok, err := p.secret.config().TokenSource(ctx, p.secret.token()).Token()
	if err != nil {
		return nil, fmt.Errorf("%w (refreshing the sign-in failed: %v)", errNeedsSignIn, err)
	}
	next := *p.secret
	next.AccessToken, next.TokenType, next.Expiry = tok.AccessToken, tok.TokenType, tok.Expiry
	if tok.RefreshToken != "" {
		next.RefreshToken = tok.RefreshToken
	}
	if err := p.s.mcpSecrets.setOAuth(p.server, &next); err != nil {
		p.s.logger.Printf("mcp: keeping %s's refreshed sign-in: %v", p.server, err)
	}
	p.s.rebuildCredScrubber()
	p.secret = &next
	return tok, nil
}

// oauthHandlerFor is the handler a turn's connection to srv uses.
func (s *Server) oauthHandlerFor(name string, srv MCPServerConfig) auth.OAuthHandler {
	return &storedOAuth{s: s, server: name, endpoint: srv.URL}
}

// A SIGN-IN IN PROGRESS.
type mcpAuthFlow struct {
	id       string
	server   string
	path     string // the callback's unguessable path
	redirect string

	urlReady chan struct{}
	authURL  string

	codes   chan *auth.AuthorizationResult
	gotCode sync.Once

	done    chan struct{}
	state   string
	detail  string
	cancel  context.CancelFunc
	expires time.Time
}

// mcpAuthFlows holds the sign-ins in progress, one per server at most.
type mcpAuthFlows struct {
	mu    sync.Mutex
	byID  map[string]*mcpAuthFlow
	byKey map[string]*mcpAuthFlow
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// finish records how the flow ended, once.
func (f *mcpAuthFlow) finish(state, detail string) {
	select {
	case <-f.done:
		return
	default:
	}
	f.state, f.detail = state, detail
	close(f.done)
}

// mcpAuthStart begins a sign-in and answers with the address to open.
func (s *Server) mcpAuthStart(req protocol.MCPRequest) protocol.MCPResponse {
	cfg, _ := s.agentConfig()
	name := strings.TrimSpace(req.Name)
	srv, ok := cfg.MCP.Servers[name]
	switch {
	case !ok:
		return protocol.MCPResponse{Error: fmt.Sprintf("no server named %s is configured", name)}
	case !srv.remote():
		return protocol.MCPResponse{Error: fmt.Sprintf("%s is a program on this machine; only a remote server is signed in to", name)}
	case srv.Disabled:
		return protocol.MCPResponse{Error: fmt.Sprintf("%s is disabled; enable it first", name)}
	case !srv.AcknowledgedUnconfined:
		return protocol.MCPResponse{Error: fmt.Sprintf("%s has not been agreed to", name)}
	}
	if s.mcpSecrets == nil {
		return protocol.MCPResponse{Error: "this daemon has nowhere to keep a sign-in"}
	}

	// One sign-in per server: a new one ends the last.
	s.mcpAuth.mu.Lock()
	if s.mcpAuth.byID == nil {
		s.mcpAuth.byID, s.mcpAuth.byKey = map[string]*mcpAuthFlow{}, map[string]*mcpAuthFlow{}
	}
	if old := s.mcpAuth.byKey[name]; old != nil {
		old.cancel()
	}
	s.mcpAuth.mu.Unlock()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return protocol.MCPResponse{Error: "listening for the browser's return: " + err.Error()}
	}
	flow := &mcpAuthFlow{
		id: randomToken(12), server: name, path: "/cb/" + randomToken(16),
		urlReady: make(chan struct{}), codes: make(chan *auth.AuthorizationResult, 1), done: make(chan struct{}),
		state: protocol.MCPAuthPending, expires: time.Now().Add(mcpSignInWindow),
	}
	flow.redirect = fmt.Sprintf("http://%s%s", listener.Addr().String(), flow.path)
	ctx, cancel := context.WithTimeout(context.Background(), mcpSignInWindow)
	flow.cancel = cancel

	s.mcpAuth.mu.Lock()
	s.mcpAuth.byID[flow.id], s.mcpAuth.byKey[name] = flow, flow
	s.mcpAuth.mu.Unlock()

	// The browser comes back here: the first request on the secret path is
	// the answer; anything else is a 404. The page says nothing it was sent.
	callback := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != flow.path {
				http.NotFound(w, r)
				return
			}
			flow.deliver(r.URL.Query())
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, signInDonePage)
		}),
	}
	go func() { _ = callback.Serve(listener) }()

	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			RedirectURIs:            []string{flow.redirect},
			ClientName:              "Mochiii",
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			TokenEndpointAuthMethod: "none",
		}},
		RedirectURL:         flow.redirect,
		RequestRefreshToken: true,
		Client:              oauthHTTPClient(),
		AuthorizationCodeFetcher: func(fctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			u, err := url.Parse(args.URL)
			if err != nil || (u.Scheme != "https" && !isLoopbackHostname(u.Hostname())) {
				return nil, errors.New("the server's sign-in page is not https; refused")
			}
			flow.authURL = args.URL
			close(flow.urlReady)
			select {
			case res := <-flow.codes:
				return res, nil
			case <-fctx.Done():
				return nil, errors.New("the sign-in was not finished in time")
			}
		},
		// The token, and what refreshing it needs, kept the moment it is issued.
		NewTokenSource: func(tctx context.Context, oc *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			secret := &mcpOAuthSecret{
				Endpoint: srv.URL, ClientID: oc.ClientID, ClientSecret: oc.ClientSecret,
				AuthURL: oc.Endpoint.AuthURL, TokenURL: oc.Endpoint.TokenURL, AuthStyle: int(oc.Endpoint.AuthStyle),
				Scopes: oc.Scopes, AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken,
				TokenType: tok.TokenType, Expiry: tok.Expiry,
			}
			if err := s.mcpSecrets.setOAuth(name, secret); err != nil {
				return nil, fmt.Errorf("keeping the sign-in: %w", err)
			}
			s.rebuildCredScrubber()
			return &persistingTokenSource{s: s, server: name, endpoint: srv.URL, secret: secret}, nil
		},
	})
	if err != nil {
		cancel()
		_ = callback.Close()
		return protocol.MCPResponse{Error: "setting up the sign-in: " + err.Error()}
	}

	// The sign-in IS a connection: connecting meets the 401, the handler
	// signs in, and the connection goes on with the token. Then its tools are
	// listed, as a check's are.
	go func() {
		defer func() {
			cancel()
			_ = callback.Close()
		}()
		s.mcpStates.beforeStart(name)
		client, err := mcp.ConnectRemote(ctx, mcp.RemoteConfig{
			Name: name, URL: srv.URL, Transport: srv.Type, Headers: mustHeaders(s, name, srv),
			OAuth: handler, Logf: s.logger.Printf,
			MaxMessageBytes: cfg.MCP.Budget.resolvedMaxMessageBytes(), ConnectTimeout: mcpSignInWindow,
		})
		if err != nil {
			s.mcpStates.started(name, err)
			flow.finish(protocol.MCPAuthFailed, truncateForClient(err.Error(), 240))
			return
		}
		tools, lerr := client.ListTools(ctx)
		_ = client.Close()
		s.mcpStates.started(name, nil)
		s.mcpStates.listed(map[string]mcp.ListOutcome{name: {Tools: len(tools), Err: lerr}})
		s.logger.Printf("mcp: signed in to %s", name)
		flow.finish(protocol.MCPAuthDone, fmt.Sprintf("Signed in to %s. It offers %d tool(s).", name, len(tools)))
	}()

	// The address, or -- for a server that needed no sign-in, or failed before
	// asking -- how it ended.
	select {
	case <-flow.urlReady:
		return protocol.MCPResponse{Ok: true, AgentMode: cfg.MCP.Enabled, AuthID: flow.id, AuthURL: flow.authURL,
			AuthState: protocol.MCPAuthPending, ExpiresInSeconds: int(mcpSignInWindow / time.Second),
			Detail: fmt.Sprintf("Open this address to sign in to %s (%s).", name, remoteHost(srv.URL))}
	case <-flow.done:
		return s.authAnswer(flow)
	case <-time.After(45 * time.Second):
		flow.cancel()
		return protocol.MCPResponse{Error: fmt.Sprintf("%s did not start a sign-in within 45 seconds", name)}
	}
}

func mustHeaders(s *Server, name string, srv MCPServerConfig) map[string]string {
	h, _ := s.remoteHeaders(name, srv)
	return h
}

// deliver hands the browser's return to the waiting fetcher, once.
func (f *mcpAuthFlow) deliver(q url.Values) {
	f.gotCode.Do(func() {
		if e := q.Get("error"); e != "" {
			f.cancel()
			f.finish(protocol.MCPAuthFailed, "the sign-in was refused: "+truncateForClient(e, 60))
			return
		}
		f.codes <- &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}
	})
}

// authAnswer is a flow's state as a response.
func (s *Server) authAnswer(flow *mcpAuthFlow) protocol.MCPResponse {
	cfg, _ := s.agentConfig()
	resp := protocol.MCPResponse{Ok: true, AgentMode: cfg.MCP.Enabled, AuthID: flow.id, AuthState: flow.state}
	select {
	case <-flow.done:
		resp.AuthState, resp.Detail = flow.state, flow.detail
		if flow.state == protocol.MCPAuthFailed {
			resp.Ok, resp.Error = false, flow.detail
		}
		resp.Servers = s.serverInfos(cfg)
	default:
		resp.AuthState = protocol.MCPAuthPending
	}
	return resp
}

func (s *Server) authFlow(id string) *mcpAuthFlow {
	s.mcpAuth.mu.Lock()
	defer s.mcpAuth.mu.Unlock()
	return s.mcpAuth.byID[id]
}

// mcpAuthWait waits up to 45 seconds for a sign-in to end.
func (s *Server) mcpAuthWait(req protocol.MCPRequest) protocol.MCPResponse {
	flow := s.authFlow(req.AuthID)
	if flow == nil {
		return protocol.MCPResponse{Error: "no sign-in by that id is in progress"}
	}
	select {
	case <-flow.done:
	case <-time.After(45 * time.Second):
	}
	return s.authAnswer(flow)
}

// mcpAuthComplete takes the address the browser ended on, pasted -- for a
// browser that could not reach this machine. Only an address on this flow's
// own secret path is taken.
func (s *Server) mcpAuthComplete(req protocol.MCPRequest) protocol.MCPResponse {
	flow := s.authFlow(req.AuthID)
	if flow == nil {
		return protocol.MCPResponse{Error: "no sign-in by that id is in progress"}
	}
	u, err := url.Parse(strings.TrimSpace(req.CallbackURL))
	if err != nil || u.Path != flow.path {
		return protocol.MCPResponse{Error: "that is not the address this sign-in sent the browser back to; paste the whole " +
			"address from the browser's address bar"}
	}
	flow.deliver(u.Query())
	select {
	case <-flow.done:
	case <-time.After(30 * time.Second):
	}
	return s.authAnswer(flow)
}

// mcpAuthCancel ends a sign-in.
func (s *Server) mcpAuthCancel(req protocol.MCPRequest) protocol.MCPResponse {
	if flow := s.authFlow(req.AuthID); flow != nil {
		flow.cancel()
		flow.finish(protocol.MCPAuthFailed, "cancelled")
	}
	return protocol.MCPResponse{Ok: true, Detail: "Cancelled; nothing was stored."}
}

// mcpSignOut forgets a server's sign-in.
func (s *Server) mcpSignOut(req protocol.MCPRequest) protocol.MCPResponse {
	name := strings.TrimSpace(req.Name)
	if err := s.mcpSecrets.setOAuth(name, nil); err != nil {
		return protocol.MCPResponse{Error: "forgetting the sign-in: " + err.Error()}
	}
	s.rebuildCredScrubber()
	s.mcpStates.forget(name)
	cfg, _ := s.agentConfig()
	return protocol.MCPResponse{Ok: true, AgentMode: cfg.MCP.Enabled, Servers: s.serverInfos(cfg),
		Detail: fmt.Sprintf("Signed out of %s; its token was deleted from this machine.", name)}
}

// signInDonePage is what the browser shows on its return. It repeats nothing
// it was sent.
const signInDonePage = `<!doctype html><meta charset="utf-8"><title>Mochiii</title>
<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;line-height:1.5">
<h2>Back to Mochiii</h2><p>The sign-in was received. You can close this tab and return to your editor.</p></body>`
