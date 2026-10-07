package main

import (
	"context"
	"strings"

	"mochiii/daemon/mcp"
)

// WHAT THE AGENT CAN REACH ON THE USER'S MACHINE, SAID TO THE AGENT.
//
// FOUND 2026-10-06, by the owner asking it: "can you perform tasks all over my
// machine?" The answer was a screen of confident detail and wrong where it
// mattered -- "I can only read/write files within the workspace directory",
// "I can touch my-project/src/main.go but not ~/notes.txt", "all operations are
// confined to a temporary, isolated copy" -- while its own tool list, sent with
// that very request, said the opposite: read_file and list_directory take any
// path on the machine after a yes, and propose_edit writes anywhere in the home
// folder after review (outsideread.go, editapply/outside.go). It also offered
// to DELETE files inside the project, which no tool does.
//
// THE CAUSE WAS AN OMISSION, NOT A WRONG SENTENCE. Nothing told the model what
// it could reach. The facts existed only as clauses inside four tool
// descriptions, and a model asked about itself does not read its tool list like
// a contract: it answers with what assistants of its kind usually say, which is
// "I am sandboxed to your project". The base prompt even nudged it there -- its
// one example of a path is "relative/path/to/file.go".
//
// So the reach is stated once, in the system message, in the words a user would
// ask in. Three rules keep it honest:
//
//   - IT IS BUILT FROM THE TOOLS THIS TURN REALLY HAS (the registry, after
//     policy), never from a paragraph that assumes them. Plan mode has no
//     propose_edit; a config may deny read_file. A sentence promising either
//     would be the same defect pointing the other way.
//   - IT SAYS WHAT CANNOT BE DONE, by name. "Delete, move, rename" is the
//     question that follows "create and edit", and with nothing said the model
//     answered yes.
//   - Each claim is pinned to the code that makes it true
//     (TestMachineReachSaysOnlyWhatTheToolsDo), so widening or narrowing a tool
//     without this text fails a test rather than a user's trust.
//
// Agent mode only. A client that cannot answer an approval gets no tools and
// the single-turn request, whose bytes are unchanged.

const (
	builtinReadFileName      = "read_file"
	builtinListDirectoryName = "list_directory"
	builtinProposeEditName   = "propose_edit"
	builtinSandboxExecName   = "sandbox_exec"
)

// machineReachHeader opens the section. The instruction to ANSWER FROM IT is
// the point: the facts alone were already in front of the model.
//
// THE LAST SENTENCE IS A LEASH ON THE FIRST. Told it could read anywhere, the
// model asked to list the user's home folder -- twice, in one turn -- while
// looking for a project file that simply did not exist (run live 2026-10-06).
// Being ABLE to reach a place is not a reason to go there, and each such call
// is a question put to the user that they did not ask for.
const machineReachHeader = "WHAT YOU CAN REACH ON THIS MACHINE -- this is what your tools really do. " +
	"When the user asks what you can do or access here, answer from this list: do not say you are " +
	"limited to the project folder, and do not claim an ability that is not on it. Work inside the " +
	"project unless the request points somewhere else: do not go looking through the user's other " +
	"folders on your own."

// machineReach is the section for a turn in which offered reports the built-in
// tools on the menu. programs is what sandbox_exec will start.
func machineReach(offered func(tool string) bool, mode string, programs []string) string {
	var b strings.Builder
	b.WriteString(machineReachHeader)

	var readers []string
	for _, tool := range []string{builtinReadFileName, builtinListDirectoryName} {
		if offered(tool) {
			readers = append(readers, tool)
		}
	}
	switch len(readers) {
	case 0:
		b.WriteString("\n- You cannot read files or list folders in this turn.")
	default:
		verb := "READ files and list folders"
		if len(readers) == 1 && readers[0] == builtinReadFileName {
			verb = "READ files"
		} else if len(readers) == 1 {
			verb = "LIST folders"
		}
		b.WriteString("\n- " + verb + " ANYWHERE on the machine, not only in the project: give " +
			strings.Join(readers, " or ") + " an absolute or ~/ path. The user is asked first (y/n). " +
			"Always refused, even with a yes: private keys and credential stores (Mochiii's own key and " +
			"memory, editors' secret stores, keyrings), shell histories, and /proc, /sys, /dev and /run " +
			"-- however the path is spelt or linked. Do not look for another way in.")
	}

	switch {
	case !offered(builtinProposeEditName):
		b.WriteString("\n- You cannot create or change files in this turn.")
	case isSpecMode(mode):
		b.WriteString("\n- CREATE and EDIT files under specs/ only, in this mode. Nothing is written " +
			"until the user accepts the change.")
	default:
		b.WriteString("\n- CREATE and EDIT files in the project and ANYWHERE IN THE USER'S HOME FOLDER " +
			"(~/Desktop, ~/Documents, ...): give propose_edit a ~/ path; folders are created as needed. " +
			"Nothing is written until the user accepts the change. Always refused: hidden files and " +
			"folders, ~/bin, .desktop files, and any path outside the home folder.")
	}

	b.WriteString("\n- You CANNOT delete, move or rename a file or folder: no tool does it. Say so, " +
		"and give the user the command to run themselves.")

	if offered(builtinSandboxExecName) {
		b.WriteString("\n- RUN only build and test commands (" + strings.Join(programs, ", ") +
			"), in the project folder: sandbox_exec is not a shell.")
	} else {
		b.WriteString("\n- You cannot run commands in this turn.")
	}
	return b.String()
}

// withMachineReach returns messages with the reach section added to the system
// message, for the tools registry really offers this turn. It is a system note
// like the working copy's (withSystemNote): a copy, and nothing at all on a
// request that has no system message to add it to.
func (s *Server) withMachineReach(ctx context.Context, registry *mcp.Registry, mode string, messages []chatMessage) []chatMessage {
	offered := func(tool string) bool {
		_, policy, err := registry.Lookup(ctx, mcp.BuiltinServerName+mcp.QualifiedNameSeparator+tool)
		return err == nil && policy != mcp.PolicyDeny
	}
	return withSystemNote(messages, machineReach(offered, mode, s.execPrograms()))
}
