package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"mochiii/protocol"
)

// THE LAUNCH CREDENTIAL: a key handed to the daemon on stdin by whatever started
// it, instead of through its environment.
//
// WHY NOT THE ENVIRONMENT. A variable a process is started with stays readable in
// /proc/<pid>/environ for that process's whole life, by anything running as the
// same user -- and os.Unsetenv does not help: on Linux that file is the original
// environment block, which the runtime never rewrites. The VS Code extension
// used to start the daemon with its SecretStorage key as MOCHIII_API_KEY, so a
// key the user had put in the OS keychain sat in plain text in procfs from the
// first second the daemon ran. The only fix is never to put it there, so the
// extension now writes one JSON line to the daemon's stdin and closes it, and
// starts the daemon with --credentials-from-stdin and a child environment that
// carries no key at all.
//
// WHERE IT SITS. Exactly where the stored credential (~/.mochiii/credentials.json)
// sits: below MOCHIII_API_KEY in the environment, which still wins every time --
// a deployment that exports one must keep behaving as it did. When the launcher
// supplies a key it IS this process's stored credential and the file is not
// consulted; when it supplies none ("{}"), the file is used exactly as before.
// See resolveLaunchSlot.
//
// WHAT IT IS NOT. A live-update channel. It is read once, before anything else
// the daemon does, and stdin is never read again. Changing the key of a running
// daemon is /connect over the authenticated socket, or a restart.

const (
	// credentialsFromStdinFlag is the flag that turns the read on. Without it the
	// daemon never touches stdin, so nothing changes for a terminal user.
	credentialsFromStdinFlag = "credentials-from-stdin"

	// launchCredentialMaxBytes caps the line. A key is under a kilobyte and a
	// base under a few hundred bytes; 16 KiB is generous for both and still too
	// small to be a way of making the daemon buffer something large.
	launchCredentialMaxBytes = 16 << 10

	// launchCredentialTimeout bounds the wait. The launcher writes the line
	// immediately after spawning, so it is in the pipe before the runtime has
	// finished starting; the timeout exists for a launcher that died or forgot,
	// and turns that into a clear exit rather than a daemon hung at startup.
	launchCredentialTimeout = 5 * time.Second
)

// launchCredential is the line's shape. Every field is optional; "{}" is a
// launcher saying it has nothing to hand over.
//
// ProxyKey is MOCHIII_PROXY_KEY's channel, and it is a field of its own for the
// same reason main.go keeps the two environment variables apart: a provider key
// must never reach the proxy, and a proxy key must never reach a provider. One
// field for both would erase that boundary at the first mixed configuration.
type launchCredential struct {
	APIKey   string `json:"api_key"`
	APIBase  string `json:"api_base"`
	Provider string `json:"provider"`
	ProxyKey string `json:"proxy_key"`
}

// secrets returns the credential values the line carried, for the literal
// scrubber's registry (credscrub_apply.go). A key that arrived but lost to
// MOCHIII_API_KEY is still registered: it was in this process's memory.
func (c launchCredential) secrets() []string {
	var out []string
	for _, v := range []string{c.APIKey, c.ProxyKey} {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// launchKeySupplied records that the launcher handed this daemon a provider key
// on stdin. Set once in main, before the daemon serves anything; read by
// launcherKeyWins. An atomic so a test that sets it cannot race a reader.
var launchKeySupplied atomic.Bool

// readLaunchCredential reads exactly one line from r and parses it, or fails.
// It never reads past the cap, and gives up after timeout. FAIL CLOSED: every
// error is fatal at the call site, because a daemon that guessed its way past a
// garbled credential would start with no key, or the wrong one, and say
// "listening".
//
// ERRORS NEVER QUOTE THE INPUT. The line holds a key; an error that echoed it
// would put the key in the daemon log, which is tee'd to a file.
func readLaunchCredential(r io.Reader, timeout time.Duration) (launchCredential, error) {
	type result struct {
		line []byte
		err  error
	}
	got := make(chan result, 1)
	go func() {
		line, err := readOneLine(r, launchCredentialMaxBytes)
		got <- result{line, err}
	}()
	select {
	case res := <-got:
		if res.err != nil {
			return launchCredential{}, res.err
		}
		return parseLaunchCredential(res.line)
	case <-time.After(timeout):
		// The reader goroutine stays blocked on stdin; the caller exits.
		return launchCredential{}, fmt.Errorf("no credentials line arrived on stdin within %s "+
			"(the process that started this daemon passed --%s and then wrote nothing)",
			timeout, credentialsFromStdinFlag)
	}
}

// readOneLine reads up to the first newline, or to EOF, refusing a line longer
// than max. The LimitReader is what makes "never reads past the cap" true: the
// buffered reader cannot pull more than max+1 bytes off the pipe however long
// the input is.
func readOneLine(r io.Reader, max int) ([]byte, error) {
	br := bufio.NewReader(io.LimitReader(r, int64(max)+1))
	line, err := br.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reading credentials from stdin: %w", err)
	}
	line = bytes.TrimRight(line, "\r\n")
	if len(line) > max {
		return nil, fmt.Errorf("the credentials line on stdin is longer than %d bytes", max)
	}
	if len(bytes.TrimSpace(line)) == 0 {
		return nil, errors.New("stdin closed without a credentials line (expected one JSON object, \"{}\" for none)")
	}
	return line, nil
}

// parseLaunchCredential decodes and validates one line. Strict on purpose: an
// unknown field is refused rather than ignored, because the likeliest unknown
// field is a misspelt "api_key" -- and ignoring it would start the daemon
// keyless, which reads to the user as the provider refusing a key they gave.
func parseLaunchCredential(line []byte) (launchCredential, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return launchCredential{}, errors.New("the credentials line on stdin is not a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var c launchCredential
	if err := dec.Decode(&c); err != nil {
		return launchCredential{}, fmt.Errorf("the credentials line on stdin is not valid: %s", describeJSONError(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return launchCredential{}, errors.New("the credentials line on stdin has data after the JSON object")
	}

	c.APIKey = strings.TrimSpace(c.APIKey)
	c.ProxyKey = strings.TrimSpace(c.ProxyKey)
	c.APIBase = strings.TrimSpace(c.APIBase)
	c.Provider = strings.TrimSpace(c.Provider)

	for name, v := range map[string]string{"api_key": c.APIKey, "proxy_key": c.ProxyKey} {
		if strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			// It goes into an Authorization header; a control character there is
			// a malformed request at best and a header injection at worst.
			return launchCredential{}, fmt.Errorf("%s on stdin contains whitespace or a control character", name)
		}
	}

	if c.Provider != "" {
		p, ok := protocol.ProviderByName(c.Provider)
		if !ok {
			// Not echoed: a key pasted into the wrong field would land in the log.
			return launchCredential{}, fmt.Errorf("provider on stdin is not one this daemon knows (known: %s)", knownProviderIDs())
		}
		switch {
		case c.APIBase == "":
			c.APIBase = p.APIBase
		case !sameAPIBase(c.APIBase, p.APIBase):
			// Two statements of where the key goes that disagree. Picking either
			// could send the key somewhere it was not issued for.
			return launchCredential{}, fmt.Errorf("api_base and provider on stdin disagree: %s is not %s's address (%s)",
				c.APIBase, p.Name, p.APIBase)
		}
	}
	if c.APIBase != "" {
		if err := validateBaseNamed("api_base on stdin", c.APIBase); err != nil {
			return launchCredential{}, err
		}
	}
	return c, nil
}

// describeJSONError names what was wrong with the line without quoting it.
// encoding/json's syntax errors carry only an offset and the offending byte,
// and its type errors only the field name, so they are safe to pass through --
// but they are rebuilt here so that stays true if the library ever changes.
func describeJSONError(err error) string {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		return fmt.Sprintf("malformed JSON at byte %d", syn.Offset)
	case errors.As(err, &typ):
		return fmt.Sprintf("field %q must be a string", typ.Field)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		// The field NAME, never a value.
		return strings.TrimPrefix(err.Error(), "json: ")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "the JSON object is cut off"
	default:
		return "malformed JSON"
	}
}

func knownProviderIDs() string {
	var ids []string
	for _, p := range protocol.Providers() {
		ids = append(ids, p.ID)
	}
	return strings.Join(ids, ", ")
}

// resolveLaunchSlot fills the stored-credential slot from the launch credential.
// envKey and envBase are what the environment said; the result is the key and
// base the daemon starts with, and whether the launch key is the one in force.
//
// The base: the environment's if it named one, else the line's.
//
// The key: the environment's if it named one -- MOCHIII_API_KEY always wins --
// else the line's. A line key that names no api_base is used with whatever base
// is in force, exactly as MOCHIII_API_KEY is; one that names an api_base (or a
// provider) goes only to that address, the same rule the stored credential
// follows (storedCredential.issuedFor), so a launcher cannot have its key sent
// to a base the environment chose without it.
func resolveLaunchSlot(envKey, envBase string, launch launchCredential, logf func(string, ...any)) (apiKey, apiBase string, inForce bool) {
	apiKey, apiBase = envKey, envBase
	if apiBase == "" {
		apiBase = launch.APIBase
	}
	if launch.APIKey == "" {
		return apiKey, apiBase, false
	}
	if apiKey != "" {
		logf("MOCHIII_API_KEY in the environment outranks the key supplied on stdin; the stdin key is not used")
		return apiKey, apiBase, false
	}
	if launch.APIBase != "" && !sameAPIBase(launch.APIBase, firstNonEmpty(apiBase, defaultAPIBase)) {
		logf("not using the key supplied on stdin: it is for %s, and MOCHIII_API_BASE is %s", launch.APIBase, apiBase)
		return apiKey, apiBase, false
	}
	// Masked, always -- the log is tee'd to --log-file.
	logf("using the key supplied on stdin by the process that started this daemon (%s); MOCHIII_API_KEY in the environment would override it",
		maskKey(launch.APIKey))
	return launch.APIKey, apiBase, true
}
