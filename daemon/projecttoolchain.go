package main

import (
	"os"
	"sort"
	"strings"
)

// THE PROJECT'S OWN TEST RUNNERS (2026-10-15).
//
// sandbox_exec runs go, npm, make and cargo, and anything in
// mcp.builtin.extra_programs where the host confines the command. A Python,
// pnpm, Gradle or .NET project therefore could not test the agent's edits in
// the turn's working copy at all: run_command ran in the real project (until
// runplace.go, 2026-10-26), which does not have them. Untested code followed.
//
// So the runners a project's own manifests call for are added to the extras --
// and, being extras, they run ONLY where the host confines the command, which
// is the condition extra_programs has always had (execProgramsWith). Each one
// runs project code, as npm and make already do; what contains it is the
// sandbox, and what authorises it is the approval, both unchanged. Nothing is
// added for a project that has no such manifest.
//
// Only the project root is read, and only names: no file is opened.

// maxToolchainRootEntries bounds how much of the project root is listed.
const maxToolchainRootEntries = 2000

// toolchainByFile maps a manifest at the project root to the programs that
// run its builds and tests.
var toolchainByFile = map[string][]string{
	"pyproject.toml":   {"python", "python3", "pytest"},
	"requirements.txt": {"python", "python3", "pytest"},
	"setup.py":         {"python", "python3", "pytest"},
	"setup.cfg":        {"python", "python3", "pytest"},
	"Pipfile":          {"python", "python3", "pytest"},
	"tox.ini":          {"python", "python3", "pytest", "tox"},
	"uv.lock":          {"uv"},
	"poetry.lock":      {"poetry"},
	"package.json":     {"node", "npx"},
	"pnpm-lock.yaml":   {"pnpm"},
	"yarn.lock":        {"yarn"},
	"bun.lock":         {"bun"},
	"bun.lockb":        {"bun"},
	"deno.json":        {"deno"},
	"deno.jsonc":       {"deno"},
	"pom.xml":          {"mvn"},
	"build.gradle":     {"gradle"},
	"build.gradle.kts": {"gradle"},
	"Gemfile":          {"ruby", "bundle", "rake"},
	"composer.json":    {"php", "composer"},
	"mix.exs":          {"mix"},
	"Package.swift":    {"swift"},
	"pubspec.yaml":     {"dart", "flutter"},
	"CMakeLists.txt":   {"cmake", "ctest"},
}

// toolchainBySuffix is the same for manifests named after the project.
var toolchainBySuffix = map[string][]string{
	".csproj": {"dotnet"},
	".fsproj": {"dotnet"},
	".sln":    {"dotnet"},
}

// projectToolchainPrograms is every program root's manifests call for,
// sorted, and none of the four sandbox_exec always runs.
func projectToolchainPrograms(root string) []string {
	if root == "" {
		return nil
	}
	// BOUNDED AT SOURCE: os.ReadDir allocates whatever the directory holds.
	// Manifests sit among a project root's first entries in any real project;
	// a root with more than this is read only this far.
	dir, err := os.Open(root)
	if err != nil {
		return nil
	}
	entries, _ := dir.ReadDir(maxToolchainRootEntries)
	_ = dir.Close()
	set := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		for _, p := range toolchainByFile[name] {
			set[p] = true
		}
		for suffix, programs := range toolchainBySuffix {
			if strings.HasSuffix(name, suffix) {
				for _, p := range programs {
					set[p] = true
				}
			}
		}
	}
	var out []string
	for p := range set {
		if !execAllowedBinaries[p] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
