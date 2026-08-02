package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"

	"codeterminal/protocol"
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

func (s *daemonSession) Close() error {
	return s.conn.Close()
}

// lockPathFunc resolves the daemon lockfile's path. It's a variable — not a
// direct call to protocol.LockPath() — purely so tests can point it at a
// fake daemon's lockfile (see stream_test.go); production code never
// changes it.
var lockPathFunc = protocol.LockPath

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
		return nil, fmt.Errorf("daemon not found (expected a lockfile at %s; start it with: codeterminal-daemon): %w", lockPath, err)
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
		conn.Close()
		return nil, fmt.Errorf("sending handshake: %w", err)
	}

	var hsResp protocol.HandshakeResponse
	if err := dec.Decode(&hsResp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("reading handshake response: %w", err)
	}
	if !hsResp.Ok {
		conn.Close()
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
