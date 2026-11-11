package tracecheck

import (
	"strings"
	"testing"
)

const good = `{"v":1,"t":1794327983155,"src":"daemon","turn":"1c9b481d","n":1,"stage":"request","status":"ok","dur_ms":0,"tool":"prompt","call":0,"err":""}`

func TestAGoodLinePassesAndEveryDefectIsNamed(t *testing.T) {
	s, err := Load("../trace-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckLine([]byte(good)); err != nil {
		t.Fatalf("a good line was refused: %v", err)
	}
	sid := strings.Replace(good, `"err":""`, `"err":"","sid":"0123abcd"`, 1)
	if err := s.CheckLine([]byte(sid)); err != nil {
		t.Fatalf("a line with a sid was refused: %v", err)
	}
	for name, tc := range map[string]struct{ line, want string }{
		"an extra key":           {strings.Replace(good, `"err":""`, `"err":"","note":"hello"`, 1), "not in the schema"},
		"another major":          {strings.Replace(good, `"v":1`, `"v":2`, 1), "v is"},
		"an unknown stage":       {strings.Replace(good, `"stage":"request"`, `"stage":"made_up"`, 1), "closed list"},
		"text in the tool":       {strings.Replace(good, `"tool":"prompt"`, `"tool":"read the file x"`, 1), "id pattern"},
		"a capital in the tool":  {strings.Replace(good, `"tool":"prompt"`, `"tool":"Builtin__x"`, 1), "id pattern"},
		"an error on an ok line": {strings.Replace(good, `"err":""`, `"err":"auth"`, 1), "failures only"},
		"an unknown class":       {strings.Replace(good, `"status":"ok","dur_ms":0,"tool":"prompt","call":0,"err":""`, `"status":"fail","dur_ms":0,"tool":"prompt","call":0,"err":"the key was bad"`, 1), "closed list"},
		"a string for a number":  {strings.Replace(good, `"n":1`, `"n":"1"`, 1), "not a number"},
		"a negative duration":    {strings.Replace(good, `"dur_ms":0`, `"dur_ms":-1`, 1), "want an integer"},
		"a fraction":             {strings.Replace(good, `"call":0`, `"call":0.5`, 1), "want an integer"},
		"a missing key":          {strings.Replace(good, `"turn":"1c9b481d",`, "", 1), "missing"},
		"a bad turn id":          {strings.Replace(good, `"1c9b481d"`, `"NOT-AN-ID"`, 1), "id pattern"},
		"a bad sid":              {strings.Replace(good, `"err":""`, `"err":"","sid":"xyz"`, 1), "id pattern"},
		"data after the object":  {good + ` {"v":1}`, "after the JSON object"},
		"not json":               {`hello`, "not one JSON object"},
		"over the cap":           {strings.Replace(good, `"tool":"prompt"`, `"tool":"`+strings.Repeat("a", 600)+`"`, 1), "over the 512 cap"},
	} {
		err := s.CheckLine([]byte(tc.line))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, tc.want)
		}
	}
	if errs := s.CheckFile([]byte(good + "\n" + `nope` + "\n" + good + "\n")); len(errs) != 1 || !strings.Contains(errs[0].Error(), "line 2") {
		t.Errorf("CheckFile = %v, want one error naming line 2", errs)
	}
}

func TestAnIncompleteSchemaIsRefused(t *testing.T) {
	if _, err := Load("no-such-file.json"); err == nil {
		t.Error("a missing schema file was accepted")
	}
	if _, err := Load("tracecheck.go"); err == nil {
		t.Error("a file that is not JSON was accepted")
	}
}
