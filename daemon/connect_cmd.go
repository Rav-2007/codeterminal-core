package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"mochiii/protocol"
)

// `mochiii-daemon connect` -- accept the user's provider API key, prove it, and
// store it, so inference works without anyone exporting an environment variable.
//
// It is a one-shot subcommand and needs NO RUNNING DAEMON: it writes a file the
// next startup reads. That is deliberate -- the first thing a new user does is
// supply a key, which is exactly when there is no daemon up yet to ask.
//
// SCOPE: the direct provider key only (the one that becomes `Authorization:
// Bearer` for inference). Proxy mode keeps its environment variables, because the
// two credentials are NOT interchangeable -- a proxy key sent to the provider is a
// credential going to a host it was not issued for, and main.go's existing fatal
// guard is there for that mistake. Mixing them here would be the same bug with a
// friendlier interface.

// connectVerifyTimeout bounds the one live check, here and for /connect over the
// socket. Short, because this is a human waiting at a prompt, and an unreachable
// base is a legitimate answer ("saved, unverified") rather than something to
// hang on. A var so a test can shorten it.
var connectVerifyTimeout = 15 * time.Second

const (
	// maxKeyBytes bounds what is read as a key. Provider keys are under a hundred
	// characters; this is far above any of them and far below a size that lets a
	// misdirected pipe (a log file, a tarball) be read into memory as a "key".
	maxKeyBytes = 8 << 10
)

// connectOut is where this command's report goes. A var so a test can read what
// the user would have seen -- in particular, that it never contains the key.
var connectOut io.Writer = os.Stdout

func runConnectCommand(args []string, logger *log.Logger) error {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	apiBase := fs.String("api-base", "", "the provider base URL the key is for (default: the provider the key's own prefix names, else the stored one, else "+defaultAPIBase+")")
	providerFlag := fs.String("provider", "", "the provider the key is for, by name ("+providerIDs()+"); the same as --api-base with that provider's address")
	fromStdin := fs.Bool("stdin", false, "read the key from stdin instead of prompting (implied when stdin is not a terminal)")
	noVerify := fs.Bool("no-verify", false, "store the key without checking it with the provider; the stored record says it is unverified")
	show := fs.Bool("show", false, "print what is stored -- never the key itself -- and exit")
	forget := fs.Bool("forget", false, "delete the stored key and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// A KEY MUST NOT ARRIVE AS AN ARGUMENT. In argv it is visible to every process
	// on the machine through ps, and it lands in the shell history file. Refusing
	// is better than accepting-with-a-warning, because by the time the warning is
	// read the key has already leaked and the only fix is to rotate it.
	if fs.NArg() > 0 {
		return errors.New("connect takes no positional arguments, and deliberately will not accept a key as one: " +
			"a key in the command line is visible to other processes via ps and is written to your shell history. " +
			"Run `mochiii-daemon connect` and paste it at the prompt, or pipe it: " +
			"`printf %s \"$KEY\" | mochiii-daemon connect --stdin`")
	}

	path, err := credentialsPath()
	if err != nil {
		return err
	}

	switch {
	case *show && *forget:
		return fmt.Errorf("--show and --forget ask for different things; run one at a time")
	case *show:
		return showCredential(path)
	case *forget:
		removed, err := forgetCredential(path)
		if err != nil {
			return fmt.Errorf("removing %s: %w", path, err)
		}
		if removed {
			_, _ = fmt.Fprintf(connectOut, "Stored key removed (%s).\n", path)
		} else {
			_, _ = fmt.Fprintf(connectOut, "No key was stored (%s), so there was nothing to remove.\n", path)
		}
		_, _ = fmt.Fprintln(connectOut, "Inference will use MOCHIII_API_KEY from the environment, if it is set.")
		return nil
	}

	stored, warn, err := loadCredential(path)
	if err != nil {
		// A credential file that cannot be read must not stop the command whose
		// whole job is to replace it.
		logger.Printf("warning: %v", err)
	}
	if warn != "" {
		logger.Printf("warning: %s", warn)
	}

	explicit := *apiBase
	if name := strings.TrimSpace(*providerFlag); name != "" {
		if explicit != "" {
			return fmt.Errorf("--provider and --api-base both say where the key goes; give one")
		}
		p, ok := protocol.ProviderByName(name)
		if !ok {
			return fmt.Errorf("unknown provider %q; the names are: %s (or give --api-base)", name, providerIDs())
		}
		explicit = p.APIBase
	}

	// The key is read BEFORE the address is decided, because the key is what
	// says whose it is (resolveConnectBase).
	key, err := readKey(*fromStdin)
	if err != nil {
		return err
	}
	if err := validateKeyShape(key); err != nil {
		return fmt.Errorf("%w; nothing was saved", err)
	}

	// Precedence for the base: the flag, then the provider the key's prefix
	// names, then what is already stored, then the environment, then the
	// built-in default. The key is what this command is for; the base is a
	// detail it should not make the user restate.
	target := resolveConnectBase(explicit, key, "", stored, os.Getenv("MOCHIII_API_BASE"))
	if len(target.Candidates) > 0 {
		names := make([]string, 0, len(target.Candidates))
		for _, p := range target.Candidates {
			names = append(names, p.ID)
		}
		return fmt.Errorf("keys that start with \"sk-\" are issued by several providers, and nothing in this one says which.\n"+
			"It was sent nowhere and nothing was saved. Run it again with --provider and one of: %s", strings.Join(names, ", "))
	}
	base := target.Base
	if err := validateConnectBase(base); err != nil {
		return err
	}

	cred := storedCredential{APIBase: base, APIKey: key}

	if *noVerify {
		cred.Verified = false
		if err := saveCredential(path, cred); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(connectOut, "Saved %s for %s (%s).\n", maskKey(key), base, path)
		_, _ = fmt.Fprintln(connectOut, "NOT verified: --no-verify was given, so nothing has confirmed this key works.")
		if !usesRoutingDialect(base) {
			_, _ = fmt.Fprintf(connectOut, "No model was chosen either: %s serves its own models, and finding one that answers is part of the check.\n", providerName(base))
		}
		return nil
	}

	_, _ = fmt.Fprintf(connectOut, "Checking the key with %s ...\n", base)
	timeout := connectVerifyTimeout
	if !usesRoutingDialect(base) {
		timeout = providerSetupTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	setup := setUpProvider(ctx, http.DefaultClient, base, key, logger.Printf)
	result := setup.Check

	// A REFUSED KEY IS NOT SAVED. Storing it would replace a key that may be
	// working with one the provider has just said no to, and the user would find
	// out at their next prompt.
	if result.Outcome == verifyRejected {
		return fmt.Errorf("that key was refused by %s: %s.\nNothing was saved%s", providerName(base), result.Detail,
			stillStoredNote(stored))
	}

	cred.Verified = result.proven()
	cred.DefaultModel, cred.Models, cred.QuietModels = setup.DefaultModel, setup.Models, setup.Quiet
	if err := saveCredential(path, cred); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(connectOut, "Saved %s for %s (%s).\n", maskKey(key), base, path)
	switch result.Outcome {
	case verifyAccepted:
		_, _ = fmt.Fprintf(connectOut, "Verified: %s.\n", result.Detail)
	case verifyInconclusive:
		_, _ = fmt.Fprintf(connectOut, "Saved, but NOT verified: %s.\n", result.Detail)
	case verifyUnreachable:
		_, _ = fmt.Fprintf(connectOut, "Saved, but NOT verified: %s.\n", result.Detail)
		_, _ = fmt.Fprintln(connectOut, "The key may well be correct; this machine could not reach the provider to find out.")
	}
	if setup.DefaultModel != "" {
		_, _ = fmt.Fprintf(connectOut, "Model: %s (%d available; /model lists them).\n", setup.DefaultModel, len(setup.Models))
	}
	for _, note := range connectNotes(base, setup, false) {
		_, _ = fmt.Fprintf(connectOut, "NOTE: %s\n", note)
	}
	_, _ = fmt.Fprintln(connectOut, "Restart the daemon for it to take effect.")
	return nil
}

// providerIDs lists the provider names --provider and /connect accept.
func providerIDs() string {
	var ids []string
	for _, p := range protocol.Providers() {
		ids = append(ids, p.ID)
	}
	return strings.Join(ids, ", ")
}

// showCredential prints what is stored, and never the key.
func showCredential(path string) error {
	cred, warn, err := loadCredential(path)
	if err != nil {
		return err
	}
	if !cred.configured() {
		_, _ = fmt.Fprintf(connectOut, "No key is stored (%s).\n", path)
		if env := os.Getenv("MOCHIII_API_KEY"); env != "" {
			_, _ = fmt.Fprintf(connectOut, "MOCHIII_API_KEY is set in this environment (%s), and takes precedence over a stored key.\n",
				maskKey(env))
		} else {
			_, _ = fmt.Fprintln(connectOut, "MOCHIII_API_KEY is not set either, so requests would be sent with no Authorization header.")
		}
		return nil
	}
	_, _ = fmt.Fprintf(connectOut, "Stored key:  %s\n", maskKey(cred.APIKey))
	_, _ = fmt.Fprintf(connectOut, "API base:    %s\n", cred.APIBase)
	_, _ = fmt.Fprintf(connectOut, "Saved at:    %s\n", cred.SavedAt)
	if cred.Verified {
		_, _ = fmt.Fprintln(connectOut, "Verified:    yes -- the provider accepted this key when it was saved")
	} else {
		_, _ = fmt.Fprintln(connectOut, "Verified:    NO -- nothing has confirmed this key works")
	}
	if cred.DefaultModel != "" {
		_, _ = fmt.Fprintf(connectOut, "Model:       %s (%d listed by the provider when the key was saved)\n", cred.DefaultModel, len(cred.Models))
	}
	_, _ = fmt.Fprintf(connectOut, "File:        %s\n", path)
	// Said here too, because a key in the environment silently wins and that is
	// otherwise invisible: someone debugging "I connected but it uses the wrong
	// key" needs to be told which one is actually in force.
	if env := os.Getenv("MOCHIII_API_KEY"); env != "" {
		_, _ = fmt.Fprintf(connectOut, "\nNOTE: MOCHIII_API_KEY is also set in this environment (%s). The environment takes "+
			"precedence, so the stored key above is NOT the one in use here.\n", maskKey(env))
	}
	if warn != "" {
		_, _ = fmt.Fprintf(connectOut, "\nWARNING: %s\n", warn)
	}
	return nil
}

// stillStoredNote says what survives a refusal, so "nothing was saved" cannot be
// misread as "there is now no key".
func stillStoredNote(stored storedCredential) string {
	if stored.configured() {
		return fmt.Sprintf(", and the previously stored key (%s) is untouched.", maskKey(stored.APIKey))
	}
	return ", and no key is stored."
}

// readKey gets the key from stdin, or from the terminal with echo off.
//
// Piped input is taken as-is (one trimmed line or blob), which is what a script
// or a password manager does. At a terminal the key is read with echo disabled so
// it is not left in the scrollback -- and if echo cannot be disabled, that is said
// out loud rather than quietly echoing a secret.
func readKey(forceStdin bool) (string, error) {
	if forceStdin || !stdinIsTerminal() {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, maxKeyBytes))
		if err != nil {
			return "", fmt.Errorf("reading the key from stdin: %w", err)
		}
		if len(data) == 0 {
			return "", fmt.Errorf("no key on stdin")
		}
		return strings.TrimSpace(string(data)), nil
	}

	fmt.Print("Paste your provider API key (it will not be shown): ")
	var line string
	var readErr error
	echoOff := withoutEcho(func() {
		r := bufio.NewReader(io.LimitReader(os.Stdin, maxKeyBytes))
		line, readErr = r.ReadString('\n')
		if readErr == io.EOF && line != "" {
			readErr = nil // a key with no trailing newline is still a key
		}
	})
	_, _ = fmt.Fprintln(connectOut)
	if !echoOff {
		_, _ = fmt.Fprintln(connectOut, "WARNING: terminal echo could not be turned off, so the key above was visible as you "+
			"typed it. Clear your scrollback, and consider rotating the key if the session was recorded.")
	}
	if readErr != nil {
		return "", fmt.Errorf("reading the key: %w", readErr)
	}
	return strings.TrimSpace(line), nil
}

// firstNonEmpty returns the first value that is not empty after trimming.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
