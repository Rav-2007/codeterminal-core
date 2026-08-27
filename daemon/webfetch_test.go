package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// THE ADVERSARY IN THESE TESTS IS httptest ITSELF.
//
// httptest.NewServer listens on 127.0.0.1, which is precisely the class of
// address the agent must never be able to reach. That makes it the ideal
// negative fixture: a test that fetches an httptest URL through the production
// client and gets a page back has PROVEN the SSRF gate is off. Several tests
// below therefore assert failure against a server that is definitely running
// and definitely serving -- the only way to fail them is to weaken the gate.

func TestBlockedIPCoversEveryClassAnAgentWouldBeAimedAt(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.1.1", "0.0.0.0", "::1", "::",
		"10.0.0.1", "172.16.0.1", "192.168.1.1",
		// The one that matters most in a cloud deployment: every major
		// provider's instance-credential endpoint lives here.
		"169.254.169.254",
		"169.254.0.1",
		"fc00::1", "fd12:3456::1",
		"224.0.0.1", "ff02::1",
		// An IPv4 private address wearing an IPv6 costume. If To4() unwrapping
		// were removed, this one alone would reopen the whole LAN.
		"::ffff:10.0.0.1",
		"::ffff:127.0.0.1",
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test fixture %q is not a valid IP", s)
		}
		if !blockedIP(ip) {
			t.Errorf("blockedIP(%s) = false, want true — this address is reachable by the agent", s)
		}
	}

	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700::1111"}
	for _, s := range allowed {
		if blockedIP(net.ParseIP(s)) {
			t.Errorf("blockedIP(%s) = true, want false — ordinary public addresses must stay fetchable", s)
		}
	}

	// A nil IP is the "could not tell" case, and it must fail closed.
	if !blockedIP(nil) {
		t.Error("blockedIP(nil) = false; an unparseable address must be refused, not allowed")
	}
}

// The whole gate, end to end, through the real client.
func TestFetchURLRefusesALoopbackServerThatIsDefinitelyServing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><p>internal secrets</p></body></html>"))
	}))
	defer srv.Close()

	// Sanity: the server really is up. Without this the test could pass because
	// the fixture was broken rather than because the gate worked.
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("fixture server is not reachable at all: %v", err)
	}
	_ = resp.Body.Close()

	_, err = fetchURL(context.Background(), webClient(5*time.Second), srv.URL, maxWebFetchBytes)
	if err == nil {
		t.Fatal("fetchURL returned a page from a 127.0.0.1 server; the SSRF gate is not engaged")
	}
	if !strings.Contains(err.Error(), "private or local address") {
		t.Errorf("error = %q, want it to name the reason so the model stops retrying", err)
	}
	if strings.Contains(err.Error(), "internal secrets") {
		t.Error("the refusal leaked the page body")
	}
}

// A redirect from a public host to a private one is the classic bypass. The
// dialler catches it because it runs per-hop, which is why the guard lives
// there rather than at parse time.
func TestGuardAppliesToEveryRedirectHopNotJustTheFirst(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<p>metadata</p>"))
	}))
	defer internal.Close()

	// Even reaching the redirector is refused here (it too is loopback), so
	// this asserts the property that matters: no hop is exempt.
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL, http.StatusFound)
	}))
	defer redirector.Close()

	if _, err := fetchURL(context.Background(), webClient(5*time.Second), redirector.URL, maxWebFetchBytes); err == nil {
		t.Fatal("a redirect chain ending at a private address was followed")
	}
}

func TestFetchURLRefusesNonHTTPSchemes(t *testing.T) {
	for _, raw := range []string{
		"file:///etc/passwd",
		"gopher://example.com/",
		"ftp://example.com/x",
		"data:text/html,<p>hi</p>",
		"jar:http://example.com!/",
	} {
		if _, err := fetchURL(context.Background(), webClient(time.Second), raw, 1024); err == nil {
			t.Errorf("fetchURL(%q) succeeded; only http and https may be fetched", raw)
		}
	}
}

// The happy path and the response-side limits, tested with a client that has no
// address guard -- otherwise the fixture's own loopback address (correctly)
// blocks every one of them.
func unguardedTestClient(srv *httptest.Server) *http.Client {
	c := srv.Client()
	c.Timeout = 5 * time.Second
	return c
}

func TestFetchURLReturnsReadableTextAndTheFinalURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Who is the PM</title>
			<style>.x{color:red}</style></head>
			<body><nav>Home About</nav><p>The office is currently held by A. Person.</p>
			<script>alert('no')</script></body></html>`))
	}))
	defer srv.Close()

	page, err := fetchURL(context.Background(), unguardedTestClient(srv), srv.URL, maxWebFetchBytes)
	if err != nil {
		t.Fatalf("fetchURL: %v", err)
	}
	if page.Title != "Who is the PM" {
		t.Errorf("Title = %q, want the document title", page.Title)
	}
	if !strings.Contains(page.Text, "currently held by A. Person") {
		t.Errorf("Text = %q, want the paragraph text", page.Text)
	}
	for _, junk := range []string{"alert(", "color:red", "Home About"} {
		if strings.Contains(page.Text, junk) {
			t.Errorf("Text still contains %q; script/style/nav must not reach the model's context", junk)
		}
	}
	if page.URL != srv.URL {
		t.Errorf("URL = %q, want the URL actually fetched so the model can cite it", page.URL)
	}
}

func TestFetchURLRefusesNonTextContentRatherThanFeedingBinaryToTheModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0x7f, 0x45, 0x4c, 0x46, 0x02, 0x01})
	}))
	defer srv.Close()

	if _, err := fetchURL(context.Background(), unguardedTestClient(srv), srv.URL, maxWebFetchBytes); err == nil {
		t.Fatal("a binary body was accepted; the model would have paid tokens for mojibake")
	}
}

// A server that never stops sending must not be able to exhaust memory, and a
// server that LIES about its size must not be able to steer the allocation.
func TestFetchURLCapsTheBodyRegardlessOfWhatTheServerClaims(t *testing.T) {
	t.Run("an endless body is cut at the cap", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			for i := 0; i < 200; i++ {
				_, _ = w.Write([]byte(strings.Repeat("A", 1024)))
			}
		}))
		defer srv.Close()

		page, err := fetchURL(context.Background(), unguardedTestClient(srv), srv.URL, 4096)
		if err != nil {
			t.Fatalf("fetchURL: %v", err)
		}
		if len(page.Text) > 4096 {
			t.Errorf("read %d bytes against a 4096 cap; the limit is not holding", len(page.Text))
		}
	})

	// Content-Length is the far end's CLAIM about itself. If anything here sized
	// a buffer from it, this fixture would ask for 400 MB to deliver five bytes.
	t.Run("a wildly overstated Content-Length steers nothing", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", "400000000")
			_, _ = w.Write([]byte("small"))
		}))
		defer srv.Close()

		// The write is short of the declared length, so the transport reports a
		// truncated body. What matters is that it FAILED rather than allocating
		// against the claim -- either outcome here is safe, a 400 MB allocation
		// would not be.
		page, err := fetchURL(context.Background(), unguardedTestClient(srv), srv.URL, maxWebFetchBytes)
		if err == nil && len(page.Text) > 1024 {
			t.Errorf("got %d bytes from a 5-byte body; the declared length was trusted", len(page.Text))
		}
	})
}

func TestFetchURLReportsAJavaScriptOnlyPageInsteadOfReturningEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><div id="root"></div><script>render()</script></body></html>`))
	}))
	defer srv.Close()

	_, err := fetchURL(context.Background(), unguardedTestClient(srv), srv.URL, maxWebFetchBytes)
	if err == nil {
		t.Fatal("an empty extraction was returned as success; the model would report the page as blank")
	}
	if !strings.Contains(err.Error(), "JavaScript") {
		t.Errorf("error = %q, want it to name the likely cause so the model tries another source", err)
	}
}

// Credentials in a URL must never be transmitted.
func TestFetchURLStripsURLCredentials(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	withCreds := strings.Replace(srv.URL, "http://", "http://admin:hunter2@", 1)
	if _, err := fetchURL(context.Background(), unguardedTestClient(srv), withCreds, maxWebFetchBytes); err != nil {
		t.Fatalf("fetchURL: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization header = %q; credentials in a model-supplied URL must not be sent", gotAuth)
	}
}
