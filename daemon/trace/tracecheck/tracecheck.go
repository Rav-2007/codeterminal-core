// Package tracecheck reads a trace line the way the schema says it must read
// and reports what does not fit. It is used by tests (the daemon's leak test and
// this package's own); nothing in the daemon imports it, so it is not in any
// binary.
//
// It is deliberately stricter than a reader needs to be. A reader ignores a key
// it does not know; this refuses one, because for an emitter an unknown key is
// the shape of a leak. Every string value must be a member of a closed list or
// match an id pattern, so a line that carries text has nowhere to hide.
package tracecheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
)

// Schema is the part of trace-schema.json the check needs.
type Schema struct {
	V            int      `json:"v"`
	MaxLineBytes int      `json:"max_line_bytes"`
	Fields       []string `json:"fields"`
	Srcs         []string `json:"srcs"`
	Statuses     []string `json:"statuses"`
	Stages       []struct {
		Name string `json:"name"`
	} `json:"stages"`
	RequestKinds []string          `json:"request_kinds"`
	ErrorClasses []string          `json:"error_classes"`
	Patterns     map[string]string `json:"patterns"`

	turn, id, sid *regexp.Regexp
}

// Load reads and compiles a schema file.
func Load(path string) (*Schema, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Schema
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	for name, dst := range map[string]**regexp.Regexp{"turn": &s.turn, "id": &s.id, "sid": &s.sid} {
		re, err := regexp.Compile(s.Patterns[name])
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", name, err)
		}
		*dst = re
	}
	if s.V != 1 || s.MaxLineBytes <= 0 || len(s.Fields) == 0 || len(s.Stages) == 0 || len(s.ErrorClasses) == 0 {
		return nil, errors.New("the schema file is incomplete")
	}
	return &s, nil
}

// StageNames lists the schema's stages.
func (s *Schema) StageNames() []string {
	out := make([]string, 0, len(s.Stages))
	for _, st := range s.Stages {
		out = append(out, st.Name)
	}
	return out
}

// CheckLine reports why line (one line, without its newline) does not fit, or
// nil. It checks: at most max_line_bytes; exactly one JSON object with nothing
// after it; every key is a schema field; v is 1; the numbers are non-negative
// integers; every string is a member of its closed list or matches its
// pattern; and err is empty unless status is fail.
func (s *Schema) CheckLine(line []byte) error {
	if len(line) > s.MaxLineBytes {
		return fmt.Errorf("%d bytes, over the %d cap", len(line), s.MaxLineBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return fmt.Errorf("not one JSON object: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("data after the JSON object")
	}
	for k := range m {
		if !slices.Contains(s.Fields, k) {
			return fmt.Errorf("key %q is not in the schema", k)
		}
	}
	num := func(k string, min int64, required bool) error {
		v, ok := m[k]
		if !ok {
			if required {
				return fmt.Errorf("%q is missing", k)
			}
			return nil
		}
		n, isNum := v.(json.Number)
		if !isNum {
			return fmt.Errorf("%q is not a number", k)
		}
		i, err := n.Int64()
		if err != nil || i < min {
			return fmt.Errorf("%q is %s, want an integer >= %d", k, n, min)
		}
		return nil
	}
	if err := num("v", 1, true); err != nil {
		return err
	}
	if m["v"].(json.Number).String() != "1" {
		return fmt.Errorf("v is %v, want 1", m["v"])
	}
	for _, k := range []string{"t", "n"} {
		if err := num(k, 1, true); err != nil {
			return err
		}
	}
	for _, k := range []string{"dur_ms", "call"} {
		if err := num(k, 0, true); err != nil {
			return err
		}
	}
	str := func(k string, ok func(string) bool, required bool) error {
		v, present := m[k]
		if !present {
			if required {
				return fmt.Errorf("%q is missing", k)
			}
			return nil
		}
		sv, isStr := v.(string)
		if !isStr {
			return fmt.Errorf("%q is not a string", k)
		}
		if !ok(sv) {
			return fmt.Errorf("%q is %q, not a member of its closed list or its id pattern", k, sv)
		}
		return nil
	}
	checks := []struct {
		key string
		ok  func(string) bool
		req bool
	}{
		{"src", func(v string) bool { return slices.Contains(s.Srcs, v) }, true},
		{"turn", s.turn.MatchString, true},
		{"stage", func(v string) bool { return slices.Contains(s.StageNames(), v) }, true},
		{"status", func(v string) bool { return slices.Contains(s.Statuses, v) }, true},
		{"tool", func(v string) bool { return v == "" || s.id.MatchString(v) }, true},
		{"err", func(v string) bool { return v == "" || slices.Contains(s.ErrorClasses, v) }, true},
		{"sid", s.sid.MatchString, false},
	}
	for _, c := range checks {
		if err := str(c.key, c.ok, c.req); err != nil {
			return err
		}
	}
	if m["err"] != "" && m["status"] != "fail" {
		return fmt.Errorf("err is %q on a %q line; it is for failures only", m["err"], m["status"])
	}
	return nil
}

// CheckFile checks every line of b and returns one error per bad line.
func (s *Schema) CheckFile(b []byte) []error {
	var errs []error
	for i, line := range bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n")) {
		if err := s.CheckLine(line); err != nil {
			errs = append(errs, fmt.Errorf("line %d: %w", i+1, err))
		}
	}
	return errs
}
