package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"mochiii/protocol"
)

// THE GITHUB TOKEN: where it is kept, how it is checked, and what is said
// about it.
//
// KEPT LIKE THE PROVIDER KEY (credentials.go): its own file in ~/.mochiii,
// 0600, written atomically. That folder is the one the agent's reads and edits
// refuse, and the token is registered with the literal credential scrubber
// (credscrub_apply.go), so if it ever turned up in a command's output or a
// tool's arguments it would be cut out before reaching the model or the web.
// It is never put in the environment or on a command line -- no child process
// needs it, because every git operation Mochiii makes goes through GitHub's
// API, from here.
//
// A BOT ACCOUNT, BY THE OWNER'S DECISION (2026-10-15). GitHub never lets the
// author of a pull request approve it, so a token for the owner's own account
// would leave them unable to approve Mochiii's work. Connect says so when the
// token belongs to the repository's owner. A dedicated account also scopes the
// token by itself: it can only reach the repositories the account was invited
// to, whatever the token's scopes say.

// githubCredFileName is the file's name inside ~/.mochiii.
const githubCredFileName = "github.json"

// githubCredential is the file's content.
type githubCredential struct {
	Token   string `json:"token"`
	Login   string `json:"login"`
	SavedAt string `json:"saved_at"`
	// Repos maps a project folder to the repository its origin named when it
	// was confirmed: "owner/name". Pinned, so a project whose origin is later
	// changed does not quietly start talking to another repository.
	Repos map[string]string `json:"repos,omitempty"`
}

// githubCredPathOrNone is where a real daemon keeps the token: beside the
// provider key. "" when there is no home folder, and then nothing is stored.
func githubCredPathOrNone(logger *log.Logger) string {
	home, err := os.UserHomeDir()
	if err != nil {
		logger.Printf("github: no home folder, so no token can be kept: %v", err)
		return ""
	}
	return filepath.Join(home, ".mochiii", githubCredFileName)
}

// githubCredentialLocked is the stored credential, read once. Caller holds
// s.github.mu.
func (s *Server) githubCredentialLocked() (githubCredential, error) {
	if s.github.loaded {
		return s.github.cred, nil
	}
	if s.githubCredPath == "" {
		s.github.loaded = true
		return githubCredential{}, nil
	}
	cred, err := loadGitHubCredential(s.githubCredPath)
	if err != nil {
		return githubCredential{}, err
	}
	s.github.cred, s.github.loaded = cred, true
	return cred, nil
}

// loadGitHubCredential reads path: nothing when there is no file.
func loadGitHubCredential(path string) (githubCredential, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return githubCredential{}, nil
	}
	if err != nil {
		return githubCredential{}, err
	}
	if !info.Mode().IsRegular() {
		return githubCredential{}, fmt.Errorf("%s is not a plain file; refusing to use it", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return githubCredential{}, fmt.Errorf("%s is mode %#o, readable by more than you, so the token in it may "+
			"already have been read. Revoke it on GitHub, then /github disconnect and connect again",
			path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return githubCredential{}, err
	}
	var cred githubCredential
	if err := json.Unmarshal(data, &cred); err != nil {
		return githubCredential{}, fmt.Errorf("%s could not be read (%v); /github connect writes it again", path, err)
	}
	return cred, nil
}

// githubTokenForScrub is the stored token, for the credential scrubber, or ""
// when there is none or it cannot be read. Called with credMu held: the lock
// order is credMu, then github.mu, and nothing takes them the other way.
func (s *Server) githubTokenForScrub() string {
	s.github.mu.Lock()
	defer s.github.mu.Unlock()
	cred, err := s.githubCredentialLocked()
	if err != nil {
		return ""
	}
	return cred.Token
}

// saveGitHubCredentialLocked writes cred and keeps it as the one in force.
// Caller holds s.github.mu, and rebuilds the scrubber after releasing it.
func (s *Server) saveGitHubCredentialLocked(cred githubCredential) error {
	if s.githubCredPath == "" {
		return fmt.Errorf("there is nowhere to keep the token: no home folder")
	}
	cred.SavedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(cred, "", "  ")
	if err != nil {
		return err
	}
	if err := writePrivateFile(s.githubCredPath, append(data, '\n')); err != nil {
		return err
	}
	s.github.cred, s.github.loaded = cred, true
	return nil
}

// githubToolState is what reading needs: the token and this project's
// repository, or why there is none.
func (s *Server) githubToolState() (token, repo string, err error) {
	s.github.mu.Lock()
	defer s.github.mu.Unlock()
	cred, err := s.githubCredentialLocked()
	switch {
	case err != nil:
		return "", "", err
	case cred.Token == "":
		return "", "", fmt.Errorf("Mochiii is not connected to GitHub. /github connect, then paste a token for " +
			"Mochiii's own GitHub account")
	}
	repo = cred.Repos[s.workspace]
	if repo == "" {
		return "", "", fmt.Errorf("this project has no GitHub repository set. /github use sets it from the " +
			"project's origin remote")
	}
	return cred.Token, repo, nil
}

// --- the origin remote ---

// githubRemotePatterns are the forms a github.com remote is written in.
var githubRemotePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^https://(?:[^@/]+@)?github\.com/([^/]+/[^/]+?)(?:\.git)?/?$`),
	regexp.MustCompile(`^(?:ssh://)?git@github\.com[:/]([^/]+/[^/]+?)(?:\.git)?/?$`),
}

// parseGitHubRemote is "owner/name" for a github.com remote URL.
func parseGitHubRemote(remote string) (string, bool) {
	remote = strings.TrimSpace(remote)
	for _, p := range githubRemotePatterns {
		if m := p.FindStringSubmatch(remote); m != nil && validGitHubRepo(m[1]) {
			return m[1], true
		}
	}
	return "", false
}

// originRepo is the repository this project's origin remote names. Git is run
// the way git_history runs it (githistory.go): no system config, no prompts,
// every config key that runs a command neutralised -- reading one config value
// runs nothing, and the rule costs nothing to keep.
func (s *Server) originRepo(ctx context.Context) (string, error) {
	root, err := s.realWorkspaceRoot()
	if err != nil {
		return "", err
	}
	runCtx, cancel := context.WithTimeout(ctx, gitHistoryTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "git", gitHistoryArgs(root, "config", "--get", "remote.origin.url")...)
	cmd.Dir = root
	cmd.Env = gitHistoryEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("this project has no remote named origin")
	}
	repo, ok := parseGitHubRemote(string(out))
	if !ok {
		return "", fmt.Errorf("this project's origin is not a github.com repository (%s)", clipRunes(oneLine(string(out)), 80))
	}
	return repo, nil
}

// --- connect, use, status, disconnect ---

// githubTokenShapeError is why a pasted token cannot be one, or "".
func githubTokenShapeError(token string) string {
	switch {
	case token == "":
		return "no token was entered"
	case strings.ContainsAny(token, " \t"):
		return "the token has a space in it -- it was probably pasted with text around it"
	case strings.IndexFunc(token, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0:
		return "the token has a line break or another control character in it -- it was probably pasted with one"
	case len(token) > 255:
		return "that is longer than any GitHub token"
	}
	return ""
}

// githubAccess is what GitHub says a token may do in a repository.
type githubAccess struct {
	Private     bool `json:"private"`
	Permissions struct {
		Admin bool `json:"admin"`
		Push  bool `json:"push"`
		Pull  bool `json:"pull"`
	} `json:"permissions"`
	Owner struct {
		Login string `json:"login"`
	} `json:"owner"`
	DefaultBranch string `json:"default_branch"`
}

// githubConnect checks token with GitHub, and stores it only if it works and
// can see this project's repository (when the project has one).
func (s *Server) githubConnect(ctx context.Context, token string) protocol.GitHubResponse {
	token = strings.TrimSpace(token)
	if why := githubTokenShapeError(token); why != "" {
		return protocol.GitHubResponse{Error: why + "; nothing was stored"}
	}
	var user struct {
		Login string `json:"login"`
	}
	header, err := githubCall(ctx, token, http.MethodGet, "/user", nil, &user)
	if err != nil {
		return protocol.GitHubResponse{Error: err.Error() + " Nothing was stored."}
	}
	login := clipRunes(oneLine(user.Login), 60)

	var lines []string
	repo, remoteErr := s.originRepo(ctx)
	if remoteErr == nil {
		var access githubAccess
		if _, err := githubCall(ctx, token, http.MethodGet, githubRepoPath(repo, ""), nil, &access); err != nil {
			return protocol.GitHubResponse{Error: fmt.Sprintf("the token works, but it cannot reach %s, this "+
				"project's repository: %v Invite the account %s to the repository, or use a token that can "+
				"see it. Nothing was stored.", repo, err, login)}
		}
		lines = append(lines, githubAccessLine(repo, access))
		if strings.EqualFold(access.Owner.Login, login) {
			lines = append(lines, "This is the repository owner's own account. GitHub never lets the author "+
				"of a pull request approve it, so you could not approve the ones Mochiii opens. A separate "+
				"account for Mochiii, invited to the repository, avoids that.")
		}
	} else {
		lines = append(lines, "No repository is set for this project: "+remoteErr.Error()+".")
	}
	lines = append(lines, githubTokenNotes(header)...)

	s.github.mu.Lock()
	cred, err := s.githubCredentialLocked()
	if err != nil {
		cred = githubCredential{} // an unreadable file is replaced, which is what connect is for
	}
	cred.Token, cred.Login = token, login
	if cred.Repos == nil {
		cred.Repos = map[string]string{}
	}
	if remoteErr == nil && s.workspace != "" {
		cred.Repos[s.workspace] = repo
	}
	err = s.saveGitHubCredentialLocked(cred)
	s.github.mu.Unlock()
	if err != nil {
		return protocol.GitHubResponse{Error: "the token works, but it could not be stored: " + err.Error()}
	}
	s.rebuildCredScrubber()

	text := "Connected to GitHub as " + login + ".\n" + strings.Join(lines, "\n")
	resp := protocol.GitHubResponse{Text: text, Connected: true, Login: login}
	if remoteErr == nil {
		resp.Repo = repo
	}
	return resp
}

// githubAccessLine says what the account may do in repo.
func githubAccessLine(repo string, a githubAccess) string {
	visibility := "public"
	if a.Private {
		visibility = "private"
	}
	can := "read it only"
	switch {
	case a.Permissions.Admin:
		can = "read it, push branches and administer it"
	case a.Permissions.Push:
		can = "read it and push branches"
	}
	return fmt.Sprintf("Repository for this project: %s (%s). This account can %s.", repo, visibility, can)
}

// githubTokenNotes is what the token's headers say that a user should know:
// scopes that reach further than Mochiii needs, and when it expires.
func githubTokenNotes(header http.Header) []string {
	var notes []string
	scopes := map[string]bool{}
	for _, sc := range strings.Split(header.Get("X-OAuth-Scopes"), ",") {
		if sc = strings.TrimSpace(sc); sc != "" {
			scopes[sc] = true
		}
	}
	if scopes["workflow"] {
		notes = append(notes, "The token can change GitHub Actions workflows (scope \"workflow\"). Mochiii never "+
			"will, but a token without that scope is safer.")
	}
	for _, wide := range []string{"delete_repo", "admin:org", "admin:repo_hook", "admin:public_key"} {
		if scopes[wide] {
			notes = append(notes, fmt.Sprintf("The token has the scope %q, far more than Mochiii needs. "+
				"Consider a new one with only \"repo\".", wide))
		}
	}
	if raw := header.Get("GitHub-Authentication-Token-Expiration"); raw != "" {
		for _, layout := range []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700", time.RFC3339} {
			if at, err := time.Parse(layout, raw); err == nil {
				note := "The token expires on " + at.Local().Format("2 January 2006") + "."
				if time.Until(at) < 7*24*time.Hour {
					note += " That is within a week: make a new one before then."
				}
				notes = append(notes, note)
				break
			}
		}
	}
	return notes
}

// githubUse sets this project's repository from its origin, with the token
// already stored.
func (s *Server) githubUse(ctx context.Context) protocol.GitHubResponse {
	s.github.mu.Lock()
	cred, err := s.githubCredentialLocked()
	s.github.mu.Unlock()
	switch {
	case err != nil:
		return protocol.GitHubResponse{Error: err.Error()}
	case cred.Token == "":
		return protocol.GitHubResponse{Error: "Mochiii is not connected to GitHub yet: /github connect first"}
	case s.workspace == "":
		return protocol.GitHubResponse{Error: "this daemon serves no project"}
	}
	repo, err := s.originRepo(ctx)
	if err != nil {
		return protocol.GitHubResponse{Error: err.Error()}
	}
	var access githubAccess
	if _, err := githubCall(ctx, cred.Token, http.MethodGet, githubRepoPath(repo, ""), nil, &access); err != nil {
		return protocol.GitHubResponse{Error: fmt.Sprintf("%s cannot be reached with the stored token: %v", repo, err)}
	}
	s.github.mu.Lock()
	cred, err = s.githubCredentialLocked()
	if err == nil {
		if cred.Repos == nil {
			cred.Repos = map[string]string{}
		}
		cred.Repos[s.workspace] = repo
		err = s.saveGitHubCredentialLocked(cred)
	}
	s.github.mu.Unlock()
	if err != nil {
		return protocol.GitHubResponse{Error: "could not store it: " + err.Error()}
	}
	return protocol.GitHubResponse{Text: githubAccessLine(repo, access), Connected: true, Login: cred.Login, Repo: repo}
}

// githubStatus says where things stand, asking GitHub that the token still
// works.
func (s *Server) githubStatus(ctx context.Context) protocol.GitHubResponse {
	s.github.mu.Lock()
	cred, err := s.githubCredentialLocked()
	s.github.mu.Unlock()
	if err != nil {
		return protocol.GitHubResponse{Error: err.Error()}
	}
	if cred.Token == "" {
		return protocol.GitHubResponse{Text: "Mochiii is not connected to GitHub.\n" + githubConnectHelp}
	}
	resp := protocol.GitHubResponse{Connected: true, Login: cred.Login, Repo: cred.Repos[s.workspace]}
	lines := []string{"Connected to GitHub as " + cred.Login + "."}
	var user struct {
		Login string `json:"login"`
	}
	header, err := githubCall(ctx, cred.Token, http.MethodGet, "/user", nil, &user)
	if err != nil {
		lines = append(lines, "But: "+err.Error())
	} else {
		lines = append(lines, githubTokenNotes(header)...)
	}
	if resp.Repo == "" {
		lines = append(lines, "This project has no repository set: /github use sets it from its origin remote.")
	} else {
		lines = append(lines, "Repository for this project: "+resp.Repo+".")
		if origin, err := s.originRepo(ctx); err == nil && !strings.EqualFold(origin, resp.Repo) {
			lines = append(lines, "Its origin remote now names "+origin+"; Mochiii keeps using "+resp.Repo+
				" until you run /github use.")
		}
	}
	resp.Text = strings.Join(lines, "\n")
	return resp
}

// githubConnectHelp is how to get a token, said where it is needed.
const githubConnectHelp = "To connect: make a GitHub account for Mochiii, invite it to the repository, sign in " +
	"as it, create a classic token with the scope \"repo\" only (Settings > Developer settings > Personal access " +
	"tokens), then /github connect and paste it."

// githubDisconnect forgets the token.
func (s *Server) githubDisconnect() protocol.GitHubResponse {
	s.github.mu.Lock()
	removed := false
	var err error
	if s.githubCredPath != "" {
		err = os.Remove(s.githubCredPath)
		removed = err == nil
		if os.IsNotExist(err) {
			err = nil
		}
	}
	if err == nil {
		s.github.cred, s.github.loaded = githubCredential{}, true
	}
	s.github.mu.Unlock()
	if err != nil {
		return protocol.GitHubResponse{Error: "could not remove the stored token: " + err.Error()}
	}
	s.rebuildCredScrubber()
	if !removed {
		return protocol.GitHubResponse{Text: "No GitHub token was stored."}
	}
	return protocol.GitHubResponse{Text: "The GitHub token is forgotten. It still works on GitHub until you " +
		"revoke it there."}
}
