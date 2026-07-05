// Command codeterminal-tui is a thin CLI client that stands in for the
// future TUI. It finds the daemon via its lockfile, connects over the Unix
// domain socket, does the version handshake, sends one prompt, and prints
// tokens to stdout as they stream back.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"codeterminal/protocol"
)

func main() {
	promptFlag := flag.String("prompt", "", "prompt text to send (reads stdin if omitted)")
	flag.Parse()

	prompt := readPrompt(*promptFlag)
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "error: no prompt given (use --prompt \"...\" or pipe text on stdin)")
		os.Exit(1)
	}

	lockPath := protocol.LockPath()
	lock, err := readLockFile(lockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: daemon not found: %v\n", err)
		fmt.Fprintf(os.Stderr, "  expected a lockfile at %s\n", lockPath)
		fmt.Fprintln(os.Stderr, "  start it with: (cd daemon && go run .)")
		os.Exit(1)
	}

	conn, err := net.Dial("unix", lock.SocketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not connect to daemon at %s: %v\n", lock.SocketPath, err)
		fmt.Fprintln(os.Stderr, "  the daemon may have crashed or been stopped; restart it and try again")
		os.Exit(1)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	if err := enc.Encode(protocol.HandshakeRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		ClientName:      "codeterminal-tui",
	}); err != nil {
		fmt.Fprintf(os.Stderr, "error: sending handshake: %v\n", err)
		os.Exit(1)
	}

	var hsResp protocol.HandshakeResponse
	if err := dec.Decode(&hsResp); err != nil {
		fmt.Fprintf(os.Stderr, "error: reading handshake response: %v\n", err)
		os.Exit(1)
	}
	if !hsResp.Ok {
		fmt.Fprintf(os.Stderr, "error: daemon rejected handshake: %s\n", hsResp.Error)
		fmt.Fprintf(os.Stderr, "  this client speaks protocol v%d; make sure client and daemon are the same build\n", protocol.ProtocolVersion)
		os.Exit(1)
	}

	if err := enc.Encode(protocol.PromptRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Prompt:          prompt,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "error: sending prompt: %v\n", err)
		os.Exit(1)
	}

	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			if err == io.EOF {
				break
			}
			fmt.Fprintf(os.Stderr, "\nerror: reading token stream: %v\n", err)
			os.Exit(1)
		}
		if tok.Error != "" {
			fmt.Fprintf(os.Stderr, "\nerror from daemon: %s\n", tok.Error)
			os.Exit(1)
		}
		if tok.Token != "" {
			os.Stdout.WriteString(tok.Token) // written immediately, no buffering, so streaming is visible
		}
		if tok.Done {
			break
		}
	}
	os.Stdout.WriteString("\n")
}

func readPrompt(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	fmt.Fprintln(os.Stderr, "(reading prompt from stdin; press Ctrl-D when done)")
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading stdin: %v\n", err)
		os.Exit(1)
	}
	return strings.TrimSpace(string(data))
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
