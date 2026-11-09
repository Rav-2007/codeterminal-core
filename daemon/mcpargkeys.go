package main

import (
	"errors"
	"fmt"
	"strings"

	"mochiii/protocol"
)

// A KEY ON A SERVER'S COMMAND LINE: the rule is protocol.FindArgKeys (shared
// with the terminal client, which refuses before sending). What the daemon
// does with it:
//
//   - a new or changed server with one is refused (refuseArgKeys), on every
//     way in: add, add_json, update -- the CLI, the TUI and VS Code alike;
//   - one already stored keeps working -- refusing it at load would stop a
//     server the user relies on -- but its key is masked wherever its command
//     line is shown or logged (commandLine, serverInfos, previewInfo), and it
//     carries a warning saying how to move it (argKeyWarning).

// maskArgKeys is what is shown and logged for a command line's arguments.
func maskArgKeys(args []string) []string { return protocol.MaskArgKeys(args) }

// refuseArgKeys refuses a new or changed server whose command line holds a key.
func refuseArgKeys(server string, args []string) error {
	keys := protocol.FindArgKeys(args)
	if len(keys) == 0 {
		return nil
	}
	return errors.New(protocol.ArgKeyRefusal(server, keys[0]))
}

// argKeyWarning is said about a server stored before refuseArgKeys existed,
// whose command line still holds a key: shown masked, kept as written.
func argKeyWarning(server, where string, args []string) []string {
	keys := protocol.FindArgKeys(args)
	if len(keys) == 0 {
		return nil
	}
	k := keys[0]
	return []string{fmt.Sprintf("its command line holds a key (%s), shown here as %s but stored as written in %s; "+
		"change the argument to %s and give the value with /mcp-server key %s %s",
		strings.TrimSuffix(k.What, "="), protocol.ArgKeyMask, where, k.Templated(), server, k.Name)}
}
