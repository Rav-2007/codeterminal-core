package tui

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"mochiii/protocol"
)

// FIRST RUN IN A FOLDER (2026-10-17).
//
// Code search needs two things nothing used to set up. One is the embedding
// model, downloaded once per computer. The other is the folder's index, built
// once per folder: the daemon keeps an existing index fresh but never builds
// the first (daemon/retrieval_setup.go turns retrieval off when there is none).
// So for every new user "knows your codebase" was silently off. Before the chat
// opens, in the plain terminal, this offers to set up both.
//
// It asks rather than assumes. Indexing reads every file in the folder and takes
// minutes on a large one, and a folder can be the home directory, which the
// extension once indexed by accident (clients/vscode/src/extension.ts). The
// answer "never" is remembered per folder; "not now" asks again next time.

// indexStampFile is written last by a successful `index` run
// (daemon/embedderstamp.go). An index directory without it is unfinished.
const indexStampFile = "embedder_stamp.json"

// folderIndexed reports whether root has a finished index.
func folderIndexed(root string) bool {
	return isRegularFile(filepath.Join(root, ".mochiii", "index", indexStampFile))
}

// indexableFolder reports whether root is a folder Mochiii may offer to index
// on its own. The home directory and a filesystem root are never offered.
func indexableFolder(root, home string) bool {
	clean := filepath.Clean(root)
	if filepath.Dir(clean) == clean {
		return false
	}
	return home == "" || clean != filepath.Clean(home)
}

// folderSetup is everything the first-run offer needs, passed in so the
// decisions can be tested without a daemon binary or a terminal.
type folderSetup struct {
	root     string
	home     string
	in       *bufio.Reader
	out      io.Writer
	runner   func(args []string, stdout, stderr io.Writer) error // runs mochiii-daemon <args>
	declined *declinedFolders
	running  func() bool  // a daemon serves this folder now
	stop     func() error // stop it
}

// offer runs the first-run offer. force skips the question (--index), and
// rebuilds an index that already exists.
func (s folderSetup) offer(force bool) {
	if !force {
		if folderIndexed(s.root) || !indexableFolder(s.root, s.home) || s.declined.has(s.root) {
			return
		}
		say(s.out, "\nMochiii can index this folder so it finds the right code for your questions.\n"+
			"It runs on this computer and takes a minute or two for most projects.\n"+
			"Index %s now? [Y/n/never] ", s.root)
		switch answer := strings.ToLower(strings.TrimSpace(readLine(s.in))); answer {
		case "", "y", "yes":
		case "never":
			if err := s.declined.add(s.root); err != nil {
				say(s.out, "(could not remember that: %v)\n", err)
			}
			say(s.out, "Mochiii won't ask again for this folder. To index it later, run: mochiii --index\n")
			return
		default:
			say(s.out, "Skipped for now. Mochiii will ask again next time, or run: mochiii --index\n")
			return
		}
	}

	// A daemon reading the index must not have it rebuilt underneath it, and
	// one that started without an index never picks one up. Either way it has
	// to go before indexing; it is started again straight after.
	if s.running() {
		if err := s.stop(); err != nil && !errors.Is(err, errNoDaemonRunning) {
			say(s.out, "Could not stop the background service, so indexing is skipped: %v\n", err)
			return
		}
	}

	if !s.ensureModel() {
		return
	}

	say(s.out, "Indexing %s… (Ctrl-C to skip)\n", s.root)
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)
	err := s.runner([]string{"index", s.root}, s.out, s.out)
	signal.Stop(interrupted)
	select {
	case <-interrupted:
		say(s.out, "Skipped. Code search stays off for this folder until you run: mochiii --index\n")
		return
	default:
	}
	if err != nil {
		say(s.out, "Indexing did not finish (%v). Mochiii works without code search; to try again: mochiii --index\n", err)
		return
	}
	say(s.out, "Indexed. Code search is on for this folder.\n")
}

// ensureModel downloads the embedding model when it is missing. It reports
// whether the model is in place afterwards.
func (s folderSetup) ensureModel() bool {
	var check strings.Builder
	if err := s.runner([]string{"download-model", "--check"}, &check, io.Discard); err != nil && check.Len() == 0 {
		// --check exits 1 both when the model is missing and on a real error,
		// so only an empty answer is an error (see modelSetup.ts).
		say(s.out, "Could not check for the code-search model: %v\n", err)
		return false
	}
	var state struct {
		Cached        bool  `json:"cached"`
		DownloadBytes int64 `json:"download_bytes"`
	}
	if err := json.Unmarshal([]byte(lastLine(check.String())), &state); err != nil {
		say(s.out, "Could not read the code-search model's status (%q).\n", strings.TrimSpace(check.String()))
		return false
	}
	if state.Cached {
		return true
	}
	say(s.out, "Downloading Mochiii's code-search model (%d MB, once for this computer)…\n", (state.DownloadBytes+(1<<20)-1)>>20)
	if err := s.runner([]string{"download-model"}, s.out, s.out); err != nil {
		say(s.out, "The download did not finish (%v). Mochiii works without code search; to try again: mochiii --index\n", err)
		return false
	}
	return true
}

// runDaemonCommand returns a runner for folderSetup that runs the installed
// daemon binary in the foreground.
func runDaemonCommand(daemon string) func(args []string, stdout, stderr io.Writer) error {
	return func(args []string, stdout, stderr io.Writer) error {
		cmd := daemonCommand(daemon, args...)
		cmd.Stdin = nil
		cmd.Stdout, cmd.Stderr = stdout, stderr
		return cmd.Run()
	}
}

func readLine(r *bufio.Reader) string {
	if r == nil {
		return ""
	}
	line, _ := r.ReadString('\n')
	return line
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// declinedFolders is the set of folders whose owner answered "never". It
// holds workspace tags, not paths, so the file says nothing about which
// projects exist on this computer.
type declinedFolders struct{ path string }

func newDeclinedFolders() *declinedFolders {
	dir, err := stateDir()
	if err != nil {
		return &declinedFolders{}
	}
	return &declinedFolders{path: filepath.Join(dir, "no-index")}
}

func (d *declinedFolders) has(root string) bool {
	if d == nil || d.path == "" {
		return false
	}
	data, err := os.ReadFile(d.path)
	if err != nil {
		return false
	}
	tag := protocol.WorkspaceTag(root)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == tag {
			return true
		}
	}
	return false
}

func (d *declinedFolders) add(root string) error {
	if d == nil || d.path == "" {
		return errors.New("no state directory")
	}
	if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(d.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, protocol.WorkspaceTag(root))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
