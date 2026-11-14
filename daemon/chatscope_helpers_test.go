package main

import "context"

// tscope is the chat the existing tests use: workspace ws, on the surface a test
// client reaches ("test" is not a name the daemon knows, so surfaceOf gives
// SurfaceOther). Tests of the split itself name their surfaces.
func tscope(ws string) chatScope { return chatScope{Workspace: ws, Surface: SurfaceOther} }

// testScope is the chat a bare context -- one that came from no connection --
// refers to on this server: the same one, so a test that writes through a
// Server method and reads through a connection sees one chat.
func (s *Server) testScope() chatScope { return s.scope(context.Background()) }

// The turns table and its workspace index as they were through schema v5, for
// the tests that build an OLD database by hand and let the store migrate it.
const (
	legacyTurnsTableDDL = `
CREATE TABLE IF NOT EXISTS turns (
	id         INTEGER PRIMARY KEY,
	workspace  TEXT NOT NULL,
	role       TEXT NOT NULL,
	content    TEXT NOT NULL,
	created_at TEXT NOT NULL
)`
	legacyTurnsWorkspaceIndexDDL = `
CREATE INDEX IF NOT EXISTS idx_turns_workspace_id ON turns(workspace, id)`
)
