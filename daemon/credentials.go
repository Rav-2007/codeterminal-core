package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// THE STORED PROVIDER CREDENTIAL: what `mochiii-daemon connect` writes, and what
// startup reads when the environment does not carry a key.
//
// WHY A FILE OF ITS OWN, AND NOT models.json. models.json is configuration:
// people copy it between machines, commit it, and paste it into bug reports. A
// key does not belong in anything with that life. This is a separate file, 0600,
// in the same per-user ~/.mochiii directory the model cache already uses.
//
// WHY A FILE AND NOT THE OS KEYRING. The keyring is better at rest, and it costs
// a cgo dependency per platform plus a headless-machine story (no keyring on a
// build agent or over ssh) -- and this daemon's own threat model already treats
// the user's home as trusted: the sandbox grants the workspace and the command's
// cache and nothing else precisely because ~/.ssh and ~/.aws live in the home and
// are read by everything the user runs. A 0600 file sits exactly where those do.
// If that changes, this is the one file to move.

// storedCredential is the on-disk form. It is deliberately small: a base, a key,
// and enough provenance to answer "where did this come from and was it ever
// checked?" without holding anything else about the user.
type storedCredential struct {
	APIBase string `json:"api_base"`
	APIKey  string `json:"api_key"`
	SavedAt string `json:"saved_at"`

	// Verified records whether the provider ACCEPTED this key when it was saved,
	// so `connect --show` can never imply a check that did not happen. False for
	// a --no-verify save and for one the provider could not answer.
	Verified bool `json:"verified"`
}

// configured reports whether there is a key here at all.
func (c storedCredential) configured() bool { return strings.TrimSpace(c.APIKey) != "" }

// credentialsPath is where the credential lives. It follows the same per-user
// directory as the model cache (defaultModelCacheDir), so a user has one Mochiii
// directory rather than two.
//
// A var, like lookPath and the sandbox probes, so a test can point it at a temp
// directory rather than at the developer's own real credential.
var credentialsPath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".mochiii", "credentials.json"), nil
}

// loadCredential reads the stored credential.
//
// A MISSING FILE IS NOT AN ERROR. "Nobody has run connect" is an ordinary state,
// not a failure, and returning an error for it would make every startup on a
// key-in-the-environment deployment log a problem it does not have.
//
// warn is non-empty when the file is readable by someone other than its owner.
// That is reported rather than silently repaired, because a mode that wide means
// the key may already have been read, and quietly chmod-ing it back would hide
// that. Not checked on Windows, where Go synthesises a mode that says nothing
// about the real ACL and would warn on every read.
func loadCredential(path string) (cred storedCredential, warn string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return storedCredential{}, "", nil
		}
		return storedCredential{}, "", err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		warn = fmt.Sprintf("%s is mode %#o: readable by more than its owner, so the key in it may already "+
			"have been read. Run `mochiii-daemon connect --forget`, rotate the key at your provider, and "+
			"connect again", path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return storedCredential{}, warn, err
	}
	if err := json.Unmarshal(data, &cred); err != nil {
		return storedCredential{}, warn, fmt.Errorf("%s is not readable as a credential (%w); "+
			"run `mochiii-daemon connect` to write it again", path, err)
	}
	return cred, warn, nil
}

// saveCredential writes the credential 0600, atomically.
//
// ATOMICALLY BECAUSE THE FAILURE IS SILENT OTHERWISE: a write truncated by a full
// disk or a signal leaves a file that parses as nothing, and the next startup
// reports a provider outage rather than a broken credential. Temp-then-rename
// means the file is either the old one or the new one.
//
// The temp file is created 0600 from the start, never widened and narrowed, so
// the key is not briefly world-readable on a machine somebody else is on.
func saveCredential(path string, cred storedCredential) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	cred.SavedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(cred, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	// Synced before the rename: a rename that lands before the data does leaves a
	// present, empty credential, which is the one outcome this whole function is
	// arranged to prevent.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("syncing %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("closing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

// forgetCredential removes the stored credential. Removing one that is not there
// succeeds: `connect --forget` means "there must be no stored key afterwards",
// and that is already true.
func forgetCredential(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// maskKey renders a key for a human to RECOGNISE but not to use.
//
// The last four characters are the conventional way to answer "which key is this?"
// and are not enough to reconstruct one. A key too short for that reveals nothing
// but its length -- a short string is closer to guessable, so it gets less, not
// more. Everything that prints a key anywhere goes through here.
func maskKey(key string) string {
	key = strings.TrimSpace(key)
	switch {
	case key == "":
		return "(none)"
	case len(key) < 12:
		return fmt.Sprintf("(%d characters, too short to show safely)", len(key))
	default:
		return fmt.Sprintf("...%s (%d characters)", key[len(key)-4:], len(key))
	}
}

// validateKeyShape rejects a key that cannot work as an HTTP header value before
// it is stored or sent.
//
// A pasted key with a newline in the middle is the common case, and it is worth
// catching here: Go refuses such a header at request time, so the failure would
// otherwise surface much later as an unexplained provider error, from a key the
// user watched `connect` accept.
func validateKeyShape(key string) error {
	if key == "" {
		return fmt.Errorf("no key given")
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("the key contains a control character, so it cannot be sent as an HTTP " +
				"header -- it was probably pasted with a line break in it")
		}
		if r == ' ' || r == '\t' {
			return fmt.Errorf("the key contains a space, which no provider key has -- it was probably " +
				"pasted with surrounding text")
		}
	}
	return nil
}
