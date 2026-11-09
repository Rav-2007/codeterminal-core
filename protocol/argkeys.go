package protocol

import (
	"regexp"
	"strings"
)

// A KEY ON AN MCP SERVER'S COMMAND LINE. A server's setup page often shows its
// key inline -- `--api-key sk-…`, `--token=…`, `docker run -e API_KEY=…` -- and
// a command line is stored as written (~/.mochiii/mcp-servers.json), logged
// when the server is added, and shown wherever the server is listed. The env
// list already refuses NAME=value for exactly that reason; args were never
// looked at (FOUND 2026-11-08: a dummy key in args was accepted silently,
// stored, logged and displayed).
//
// One rule, here, because the daemon (which refuses, masks and warns) and the
// terminal client (which refuses before sending, so the line is not recalled)
// both apply it, and two copies would drift.
//
// Two tests, both deliberately narrow -- a general "looks like a key" test
// misfires on paths, hashes and package names:
//   - a KNOWN CREDENTIAL FLAG with a value: --api-key VALUE, --api-key=VALUE,
//     and the common spellings (credentialFlags);
//   - NAME=value where NAME is an environment variable (all capitals) or a
//     credential name: the env list's own rule, applied to an argument.
//
// A ${NAME} template is not a key and is allowed: it is the way to hand a key
// to a flag (--api-key ${GITHUB_TOKEN}), filled at launch from the value given
// with /mcp-server key.

// ArgKeyMask stands for a value that is not shown.
const ArgKeyMask = "***"

// credentialFlags are flag names, lower-cased with '-', '_' and '.' removed,
// that take a credential as their value.
var credentialFlags = map[string]bool{
	"apikey": true, "apitoken": true, "apisecret": true,
	"token": true, "accesstoken": true, "authtoken": true, "bearertoken": true, "personalaccesstoken": true,
	"password": true, "passwd": true,
	"secret": true, "clientsecret": true, "secretkey": true, "secretaccesskey": true,
}

// envShapedName is an environment variable's name as written by convention.
var envShapedName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func normalizedFlagName(s string) string {
	return strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(s))
}

// credentialFlag reports whether arg is a credential flag ("--api-key",
// "--api_key=…", "-token"), and its value when given inline after '='.
func credentialFlag(arg string) (flag string, inline string, hasInline bool, ok bool) {
	if !strings.HasPrefix(arg, "-") {
		return "", "", false, false
	}
	name, value, eq := strings.Cut(arg, "=")
	if !credentialFlags[normalizedFlagName(strings.TrimLeft(name, "-"))] {
		return "", "", false, false
	}
	return name, value, eq, true
}

// isArgValue is whether s is a value someone wrote, rather than nothing or a
// ${NAME} template.
func isArgValue(s string) bool {
	return strings.TrimSpace(s) != "" && !strings.Contains(s, "${")
}

// ArgKey is one key found on a command line, by where its value is. It never
// holds the value.
type ArgKey struct {
	At    int    // index of the argument holding the value
	Split int    // the value starts at this byte of that argument (0: all of it)
	What  string // how to name it without its value: "--api-key", "API_KEY="
	Name  string // a variable name to suggest for it
}

// FindArgKeys is every key on a command line, by the two tests above.
func FindArgKeys(args []string) []ArgKey {
	var out []ArgKey
	for i := 0; i < len(args); i++ {
		a := args[i]
		if flag, inline, hasInline, ok := credentialFlag(a); ok {
			suggest := strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(strings.TrimLeft(flag, "-")))
			switch {
			case hasInline && isArgValue(inline):
				out = append(out, ArgKey{At: i, Split: len(flag) + 1, What: flag, Name: suggest})
			case !hasInline && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && isArgValue(args[i+1]):
				out = append(out, ArgKey{At: i + 1, What: flag, Name: suggest})
				i++
			}
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		name, value, eq := strings.Cut(a, "=")
		if !eq || name == "" || !isArgValue(value) {
			continue
		}
		if envShapedName.MatchString(name) || credentialFlags[normalizedFlagName(name)] {
			out = append(out, ArgKey{At: i, Split: len(name) + 1, What: name + "=", Name: strings.ToUpper(name)})
		}
	}
	return out
}

// MaskArgKeys is args with every key's value replaced by ArgKeyMask: what is
// shown and logged. The slice it is given is not changed.
func MaskArgKeys(args []string) []string {
	keys := FindArgKeys(args)
	if len(keys) == 0 {
		return args
	}
	out := append([]string(nil), args...)
	for _, k := range keys {
		out[k.At] = out[k.At][:k.Split] + ArgKeyMask
	}
	return out
}

// Templated is how k's argument is written with a template instead of the
// key: "--api-key ${API_KEY}" or "API_KEY=${API_KEY}".
func (k ArgKey) Templated() string {
	if strings.HasSuffix(k.What, "=") {
		return k.What + "${" + k.Name + "}"
	}
	return k.What + " ${" + k.Name + "}"
}

// ArgKeyRefusal is what a new or changed server whose command line holds a
// key is refused with, by the daemon and the terminal client alike. It names
// the argument, never its value, and says where the key goes instead.
func ArgKeyRefusal(server string, k ArgKey) string {
	why := "the value after " + k.What + " is a key"
	if strings.HasSuffix(k.What, "=") {
		why = "the argument " + k.What + "… carries a value; like the env list, a command line takes variable names, not values"
	}
	return why + ", and a command line is stored and shown. Write it as " + k.Templated() +
		" and give the value with /mcp-server key " + server + " " + k.Name + " (Set key in VS Code), which keeps it hidden"
}
