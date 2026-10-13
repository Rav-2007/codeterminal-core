package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// WHERE A THIRD-PARTY MCP SERVER IS STARTED.
//
// THE DEFECT (FOUND 2026-10-12 by reading; reproduced by mcpcwd_test.go before
// this file existed). mcp.Connect never set the subprocess's folder, so a
// server started wherever the daemon was -- and the VS Code extension starts
// the daemon inside the project that is open. validateUserCommand refuses a
// relative command for exactly this reason ("the same stored line would run
// one program today and a different one, chosen by whoever wrote the next
// repository, tomorrow") and the reasoning stopped one step short: most real
// server lines name a LAUNCHER, and a launcher decides what to run partly from
// the folder it is in. npx reads that folder's .npmrc -- which can name another
// registry -- and prefers its node_modules/.bin; `python -m` puts it first on
// the import path; uvx reads its uv.toml. A server the user added once, for
// every project, would be resolved by whichever project they opened next, at
// the start of a turn, before any tool had asked for anything.
//
// THE RULE. A third-party server starts in a private folder of its own, which
// no project can write to. A server that is about the project asks for the
// project in its block ("cwd": "workspace"), and the sentence a user agrees to
// when adding it says so. With nowhere private to start it, a server is NOT
// STARTED: falling back to the daemon's own folder is the defect.

// mcpRunDirName is the folder, inside this product's cache, that holds one
// folder per server.
const mcpRunDirName = "mcp-run"

// defaultMCPRunDir is where a real daemon starts its third-party servers: the
// user's cache folder, not ~/.mochiii. A server writes what it likes into the
// folder it starts in, and that must not be the folder that holds the key.
func defaultMCPRunDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "mochiii", mcpRunDirName), nil
}

// mcpRunDirOrNone is defaultMCPRunDir for main, or "" -- with a line in the log
// saying what that costs -- when the cache folder cannot be found.
func mcpRunDirOrNone(logger *log.Logger) string {
	dir, err := defaultMCPRunDir()
	if err != nil {
		logger.Printf("warning: third-party MCP servers will not start: no private folder to start them in (%v)", err)
		return ""
	}
	return dir
}

// serverStartDir returns the folder srv is started in, making it if need be.
func (s *Server) serverStartDir(name string, srv MCPServerConfig) (string, error) {
	if srv.Cwd == ServerCwdWorkspace {
		if strings.TrimSpace(s.workspace) == "" {
			return "", errors.New("its block asks to start in the project, and this daemon has no project")
		}
		return s.workspace, nil
	}
	base := strings.TrimSpace(s.mcpRunDir)
	if base == "" {
		return "", errors.New("there is no private folder to start it in, and it is never started in the folder " +
			"this daemon happens to be running in")
	}
	dir := filepath.Join(base, startDirName(name))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("making the folder it starts in: %w", err)
	}
	// A REAL FOLDER, NOT A LINK TO ONE. MkdirAll is satisfied by a link that
	// points at a directory, and a link pointing at a project would undo all of
	// the above while looking exactly like the private folder.
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("checking the folder it starts in: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a plain folder, so nothing is started in it; remove it and try again", dir)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", fmt.Errorf("making the folder it starts in private: %w", err)
		}
	}
	return dir, nil
}

// startDirName is the folder name for a server: the server's own name when that
// is a plain one, otherwise a digest of it. A server defined in the config file
// may be called anything mcp.ValidateServerName allows -- which includes "..",
// and names with a separator in them -- and none of those may choose a folder.
func startDirName(name string) string {
	if validateUserServerName(name) == nil {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return "server-" + hex.EncodeToString(sum[:8])
}

// startsWhere is the sentence that tells a user which folder a server they are
// about to add will run in.
func startsWhere(srv MCPServerConfig) string {
	if srv.Cwd == ServerCwdWorkspace {
		return "It will be started INSIDE whatever project this daemon is working on. A launcher such as npx, uvx or " +
			"python decides what to run partly from the folder it starts in, so a project you open can change what " +
			"this line runs. Choose this only for a server that has to be in the project."
	}
	return "It will be started in a folder of its own, not in your project, so no project you open can change what " +
		"this line runs. A server that needs the project should be given its path as an argument."
}
