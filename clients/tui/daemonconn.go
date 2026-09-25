package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"

	"mochiii/protocol"
)

// daemonSession is an established, handshake-verified connection to the
// daemon, ready for exactly one PromptRequest — the wire protocol is one
// prompt per connection (see daemon/server.go's handleConn).
//
// handshake is the daemon's HandshakeResponse, retained so a caller can read
// HandshakeResponse.PersistedHistory. It is populated on every connection —
// only runChat's one-time preflight connection (main.go) is meant to
// actually use it; the streaming path (stream.go) never reads this field,
// so a per-prompt connection can never re-hydrate turns the TUI already
// has.
type daemonSession struct {
	conn      net.Conn
	enc       *json.Encoder
	dec       *json.Decoder
	handshake protocol.HandshakeResponse
}

// Close releases the connection.
//
// ITS ERROR IS IGNORED AT ALL FIVE CALL SITES IN THIS CLIENT, and that is a
// decision rather than an oversight, so the reasoning lives here once instead
// of five times.
//
// A close error on a socket can mean one of two things. On a WRITE path it can
// mean buffered data was never flushed, which is real data loss and must be
// reported. On a read path, or where everything written has already left, it
// carries no information a caller could act on: the file descriptor is
// released either way, and there is nothing left to retry.
//
// This client is only ever the second kind. sess.enc is a json.Encoder wrapped
// directly around the connection with no bufio in between (see dialDaemon), so
// every request is on the wire the moment Encode returns -- there is no buffer
// left to lose at close. The five sites are a preflight connection whose
// handshake has already been read, three deferred closes after the reply has
// been consumed, and one deliberate close from another goroutine to unblock a
// read, where an error is the expected outcome of the race.
//
// If a buffered writer is ever introduced between the encoder and the
// connection, this stops being true and every one of those sites becomes a
// place data can vanish silently.
func (s *daemonSession) Close() error {
	return s.conn.Close()
}

// daemonWorkspaceRoot is the canonical, symlink-resolved workspace root this
// process talks to a daemon about, and it is what puts this client on the same
// lockfile as its daemon.
//
// PER WORKSPACE, AND THIS CLIENT WAS THE ONE THAT MISSED IT. The daemon's
// lockfile moved from per-user (`daemon.lock`) to per-workspace
// (`daemon-<tag>.lock`) so two projects could both be open -- see
// protocol.WorkspaceTag and daemon/twoworkspaces_test.go. That change updated
// the daemon, the protocol and the VS Code client; it did not update this one,
// which went on reading protocol.LockPath(). The daemon always writes the
// tagged name (daemon/main.go passes a non-empty root), so the two could never
// meet and every terminal run failed with "daemon not found" naming a path
// nothing writes.
//
// NO TEST CAUGHT IT because every test here substitutes lockPathFunc for a
// fake, so the real derivation was the one thing never exercised. That is what
// TestTheLockPathMatchesTheDaemons is for.
//
// A PACKAGE VARIABLE, set once in main before anything connects and never
// written again. The workspace is a property of the process -- this client
// serves exactly one -- so threading it through connectToDaemon, and through
// fetchAvailableTiers, runSearch and resetHistoryOnDaemon which do not
// otherwise care, would be five parameters carrying one fact. The VS Code
// client holds the same fact the same way (clients/vscode/src/daemonClient.ts).
//
// Empty is a valid value: protocol.LockPathFor falls back to the per-user name
// for a client with no workspace to offer, which is what one-shot runs from a
// directory that cannot be resolved get.
var daemonWorkspaceRoot string

// setDaemonWorkspaceRoot records the root for the rest of the process. Called
// from main, once, BEFORE the first connection -- a call after that point would
// change which daemon later requests reach mid-session.
func setDaemonWorkspaceRoot(realRoot string) { daemonWorkspaceRoot = realRoot }

// lockPathFunc resolves the daemon lockfile's path. It's a variable — not a
// direct call to protocol.LockPathFor() — purely so tests can point it at a
// fake daemon's lockfile (see stream_test.go); production code never
// changes it.
var lockPathFunc = func() string { return protocol.LockPathFor(daemonWorkspaceRoot) }

// connectToDaemon finds the daemon via its lockfile, dials its Unix domain
// socket, and performs the version handshake. It's shared by both the
// one-shot client path (main.go) and the interactive chat's streaming
// goroutine (stream.go), so the transport and handshake are implemented
// exactly once.
//
// capabilities is what THIS caller can actually do, not what the binary can.
// It is a parameter rather than a constant precisely because the two callers
// differ: the chat UI can render a mid-stream approval and the one-shot path
// generally cannot, and a client that declares a capability it cannot honour
// leaves the daemon asking a question nobody will answer.
func connectToDaemon(clientName string, capabilities ...string) (*daemonSession, error) {
	lockPath := lockPathFunc()
	lock, err := readLockFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("daemon not found (expected a lockfile at %s; start it with: mochiii-daemon): %w", lockPath, err)
	}

	// The lockfile carries the address, transport and all, so this dials what
	// it was told rather than deriving it -- which is what lets one line serve
	// a Unix socket and a Windows named pipe. AddressFromLock also accepts a
	// lockfile written before Address existed.
	addr := protocol.AddressFromLock(lock)
	conn, err := protocol.Dial(addr)
	if err != nil {
		return nil, fmt.Errorf("could not connect to daemon at %s (it may have crashed or been stopped; restart it and try again): %w", addr, err)
	}

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	if err := enc.Encode(protocol.HandshakeRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		ClientName:      clientName,
		// CapToolApproval here is THE SWITCH THAT TURNS AGENT MODE ON: the
		// daemon runs the agentic loop only for a client that declared it can
		// answer a mid-stream approval (see agentModeEngaged). Declaring it is a
		// promise, so it is passed in by the caller that can keep it rather than
		// hardcoded for every caller that shares this function.
		Capabilities: capabilities,
	}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sending handshake: %w", err)
	}

	var hsResp protocol.HandshakeResponse
	if err := dec.Decode(&hsResp); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("reading handshake response: %w", err)
	}
	if !hsResp.Ok {
		_ = conn.Close()
		return nil, fmt.Errorf("daemon rejected handshake: %s (this client speaks protocol v%d; make sure client and daemon are the same build)", hsResp.Error, protocol.ProtocolVersion)
	}

	return &daemonSession{conn: conn, enc: enc, dec: dec, handshake: hsResp}, nil
}

func readLockFile(path string) (protocol.LockFile, error) {
	var lock protocol.LockFile
	data, err := os.ReadFile(path)
	if err != nil {
		return lock, err
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return lock, fmt.Errorf("corrupt lockfile: %w", err)
	}
	return lock, nil
}

// fetchAvailableTiers asks the daemon for models.json tiers via StatusRequest.
// Used by /model so the menu always matches the running config.
func fetchAvailableTiers(clientName string) ([]protocol.StatusTier, error) {
	sess, err := connectToDaemon(clientName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()

	if err := sess.enc.Encode(protocol.StatusRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Status:          true,
	}); err != nil {
		return nil, err
	}
	var resp protocol.StatusResponse
	if err := sess.dec.Decode(&resp); err != nil {
		return nil, err
	}
	return resp.AvailableTiers, nil
}

// runSearch asks the daemon to search its conversation history for the query.
func runSearch(clientName, workspace, query string) string {
	sess, err := connectToDaemon(clientName)
	if err != nil {
		return "search failed: " + err.Error()
	}
	defer func() { _ = sess.Close() }()

	if err := sess.enc.Encode(protocol.SearchRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Search:          true,
		Workspace:       workspace,
		Query:           query,
	}); err != nil {
		return "search failed: " + err.Error()
	}
	var resp protocol.SearchResponse
	if err := sess.dec.Decode(&resp); err != nil {
		return "search failed: " + err.Error()
	}
	if resp.Error != "" {
		return "search failed: " + resp.Error
	}
	if len(resp.Results) == 0 {
		return "no matching turns found in project memory"
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("found %d match(es):\n", len(resp.Results)))
	for i, hit := range resp.Results {
		b.WriteString(fmt.Sprintf("%d. [%s] (%s)\n   %s\n", i+1, hit.Role, hit.CreatedAt, hit.Snippet))
	}
	return strings.TrimRight(b.String(), "\n")
}

// sendConnect hands the daemon a ConnectRequest and returns its answer.
//
// NOTHING HERE LOGS THE REQUEST. It carries a provider key, and the one thing
// that must never happen to it is being written down -- so errors below name the
// step that failed and never the payload.
func sendConnect(clientName string, req protocol.ConnectRequest) (protocol.ConnectResponse, error) {
	sess, err := connectToDaemon(clientName)
	if err != nil {
		return protocol.ConnectResponse{}, err
	}
	defer func() { _ = sess.Close() }()

	if err := sess.enc.Encode(req); err != nil {
		return protocol.ConnectResponse{}, fmt.Errorf("sending the request: %w", err)
	}
	var resp protocol.ConnectResponse
	if err := sess.dec.Decode(&resp); err != nil {
		return protocol.ConnectResponse{}, fmt.Errorf("reading the answer: %w", err)
	}
	return resp, nil
}
