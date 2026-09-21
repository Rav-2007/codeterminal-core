package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Startup validation of the two settings that are NOT part of models.json:
// the model API base URL (an environment variable) and the grounding
// workspace (a flag). Both used to be accepted unexamined, and both failed
// later in ways that pointed away from the real cause.
//
// The split applied here, and the reasoning for each half:
//
//   - STRUCTURAL validity is checked, and a failure is fatal. A base URL that
//     cannot be parsed, or a workspace path that does not exist, is a typo. It
//     can never start working on its own, no request will ever succeed, and
//     the daemon knows this before it claims to be ready. Refusing to start
//     with a message naming the setting is strictly better than starting and
//     misattributing the fault per-request forever.
//
//   - REACHABILITY is deliberately NOT checked. Whether the provider answers
//     right now says nothing about whether the config is correct: a laptop is
//     offline, a provider has a bad ten minutes. Probing at startup would make
//     a local-first daemon refuse to start when the network is down — the
//     wrong trade for a tool whose non-inference features (retrieval, apply,
//     undo, search) work fine offline — and would add a network round-trip to
//     every start. Reachability is a RUNTIME state, and it is reported as one:
//     the per-request path already classifies it (ClassUpstreamUnavailable),
//     and the status surface reports the most recent outcome.
//
// The distinction is the point. "This will never work" is a startup error;
// "this is not working at the moment" is a degraded state.

// defaultAPIBase is where the daemon talks when nobody has said otherwise.
//
// THE THIRD CATEGORY THIS FILE DID NOT HAVE. The split above is structural
// error versus runtime state, and an ABSENT base is neither: it is "not
// configured yet". It was treated as the first -- logger.Fatal, exit 1 -- and
// that made the VS Code extension dead on arrival for 72 days, because
// spawnDaemon inherits an extension host's environment and a VS Code started
// from a desktop icon carries no shell exports. Every test supplied the
// variable; no install did.
//
// The value is not invented here: proxy/main.go has carried exactly this
// constant as defaultUpstreamBase since it was written. The daemon lacking the
// default its own proxy already had IS the defect, stated in one line.
//
// A default endpoint is not a default credential. With no key the daemon starts,
// serves retrieval, apply, undo and search -- which this file's own reasoning
// says work fine offline -- and reports the provider outcome per request, which
// is the degraded state the split above is for. That is strictly better than
// refusing to start, which told the user only "exited 1 5 times".
const defaultAPIBase = "https://openrouter.ai/api/v1"

// validateAPIBase checks that base is a usable absolute HTTP(S) endpoint.
//
// It exists because a malformed base was previously indistinguishable, from
// the client's seat, from a provider outage: `:::not a url` produced three
// retries with backoff and then "the model provider is unreachable or failing
// right now — this is usually temporary". It was not temporary. Nothing about
// that request could ever have succeeded, and the one component that could
// have said so — the daemon, at startup, holding the actual string — said
// "listening".
func validateAPIBase(base string) error {
	trimmed := strings.TrimSpace(base)
	if trimmed == "" {
		// Distinguished from the caller's own unset check: a whitespace-only
		// value passes a `== ""` test but is no more usable than an empty one.
		return fmt.Errorf("MOCHIII_API_BASE is blank")
	}
	if trimmed != base {
		return fmt.Errorf("MOCHIII_API_BASE %q has leading or trailing whitespace", base)
	}

	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("MOCHIII_API_BASE %q is not a valid URL: %w", base, err)
	}
	switch u.Scheme {
	case "http", "https":
	case "":
		return fmt.Errorf("MOCHIII_API_BASE %q has no scheme (expected an absolute URL such as https://openrouter.ai/api/v1)", base)
	default:
		return fmt.Errorf("MOCHIII_API_BASE %q uses unsupported scheme %q (expected http or https)", base, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("MOCHIII_API_BASE %q has no host", base)
	}
	return nil
}

// validateWorkspace checks that the grounding workspace exists and is a
// directory, returning its absolute form.
//
// A path that doesn't exist previously started fine and then reported, on
// every prompt, "no index has been built for this workspace yet (run `index`
// to enable grounded answers)" — advice that cannot work, since indexing a
// nonexistent directory fails too. The same wrong diagnosis appeared when
// --workspace named a regular file. Both are startup-time typos and are
// refused here, where the message can name the actual problem.
//
// Note that a workspace with no index is NOT an error and never reaches this
// function's failure paths: that is a legitimate, well-reported degraded state
// (setupRetrieval's reasonNoIndex), and the daemon serves ungrounded prompts
// perfectly well in it. What's refused is only a path that is not a workspace
// at all.
func validateWorkspace(workspace string) (string, error) {
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("--workspace %q could not be resolved to an absolute path: %w", workspace, err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("--workspace %s does not exist", abs)
		}
		return "", fmt.Errorf("--workspace %s could not be read: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("--workspace %s is not a directory", abs)
	}
	return abs, nil
}
