package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GITHUB (protocol/github.go has what and why). This file is the one way the
// daemon talks to GitHub: one host, the token in a header and nowhere else,
// and an answer a person can act on when GitHub says no.
//
// WHY NOT A GITHUB MCP SERVER. One exists, and adding it is one command. It
// would run with the user's full access holding the token, offer the model
// dozens of tools against a budget of 24, upload file contents past the edit
// review, and put text anyone on the internet wrote in front of the model with
// no fence. Everything here is the same verbs without those four.
//
// EVERY URL IS BUILT HERE, from a repository name checked against GitHub's own
// rules and numbers the caller parsed. Nothing the model writes becomes a path
// or a host, so a read cannot carry text anywhere but GitHub's API -- the
// exfiltration web_fetch had (webegress.go) has no way in.

// githubAPIBase is where every call goes. A var so a test can point it at a
// stand-in; nothing else changes it.
var githubAPIBase = "https://api.github.com"

const (
	githubAPIVersion = "2022-11-28"
	githubTimeout    = 20 * time.Second
	// githubMaxBody bounds one JSON answer. A page of 100 issues with long
	// bodies is well under it.
	githubMaxBody = 4 << 20
	// githubMaxLog bounds a CI log read before the failure is cut out of it.
	githubMaxLog = 8 << 20
)

// githubState is what the daemon knows about GitHub, behind one lock.
// Embedded in serverExtras, because server.go must not grow (serverextras.go).
type githubState struct {
	mu sync.Mutex
	// loaded says cred is the file's content, read once and kept current by
	// every save and forget. Before that, cred is empty.
	loaded bool
	cred   githubCredential
}

// githubRepoPattern is a repository as "owner/name" by GitHub's rules: an
// owner of letters, digits and single hyphens, a name of letters, digits, '.',
// '-' and '_'. Checked before a name goes into any path.
var githubRepoPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)

// validGitHubRepo reports whether repo can be put in a URL path as it is.
func validGitHubRepo(repo string) bool {
	if !githubRepoPattern.MatchString(repo) {
		return false
	}
	name := repo[strings.IndexByte(repo, '/')+1:]
	return name != "." && name != ".."
}

// githubRepoPath is the API path for something in repo: "/repos/o/n" + rest.
func githubRepoPath(repo, rest string) string {
	return "/repos/" + repo + rest
}

// githubAPIError is GitHub saying no, in the words a user needs.
type githubAPIError struct {
	status int
	// message is GitHub's own explanation, one clean line.
	message string
	// resetAt is when the rate limit refills, when that is why.
	resetAt time.Time
}

func (e *githubAPIError) Error() string {
	why := e.message
	if why == "" {
		why = http.StatusText(e.status)
	}
	switch {
	case e.status == http.StatusUnauthorized:
		return "GitHub refused the token (401): it is wrong, expired or revoked. /github connect to enter a new one."
	case !e.resetAt.IsZero():
		return fmt.Sprintf("GitHub's limit on requests is used up until %s; try again then (%s).",
			e.resetAt.Local().Format("15:04"), why)
	case e.status == http.StatusForbidden:
		return fmt.Sprintf("GitHub would not let this token do that (403): %s", why)
	case e.status == http.StatusNotFound:
		return "GitHub found nothing there (404): it does not exist, or this token cannot see it."
	}
	return fmt.Sprintf("GitHub answered %d: %s", e.status, why)
}

// githubNoRedirect is the client for the API itself: a redirect is never
// followed, so a token only ever goes to githubAPIBase.
var githubNoRedirect = &http.Client{
	Timeout:       githubTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// githubCall makes one request with token and decodes a JSON answer into out
// (when out is not nil). The headers come back for what they say about the
// token (scopes, expiry) and the rate limit.
func githubCall(ctx context.Context, token, method, path string, body, out any) (http.Header, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, githubAPIBase+path, reader)
	if err != nil {
		return nil, err
	}
	githubHeaders(req, token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := githubNoRedirect.Do(req)
	if err != nil {
		return nil, githubTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, githubMaxBody+1))
	if err != nil {
		return resp.Header, fmt.Errorf("reading GitHub's answer: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.Header, githubErrorFrom(resp, data)
	}
	if len(data) > githubMaxBody {
		return resp.Header, fmt.Errorf("GitHub's answer was larger than %d MB; not read", githubMaxBody>>20)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.Header, fmt.Errorf("GitHub's answer was not what was expected: %v", err)
		}
	}
	return resp.Header, nil
}

// githubHeaders sets what every API request carries.
func githubHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("User-Agent", "mochiii")
}

// githubTransportError keeps a network failure's sense and drops its detail:
// a *url.Error prints the whole URL, which is harmless here but says nothing a
// user can act on that "could not reach GitHub" does not.
func githubTransportError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		if uerr.Timeout() {
			return fmt.Errorf("GitHub did not answer within %s", githubTimeout)
		}
		return fmt.Errorf("could not reach GitHub: %v", uerr.Err)
	}
	return fmt.Errorf("could not reach GitHub: %v", err)
}

// githubErrorFrom reads GitHub's refusal: its message, and the rate limit when
// that is the reason.
func githubErrorFrom(resp *http.Response, data []byte) error {
	var body struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &body)
	e := &githubAPIError{status: resp.StatusCode, message: clipRunes(oneLine(body.Message), 200)}
	limited := resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0")
	if limited {
		if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > 0 {
			e.resetAt = time.Unix(reset, 0)
		} else if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			e.resetAt = time.Now().Add(time.Duration(secs) * time.Second)
		} else {
			e.resetAt = time.Now().Add(time.Minute)
		}
	}
	return e
}

// githubLogURLAllowed says whether a CI log may be downloaded from u.
//
// GitHub answers a job's log request with a redirect to a short-lived signed
// URL on its storage, which is not api.github.com. That one redirect is
// followed, over https, to those hosts only, and WITHOUT the token: the
// signature in the URL is the whole authorisation, and the token must never
// reach a host that is not GitHub's API. A var so a test's stand-in host can
// be allowed.
var githubLogURLAllowed = func(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range []string{".actions.githubusercontent.com", ".blob.core.windows.net"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// githubJobLog downloads one Actions job's log (the job id is the check run's).
func githubJobLog(ctx context.Context, token, repo string, jobID int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		githubAPIBase+githubRepoPath(repo, fmt.Sprintf("/actions/jobs/%d/logs", jobID)), nil)
	if err != nil {
		return "", err
	}
	githubHeaders(req, token)
	resp, err := githubNoRedirect.Do(req)
	if err != nil {
		return "", githubTransportError(err)
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, githubMaxBody))
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return string(data), nil // some servers answer with the log itself
	case resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusTemporaryRedirect:
		return "", githubErrorFrom(resp, data)
	}
	where, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || where.Host == "" {
		return "", fmt.Errorf("GitHub sent the log somewhere this could not read")
	}
	if !githubLogURLAllowed(where) {
		return "", fmt.Errorf("GitHub sent the log to %s, which is not one of the hosts its logs come from; not followed",
			where.Hostname())
	}
	// A new request, not a followed redirect: it carries no header of the
	// first, the token above all.
	logReq, err := http.NewRequestWithContext(ctx, http.MethodGet, where.String(), nil)
	if err != nil {
		return "", err
	}
	logReq.Header.Set("User-Agent", "mochiii")
	logResp, err := githubNoRedirect.Do(logReq)
	if err != nil {
		return "", githubTransportError(err)
	}
	defer func() { _ = logResp.Body.Close() }()
	if logResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the log could not be downloaded (%d)", logResp.StatusCode)
	}
	log, err := io.ReadAll(io.LimitReader(logResp.Body, githubMaxLog))
	if err != nil {
		return "", fmt.Errorf("reading the log: %v", err)
	}
	return string(log), nil
}
