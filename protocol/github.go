package protocol

// GITHUB: WHAT MOCHIII DOES ON THE PROJECT'S GITHUB REPOSITORY.
//
// The owner asked on 2026-10-14 whether Mochiii can read issues and CI logs,
// push, open pull requests and merge, the way GitHub-connected agents do. The
// design they approved on 2026-10-15 rests on one rule: THE MODEL READS, THE
// DAEMON ACTS, THE OWNER DECIDES. So:
//
//   - The token is the daemon's. A client sends it once, when the user pastes
//     it, and never sees it again; the model never sees it at all.
//   - Mochiii acts as its own GitHub account (a bot), so the owner keeps
//     approving its pull requests as themselves.
//   - Reading is a request a client makes for the user, or a tool the model
//     calls. Writing -- later phases -- is a proposal the user reads word for
//     word before the daemon sends it.
//
// One request type carries every action, told apart by Action, so the socket
// has one discriminator for all of it ("github").

// FeatureGitHub: GitHubRequest is understood.
const FeatureGitHub = "github"

// What a GitHubRequest asks for.
const (
	// GitHubActionStatus is what an empty Action means: who Mochiii is on
	// GitHub, and which repository this project's is.
	GitHubActionStatus = "status"
	// GitHubActionConnect stores Token after checking it with GitHub.
	GitHubActionConnect = "connect"
	// GitHubActionUse sets this project's repository from its origin remote,
	// with the token already stored.
	GitHubActionUse = "use"
	// GitHubActionDisconnect forgets the stored token.
	GitHubActionDisconnect = "disconnect"
	// GitHubActionIssue is one issue and its comments (Number).
	GitHubActionIssue = "issue"
	// GitHubActionIssues is the open issues, Label narrowing them.
	GitHubActionIssues = "issues"
	// GitHubActionPR is one pull request: its checks, reviews and whether it
	// can merge (Number).
	GitHubActionPR = "pr"
	// GitHubActionPRs is the open pull requests.
	GitHubActionPRs = "prs"
	// GitHubActionLog is the failure in a pull request's CI (Number): the
	// lines that name it, not the whole log.
	GitHubActionLog = "log"
)

// GitHubRequest asks the daemon to do one thing on GitHub for the user.
type GitHubRequest struct {
	ProtocolVersion int `json:"protocol_version"`

	// GitHub is the discriminator, like RulesRequest.Rules.
	GitHub bool `json:"github"`

	// Action is one of the GitHubAction* constants; empty means status.
	Action string `json:"action,omitempty"`

	// Number is the issue or pull request.
	Number int `json:"number,omitempty"`

	// Label narrows GitHubActionIssues.
	Label string `json:"label,omitempty"`

	// Token is the token to store, for GitHubActionConnect only. The daemon
	// never logs it, never echoes it, and never sends it anywhere but GitHub.
	Token string `json:"token,omitempty"`
}

// GitHubResponse is the daemon's answer.
type GitHubResponse struct {
	ProtocolVersion int `json:"protocol_version"`

	// Text is the answer, for the user to read: plain lines, which a client
	// still sanitises before drawing -- most of it was written on GitHub.
	Text string `json:"text,omitempty"`

	// Attach says Text is worth sending to the model with the user's next
	// question: an issue, a pull request, a failure. Not set for a status line
	// or a refusal.
	Attach bool `json:"attach,omitempty"`

	// Connected, Login and Repo say where things stand after the action:
	// whether a token is stored, whose it is, and this project's repository
	// ("owner/name", empty when none is set).
	Connected bool   `json:"connected,omitempty"`
	Login     string `json:"login,omitempty"`
	Repo      string `json:"repo,omitempty"`

	// Error, when set, is why nothing was done. Written for the user.
	Error string `json:"error,omitempty"`
}
