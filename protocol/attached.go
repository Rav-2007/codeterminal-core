package protocol

// COMMAND OUTPUT A CLIENT ATTACHES TO ONE QUESTION.
//
// A client's own commands -- /git, /search -- print text the user may want to
// ask about. That text used to reach the model by being left in the transcript
// as an ASSISTANT turn and re-sent as history with every later question: words
// the model never wrote, in its own voice, every time. And they are not the
// user's words either. `git status` prints file and branch names chosen by
// whoever made the repository; a search over saved chats prints whatever those
// chats held.
//
// So the client sends the output ONCE, with the next question, in this field --
// and the DAEMON puts it in front of the model, marked as what it is. The fence
// is the daemon's and not the client's for the same reason every other fence
// is: the daemon is the one place that neutralises a forged tag, scrubs a
// secret and bounds the size, and a client that did those itself would be a
// second copy that could drift.

// FeatureAttachedOutput: PromptRequest.Attached is understood. A daemon without
// it ignores the field, so a client must check before promising the user that
// the output was sent.
const FeatureAttachedOutput = "attached_output"

// AttachedOutput is the output of one local command.
type AttachedOutput struct {
	// Command is the command as the user typed its name: "/git", "/search".
	// The daemon accepts a short lower-case command name and nothing else.
	Command string `json:"command"`
	// Output is what the command printed, as the client showed it.
	Output string `json:"output"`
}
