package main

import (
	"strings"
	"testing"

	"mochiii/daemon/mcp"
	"mochiii/editapply"
)

// classFromFlags is the class a tool's own declared flags give it -- the truth
// the table in mcpconfig.go must agree with. Network first: a tool that reaches
// the internet is never local, whatever else it does.
func classFromFlags(t mcp.Tool) builtinClass {
	switch {
	case t.ReachesNetwork:
		return classNetwork
	case t.ExecutesCode:
		return classExecutes
	case t.LaunchesSubprocess:
		return classLaunches
	default:
		return classConfined
	}
}

// THE TABLE IS A LIST, SO IT IS CHECKED AGAINST WHAT IT LISTS.
//
// builtinToolClasses exists because the startup warning runs before any Server
// does, and builtinTools() cannot be called there. A hand-written table is
// exactly the thing this repository keeps learning drifts -- the name split it
// replaces (isWebToolName) is how the warning came to call three
// language-server tools and sandbox_exec "confined". So a real Server is built,
// every tool it would register is classified by its own flags, and any
// disagreement, missing tool or stray name fails here.
//
// Neuter check: classify query_compiler_definition as classConfined in the
// table, or delete sandbox_exec from it, and this fails naming the tool.
func TestBuiltinClassTableMatchesTheTools(t *testing.T) {
	s := builtinTestServer(t)
	tools := s.builtinTools(&proposalSink{}, "") // not plan mode: the widest set

	// ANTI-VACUITY: a table checked against nothing agrees with everything.
	if len(tools) < 10 {
		t.Fatalf("builtinTools returned only %d tools; the check below would mean nothing", len(tools))
	}

	seen := map[string]bool{}
	for _, b := range tools {
		name := b.Tool.Name
		seen[name] = true
		want := classFromFlags(b.Tool)
		got, listed := builtinToolClasses[name]
		switch {
		case !listed:
			t.Errorf("built-in %q is missing from builtinToolClasses; the startup warning would call it unknown", name)
		case got != want:
			t.Errorf("built-in %q is classed %d in the table but its flags say %d", name, got, want)
		}
	}
	for name := range builtinToolClasses {
		if !seen[name] {
			t.Errorf("builtinToolClasses lists %q, which no built-in is called", name)
		}
	}
}

// The warning's own words, for each class a user can set to "allow". A line
// that files a tool under the wrong class is a false statement made at the
// moment the user is deciding what to trust, which is the failure mode the
// network line was added to stop and this one reproduced for two more classes.
func TestTheStartupWarningDescribesEachToolByWhatItDoes(t *testing.T) {
	cfg := &Config{MCP: MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{
			"read_file":                 PolicyAllow,
			"query_compiler_definition": PolicyAllow,
			"sandbox_exec":              PolicyAllow,
			"read_fiel":                 PolicyAllow, // a typo a user could make
		}},
	}}
	cfg.warnMCPPolicySurface()

	var confined, launches, executes, unknown string
	for _, w := range cfg.Warnings() {
		switch {
		case strings.Contains(w, "confined and do not write"):
			confined = w
		case strings.Contains(w, "can start a language server"):
			launches = w
		case strings.Contains(w, "RUN COMMANDS"):
			executes = w
		case strings.Contains(w, "no built-in tool has that name"):
			unknown = w
		}
	}

	if !strings.Contains(confined, "read_file") {
		t.Errorf("the confined line lost read_file: %q", confined)
	}
	for _, notConfined := range []string{"query_compiler_definition", "sandbox_exec", "read_fiel"} {
		if strings.Contains(confined, notConfined) {
			t.Errorf("the startup warning calls %q confined: %q", notConfined, confined)
		}
	}

	// Launch: named, the program named, and the item-32 promise stated.
	if !strings.Contains(launches, "query_compiler_definition") || !strings.Contains(launches, "gopls") {
		t.Errorf("the launch line does not name the tool and gopls: %q", launches)
	}
	if !strings.Contains(launches, "always asks you first") {
		t.Errorf("the launch line does not say starting a server still asks: %q", launches)
	}

	// Execute: named, and the unsandboxed case stated rather than implied.
	if !strings.Contains(executes, "sandbox_exec") || !strings.Contains(executes, "full privileges") {
		t.Errorf("the exec line does not name sandbox_exec and say it may run with full privileges: %q", executes)
	}

	if !strings.Contains(unknown, "read_fiel") {
		t.Errorf("a misspelled tool name was not reported as naming nothing: %q", unknown)
	}
}

// The programs named in the launch line come from the table the bridge uses.
func TestLanguageServerNamesAreTheBridgesOwn(t *testing.T) {
	got := strings.Join(languageServerNames(), ",")
	for _, lang := range []editapply.Language{editapply.LangGo, editapply.LangTypeScript, editapply.LangPython} {
		want, err := serverCommand(lang)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, want) {
			t.Errorf("languageServerNames() = %q, missing %q", got, want)
		}
	}
	if strings.Count(got, "typescript-language-server") != 1 {
		t.Errorf("typescript-language-server serves two languages and must be named once: %q", got)
	}
}
