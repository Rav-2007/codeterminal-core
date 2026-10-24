package protocol

// MODEL SERVERS ON THIS MACHINE (1.3.0).
//
// Ollama, LM Studio, llama.cpp and vLLM all serve the OpenAI chat API, and the
// daemon could always be pointed at one with MOCHIII_API_BASE. What this adds is
// for the terminal app: finding a server that is running, connecting to it with
// no key, and fitting what is sent into the model's context window, which a
// local server gives a fraction of what the model can read (Ollama: 4096 tokens
// of 32768 for qwen2.5-coder, measured 2026-10-23) and cuts without saying so.
//
// Everything here is something a client asks for. A client that never asks --
// the VS Code extension -- sees the daemon behave exactly as before.

// FeatureLocalModels: this daemon understands LocalRequest, connects to a
// server on this machine with no key, and honours CapNoNetworkTools. A client
// talking to a daemon without it must not rely on any of the three.
const FeatureLocalModels = "local_models"

// FeatureLocalInForce is named in a handshake while the address prompts go to
// is a server on this machine. A client declares its capabilities before it
// sees the answer, so it uses this to notice it declared the wrong ones and
// connect again -- which only ever happens with a local model in force.
const FeatureLocalInForce = "local_model_in_force"

// CapNoNetworkTools is a handshake capability: in agent mode, neither offer this
// client's model a tool that reaches the internet nor run one it names anyway.
// The terminal app declares it for a model on this machine unless the user has
// turned web tools on, so a private setup stays private by default.
const CapNoNetworkTools = "no_network_tools"

// AN AGENT THAT FITS THE MODEL (1.4.0). Whether a model can drive the agent is
// a property of that model -- measured 2026-10-24, a 3B coder model Ollama
// lists with "tools" made no valid tool call in 15 tries -- so the terminal app
// asks the daemon to try (LocalRequest.Check) and, from the answer, asks each
// question as chat, the compact agent (CapCompactAgent) or the full agent.

// FeatureLocalAgentCheck: this daemon answers LocalRequest.Check and honours
// CapCompactAgent.
const FeatureLocalAgentCheck = "local_agent_check"

// CapCompactAgent is a handshake capability: in agent mode, give this client's
// model short instructions and the five tools a coding question needs, rather
// than the full agent's, which do not fit a small context window.
const CapCompactAgent = "compact_agent"

// DegradedLocalNoTools names the notice that the model's server refused tools,
// so the question was answered without them.
const DegradedLocalNoTools = "local_no_tools"

// DegradedLocalToolsStopped names the notice that the model made tool calls in
// a row that could not run, so the rest of the turn was asked without tools.
const DegradedLocalToolsStopped = "local_tools_stopped"

// LocalRequest asks about model servers on this machine. Local is the
// discriminator; one of Discover, Fit and Check says what to do.
type LocalRequest struct {
	ProtocolVersion int  `json:"protocol_version"`
	Local           bool `json:"local"`
	// Discover lists the servers answering on this machine's usual ports.
	Discover bool `json:"discover,omitempty"`
	// Fit finds the context window of the model in force, when the daemon's
	// address is a server on this machine, and fits the code context into it.
	Fit bool `json:"fit,omitempty"`
	// Check asks the server what the model in force can do and tries whether
	// it calls tools, and says how it should work (LocalCheck).
	Check bool `json:"check,omitempty"`
}

// The ways a model on this machine can work, in LocalCheck.Way.
const (
	LocalWayChat    = "chat"    // no tools: the code that fits, and edits written as text
	LocalWayCompact = "compact" // the compact agent: CapCompactAgent
	LocalWayFull    = "full"    // the full agent
)

// LocalCheck is what Check found about the model in force.
type LocalCheck struct {
	APIBase string `json:"api_base"`
	Kind    string `json:"kind"`
	Model   string `json:"model"`
	Window  int    `json:"window"`
	// Params is the model's size as the server names it ("7.6B"); "" if unknown.
	Params string `json:"params,omitempty"`
	// Tools is what the server says about the model's tool calling: "yes",
	// "no", or "" when it does not say.
	Tools string `json:"tools,omitempty"`
	// Tried and Passed count the test requests: how many were made, and how
	// many came back as a valid call. Tried is 0 when the server's own answer
	// settled it.
	Tried  int `json:"tried"`
	Passed int `json:"passed"`
	// FullTried and FullPassed are the same with the full agent's
	// instructions, tried only when the window holds the full agent: a model
	// that calls tools under short instructions may not under the full ones.
	FullTried  int `json:"full_tried,omitempty"`
	FullPassed int `json:"full_passed,omitempty"`
	// Way is how the model should work: LocalWayChat, LocalWayCompact or
	// LocalWayFull. Reason says why, in a sentence for the person.
	Way    string `json:"way"`
	Reason string `json:"reason"`
	// Raise says how to give the model more room on this server, "" if unknown.
	Raise string `json:"raise,omitempty"`
}

// LocalServer is one model server found on this machine.
type LocalServer struct {
	// Kind is "ollama", "lmstudio", "llamacpp" or "vllm"; "" for a server that
	// speaks the API but could not be told apart.
	Kind string `json:"kind"`
	// Name is how it is written in a sentence: "Ollama".
	Name string `json:"name"`
	// APIBase is its OpenAI-compatible address: http://127.0.0.1:11434/v1.
	APIBase string `json:"api_base"`
	// Models are the chat models it lists, as the server names them.
	Models []string `json:"models,omitempty"`
}

// LocalContext is what Fit found.
type LocalContext struct {
	APIBase string `json:"api_base"`
	Kind    string `json:"kind"`
	Model   string `json:"model"`
	// Window is the context the server gives the model now, in tokens; 0 when
	// it could not be told.
	Window int `json:"window"`
	// Trained is the most the model can read, when the server says; 0 if not.
	Trained int `json:"trained,omitempty"`
	// BudgetChars is the code context the daemon now sends this model, and
	// Fitted reports that it was cut down to fit Window.
	BudgetChars int  `json:"budget_chars"`
	Fitted      bool `json:"fitted,omitempty"`
	// AgentFits reports that agent mode's request -- the tool list and its
	// instructions -- fits Window. False when Window is unknown.
	AgentFits bool `json:"agent_fits"`
	// Raise says how to give the model more room on this server, "" if unknown.
	Raise string `json:"raise,omitempty"`
}

// LocalResponse answers a LocalRequest.
type LocalResponse struct {
	Ok      bool          `json:"ok"`
	Error   string        `json:"error,omitempty"`
	Servers []LocalServer `json:"servers,omitempty"`
	Context *LocalContext `json:"context,omitempty"`
	Check   *LocalCheck   `json:"check,omitempty"`
}
