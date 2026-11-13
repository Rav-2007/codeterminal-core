package tuitrace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// checkLine reports why line (without its newline) does not fit the schema, or
// nil. It is the strict reading the daemon's tracecheck does, written again here
// because this module cannot import the daemon: at most max_line_bytes; one JSON
// object; every key a schema field; v is 1; the numbers are non-negative
// integers; every string is a member of its closed list or matches its pattern;
// err is empty unless status is fail. (The daemon module's e2e test runs the real
// tracecheck over a real client's file, so a disagreement between the two readers
// shows up there.)
func checkLine(s schema, line []byte) error {
	if len(line) > s.MaxLineBytes {
		return fmt.Errorf("%d bytes, over the %d cap", len(line), s.MaxLineBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return fmt.Errorf("not one JSON object: %w", err)
	}
	if dec.More() {
		return fmt.Errorf("data after the JSON object")
	}
	for k := range m {
		if !slices.Contains(s.Fields, k) {
			return fmt.Errorf("key %q is not in the schema", k)
		}
	}
	for _, k := range []string{"v", "t", "n", "dur_ms", "call"} {
		n, ok := m[k].(json.Number)
		if !ok {
			return fmt.Errorf("%q is missing or not a number", k)
		}
		if i, err := n.Int64(); err != nil || i < 0 {
			return fmt.Errorf("%q is %s", k, n)
		}
	}
	if m["v"].(json.Number).String() != "1" {
		return fmt.Errorf("v is %v", m["v"])
	}
	str := func(k string, ok func(string) bool) error {
		v, isStr := m[k].(string)
		if !isStr {
			return fmt.Errorf("%q is missing or not a string", k)
		}
		if !ok(v) {
			return fmt.Errorf("%q is %q, not a member of its closed list or its id pattern", k, v)
		}
		return nil
	}
	turn, id := regexp.MustCompile(s.Patterns["turn"]), regexp.MustCompile(s.Patterns["id"])
	for k, ok := range map[string]func(string) bool{
		"src":    func(v string) bool { return slices.Contains(s.Srcs, v) },
		"turn":   turn.MatchString,
		"stage":  func(v string) bool { return slices.Contains(s.stageNames(), v) },
		"status": func(v string) bool { return slices.Contains(s.Statuses, v) },
		"tool":   func(v string) bool { return v == "" || id.MatchString(v) },
		"err":    func(v string) bool { return v == "" || slices.Contains(s.ErrorClasses, v) },
	} {
		if err := str(k, ok); err != nil {
			return err
		}
	}
	if m["err"] != "" && m["status"] != "fail" {
		return fmt.Errorf("err is %q on a %q line", m["err"], m["status"])
	}
	return nil
}

// checkFile checks every line of b.
func checkFile(s schema, b []byte) []error {
	var errs []error
	for i, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if err := checkLine(s, []byte(l)); err != nil {
			errs = append(errs, fmt.Errorf("line %d: %w", i+1, err))
		}
	}
	return errs
}

func TestTheLineCheckerRefusesWhatItShould(t *testing.T) {
	s := loadSchema(t)
	good := `{"v":1,"t":1731234567123,"src":"tui","turn":"a1b2c3d4","n":1,"stage":"dial","status":"fail","dur_ms":3,"tool":"prompt","call":0,"err":"no_lockfile"}`
	if err := checkLine(s, []byte(good)); err != nil {
		t.Fatalf("a good line was refused: %v", err)
	}
	if errs := checkFile(s, []byte(good+"\n"+good+"\n")); len(errs) != 0 {
		t.Fatalf("a good file was refused: %v", errs)
	}
	if errs := checkFile(s, []byte(good+"\n"+strings.Replace(good, `"dial"`, `"keypress"`, 1)+"\n")); len(errs) != 1 {
		t.Fatalf("a file with one bad line gave %d errors: %v", len(errs), errs)
	}
	for name, bad := range map[string]string{
		"unknown key":     strings.Replace(good, `"call":0`, `"call":0,"text":"hi"`, 1),
		"unknown stage":   strings.Replace(good, `"dial"`, `"keypress"`, 1),
		"text in tool":    strings.Replace(good, `"prompt"`, `"two words"`, 1),
		"unknown class":   strings.Replace(good, `no_lockfile`, `daemon not found at /x`, 1),
		"err on ok":       strings.Replace(strings.Replace(good, `"fail"`, `"ok"`, 1), ``, ``, 1),
		"bad turn":        strings.Replace(good, `a1b2c3d4`, `A1B2C3D4!`, 1),
		"negative number": strings.Replace(good, `"dur_ms":3`, `"dur_ms":-3`, 1),
		"too long":        strings.Replace(good, `"tool":"prompt"`, `"tool":"`+strings.Repeat("a", 600)+`"`, 1),
		"two objects":     good + good,
	} {
		if checkLine(s, []byte(bad)) == nil {
			t.Errorf("%s: a bad line was accepted: %s", name, bad)
		}
	}
}
