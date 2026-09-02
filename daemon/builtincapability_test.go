package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// A BUILT-IN THAT STARTS A PROGRAM MUST SAY SO, and this test asks the CODE
// rather than a list somebody maintained.
//
// WHY IT EXISTS. sandboxconsent_test.go already carried the right argument --
// "the exemption is a property, not a name... asking the tool what it IS
// survives the next arrival" -- and then enumerated the properties by hand:
// ExecutesCode and ReachesNetwork. query_compiler_definition and
// query_compiler_references declare neither. Their handlers reach exec.Command
// through LSPBridge, which starts gopls, typescript-language-server or
// pyright-langserver in the user's workspace, and lsp_bridge.go's own header
// calls a language server UNTRUSTED INPUT because project config can load
// plugins.
//
// So both were stamped Confined, both survived plan mode's filter, and both sat
// at policy "allow" where the consent prompt is never reached. Three controls,
// each keyed on a flag nobody had set, and every hand-written list of "the tools
// that act" agreed they were harmless.
//
// The lesson is not "add a third flag". It is that a list of properties is
// itself a list, and the next tool to break the pattern will break it in a
// direction nobody has named. This walks the call graph instead.
//
// WHAT IT CANNOT SEE, stated rather than implied: the walk is within this
// package. A handler that reaches exec.Command through another module's code is
// invisible to it. It over-approximates in the safe direction -- callees are
// matched on the function's own name, ignoring the receiver, so an unrelated
// method sharing a name pulls a tool into the check rather than out of it.
func TestNoBuiltinSpawnsWithoutDeclaringIt(t *testing.T) {
	calls, spawners := daemonCallGraph(t)

	// ANTI-VACUITY, first, because everything below is a loop that passes
	// trivially if the parse found nothing.
	if len(calls) < 50 {
		t.Fatalf("parsed only %d functions from the daemon package; the walk found almost "+
			"nothing, so its verdict is meaningless", len(calls))
	}
	if len(spawners) == 0 {
		t.Fatal("no function in this package appears to call exec.Command. Either the parse " +
			"broke or the spawn detection did; a clean result here would be a false one")
	}

	s := builtinTestServer(t)
	tools := s.builtinTools(&proposalSink{}, "")
	if len(tools) == 0 {
		t.Fatal("no built-ins to check")
	}

	sawSpawner := false
	for _, b := range tools {
		fn := handlerFuncName(b.Handler)
		if fn == "" {
			t.Errorf("could not resolve a handler function name for %q, so it was not checked",
				b.Tool.Name)
			continue
		}
		if !reachesSpawn(fn, calls, spawners, map[string]bool{}) {
			continue
		}
		sawSpawner = true
		if !b.Tool.ExecutesCode && !b.Tool.LaunchesSubprocess {
			t.Errorf("built-in %q reaches exec.Command (via %s) and declares neither "+
				"ExecutesCode nor LaunchesSubprocess.\n"+
				"Three things read those flags and all three would be wrong about it: "+
				"RegisterBuiltin stamps it Confined, so the approval prompt promises "+
				"edit-review over a program it does not control; planModeDenies keeps it, "+
				"so the mode that promises to start nothing starts it; and a policy of "+
				"\"allow\" means no prompt appears at all.", b.Tool.Name, fn)
		}
	}

	// The second half of anti-vacuity: if the walk reached no built-in at all,
	// the loop above proved nothing about any of them.
	if !sawSpawner {
		t.Error("no built-in was found to reach exec.Command. sandbox_exec and the " +
			"query_compiler_* pair do; a run where none does means the handler names or " +
			"the call graph stopped lining up, not that the tools became safe")
	}
}

// daemonCallGraph parses this package and returns, for every function it can
// name, the functions it calls; plus the set that call exec.Command directly.
//
// Function literals inside builtinTools are keyed the way the runtime names
// them -- builtinTools.func1, func2 ... in source order -- so the inline
// handlers resolve the same way the method values do.
func daemonCallGraph(t *testing.T) (map[string][]string, map[string]bool) {
	t.Helper()
	calls := map[string][]string{}
	spawners := map[string]bool{}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package dir: %v", err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			continue // a build-tagged file this configuration does not compile
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			record(fd.Name.Name, fd.Body, calls, spawners)
			// Literals get the runtime's own naming scheme.
			lit := 0
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				fl, ok := n.(*ast.FuncLit)
				if !ok {
					return true
				}
				lit++
				record(fd.Name.Name+".func"+itoa(lit), fl.Body, calls, spawners)
				return true
			})
		}
	}
	return calls, spawners
}

func record(name string, body *ast.BlockStmt, calls map[string][]string, spawners map[string]bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			calls[name] = append(calls[name], f.Name)
		case *ast.SelectorExpr:
			// Matched on the selector alone, ignoring the receiver: an
			// over-approximation, and deliberately the safe one.
			if pkg, ok := f.X.(*ast.Ident); ok && pkg.Name == "exec" &&
				(f.Sel.Name == "Command" || f.Sel.Name == "CommandContext") {
				spawners[name] = true
			}
			calls[name] = append(calls[name], f.Sel.Name)
		}
		return true
	})
	if _, seen := calls[name]; !seen {
		calls[name] = nil
	}
}

func reachesSpawn(fn string, calls map[string][]string, spawners map[string]bool, seen map[string]bool) bool {
	if seen[fn] {
		return false
	}
	seen[fn] = true
	if spawners[fn] {
		return true
	}
	for _, callee := range calls[fn] {
		if reachesSpawn(callee, calls, spawners, seen) {
			return true
		}
	}
	return false
}

// handlerFuncName turns a handler value into the bare name the parser above
// uses: "codeterminal/daemon.(*Server).builtinLSPDefinition-fm" -> "builtinLSPDefinition".
func handlerFuncName(h any) string {
	if h == nil {
		return ""
	}
	full := runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()
	if i := strings.LastIndex(full, "."); i >= 0 {
		// Method values read as ...(*Server).name-fm; literals as ...outer.funcN,
		// where the last dot separates the part we want from the qualifier.
		if strings.HasPrefix(full[i+1:], "func") {
			rest := full[:i]
			if j := strings.LastIndex(rest, "."); j >= 0 {
				return rest[j+1:] + "." + full[i+1:]
			}
		}
		full = full[i+1:]
	}
	return strings.TrimSuffix(full, "-fm")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
