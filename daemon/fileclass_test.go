package main

import "testing"

func TestClassifyFile(t *testing.T) {
	cases := []struct {
		path string
		want FileClass
	}{
		{"daemon/main.go", FileClassCode},
		{"daemon/editblock.go", FileClassCode},
		{"clients/tui/main.py", FileClassCode},
		{"web/app.ts", FileClassCode},
		{"web/App.tsx", FileClassCode},
		{"scripts/build.sh", FileClassCode},
		{"README.md", FileClassDoc},
		{"daemon/prompts/system.txt", FileClassDoc},
		{"docs/architecture.rst", FileClassDoc},
		{"models.json", FileClassConfig},
		{"config.yaml", FileClassConfig},
		{"go.mod", FileClassConfig},
		{"Makefile", FileClassConfig},
		{"makefile", FileClassConfig},
		{"Dockerfile", FileClassConfig},
		{".gitattributes", FileClassConfig},
		{"LICENSE", FileClassOther},
		{"THIRD_PARTY_LICENSES.txt", FileClassDoc}, // .txt is prose, even for a licenses file
		{"nested/dir/path/handler.go", FileClassCode},
	}
	for _, c := range cases {
		if got := classifyFile(c.path); got != c.want {
			t.Errorf("classifyFile(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestIsNoiseFile(t *testing.T) {
	noise := []string{
		".gitignore", "go.sum", "package-lock.json", "yarn.lock",
		"pnpm-lock.yaml", "Cargo.lock", "Gemfile.lock", "composer.lock",
		"poetry.lock", "Pipfile.lock", "sub/dir/go.sum",
	}
	for _, p := range noise {
		if !isNoiseFile(p) {
			t.Errorf("isNoiseFile(%q) = false, want true", p)
		}
	}

	notNoise := []string{"go.mod", "README.md", "main.go", "package.json", "models.json"}
	for _, p := range notNoise {
		if isNoiseFile(p) {
			t.Errorf("isNoiseFile(%q) = true, want false", p)
		}
	}
}
