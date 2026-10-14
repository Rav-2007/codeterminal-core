package protocol

// RULES THE USER SAVED FOR A PROJECT.
//
// A correction made in one chat used to end with that chat. "Tests here are run
// with make check, not go test" was learned, used, and gone the next morning:
// nothing the user said in one conversation reached the next, and the daemon
// read no file of standing instructions. A rule is one line the user chose to
// keep; the daemon puts every saved rule in front of the model at the start of
// each turn in that project, in whichever client the turn comes from.
//
// THE DAEMON KEEPS THEM, per project and outside it (daemon/userrules.go has
// where and why): a client only asks. So a rule saved in the terminal applies
// in the editor too, and neither client has to remember anything.

// FeatureRules: RulesRequest is understood, and saved rules reach the model.
// In a file of its own with the request, like FeatureAttachedOutput, so
// protocol.go keeps its lines (workspacetag.go).
const FeatureRules = "rules"

// What a RulesRequest asks for.
const (
	RulesActionList   = "list"
	RulesActionAdd    = "add"
	RulesActionRemove = "remove"
)

// RulesRequest reads or changes the rules saved for the project this daemon
// serves.
type RulesRequest struct {
	ProtocolVersion int `json:"protocol_version"`

	// Rules is the discriminator, like MCPRequest.MCP.
	Rules bool `json:"rules"`

	// Action is RulesActionList (also what empty means), RulesActionAdd or
	// RulesActionRemove.
	Action string `json:"action,omitempty"`

	// Text is the rule to add: one line, the user's own words.
	Text string `json:"text,omitempty"`

	// Number is the rule to remove, counted from 1 in the order a list gives.
	Number int `json:"number,omitempty"`
}

// RulesResponse is the daemon's answer: the rules as they now stand.
type RulesResponse struct {
	ProtocolVersion int `json:"protocol_version"`

	// Rules is every saved rule after the action, in the order they are sent
	// to the model. Empty when there are none.
	Rules []string `json:"rules"`

	// Removed is the rule a remove took out.
	Removed string `json:"removed,omitempty"`

	// Unused counts lines of the file that are not in Rules because they are
	// over a limit. Only a file edited by hand can have any.
	Unused int `json:"unused,omitempty"`

	// Path is the file the rules are kept in, for a user who wants to read or
	// edit it directly.
	Path string `json:"path,omitempty"`

	// Error, when set, is why nothing was changed. Written for the user.
	Error string `json:"error,omitempty"`
}
