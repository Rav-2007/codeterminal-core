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

const (
	// connectVerifyTimeout bounds the one live check. Short, because this is a
	// human waiting at a prompt, and an unreachable base is a legitimate answer
	// ("saved, unverified") rather than something to hang on.
	connectVerifyTimeout = 15 * time.Second

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
	apiBase := fs.String("api-base", "", "the provider base URL to store with the key (default: keep the stored one, else "+defaultAPIBase+")")
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

	// Precedence for the base: the flag, then what is already stored, then the
	// environment, then the built-in default. The key is what this command is
	// for; the base is a detail it should not make the user restate.
	base := firstNonEmpty(*apiBase, stored.APIBase, os.Getenv("MOCHIII_API_BASE"), defaultAPIBase)
	if err := validateAPIBase(base); err != nil {
		return err
	}

	key, err := readKey(*fromStdin)
	if err != nil {
		return err
	}
	if err := validateKeyShape(key); err != nil {
		return fmt.Errorf("%w; nothing was saved", err)
	}

	cred := storedCredential{APIBase: base, APIKey: key}

	if *noVerify {
		cred.Verified = false
		if err := saveCredential(path, cred); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(connectOut, "Saved %s for %s (%s).\n", maskKey(key), base, path)
		_, _ = fmt.Fprintln(connectOut, "NOT verified: --no-verify was given, so nothing has confirmed this key works.")
		return nil
	}

	_, _ = fmt.Fprintf(connectOut, "Checking the key with %s ...\n", base)
	ctx, cancel := context.WithTimeout(context.Background(), connectVerifyTimeout)
	defer cancel()
	result := verifyKey(ctx, http.DefaultClient, base, key)

	// A REFUSED KEY IS NOT SAVED. Storing it would replace a key that may be
	// working with one the provider has just said no to, and the user would find
	// out at their next prompt.
	if result.Outcome == verifyRejected {
		return fmt.Errorf("that key was refused: %s.\nNothing was saved%s", result.Detail,
			stillStoredNote(stored))
	}

	cred.Verified = result.proven()
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
	_, _ = fmt.Fprintln(connectOut, "Restart the daemon for it to take effect.")
	return nil
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
