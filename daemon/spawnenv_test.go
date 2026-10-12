package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// EVERY CHILD PROCESS GETS AN EXPLICIT ENVIRONMENT, NEVER THE DAEMON'S.
//
// cmd.Env == nil makes os/exec hand the child THE DAEMON'S OWN ENVIRONMENT
// (os.Environ()), which can hold MOCHIII_API_KEY / MOCHIII_PROXY_KEY -- the
// inference credential the daemon runs with. A build command run via
// sandbox_exec can reach the network, so a child that also inherited that key
// would be an exfiltration path; the same is true of any program the daemon
// spawns. Every spawn site therefore sets cmd.Env from an ALLOWLIST
// (mcp.ServerEnv / mcp.LimiterEnv, helperEnv, gitHistoryEnv), never leaving it
// nil to inherit.
//
// This is a construction guard, not a theory: it parses the daemon's own
// sources and fails if any exec.Command / exec.CommandContext result is started
// without an explicit .Env, and if any os.StartProcess / syscall spawn appears
// outside the one reviewed site. It is the cheap thing that stops a future
// spawn site from silently inheriting the daemon's credentials -- the exact
// defect this file's sibling (TestRealServerNeverSeesCredentials) proves absent
// for the servers that exist today, extended to every spawn that will exist.
//
// Neuter check: set cmd.Env back to nil at any site (e.g. delete the
// `cmd.Env = gitHistoryEnv()` line in githistory.go) and this fails naming it.

// reviewedNonCmdSpawns are the functions allowed to call os.StartProcess or a
// syscall spawn directly, with the reason each is safe. A spawn of this kind
// outside the list fails the test, so a new one forces a human to classify it
// rather than inheriting the daemon env by default.
//
//	sandboxExecMain -- the sandbox helper's own entry point. It re-execs the
//	  real command with os.Environ(), but the helper was itself launched by the
//	  daemon with an allowlisted env (mcp_exec.go sets cmd.Env), so os.Environ()
//	  here is ALREADY the scrubbed set, not the daemon's.
var reviewedNonCmdSpawns = map[string]string{
	"sandboxExecMain": "sandbox helper re-exec; its own env was scrubbed by the daemon that launched it",
}

// spawnScanRoots are the module-relative directories whose non-test Go sources
// spawn, or could spawn, a child process. helper/ is scanned although it spawns
// nothing today, so a future spawn there is caught too.
var spawnScanRoots = []string{".", "../helper"}

type spawnViolation struct {
	where  string // file:line
	reason string
}

func scanSpawnSites(t *testing.T) (violations []spawnViolation, parsed, execSites int) {
	t.Helper()
	fset := token.NewFileSet()
	for _, root := range spawnScanRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == "vendor" {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return nil // a file that does not parse on this GOOS is not this test's concern
			}
			parsed++
			rel := filepath.ToSlash(path)
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				v, n := inspectFuncSpawns(fset, rel, fd)
				violations = append(violations, v...)
				execSites += n
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}
	return violations, parsed, execSites
}

// inspectFuncSpawns checks one function: every exec.Command/CommandContext
// result must be bound to a variable that later gets an explicit .Env, and any
// os.StartProcess / syscall spawn must be in reviewedNonCmdSpawns. It returns
// the violations and how many exec.Cmd sites it saw (for the vacuity floor).
func inspectFuncSpawns(fset *token.FileSet, file string, fd *ast.FuncDecl) (violations []spawnViolation, execSites int) {
	at := func(p token.Pos) string { return file + ":" + strconv.Itoa(fset.Position(p).Line) }

	execCalls := map[token.Pos]bool{} // exec.Command/CommandContext call -> seen
	boundEnvVars := map[string]bool{} // var name with an X.Env = assignment

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			switch spawnKind(node.Fun) {
			case "exec":
				execCalls[node.Pos()] = true
			case "oscreate", "syscall":
				if _, ok := reviewedNonCmdSpawns[fd.Name.Name]; !ok {
					violations = append(violations, spawnViolation{at(node.Pos()),
						"os.StartProcess/syscall spawn in " + fd.Name.Name + ", which is not in reviewedNonCmdSpawns"})
				}
			}
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Env" {
					if id, ok := sel.X.(*ast.Ident); ok {
						boundEnvVars[id.Name] = true
					}
				}
			}
		}
		return true
	})

	// Match each exec.Command call to the variable it is assigned to, and
	// require that variable to have an .Env set somewhere in the function.
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, rhs := range assign.Rhs {
			call, ok := rhs.(*ast.CallExpr)
			if !ok || spawnKind(call.Fun) != "exec" || !execCalls[call.Pos()] {
				continue
			}
			execSites++
			delete(execCalls, call.Pos()) // bound; anything left is an orphan
			if i >= len(assign.Lhs) {
				continue
			}
			id, ok := assign.Lhs[i].(*ast.Ident)
			if !ok {
				violations = append(violations, spawnViolation{at(call.Pos()),
					"exec.Command result is not assigned to a plain variable, so its Env cannot be verified"})
				continue
			}
			if !boundEnvVars[id.Name] {
				violations = append(violations, spawnViolation{at(call.Pos()),
					"exec.Command assigned to " + id.Name + ", which never gets an explicit .Env (nil inherits the daemon environment)"})
			}
		}
		return true
	})
	// An exec.Command call that was never the RHS of an assignment to a variable
	// (used inline, returned, passed as an argument) cannot be verified.
	for pos := range execCalls {
		execSites++
		violations = append(violations, spawnViolation{at(pos),
			"exec.Command result is used without binding it to a variable, so its Env cannot be verified"})
	}
	return violations, execSites
}

// spawnKind classifies a call's function expression as an "exec" (exec.Command/
// CommandContext), "oscreate" (os.StartProcess), "syscall" (syscall.Exec/
// ForkExec/StartProcess), or "" for anything else.
func spawnKind(fun ast.Expr) string {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	switch pkg.Name {
	case "exec":
		if sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext" {
			return "exec"
		}
	case "os":
		if sel.Sel.Name == "StartProcess" {
			return "oscreate"
		}
	case "syscall":
		if sel.Sel.Name == "Exec" || sel.Sel.Name == "ForkExec" || sel.Sel.Name == "StartProcess" {
			return "syscall"
		}
	}
	return ""
}

func TestEverySpawnSiteSetsAnExplicitEnv(t *testing.T) {
	violations, parsed, execSites := scanSpawnSites(t)

	// Vacuity floors: a scan that parsed little, or found no exec sites, proves
	// nothing by reporting clean. Measured 2026-10-07: 142 non-test files across
	// daemon, daemon/mcp and helper, and 10 exec.Cmd spawn sites.
	if parsed < 100 {
		t.Fatalf("vacuity floor: parsed only %d non-test files; the scan is not seeing the packages", parsed)
	}
	if execSites < 8 {
		t.Fatalf("vacuity floor: found only %d exec.Command site(s); the matcher is not recognising spawns", execSites)
	}
	if !spawnMatcherFiresOnANilEnv(t) {
		t.Fatal("vacuity floor: the matcher does not flag a known exec.Command with no .Env, " +
			"so a clean result would mean the matcher is broken, not the code safe")
	}

	if len(violations) > 0 {
		lines := make([]string, len(violations))
		for i, v := range violations {
			lines[i] = v.where + " -- " + v.reason
		}
		t.Errorf("%d spawn site(s) do not set an explicit child environment:\n  %s\n\n"+
			"cmd.Env == nil hands the child the daemon's own environment, which can hold "+
			"MOCHIII_API_KEY. Set cmd.Env from an allowlist (mcp.ServerEnv / mcp.LimiterEnv, "+
			"helperEnv, gitHistoryEnv). A new os/syscall spawn must be added to reviewedNonCmdSpawns "+
			"with the reason it is safe.",
			len(violations), strings.Join(lines, "\n  "))
	}
}

// spawnMatcherFiresOnANilEnv is the other side of the derivation: it runs the
// per-function matcher over a synthetic source with an exec.Command that never
// sets .Env, and requires exactly one violation. A clean scan above means the
// package is clean only if this proves the matcher can still fire.
func spawnMatcherFiresOnANilEnv(t *testing.T) bool {
	t.Helper()
	// Built from quoted, concatenated lines rather than a raw literal ON PURPOSE:
	// a raw literal would put `func bad()` at column zero in THIS file, and the
	// repo's line-based chunk-boundary heuristic (TestTheHeuristicAgreesWithThe
	// Compiler, which scans every .go file including this one) would read it as a
	// real top-level declaration that go/parser says is not there. Each physical
	// line here starts with a quote, so none trims to a boundary keyword.
	const src = "package p\n" +
		"import \"os/exec\"\n" +
		"func bad() { cmd := exec.Command(\"x\"); _ = cmd.Run() }\n" +
		"func good() { cmd := exec.Command(\"x\"); cmd.Env = nilEnv(); _ = cmd.Run() }\n"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parsing synthetic source: %v", err)
	}
	var bad, good int
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		v, _ := inspectFuncSpawns(fset, "synthetic.go", fd)
		switch fd.Name.Name {
		case "bad":
			bad = len(v)
		case "good":
			good = len(v)
		}
	}
	// The matcher must flag the nil-Env function and clear the one that sets Env.
	return bad == 1 && good == 0
}
