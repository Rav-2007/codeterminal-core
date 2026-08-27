package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// The daemon's only outbound path that is not a model completion.
//
// EVERY OTHER BYTE THIS PRODUCT SENDS GOES TO ONE CONFIGURED PROVIDER. That
// single-destination property is most of the privacy story: toolresult.go can
// call itself "the egress choke point" because there was exactly one place to
// choke. A web tool ends that, and pretending otherwise would be the dishonest
// way to ship it. So this file is written as the second choke point, with the
// same discipline as the first:
//
//   - What goes OUT is scrubbed before it leaves (see scrubbedQuery). A model
//     that has just read a .env can put its contents in a search query, and
//     that is a cleaner exfiltration channel than anything the chunk path ever
//     had -- the request is made by us, to a host of the model's choosing,
//     carrying text of the model's choosing.
//   - What comes BACK is untrusted input, not information. It returns through
//     the ordinary tool-result path, so it inherits renderToolResult's scrub,
//     delimiter neutralisation, control stripping and byte cap for free, and it
//     is additionally fenced (see webContentEnvelope) because a fetched page is
//     the most hostile text that will ever enter this model's context.
//   - Where it can GO is bounded structurally, not by a deny-list of strings.
//
// WHAT THIS DOES NOT DO. It does not run JavaScript, so a page that renders
// entirely client-side returns close to nothing. That is a real limitation and
// it is reported to the model as one (see the "no readable text" result) rather
// than returned as an empty success the model would reason about as "the page
// was blank".

const (
	// maxWebFetchBytes bounds one response body BEFORE extraction. Extraction
	// only shrinks, and the turn's own cap applies afterwards, so this exists
	// to stop a hostile host streaming forever into our memory rather than to
	// bound egress.
	maxWebFetchBytes = 2 << 20 // 2 MiB

	// maxWebQueryChars bounds an outbound search query. A query is not a
	// document: anything past this is not a search, it is a payload.
	maxWebQueryChars = 512

	// defaultWebTimeout bounds one request end to end, including redirects.
	defaultWebTimeout = 15 * time.Second

	// maxWebRedirects bounds a redirect chain. Each hop is re-checked, so this
	// is a cost bound, not a safety one.
	maxWebRedirects = 5

	webUserAgent = "Mochiii/1.0 (+https://github.com/codeterminal; local coding assistant)"
)

// errBlockedAddress is returned when a request resolved to an address the
// daemon will not connect to. Deliberately one error for every private class:
// distinguishing "loopback" from "link-local" in the message would turn this
// dialler into a port scanner that reports its findings.
var errBlockedAddress = errors.New("that address is not reachable from here")

// blockedIP reports whether an address is one the agent must never reach.
//
// THIS IS THE WHOLE SSRF GATE AND IT IS DELIBERATELY AN ALLOW-BY-EXCLUSION LIST
// OF STRUCTURAL PROPERTIES, not a list of hostnames. "Refuse localhost,
// 127.0.0.1 and 169.254.169.254" is the version that gets written first and it
// is defeated by 127.1, by 0.0.0.0, by 2130706433, by ::ffff:127.0.0.1, by a
// CNAME, and by any DNS record the far end controls. net.IP answers the
// question the string form cannot.
//
// The classes and why each one is here:
//
//	loopback      the daemon's own socket, the user's other services
//	private       the user's LAN -- routers, NAS, printers, internal wikis
//	link-local    169.254.0.0/16, which is where every cloud metadata service
//	              lives, and where an agent goes to steal an instance's IAM
//	              credentials
//	unique-local  fc00::/7, the IPv6 twin of private
//	unspecified   0.0.0.0 and ::, which many stacks route to loopback
//	multicast     not a fetch target, and a way to touch many hosts at once
//	NAT64/IPv4-mapped are unwrapped first, so ::ffff:10.0.0.1 cannot launder a
//	private address through an IPv6 literal.
func blockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// An IPv4-mapped IPv6 address is an IPv4 address wearing a costume. Unwrap
	// before classifying, or every check below runs against the wrong family.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() ||
		// fc00::/7. IsPrivate covers it for Go >= 1.17, kept explicit because
		// this is the check whose absence is silent.
		(len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc)
}

// guardedDialContext is the SSRF gate, placed at CONNECT time rather than at
// parse time, and that placement is the entire point.
//
// The obvious implementation resolves the URL's host, checks the IPs, and then
// hands the URL to http.Client -- which resolves it AGAIN. Between those two
// resolutions the attacker's own DNS server is free to answer differently: the
// check sees 93.184.216.34, the dial gets 127.0.0.1. That is DNS rebinding, it
// needs no privileged position, and a TTL of 0 is all it costs.
//
// Dialer.Control runs after resolution and before the socket is connected, and
// its address argument is the ACTUAL ip:port about to be used. There is no
// second lookup for anything to change under. A hostile DNS answer simply gets
// refused at the moment it would have mattered.
func guardedDialContext() func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return errBlockedAddress
			}
			ip := net.ParseIP(host)
			// Control is called with a resolved literal. A non-literal here
			// means an assumption of this function no longer holds, so it
			// fails closed rather than passing the address through.
			if ip == nil || blockedIP(ip) {
				return errBlockedAddress
			}
			return nil
		},
	}
	return d.DialContext
}

// webClient builds the HTTP client used by both web tools.
//
// No cookie jar, and that is a decision rather than an omission: a jar would
// carry state from one fetched host to the next across a turn, which is how an
// agent that visited a logged-in page hands that session to the next page it
// reads.
func webClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultWebTimeout
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           guardedDialContext(),
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: timeout,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          8,
			IdleConnTimeout:       30 * time.Second,
			// No proxy from the environment. HTTP_PROXY is a way to route the
			// agent's traffic somewhere the user did not choose, set by
			// anything that can write the daemon's environment.
			Proxy: nil,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxWebRedirects {
				return fmt.Errorf("too many redirects")
			}
			// THE SCHEME IS RE-CHECKED ON EVERY HOP. The address gate lives in
			// the dialler and needs no help here, but the scheme gate does: an
			// https URL that 302s to file:// or gopher:// has changed what the
			// request means, and only this callback sees the new one.
			return checkWebScheme(req.URL)
		},
	}
}

// checkWebScheme refuses everything but http and https.
func checkWebScheme(u *url.URL) error {
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("only http and https URLs can be fetched")
	}
}

// fetchedPage is one retrieved document, already reduced to text.
type fetchedPage struct {
	URL   string
	Title string
	Text  string
}

// fetchURL retrieves one URL and reduces it to readable text.
//
// The returned error is written for the MODEL to read and act on, in the same
// register as toolError: it says what was refused so the model can try
// something else, and it never carries an internal address or a Go error
// string that would tell an attacker what the network looks like.
func fetchURL(ctx context.Context, client *http.Client, raw string, maxBytes int) (fetchedPage, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fetchedPage{}, fmt.Errorf("that is not a valid URL")
	}
	if err := checkWebScheme(u); err != nil {
		return fetchedPage{}, err
	}
	if u.Host == "" {
		return fetchedPage{}, fmt.Errorf("that URL has no host")
	}
	// Credentials in the URL are stripped rather than refused. A model that
	// pasted user:pass@host from a page it read would otherwise send them.
	u.User = nil

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fetchedPage{}, fmt.Errorf("that URL could not be requested")
	}
	req.Header.Set("User-Agent", webUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.1")
	req.Header.Set("Accept-Language", "en")

	resp, err := client.Do(req)
	if err != nil {
		// The dialler's refusal is reported as itself, so the model learns not
		// to retry a private address. Everything else collapses to one message:
		// timeouts, TLS failures and DNS failures are all "could not be
		// reached" from the model's point of view, and distinguishing them out
		// loud maps the user's network for whoever reads the transcript.
		if errors.Is(err, errBlockedAddress) {
			return fetchedPage{}, fmt.Errorf("that host resolves to a private or local address, which this tool will not fetch")
		}
		return fetchedPage{}, fmt.Errorf("that URL could not be reached")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return fetchedPage{}, fmt.Errorf("that URL returned HTTP %d", resp.StatusCode)
	}

	// CONTENT TYPE IS AN ALLOW-LIST. Without it the model can pull a binary
	// into its own context: a 2 MiB tarball becomes 2 MiB of mojibake that
	// costs the user real tokens and tells nobody anything.
	ctype := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if !readableContentType(ctype) {
		return fetchedPage{}, fmt.Errorf("that URL returned %s, which is not readable text", displayContentType(ctype))
	}

	if maxBytes <= 0 {
		maxBytes = maxWebFetchBytes
	}
	// LimitReader, not ContentLength. A Content-Length header is the server's
	// claim about itself and a hostile server can simply lie; the reader is the
	// bound that holds either way.
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)))
	if err != nil {
		return fetchedPage{}, fmt.Errorf("that URL could not be read")
	}

	title, text := extractReadableText(string(body), ctype)
	if strings.TrimSpace(text) == "" {
		// ANNOUNCED, NOT RETURNED EMPTY, for the same reason truncation is
		// announced: a model handed "" reasons about the page as though it were
		// blank, and says so to the user. The usual cause is a page that builds
		// itself in JavaScript, which this tool does not run, and the model can
		// act on that -- by trying another source.
		return fetchedPage{}, fmt.Errorf("that page had no readable text (it may render its content with JavaScript, which this tool does not run)")
	}

	// resp.Request.URL, not u: after redirects this is where the bytes actually
	// came from, which is the URL the model should cite.
	final := u.String()
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	return fetchedPage{URL: final, Title: title, Text: text}, nil
}

// readCapped reads at most n bytes. LimitReader rather than a ContentLength
// check for the same reason fetchURL uses one: the header is the far end's
// claim about itself.
func readCapped(r io.Reader, n int) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(n)))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func readableContentType(ctype string) bool {
	switch ctype {
	case "text/html", "application/xhtml+xml", "text/plain", "text/markdown",
		"application/json", "text/xml", "application/xml", "text/csv", "":
		// An empty type is allowed because a surprising number of plain-text
		// endpoints send none, and the extractor treats unknown input as text.
		return true
	}
	return false
}

func displayContentType(ctype string) string {
	if ctype == "" {
		return "an unknown content type"
	}
	return ctype
}
