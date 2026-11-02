package main

import (
	"context"
	"fmt"
	"strings"

	"mochiii/protocol"
)

// WHERE THE USER MANAGES MCP SERVERS, in the words of the client they use.
//
// FOUND 2026-11-01: a user pasted Figma's "mcpServers" entry into the terminal
// app and asked nothing else. The model told them to add it "in Customize >
// MCP (from the Marketplace or Add Server)" -- a page only the VS Code client
// has -- because that is what the setup section said to every client. The
// terminal's way is /mcp-server, and the entry was one command away:
// /mcp-server add figma --transport http https://mcp.figma.com/mcp.
//
// So every place the setup section, or a notice, sends the user goes through
// mcpPlaces. A client is the terminal app only by the name it gives at the
// handshake; any other keeps the words it was always given.

// terminalClientName is the name the terminal app (clients/tui) connects as.
const terminalClientName = "mochiii-tui"

type clientNameKey struct{}

// withClientName carries the handshake's client name into the turn's context.
func withClientName(ctx context.Context, hs protocol.HandshakeRequest) context.Context {
	return context.WithValue(ctx, clientNameKey{}, hs.ClientName)
}

// mcpPlaces says where, in one client, the user does each thing to a server.
type mcpPlaces struct{ terminal bool }

// mcpPlacesFor is the places of the client this turn came from.
func mcpPlacesFor(ctx context.Context) mcpPlaces {
	name, _ := ctx.Value(clientNameKey{}).(string)
	return mcpPlaces{terminal: name == terminalClientName}
}

// pages is where the user sees Mochiii's own setup.
func (p mcpPlaces) pages() string {
	if p.terminal {
		return "with /account, /model and /mcp"
	}
	return "in Mochiii's own pages (Account, Customize, Settings)"
}

// list is where the servers the user added are listed.
func (p mcpPlaces) list() string {
	if p.terminal {
		return "/mcp, a panel in the terminal app"
	}
	return "Customize > MCP"
}

// add is how a server is added, finishing "The user adds them ...".
func (p mcpPlaces) add() string {
	if p.terminal {
		return "in /mcp: it opens a panel that takes a server's JSON pasted straight in from its setup page (a whole " +
			"\"mcpServers\" block of several is fine), or a command or a URL after a press of a (or with /mcp-server " +
			"add <name> <command> [args…], or /mcp-server add <name> --transport http <url> for a remote server)"
	}
	return "in Customize > MCP (its Marketplace, or Add Server)"
}

func (p mcpPlaces) approve(name string) string {
	if p.terminal {
		return "/mcp-server approve " + name
	}
	return "Approve on it in Customize > MCP, or `mcp approve`"
}

func (p mcpPlaces) signIn(name string) string {
	if p.terminal {
		return "/mcp-server sign-in " + name + ", which gives them the address to open in their browser"
	}
	return "Sign in on the server in Customize > MCP, which opens their browser"
}

// setKey is how the user gives name the values of vars, typed hidden.
func (p mcpPlaces) setKey(name string, vars []string) string {
	if p.terminal {
		return commandPerValue("/mcp-server key "+name+" ", vars) + ", which asks for the value without showing it"
	}
	return "Set key on " + name + " in Customize > MCP"
}

// setHeader is setKey for a remote server's headers.
func (p mcpPlaces) setHeader(name string, headers []string) string {
	if p.terminal {
		return commandPerValue("/mcp-server header "+name+" ", headers) + ", which asks for the value without showing it"
	}
	return "Set key on " + name + " in Customize > MCP"
}

// change is how a server's command or arguments are corrected. The terminal
// has no edit: the server is removed and added again.
func (p mcpPlaces) change(name string) string {
	if p.terminal {
		return "/mcp-server remove " + name + " and then /mcp-server add it again with the right arguments"
	}
	return "Change on the server in Customize > MCP"
}

// pasteRule is said to the terminal app's model on every turn. Pasting a
// server's JSON into the chat adds nothing, and the model cannot add one; what
// it can do is turn the entry into the command that does. The GUI's Add Server
// takes JSON itself, so its model needs no rule.
func (p mcpPlaces) pasteRule() string {
	if !p.terminal {
		return ""
	}
	return "\n- You cannot add, change or remove an MCP server: only the user can, in /mcp -- a panel that lists " +
		"every server with its state, where they add one, give it a key, sign in, pause or remove it. If the user " +
		"pastes a server's configuration -- an \"mcpServers\" entry from a setup page or another app -- pasting " +
		"it into the chat added nothing: tell them to open /mcp and paste it there (a block of several servers is " +
		"fine; each is shown, and added when its name is typed), or give them the command it becomes. An entry " +
		"with a \"url\" is /mcp-server add <name> --transport http <url> (sse instead of http only when its " +
		"\"type\" says sse), followed by /mcp-server sign-in <name> when the server signs users in (most hosted " +
		"ones, such as Figma's, do); a header it needs is --header <Header-Name>, its value given afterwards with " +
		"/mcp-server header <name> <Header-Name>. An entry with a \"command\" is /mcp-server add <name> --env " +
		"<VARIABLE> (once per variable in its \"env\") <command> <args…>, followed by /mcp-server key <name> " +
		"<VARIABLE> for each one whose value it needs. Never put a key's or token's value in a command line, and " +
		"never repeat one the user pasted: those commands ask for it without showing it."
}

// commandPerValue is prefix+value for each value, joined as the user would
// type them one after another.
func commandPerValue(prefix string, values []string) string {
	cmds := make([]string, 0, len(values))
	for _, v := range values {
		cmds = append(cmds, prefix+v)
	}
	if len(cmds) <= 1 {
		return strings.Join(cmds, "")
	}
	return fmt.Sprintf("%s, then %s", strings.Join(cmds[:len(cmds)-1], ", "), cmds[len(cmds)-1])
}
