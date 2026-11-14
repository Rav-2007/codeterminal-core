package main

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"sync/atomic"
)

// WHOSE CHAT IT IS. docs/CHAT_STORAGE_SPLIT_DESIGN_2026-11-14.md.
//
// One daemon serves every client of a workspace, and until schema v6 they all
// wrote one chat: a question asked in the editor was in the terminal's chat, and
// the terminal's fresh start (clients/tui/freshchat.go) erased the editor's. A
// chat now belongs to a SURFACE -- the kind of client that is talking -- and
// every read and write of one is keyed by (workspace, surface).
//
// The surface is DERIVED HERE, from the handshake's client_name, and nothing on
// the wire changed: no field, no client, no protocol version. The names are the
// ones the clients have always sent.

// Surface is the kind of client a chat belongs to.
type Surface string

const (
	// SurfaceTerminal is the terminal app (clients/tui), including one-shot
	// `mochiii -prompt`.
	SurfaceTerminal Surface = "terminal"
	// SurfaceVSCode is the VS Code panel.
	SurfaceVSCode Surface = "vscode"
	// SurfaceOther is any other client. It has a chat of its own, so a name this
	// daemon does not know can never read or write the terminal's or the editor's
	// -- it is NOT a fallback onto either.
	SurfaceOther Surface = "other"
)

// vscodeClientName is the name the VS Code panel connects as
// (clients/vscode/src/chatPanel.ts CLIENT_NAME). terminalClientName, its
// counterpart, lives in mcpplaces.go.
const vscodeClientName = "mochiii-vscode"

// surfaceOf says which surface a client name is.
func surfaceOf(clientName string) Surface {
	switch clientName {
	case terminalClientName:
		return SurfaceTerminal
	case vscodeClientName:
		return SurfaceVSCode
	}
	return SurfaceOther
}

// valid reports whether s is one of the three. The zero value is not.
func (s Surface) valid() bool {
	return s == SurfaceTerminal || s == SurfaceVSCode || s == SurfaceOther
}

// letter is the one character a surface adds to a chat revision's epoch, so a
// revision from one surface's chat is never accepted as another's.
func (s Surface) letter() string {
	switch s {
	case SurfaceTerminal:
		return "t"
	case SurfaceVSCode:
		return "v"
	}
	return "o"
}

// errBadSurface is what every store method answers for a scope whose surface is
// empty or unknown.
var errBadSurface = errors.New("chat storage: the surface is empty or unknown")

// errBadWindow is what they answer for a window that is not a window id, or that
// belongs to a surface which has no windows of its own.
var errBadWindow = errors.New("chat storage: the window id is malformed, or this surface has no windows")

// windowPattern is the whole of a window id: 8 to 32 lowercase hex digits. It is
// matched before an id is used for anything, because it ends up in a file name
// (current-<window>.json) and a SQL parameter, and "hex, bounded" is a form that
// can be neither a path nor a surprise.
var windowPattern = regexp.MustCompile(`^[0-9a-f]{8,32}$`)

// windowFor is the window a connection's chat belongs to: the id the client sent,
// if the surface has windows (only the terminal does -- the editor's windows share
// one chat on purpose) and the id is well formed; otherwise "", the surface's
// shared chat. A bad id is ignored rather than refused: refusing would turn a
// client's bug into a handshake failure, and the shared chat is the safe place for
// a connection that cannot say which window it is.
func windowFor(surface Surface, id string) string {
	if surface == SurfaceTerminal && windowPattern.MatchString(id) {
		return id
	}
	return ""
}

// chatScope names one chat: a workspace's chat as one kind of client keeps it,
// and, for the terminal, as one of its windows keeps it. Window is "" for the
// surface's shared chat -- everything but a terminal that sent a window id.
//
// Every MemoryStore method that reads or writes chat turns takes one, and
// refuses a scope whose surface is not valid, so a caller that forgets the
// surface fails to compile and one that passes a zero surface fails loudly.
type chatScope struct {
	Workspace string
	Surface   Surface
	Window    string
}

// check is the store's guard on every call.
func (c chatScope) check() error {
	if !c.Surface.valid() {
		return errBadSurface
	}
	if c.Window != "" && (c.Surface != SurfaceTerminal || !windowPattern.MatchString(c.Window)) {
		return errBadWindow
	}
	return nil
}

type surfaceKey struct{}

// withSurface carries the connection's surface in its context. Set once, in
// serveConn, from the handshake.
func withSurface(ctx context.Context, s Surface) context.Context {
	return context.WithValue(ctx, surfaceKey{}, s)
}

type windowKey struct{}

// withWindow carries the connection's window the same way (already passed through
// windowFor, so it is "" or a well-formed id on a surface that has windows).
func withWindow(ctx context.Context, window string) context.Context {
	return context.WithValue(ctx, windowKey{}, window)
}

// windowFrom is the window a connection's context carries; "" when it carries
// none, which is the shared chat.
func windowFrom(ctx context.Context) string {
	w, _ := ctx.Value(windowKey{}).(string)
	return w
}

// surfaceFrom is the surface a connection's context carries. A context that
// carries none (a test, or a path that did not come from a connection) is
// SurfaceOther, never a real chat.
func surfaceFrom(ctx context.Context) Surface {
	if s, ok := ctx.Value(surfaceKey{}).(Surface); ok && s.valid() {
		return s
	}
	return SurfaceOther
}

// scope is the chat this connection's requests are about.
func (s *Server) scope(ctx context.Context) chatScope {
	return chatScope{Workspace: s.workspace, Surface: surfaceFrom(ctx), Window: windowFrom(ctx)}
}

// chatState is one chat's share of what used to be daemon-wide: the lock that
// serialises everything reading or replacing it, and the count of times it was
// replaced (chatsync.go). One per chat -- per surface, and per terminal window --
// so a /compact in the terminal (which holds the lock across a model call) does
// not stop the editor's turn from being saved, and a ctrl+n in one window does not
// make another redraw.
type chatState struct {
	mu  sync.Mutex
	gen atomic.Uint64
}

// chatOf is the state of scope's chat, made on first use. The map only grows by
// one small entry per window that ever talked to this daemon.
func (s *Server) chatOf(sc chatScope) *chatState {
	key := string(sc.Surface) + "/" + sc.Window
	if v, ok := s.chats.Load(key); ok {
		return v.(*chatState)
	}
	v, _ := s.chats.LoadOrStore(key, new(chatState))
	return v.(*chatState)
}
