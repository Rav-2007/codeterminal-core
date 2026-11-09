package protocol

import (
	"slices"
	"strings"
	"testing"
)

// The rule for a key on an MCP server's command line (argkeys.go). The daemon's
// own tests (daemon/mcpargkeys_test.go) cover the refusal, masking and warning.

const dummyArgKey = "DUMMY-ARGKEY-7731"

func TestFindArgKeysByTheTwoRules(t *testing.T) {
	keyed := map[string][]string{
		"--api-key VALUE":          {"--api-key", dummyArgKey},
		"--api-key=VALUE":          {"--api-key=" + dummyArgKey},
		"--api_key VALUE":          {"--api_key", dummyArgKey},
		"--apiKey=VALUE":           {"--apiKey=" + dummyArgKey},
		"--token VALUE":            {"-y", "pkg", "--token", dummyArgKey},
		"-token VALUE":             {"-token", dummyArgKey},
		"--access-token=VALUE":     {"--access-token=" + dummyArgKey},
		"--auth-token VALUE":       {"--auth-token", dummyArgKey},
		"--password VALUE":         {"--password", dummyArgKey},
		"--client-secret=VALUE":    {"--client-secret=" + dummyArgKey},
		"--secret VALUE":           {"--secret", dummyArgKey},
		"--personal-access-token":  {"--personal-access-token", dummyArgKey},
		"NAME=value":               {"PROBE_TOKEN=" + dummyArgKey},
		"docker -e NAME=value":     {"run", "-i", "-e", "GITHUB_TOKEN=" + dummyArgKey, "ghcr.io/x/y"},
		"credential name=value":    {"api_key=" + dummyArgKey},
		"NAME=value, not a secret": {"LOG_LEVEL=debug"}, // the env list's rule: a value is a value
	}
	for what, args := range keyed {
		if got := FindArgKeys(args); len(got) != 1 {
			t.Errorf("%s %q: found %d keys, want 1", what, args, len(got))
		}
	}
	plain := map[string][]string{
		"a template after the flag":       {"--api-key", "${GITHUB_TOKEN}"},
		"a template after '='":            {"--api-key=${GITHUB_TOKEN}"},
		"NAME=${NAME}":                    {"GITHUB_TOKEN=${GITHUB_TOKEN}"},
		"docker -e NAME (pass-through)":   {"run", "-e", "GITHUB_TOKEN", "ghcr.io/x/y"},
		"a credential flag with no value": {"--token"},
		"a credential flag, then a flag":  {"--token", "--verbose"},
		"a token FILE":                    {"--token-file", "/home/u/.config/token"},
		"--key (a path as often as not)":  {"--key", "/etc/ssl/key.pem"},
		"an ordinary --flag=value":        {"--port=8080", "--db=/tmp/x.db", "--transport=stdio"},
		"lower-case name=value":           {"region=us-east-1"},
		"a package, a path, a URL":        {"-y", "@scope/pkg@1.2.3", "/srv/data", "https://x.example/a?b=c"},
		"something key-shaped, unnamed":   {"sk-live-0123456789abcdef0123456789abcdef"},
	}
	for what, args := range plain {
		if got := FindArgKeys(args); len(got) != 0 {
			t.Errorf("%s %q: found %d keys, want none", what, args, len(got))
		}
	}
}

func TestMaskArgKeysHidesOnlyTheValue(t *testing.T) {
	args := []string{"-y", "pkg", "--api-key", dummyArgKey, "--token=" + dummyArgKey, "PROBE_TOKEN=" + dummyArgKey, "--port=8080", "--key", "/k.pem"}
	got := MaskArgKeys(args)
	want := []string{"-y", "pkg", "--api-key", "***", "--token=***", "PROBE_TOKEN=***", "--port=8080", "--key", "/k.pem"}
	if !slices.Equal(got, want) {
		t.Errorf("masked %q\n want %q", got, want)
	}
	if args[3] != dummyArgKey {
		t.Error("masking changed the arguments it was given")
	}
}

func TestArgKeyRefusalNamesTheArgumentNotTheValue(t *testing.T) {
	for _, args := range [][]string{{"--api-key", dummyArgKey}, {"--token=" + dummyArgKey}, {"PROBE_TOKEN=" + dummyArgKey}} {
		keys := FindArgKeys(args)
		if len(keys) != 1 {
			t.Fatalf("%q: %d keys", args, len(keys))
		}
		msg := ArgKeyRefusal("srv", keys[0])
		if strings.Contains(msg, dummyArgKey) {
			t.Errorf("%q: the refusal repeats the key: %s", args, msg)
		}
		if !strings.Contains(msg, "/mcp-server key srv "+keys[0].Name) || !strings.Contains(msg, "${"+keys[0].Name+"}") {
			t.Errorf("%q: the refusal does not say where the key goes: %s", args, msg)
		}
	}
}
