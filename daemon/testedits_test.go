package main

import (
	"strings"
	"testing"

	"mochiii/editapply"
)

// AN EXISTING TEST IS EVIDENCE (testedits.go).

func TestTestPathsAreFoundInEveryLanguage(t *testing.T) {
	for rel, want := range map[string]bool{
		"tests/misc_tests.c": true, "test/parser_test.cpp": true, "unity_test.c": true, "test_cjson.c": true,
		"src/test/java/com/google/gson/MapTest.java": true, "gson/src/test/java/X.java": true,
		"tests/test_more.py": true, "more_itertools/tests/test_x.py": true, "foo_test.go": true,
		"testdata/golden.json": true, "spec/models/user_spec.rb": true, "web/app.test.ts": true,
		"cJSON.c": false, "src/main/java/com/google/gson/Gson.java": false, "latest.c": false, "contest.cpp": false,
		"tests/README.md": false, "specs/feature.md": false, "more_itertools/more.py": false,
	} {
		if got := isTestPath(rel); got != want {
			t.Errorf("isTestPath(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestOnlyChangingAnExistingLineIsFlagged(t *testing.T) {
	old := "void t(void) {\n    TEST_ASSERT_EQUAL(1, f());\n}\n"
	for _, tc := range []struct {
		after string
		want  bool
	}{
		{old + "void t2(void) {\n    TEST_ASSERT_EQUAL(2, g());\n}\n", false}, // a new test after it
		{"// added\n" + old, false}, // a line before it
		{"void t(void) {\n    TEST_ASSERT_EQUAL(1, f());\n    x();\n}\n", false}, // a line inside it
		{"void t(void) {\n    TEST_ASSERT_EQUAL(0, f());\n}\n", true},            // the expectation changed
		{"void t(void) {\n}\n", true},                                            // an assertion removed
		{"void t(void) {  \r\n    TEST_ASSERT_EQUAL(1, f());\r\n}\r\n", false},   // only line ends and spaces
	} {
		if got := changesExistingLines(old, tc.after); got != tc.want {
			t.Errorf("changesExistingLines(..., %q) = %v, want %v", tc.after, got, tc.want)
		}
	}
	if changesExistingLines("", "anything") {
		t.Error("creating a file was read as changing one")
	}
}

// THE cJSON CASE: the agent rewrote an existing test to fit its fix. Told at
// once, shown in the review, and the long task's judge sees it too.
func TestChangingAnExistingTestIsToldAndShown(t *testing.T) {
	test := "static void cjson_replace_item_in_object_should_preserve_name(void)\n{\n    cJSON root = { 0 };\n" +
		"    TEST_ASSERT_TRUE(cJSON_ReplaceItemInObject(&root, \"child\", replacement));\n}\n"
	s, _ := stageProject(t, map[string]string{"tests/misc_tests.c": test, "cJSON.c": "int f(void) { return 0; }\n"})
	sink := stagedSink(t, s)

	out, failed := propose(t, s, sink, "tests/misc_tests.c", "cJSON root = { 0 };", "cJSON root = { 0 };\n    root.type = cJSON_Object;")
	if failed || strings.Contains(out, "existing lines of a test") {
		t.Errorf("adding a line inside a test was flagged as changing one (failed=%v): %s", failed, out)
	}
	out, failed = propose(t, s, sink, "tests/misc_tests.c",
		"    TEST_ASSERT_TRUE(cJSON_ReplaceItemInObject(&root, \"child\", replacement));",
		"    TEST_ASSERT_FALSE(cJSON_ReplaceItemInObject(&root, \"child\", replacement));")
	if failed || !strings.Contains(out, "existing lines of a test (tests/misc_tests.c)") ||
		!strings.Contains(out, "change your code, not the test") {
		t.Errorf("the model was not told it changed an existing test (failed=%v): %s", failed, out)
	}
	if out, _ := propose(t, s, sink, "cJSON.c", "return 0;", "return 1;"); strings.Contains(out, "of a test") {
		t.Errorf("an edit to the code was flagged as a test change: %s", out)
	}
	_, info, _ := sink.finish()
	if info == nil || strings.Join(info.TestsChanged, ",") != "tests/misc_tests.c" {
		t.Errorf("the review does not name the changed test: %+v", info)
	}

	if w := testsWeakenedIn("tests/misc_tests.c", "TEST_ASSERT_TRUE(x);", "TEST_ASSERT_FALSE(x);"); w == "" {
		t.Error("the long task's judge does not see an existing C test changed")
	}
	if blocks := []editapply.EditBlock{{FilePath: "tests/new_test.c", Search: "", Replace: "void t(void) {}\n"}}; len(testsChangedIn(blocks)) != 0 {
		t.Error("a new test file was counted as a changed one")
	}
}

// Changing a test through the shell is changing it too: told, and shown.
func TestACommandThatChangesATestIsToldToo(t *testing.T) {
	s, _, sink := posixRunPlace(t, map[string]string{"tests/misc_tests.c": "TEST_ASSERT_TRUE(x);\n", "cJSON.c": "int f;\n"})
	res := runIn(t, s, sink, map[string]any{"command": "printf 'TEST_ASSERT_FALSE(x);\\n' > tests/misc_tests.c"})
	if !strings.Contains(res.Content, "existing lines of a test (tests/misc_tests.c)") {
		t.Errorf("the model was not told its command changed an existing test: %q", res.Content)
	}
	res = runIn(t, s, sink, map[string]any{"command": "printf 'int g;\\n' >> cJSON.c"})
	if strings.Contains(res.Content, "of a test") {
		t.Errorf("a command changing code was flagged as changing a test: %q", res.Content)
	}
	if _, info, _ := sink.finish(); info == nil || strings.Join(info.TestsChanged, ",") != "tests/misc_tests.c" {
		t.Errorf("the review does not name the test the command changed: %+v", info)
	}
}

// A long task's judge hears an edit that changes an existing test in any
// language, not only Go's and pytest's removed tests and skips.
func TestTheJudgeHearsAChangedTestInEveryLanguage(t *testing.T) {
	j := newCorrectionJudge("fix")
	j.edited("tests/misc_tests.c", "    TEST_ASSERT_TRUE(x);\n", "    TEST_ASSERT_FALSE(x);\n")
	if len(j.weakened) == 0 || !strings.Contains(j.weakened[0], "changed existing lines of a test") {
		t.Errorf("the judge did not hear the changed C test: %v", j.weakened)
	}
	j = newCorrectionJudge("fix")
	j.edited("tests/misc_tests.c", "    TEST_ASSERT_TRUE(x);\n", "    TEST_ASSERT_TRUE(x);\n    TEST_ASSERT_TRUE(y);\n")
	if len(j.weakened) != 0 {
		t.Errorf("an added assertion was heard as a weakened test: %v", j.weakened)
	}
}
