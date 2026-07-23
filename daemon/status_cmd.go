package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"codeterminal/protocol"
)

// statusDialTimeout bounds the connect to a daemon that may not be running.
const statusDialTimeout = 2 * time.Second

// runStatusCommand implements `codeterminal-daemon status`: it connects to a
// RUNNING daemon over the same socket every client uses, asks for its state,
// and prints it.
//
// This is the operator's actual entry point. The wire message alone would
// still have left "is the daemon healthy?" answerable only by writing a socket
// client, which is barely better than reading the source. One command, no
// arguments, human-readable by default and --json for tooling.
//
// It is a one-shot subcommand alongside index/retrieve/skills/edits (see
// main.go), and like them it never touches the long-running serve path.
// Notably it starts nothing: if no daemon is running, that is itself the
// answer and is reported as such, rather than silently launching one.
func runStatusCommand(args []string, logger *log.Logger) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the raw StatusResponse as JSON instead of a human-readable summary")
	if err := fs.Parse(args); err != nil {
		return err
	}

	socketPath := protocol.SocketPath()
	conn, err := net.DialTimeout("unix", socketPath, statusDialTimeout)
	if err != nil {
		// The most common operator question ("is it even running?") deserves a
		// plain answer, not a dial error. The socket path is included because
		// this is a local CLI printing to the operator's own terminal, not the
		// socket surface Gate 7 scrubs.
		return fmt.Errorf("no daemon is responding on %s (is it running?)", socketPath)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(bufio.NewReader(conn))

	if err := enc.Encode(protocol.HandshakeRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		ClientName:      "status-cli",
	}); err != nil {
		return fmt.Errorf("sending handshake: %w", err)
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil {
		return fmt.Errorf("reading handshake: %w", err)
	}
	if !hs.Ok {
		return fmt.Errorf("daemon refused the handshake: %s", hs.Error)
	}

	if err := enc.Encode(protocol.StatusRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Status:          true,
	}); err != nil {
		return fmt.Errorf("sending status request: %w", err)
	}

	var resp protocol.StatusResponse
	if err := dec.Decode(&resp); err != nil {
		return fmt.Errorf("reading status response: %w", err)
	}

	if *asJSON {
		out, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding status: %w", err)
		}
		fmt.Println(string(out))
		return nil
	}

	printStatus(os.Stdout, resp)
	return nil
}

// printStatus renders a StatusResponse for a human.
//
// The degraded list is printed LAST and unmissably, because it is the one
// thing the reader is most likely to actually need. A status output whose
// important line is buried among healthy ones repeats, in a smaller way, the
// exact failure this cluster is about.
func printStatus(w *os.File, s protocol.StatusResponse) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }

	p("daemon      %s (pid %d, up %s)", s.DaemonVersion, s.PID, formatUptime(s.UptimeSeconds))
	p("workspace   %s", s.Workspace)
	p("model       %s (tier %s)", s.Model, s.Tier)
	p("api key     %s", yesNo(s.APIKeyConfigured, "configured", "NOT configured (requests are sent unauthenticated)"))

	if s.Retrieval.Enabled {
		p("retrieval   semantic ON, lexical %s (top_k=%d budget=%d chars, %d chunk(s) indexed)",
			yesNo(s.Retrieval.Lexical, "ON", "OFF"),
			s.Retrieval.TopK, s.Retrieval.ContextBudgetChars, s.Retrieval.IndexedChunks)
	} else {
		p("retrieval   OFF -- %s", s.Retrieval.Reason)
	}

	p("memory      %s", yesNo(s.MemoryAvailable, "available", "UNAVAILABLE"))

	if len(s.ConfigWarnings) > 0 {
		p("")
		p("config warnings (%d):", len(s.ConfigWarnings))
		for _, warning := range s.ConfigWarnings {
			p("  - %s", warning)
		}
	}

	p("")
	if len(s.Degraded) == 0 {
		p("no degraded subsystems")
		return
	}
	p("DEGRADED (%d):", len(s.Degraded))
	for _, d := range s.Degraded {
		p("  - [%s] %s", d.Component, d.Detail)
	}
}

func yesNo(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

// formatUptime renders seconds as a compact human duration. time.Duration's
// own String() would give "1h2m3.000000004s" for a long-running daemon.
func formatUptime(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
