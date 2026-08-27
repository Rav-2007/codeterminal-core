// Package mcp is the daemon's tool surface: the types every tool is described
// by, the environment discipline every external server is launched under, and
// the Client seam the official MCP SDK sits behind.
//
// TWO LANES, ONE SURFACE.
//
// Tools reach the model from two places that share nothing but this package's
// types:
//
//   - Lane A ("builtin"): Go functions compiled into the daemon. Confined,
//     because there is no subprocess to escape and the five gates are on the
//     actual call path. None of them writes to the filesystem -- the write tool
//     proposes an edit into the existing human review flow.
//   - Lane B (external): MCP servers spawned as subprocesses and spoken to over
//     stdio via github.com/modelcontextprotocol/go-sdk. Ordinary processes with
//     the user's full privileges. NOT confined, and this package never pretends
//     otherwise -- see Tool.Confined.
//
// The registry unifies them so the loop has one Tools()/Call() surface, and the
// approval prompt carries the lane so a human can see which one they are
// authorising.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Tool is one callable tool as advertised to the model and described to the
// user. It is deliberately flat and provider-neutral: the loop turns it into an
// OpenAI-shaped tool spec, and the approval prompt turns it into a sentence.
type Tool struct {
	// Server is the source: "builtin" for Lane A, or the user's configured name
	// for a Lane B server. It is the user's word for the thing, never the
	// server's own claim about itself.
	Server string
	Name   string
	// Description and Schema go to the model. Schema is a JSON Schema object
	// describing the arguments.
	Description string
	Schema      json.RawMessage

	// Lane is protocol.LaneFirstParty or protocol.LaneThirdParty, and Confined
	// says whether this call's effects are constrained by the five-gate
	// pipeline. Both reach the user on the approval prompt, so neither may ever
	// be optimistic: Confined is false for every Lane B tool, unconditionally.
	Lane     string
	Confined bool

	// ExecutesCode marks a tool that runs arbitrary code by design.
	//
	// TWO PROPERTIES OF THIS FILE'S SECURITY MODEL ARE WRONG FOR SUCH A TOOL,
	// and they are wrong in different places, so one flag names the class once:
	//
	//   - Confined is a STATIC property of the lane for every other built-in,
	//     and a DYNAMIC property of the host for this one. RegisterBuiltin
	//     therefore stops asserting it here and takes what the tool resolved.
	//   - "Allow for the rest of this turn" means "this tool" everywhere else,
	//     which is exactly what a user choosing it intends for read_file. For a
	//     tool that executes code it would mean approving one command and
	//     authorising every later one, unseen -- so the grant is bound to the
	//     arguments as well (see agentTurn.grant).
	//
	// It is a property of the TOOL rather than of the lane because a first-party
	// tool broke the "first-party implies confined" assumption; naming the class
	// is what stops the next one breaking it silently.
	ExecutesCode bool

	// ReachesNetwork marks a tool that sends data to, and receives data from,
	// a host outside this machine.
	//
	// IT EXISTS FOR THE SAME REASON ExecutesCode DOES, and it was added the
	// same way: by a first-party tool breaking the "first-party implies
	// confined" assumption in a NEW direction. ExecutesCode named the tool
	// whose effects escape sideways, into the host. This names the tool whose
	// effects escape OUTWARD, onto the wire.
	//
	// web_search and web_fetch run no subprocess and write no file, so every
	// test RegisterBuiltin applies would have passed them and stamped them
	// Confined -- and the approval prompt would have told the user "anything it
	// changes goes through the same review you use for edits" about a call that
	// posts their query to a third party and pulls an attacker-controlled
	// document into the model's context. Nothing is "changed", so the sentence
	// is not even false; it is just answering a question nobody asked. The
	// question the user is actually asking at that prompt is "where does this
	// go", and Confined was about to answer it wrong by omission.
	//
	// So confinement is not asserted for these either. The daemon confines what
	// it can (see daemon/webfetch.go: scheme allow-list, private-address
	// refusal re-checked across every redirect, byte cap, timeout, outbound
	// scrub) and says so in the description; what it cannot confine is the
	// remote end, and the prompt says that too.
	ReachesNetwork bool

	// ReadOnlyHint and Destructive are the SERVER'S OWN claims about its tool,
	// carried for display so a client can style the prompt.
	//
	// They are never a gate. A server asserting readOnlyHint:true gets exactly
	// the policy its configuration says it gets. Letting a self-description
	// lower the bar would make consent optional for any server willing to lie,
	// which is the entire population that matters.
	ReadOnlyHint bool
	Destructive  bool
}

// QualifiedName is how a tool is named on the wire to the model. Server-scoped
// because two servers may legitimately both offer "read_file", and a collision
// that silently resolved to one of them would route a user's approval to a tool
// they did not authorise.
func (t Tool) QualifiedName() string { return t.Server + "__" + t.Name }

// SplitQualifiedName reverses QualifiedName. A name the daemon did not generate
// is refused rather than guessed at: the model is the one supplying this string
// back to us, and it is the key we dispatch on.
func SplitQualifiedName(qualified string) (server, tool string, err error) {
	server, tool, found := strings.Cut(qualified, "__")
	if !found || server == "" || tool == "" {
		return "", "", fmt.Errorf("%q is not a qualified tool name (want <server>__<tool>)", qualified)
	}
	return server, tool, nil
}

// Result is one tool call's output.
//
// Content is the text fed back to the model -- ALWAYS after scrubbing and
// truncation by the caller (daemon/toolresult.go). This package never decides
// what leaves the machine; it only reports what the tool produced.
//
// IsError marks a call the tool itself refused or failed, as distinct from a
// transport failure. Both end up back with the model, because a model that
// learns its call failed can recover, whereas one that learns nothing repeats
// the call.
type Result struct {
	Content string
	IsError bool
}

// Client is the narrow seam the MCP SDK sits behind: three methods, so the
// implementation stays swappable and every test can use a fake instead of
// spawning a process.
//
// The interface is this small on purpose. MCP is a large and moving protocol
// (the 2026-07-28 revision deprecated logging, sampling and roots in favour of
// MRTR); binding the daemon to three verbs means a spec change lands in one
// adapter rather than across the loop.
type Client interface {
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, args json.RawMessage) (Result, error)
	Close() error
}

// ErrServerUnavailable is returned when a server is not running and cannot be
// started. The loop turns it into a tool-role error the model can read, and a
// protocol.DegradedMCPServer notice the user can read.
var ErrServerUnavailable = errors.New("mcp server unavailable")

// BuiltinServerName is the reserved Server value for Lane A tools. Reserved:
// ValidateServerName refuses it for a configured server, so a user cannot
// shadow the confined tools with unconfined ones of the same name.
const BuiltinServerName = "builtin"

// QualifiedNameSeparator joins a server name to a tool name. Spelled once, so
// the three places that build a qualified name and the two that split one
// cannot drift -- an earlier bug used "." in a test and the mismatch was
// invisible because the wrong name was simply refused.
const QualifiedNameSeparator = "__"

// ForbiddenEnvNames are variables that must never reach an MCP server,
// whatever a config file's env allow-list says.
//
// The allow-list exists so a server can be given a credential of its own
// (GITHUB_TOKEN for a GitHub server, say). These are different: they are THIS
// PRODUCT'S credentials for the inference path. A server holding
// CODETERMINAL_MOCHIII_KEY can spend the user's quota; one holding
// OPENROUTER_API_KEY can spend their money directly. No legitimate MCP server
// needs either, so the allow-list is not permitted to grant them and a config
// that asks is refused rather than quietly obeyed.
var ForbiddenEnvNames = []string{
	"OPENROUTER_API_KEY",
	"CODETERMINAL_API_KEY",
	"CODETERMINAL_MOCHIII_KEY",
}

// IsForbiddenEnvName reports whether name is one this daemon refuses to pass to
// a subprocess. Matched case-insensitively: environment variables are
// case-sensitive on Unix, but a config typo'd as "openrouter_api_key" is a
// request for the same secret and refusing it costs nothing.
func IsForbiddenEnvName(name string) bool {
	for _, forbidden := range ForbiddenEnvNames {
		if strings.EqualFold(name, forbidden) {
			return true
		}
	}
	return false
}

// ServerEnv builds the environment for an MCP server subprocess.
//
// This is the direct descendant of daemon/helperproc.go's helperEnv(), and it
// exists for the same reason: exec.Cmd with a nil Env inherits the DAEMON'S
// ENTIRE ENVIRONMENT, which is where the model-API keys live. A server that
// never needed them would hold them anyway, and a hostile one would only have
// to read os.Environ().
//
// BaselineEnvNames pass through unconditionally: the variables a process needs
// to run AT ALL on the host platform. Everything else must be named in allow,
// and nothing in ForbiddenEnvNames is grantable at all.
//
// The list was POSIX-only, and on Windows that is not a tight allow-list -- it
// is a broken one. HOME does not exist there, so an MCP server was handed PATH
// and nothing else: no home directory, no temp directory, no PATHEXT (so its
// OWN child-process lookups fail), no APPDATA. Node- and Python-based servers,
// which is most of them, do not start. Nothing in the Windows additions is a
// credential; they are the same category as HOME and PATH, spelled the way that
// platform spells them.
//
// SYSTEMROOT IS LISTED BECAUSE IT ARRIVES WHETHER OR NOT WE LIST IT. os/exec's
// addCriticalEnv appends SYSTEMROOT to every Cmd on Windows if it is absent
// (exec.go, `// We already have it.`), because too much of Win32 breaks without
// it. It is therefore below this scrubber and outside its control. Naming it
// here makes the contract honest rather than leaving a variable in the child
// that this function claims it did not pass -- the first Windows CI run failed
// TestRealServerNeverSeesCredentials on exactly that discrepancy.
//
// Verified against a real server in the Phase 2 spike: on Linux the child saw
// exactly HOME, PATH and the one allow-listed name.
var BaselineEnvNames = baselineEnvNames()

func baselineEnvNames() []string {
	if runtime.GOOS == "windows" {
		return []string{
			"PATH",
			"SYSTEMROOT",  // injected by os/exec regardless; see above
			"USERPROFILE", // the HOME analogue
			"TEMP", "TMP", // no /tmp to fall back on
			"PATHEXT",                 // without it the child's own exec lookups find nothing
			"APPDATA", "LOCALAPPDATA", // where a Windows program keeps its state
			"COMSPEC", // npm and friends shell out through it
		}
	}
	return []string{"PATH", "HOME"}
}

func ServerEnv(allow []string) []string {
	var env []string
	seen := map[string]bool{}

	add := func(name string) {
		if seen[name] || IsForbiddenEnvName(name) {
			return
		}
		if v, ok := os.LookupEnv(name); ok {
			seen[name] = true
			env = append(env, name+"="+v)
		}
	}

	for _, name := range BaselineEnvNames {
		add(name)
	}
	for _, name := range allow {
		add(name)
	}
	return env
}

// ValidateEnvAllowList reports why an env allow-list cannot be honoured, or nil
// if it can. Called at config-validation time so a refused variable is a
// startup error naming the line, not a silent omission discovered later.
func ValidateEnvAllowList(allow []string) error {
	for _, name := range allow {
		if IsForbiddenEnvName(name) {
			return fmt.Errorf("env entry %q is one of this product's own inference credentials and is never "+
				"passed to an MCP server: a server holding it could spend your quota or your money. "+
				"If the server needs a credential of its own, give it that variable instead", name)
		}
	}
	return nil
}

// ValidateServerName refuses names that would make the wire ambiguous or
// shadow the built-in lane.
func ValidateServerName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return errors.New("server name is empty")
	case name == BuiltinServerName:
		return fmt.Errorf("server name %q is reserved for this daemon's own confined tools; "+
			"an external server using it could shadow them with unconfined ones", BuiltinServerName)
	case strings.Contains(name, "__"):
		return fmt.Errorf("server name %q contains %q, which separates server from tool in a qualified "+
			"tool name and would make the two impossible to tell apart", name, "__")
	}
	return nil
}

// ValidateToolName refuses a server-supplied tool name that could not safely be
// shown to a human.
//
// WHY A TOOL NAME IS A SECURITY-RELEVANT STRING, which is not obvious. A tool
// RESULT goes only to the model -- the daemon never sends result content to a
// client, and protocol.ToolActivity carries a byte count rather than bytes. A
// tool NAME goes somewhere else entirely: onto the approval prompt, rendered in
// the user's terminal, BEFORE they consent, on the same panel that carries the
// "NOT SANDBOXED" warning. An unconfined third-party subprocess therefore gets
// to put arbitrary bytes on the screen a human is reading in order to decide
// whether to trust it. A CSI sequence there does not merely look odd: \x1b[1A
// and \x1b[2K move the cursor up and erase the line, which is enough to redraw
// the security notice above it. A bare \r overwrites the line already drawn.
//
// REFUSED, NOT SANITISED. A name is the key the daemon dispatches on -- the
// model echoes the qualified name back and Registry.Call splits it -- so
// rewriting it here would mean approving one string and dispatching on another,
// and two distinct names could rewrite to the same one. Skipping the tool costs
// the user that tool, which is the same price a tool with an unserialisable
// schema already pays, and no ambiguity.
//
// C0 controls, DEL and the C1 range are all refused. Not \t or \n either: a
// name is one identifier on one line, and neither belongs in it.
func ValidateToolName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("tool name is empty")
	}
	// Bytes first, then runes, and both are needed. A lone 0x9b is the C1 CSI
	// introducer that a terminal acts on, but it is not valid UTF-8, so ranging
	// over the string decodes it to U+FFFD and a rune-only check never sees it.
	for i := 0; i < len(name); i++ {
		if b := name[i]; b < 0x20 || b == 0x7f {
			return fmt.Errorf("tool name contains a control character (%#x at byte %d), which would be "+
				"rendered into the terminal of a user deciding whether to approve it", b, i)
		}
	}
	if !utf8.ValidString(name) {
		return errors.New("tool name is not valid UTF-8, so what a terminal renders for it is " +
			"undefined and what the model echoes back may not be the same bytes")
	}
	for i, r := range name {
		if r >= 0x80 && r <= 0x9f {
			return fmt.Errorf("tool name contains a C1 control character (%#U at byte %d), which "+
				"terminals act on", r, i)
		}
	}
	return nil
}

// SanitizeForDisplay renders bytes chosen by a third party safe to write to a
// log or a terminal, by escaping every control character.
//
// For text that is DISPLAYED rather than dispatched on -- an MCP server's
// stderr, a model-supplied name in a refusal line -- where the value still has
// to be readable afterwards and refusing it is not an option. strconv.Quote is
// what Go's own %q uses, so an escape sequence arrives as the literal characters
// \x1b rather than as an escape, and stays legible.
//
// Callers that dispatch on the string must use ValidateToolName instead:
// escaping a key changes it.
func SanitizeForDisplay(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool {
		return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
	}) {
		return s
	}
	quoted := strconv.Quote(s)
	return quoted[1 : len(quoted)-1]
}

// SortTools orders tools deterministically (server, then tool). Go map
// iteration is randomised, and an advertised tool list that reshuffles between
// turns both defeats prompt caching and makes a reshuffle indistinguishable
// from a genuine change.
func SortTools(tools []Tool) {
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].Server != tools[j].Server {
			return tools[i].Server < tools[j].Server
		}
		return tools[i].Name < tools[j].Name
	})
}
