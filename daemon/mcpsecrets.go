package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// MCP SERVER KEYS AT REST: ~/.mochiii/mcp-secrets.json.
//
// The values a user gives for the variables their servers read (perplexity's
// PERPLEXITY_API_KEY) used to live in this daemon's memory only, handed over by
// VS Code from its secret store (mcpenv.go). So a key set in VS Code was
// unknown to the terminal client and the CLI, and every daemon restart needed
// the editor to hand it over again. The user chose (2026-10-14) to keep them
// where the model key already is: a file in ~/.mochiii that only its owner can
// read, the same protection credentials.json has.
//
// WHAT THE FILE HOLDS, AND WHAT IT IS NOT. Values, never programs: which program
// runs is mcp-servers.json's business and needs the typed acknowledgement; this
// file can only make a server that was already agreed to start with its key.
// It is still refused when others can write it (as mcp-servers.json is): a
// value somebody else can choose is a value an approved server trusts.
//
// SEVERAL WRITERS. One daemon runs per workspace, and the CLI writes the file
// too. Every change takes an exclusive lock on mcp-secrets.json.lock, reads the
// file again, changes it and writes it (writePrivateFile: 0600, atomic), so two
// writers never lose each other's keys. Readers notice a change by the file's
// modification time, size and identity, and read it again.
//
// Every value is a credential: all of them join the scrubber
// (credentialValuesLocked), so none can reach the model or leave in a web call.

const (
	mcpSecretsFileName = "mcp-secrets.json"
	mcpSecretsVersion  = 1
)

// mcpSecretsFile is the on-disk form.
type mcpSecretsFile struct {
	Version int `json:"version"`
	// Vars are values for any server that reads the variable: what a variable
	// means in a shell, and what set_env without a server name sets.
	Vars map[string]string `json:"vars,omitempty"`
	// Servers are values for one server only, which win over Vars -- what
	// `mcp add --env KEY=value` means.
	Servers map[string]mcpServerSecrets `json:"servers,omitempty"`
}

// mcpServerSecrets is one server's own values.
type mcpServerSecrets struct {
	Env map[string]string `json:"env,omitempty"`
	// Headers are a remote server's header values (a token): what its block's
	// "headers" leaves as "".
	Headers map[string]string `json:"headers,omitempty"`
	// Kept as written, so a daemon that does not know a field yet (a remote
	// server's headers, its sign-in) never drops what a newer one stored.
	Extra map[string]json.RawMessage `json:"-"`
}

func (m mcpServerSecrets) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	for k, v := range m.Extra {
		out[k] = v
	}
	if len(m.Env) > 0 {
		out["env"] = m.Env
	}
	if len(m.Headers) > 0 {
		out["headers"] = m.Headers
	}
	return json.Marshal(out)
}

func (m *mcpServerSecrets) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if env, ok := raw["env"]; ok {
		if err := json.Unmarshal(env, &m.Env); err != nil {
			return fmt.Errorf("env: %w", err)
		}
		delete(raw, "env")
	}
	if h, ok := raw["headers"]; ok {
		if err := json.Unmarshal(h, &m.Headers); err != nil {
			return fmt.Errorf("headers: %w", err)
		}
		delete(raw, "headers")
	}
	if len(raw) > 0 {
		m.Extra = raw
	}
	return nil
}

func (m mcpServerSecrets) empty() bool {
	return len(m.Env) == 0 && len(m.Headers) == 0 && len(m.Extra) == 0
}

// mcpSecretStore reads and writes the file. A nil store holds nothing and
// stores nothing: a Server built in a test never reaches the developer's home.
type mcpSecretStore struct {
	path string

	mu      sync.Mutex
	loaded  bool
	data    mcpSecretsFile
	stamp   mcpSecretsStamp
	loadErr error
}

// mcpSecretsStamp is what says the file changed since it was read.
type mcpSecretsStamp struct {
	info    os.FileInfo
	size    int64
	modTime time.Time
	// mode too: a chmod that opens the file to others changes nothing else.
	mode os.FileMode
}

func (a mcpSecretsStamp) same(b os.FileInfo) bool {
	if a.info == nil || b == nil {
		return a.info == nil && b == nil
	}
	return os.SameFile(a.info, b) && a.size == b.Size() && a.modTime.Equal(b.ModTime()) && a.mode == b.Mode()
}

func newMCPSecretStore(path string) *mcpSecretStore {
	return &mcpSecretStore{path: path}
}

// defaultMCPSecretsPath is beside the stored model key.
func defaultMCPSecretsPath() (string, error) {
	cred, err := credentialsPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(cred), mcpSecretsFileName), nil
}

// mcpSecretStoreOrNone is a real daemon's store, or nil -- with a line in the
// log -- when the home directory cannot be found.
func mcpSecretStoreOrNone(logger *log.Logger) *mcpSecretStore {
	path, err := defaultMCPSecretsPath()
	if err != nil {
		logger.Printf("warning: MCP server keys cannot be kept on disk: %v", err)
		return nil
	}
	return newMCPSecretStore(path)
}

// readMCPSecrets reads the file. Missing is empty. Others able to write it is
// an error, and so is a file that does not parse: half-reading a key file
// would start servers with some keys and not others, silently.
func readMCPSecrets(path string) (mcpSecretsFile, os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return mcpSecretsFile{}, nil, nil
		}
		return mcpSecretsFile{}, nil, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return mcpSecretsFile{}, info, fmt.Errorf("%s is mode %#o: writable by more than its owner, so the keys in it "+
			"cannot be trusted to be yours. Check it, then `chmod 600` it", path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return mcpSecretsFile{}, info, err
	}
	var file mcpSecretsFile
	if err := json.Unmarshal(data, &file); err != nil {
		// The JSON error can quote the text near the fault, which is a key.
		return mcpSecretsFile{}, info, fmt.Errorf("%s is not readable as MCP server keys (it is not valid JSON)", path)
	}
	if file.Version > mcpSecretsVersion {
		return mcpSecretsFile{}, info, fmt.Errorf("%s was written by a newer Mochiii (version %d); update this one",
			path, file.Version)
	}
	return file, info, nil
}

// refresh reads the file again if it changed, and reports whether it was read,
// so the caller can rebuild the scrubber. A writer replaces the file (rename),
// so a change is a new file identity even within one modification-time tick.
func (st *mcpSecretStore) refresh() (changed bool) {
	if st == nil {
		return false
	}
	info, err := os.Stat(st.path)
	if err != nil {
		info = nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.loaded && st.stamp.same(info) {
		return false
	}
	data, got, err := readMCPSecrets(st.path)
	if err != nil {
		data = mcpSecretsFile{}
	}
	st.data, st.stamp, st.loadErr, st.loaded = data, stampOf(got), err, true
	return true
}

func stampOf(info os.FileInfo) mcpSecretsStamp {
	if info == nil {
		return mcpSecretsStamp{}
	}
	return mcpSecretsStamp{info: info, size: info.Size(), modTime: info.ModTime(), mode: info.Mode()}
}

// problem is why the file could not be used, or "".
func (st *mcpSecretStore) problem() string {
	if st == nil {
		return ""
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.loadErr == nil {
		return ""
	}
	return "the MCP server keys you stored were not loaded: " + st.loadErr.Error()
}

// lookup is the value server reads for name: its own, then everyone's.
func (st *mcpSecretStore) lookup(server, name string) (string, bool) {
	if st == nil {
		return "", false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if v, ok := st.data.Servers[server].Env[name]; ok && v != "" && server != "" {
		return v, true
	}
	if v, ok := st.data.Vars[name]; ok && v != "" {
		return v, true
	}
	return "", false
}

// values is every value held, for the scrubber.
func (st *mcpSecretStore) values() []string {
	if st == nil {
		return nil
	}
	st.refresh()
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []string
	for _, v := range st.data.Vars {
		out = append(out, v)
	}
	for _, srv := range st.data.Servers {
		for _, v := range srv.Env {
			out = append(out, v)
		}
		for _, v := range srv.Headers {
			out = append(out, v)
			// "Bearer <token>": the token alone is the credential, and it is
			// what a server would echo back.
			if _, tok, ok := strings.Cut(v, " "); ok && len(tok) >= 8 {
				out = append(out, tok)
			}
		}
		out = append(out, rawSecretStrings(srv.Extra)...)
	}
	return out
}

// rawSecretStrings is the credentials inside fields this daemon reads as
// opaque (a sign-in, a newer daemon's field): every value under an "env" or
// "headers" object, and any string whose field is named like a secret
// (access_token, refresh_token, client_secret, api_key, password).
//
// NOT EVERY STRING. A sign-in also holds its endpoint, its token type
// ("Bearer") and its client id; scrubbing those would cut a URL or the word
// Bearer out of everything the model is shown.
func rawSecretStrings(extra map[string]json.RawMessage) []string {
	var out []string
	add := func(s string) {
		if len(strings.TrimSpace(s)) >= 8 {
			out = append(out, s)
		}
	}
	var walk func(v any, secret bool)
	walk = func(v any, secret bool) {
		switch t := v.(type) {
		case string:
			if secret {
				add(t)
			}
		case map[string]any:
			for k, x := range t {
				walk(x, secret || k == "env" || k == "headers" || secretFieldName(k))
			}
		case []any:
			for _, x := range t {
				walk(x, secret)
			}
		}
	}
	for k, raw := range extra {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			walk(v, secretFieldName(k))
		}
	}
	return out
}

// secretFieldName reports whether a field's name says it holds a credential.
func secretFieldName(name string) bool {
	n := strings.ToLower(name)
	if n == "token_type" || n == "token_url" || strings.HasSuffix(n, "_url") {
		return false
	}
	for _, w := range []string{"token", "secret", "password", "api_key", "apikey"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// storedNames is the variable names server would be given a stored value for.
func (st *mcpSecretStore) storedNames(server string, env []string) []string {
	var out []string
	for _, name := range env {
		if _, ok := st.lookup(server, name); ok {
			out = append(out, name)
		}
	}
	return out
}

// update changes the file under its lock: read, change, write. change sees the
// file as it is on disk now, not as this process last read it.
func (st *mcpSecretStore) update(change func(*mcpSecretsFile)) error {
	if st == nil {
		return errors.New("this daemon has nowhere to keep MCP server keys")
	}
	if err := os.MkdirAll(filepath.Dir(st.path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(st.path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("locking %s: %w", st.path, err)
	}
	defer func() { _ = lock.Close() }()
	if err := lockFile(lock); err != nil {
		return fmt.Errorf("locking %s: %w", st.path, err)
	}
	defer func() { _ = unlockFile(lock) }()

	file, _, err := readMCPSecrets(st.path)
	if err != nil {
		return err
	}
	change(&file)
	file.Version = mcpSecretsVersion
	for name, srv := range file.Servers {
		for k, v := range srv.Env {
			if v == "" {
				delete(srv.Env, k)
			}
		}
		for k, v := range srv.Headers {
			if v == "" {
				delete(srv.Headers, k)
			}
		}
		if srv.empty() {
			delete(file.Servers, name)
		}
	}
	for k, v := range file.Vars {
		if v == "" {
			delete(file.Vars, k)
		}
	}

	if len(file.Vars) == 0 && len(file.Servers) == 0 {
		if err := os.Remove(st.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		data, err := json.MarshalIndent(file, "", "  ")
		if err != nil {
			return err
		}
		if err := writePrivateFile(st.path, append(data, '\n')); err != nil {
			return err
		}
	}

	info, _ := os.Stat(st.path)
	st.mu.Lock()
	st.data, st.stamp, st.loadErr, st.loaded = file, stampOf(info), nil, true
	st.mu.Unlock()
	return nil
}

// setVar stores (or, for "", forgets) a value for server, or for every server
// when server is "".
func (st *mcpSecretStore) setVar(server, name, value string) error {
	return st.update(func(f *mcpSecretsFile) {
		if server == "" {
			if f.Vars == nil {
				f.Vars = map[string]string{}
			}
			f.Vars[name] = value
			return
		}
		if f.Servers == nil {
			f.Servers = map[string]mcpServerSecrets{}
		}
		srv := f.Servers[server]
		if srv.Env == nil {
			srv.Env = map[string]string{}
		}
		srv.Env[name] = value
		f.Servers[server] = srv
	})
}

// setServerEnv replaces server's own values for the names in values and drops
// its values for any name it no longer reads.
func (st *mcpSecretStore) setServerEnv(server string, reads []string, values map[string]string) error {
	return st.update(func(f *mcpSecretsFile) {
		if f.Servers == nil {
			f.Servers = map[string]mcpServerSecrets{}
		}
		srv := f.Servers[server]
		keep := map[string]string{}
		for _, name := range reads {
			if v, ok := values[name]; ok {
				keep[name] = v
			} else if v, ok := srv.Env[name]; ok {
				keep[name] = v
			}
		}
		srv.Env = keep
		f.Servers[server] = srv
	})
}

// forgetServer drops everything stored for server. Values every server reads
// stay: another server may read them too.
func (st *mcpSecretStore) forgetServer(server string) error {
	if st == nil {
		return nil
	}
	return st.update(func(f *mcpSecretsFile) { delete(f.Servers, server) })
}

// header is a remote server's stored value for a header.
func (st *mcpSecretStore) header(server, name string) (string, bool) {
	if st == nil {
		return "", false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	v, ok := st.data.Servers[server].Headers[name]
	return v, ok && v != ""
}

// setHeader stores (or, for "", forgets) a remote server's header value.
func (st *mcpSecretStore) setHeader(server, name, value string) error {
	return st.update(func(f *mcpSecretsFile) {
		if f.Servers == nil {
			f.Servers = map[string]mcpServerSecrets{}
		}
		srv := f.Servers[server]
		if srv.Headers == nil {
			srv.Headers = map[string]string{}
		}
		srv.Headers[name] = value
		f.Servers[server] = srv
	})
}
