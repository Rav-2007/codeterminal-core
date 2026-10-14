package main

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"mochiii/daemon/mcp"
)

// ${VAR} TEMPLATES in a server's arguments and env_map, as Claude Code's
// .mcp.json writes them: `"env": {"API_KEY": "${MY_KEY}"}`,
// `"args": ["--token", "${GITHUB_TOKEN:-}"]`. A template names where a value
// comes from without holding it, which is what lets a config file -- one that
// gets committed or pasted from a web page -- mention a key without containing it.
//
// A ${VAR} is looked up the way a server's own variable is (mcpEnvValueFor:
// its stored value, one stored for every server, one handed over at launch),
// then in the daemon's environment. One that is nowhere and has no :-default
// is a warning, not a failure: the server starts and says what it lacks.
//
// THIS PRODUCT'S OWN KEYS CANNOT BE NAMED. A template is the one way a value
// the server did not list could reach it, so ${MOCHIII_API_KEY} is refused
// when the block is read, exactly as the name is refused in "env".

var templateRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// templateVars is the variables text refers to.
func templateVars(text string) []string {
	var out []string
	for _, m := range templateRef.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// expandTemplate replaces each ${VAR} and ${VAR:-default} in text, and says
// which had neither a value nor a default.
func expandTemplate(text string, lookup func(string) (string, bool)) (string, []string) {
	var missing []string
	out := templateRef.ReplaceAllStringFunc(text, func(ref string) string {
		m := templateRef.FindStringSubmatch(ref)
		if v, ok := lookup(m[1]); ok && v != "" {
			return v
		}
		if m[2] != "" {
			return m[3]
		}
		if !slices.Contains(missing, m[1]) {
			missing = append(missing, m[1])
		}
		return ""
	})
	return out, missing
}

// validateTemplates refuses a block whose templates name this product's own
// credentials, and an env_map entry that could not be a variable.
func validateTemplates(srv MCPServerConfig) error {
	texts := append([]string{}, srv.Args...)
	for _, name := range sortedStringKeys(srv.EnvMap) {
		if !mcpEnvNamePattern.MatchString(name) {
			return fmt.Errorf("env_map entry %q is not a variable name", truncateForClient(name, 40))
		}
		if mcp.IsForbiddenEnvName(name) {
			return mcp.ValidateEnvAllowList([]string{name})
		}
		texts = append(texts, srv.EnvMap[name])
	}
	for _, text := range texts {
		for _, v := range templateVars(text) {
			if mcp.IsForbiddenEnvName(v) {
				return fmt.Errorf("${%s} names one of this product's own inference credentials, which is never "+
					"given to an MCP server; give the server a key of its own", v)
			}
		}
	}
	return nil
}

// templateLookup is where a server's ${VAR} finds its value.
func (s *Server) templateLookup(server string) func(string) (string, bool) {
	given := s.mcpEnvValueFor(server)
	return func(name string) (string, bool) {
		if mcp.IsForbiddenEnvName(name) {
			return "", false
		}
		if v, ok := given(name); ok {
			return v, true
		}
		return os.LookupEnv(name)
	}
}

// templateMissing is the ${VAR}s in srv that would expand to nothing.
func (s *Server) templateMissing(server string, srv MCPServerConfig) []string {
	lookup := s.templateLookup(server)
	var missing []string
	add := func(text string) {
		_, m := expandTemplate(text, lookup)
		for _, v := range m {
			if !slices.Contains(missing, v) {
				missing = append(missing, v)
			}
		}
	}
	for _, a := range srv.Args {
		add(a)
	}
	for _, k := range sortedStringKeys(srv.EnvMap) {
		add(srv.EnvMap[k])
	}
	return missing
}

// expandedLaunch is what srv runs once its templates are filled in: its
// arguments, the names it is given, and where their values come from.
func (s *Server) expandedLaunch(server string, srv MCPServerConfig) (args, allow []string, values func(string) (string, bool)) {
	lookup := s.templateLookup(server)
	for _, a := range srv.Args {
		if strings.Contains(a, "${") {
			a, _ = expandTemplate(a, lookup)
		}
		args = append(args, a)
	}
	allow = append([]string{}, srv.Env...)
	mapped := map[string]string{}
	for _, k := range sortedStringKeys(srv.EnvMap) {
		v, _ := expandTemplate(srv.EnvMap[k], lookup)
		mapped[k] = v
		if !slices.Contains(allow, k) {
			allow = append(allow, k)
		}
	}
	given := s.mcpEnvValueFor(server)
	values = func(name string) (string, bool) {
		if v, ok := mapped[name]; ok {
			return v, v != ""
		}
		return given(name)
	}
	return args, allow, values
}

// missingTemplateWarnings says which ${VAR}s will expand to nothing.
func missingTemplateWarnings(missing []string) []string {
	var out []string
	for _, v := range missing {
		out = append(out, fmt.Sprintf("${%s} has no value and no default, so it is left empty; set it with Set key "+
			"or `mcp set-key`", v))
	}
	return out
}

// serverTemplateVars is every variable srv's templates name.
func serverTemplateVars(srv MCPServerConfig) []string {
	var out []string
	texts := append([]string{}, srv.Args...)
	for _, k := range sortedStringKeys(srv.EnvMap) {
		texts = append(texts, srv.EnvMap[k])
	}
	for _, t := range texts {
		for _, v := range templateVars(t) {
			if !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	return out
}
