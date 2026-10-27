"""Build SWE-bench-style tasks from real fixes in real GitHub repositories.

For each task: workspace/ is the repository at the commit BEFORE the fix, with a
fresh one-commit history (so the fix cannot be looked up); hidden.patch is the
fix's own test changes, added only for grading; solution.patch is the fix's
source change, used only to prove the task is gradable; check.sh adds the hidden
tests to a directory and runs them, exit 0 = pass.

    python3 -I build_tasks.py [name ...]      build (all, or the named tasks)
    python3 -I build_tasks.py --validate ...  also prove each: fails as given, passes with the real fix

Everything it makes lives under $REALREPO_HOME (default ~/.cache/mochiii-realrepo): repos/ (clones,
fetched on first use from REPO_URLS), tasks/ (what the eval reads). See README.md for the machine setup.
"""
import os, shutil, subprocess, sys, tempfile

HOME = os.environ.get("REALREPO_HOME", os.path.expanduser("~/.cache/mochiii-realrepo"))
REPOS, TASKS = os.path.join(HOME, "repos"), os.path.join(HOME, "tasks")

# Where each task's repository comes from. A fix's commit must be reachable from the default clone.
REPO_URLS = {
    "click": "https://github.com/pallets/click",
    "more-itertools": "https://github.com/more-itertools/more-itertools",
    "cJSON": "https://github.com/DaveGamble/cJSON",
    "cxxopts": "https://github.com/jarro2783/cxxopts",
    "argparse": "https://github.com/p-ranav/argparse",
    "gson": "https://github.com/google/gson",
    "jsoup": "https://github.com/jhy/jsoup",
    # Round 6's newer tasks: every fix below was merged after 2026-06-01.
    "tomlkit": "https://github.com/python-poetry/tomlkit",
    "marshmallow": "https://github.com/marshmallow-code/marshmallow",
    "toolz": "https://github.com/pytoolz/toolz",
    "json-c": "https://github.com/json-c/json-c",
    "cmark": "https://github.com/commonmark/cmark",
    "fmt": "https://github.com/fmtlib/fmt",
    "CLI11": "https://github.com/CLIUtils/CLI11",
    "yaml-cpp": "https://github.com/jbeder/yaml-cpp",
    "commons-lang": "https://github.com/apache/commons-lang",
    "commons-text": "https://github.com/apache/commons-text",
}

PY_TESTS = "Tests run with `python -m pytest tests/` (pytest is installed)."
CMAKE = "cmake and ctest are installed"

TASKS_DEF = [
    dict(name="py-click-choice-brackets", repo="click", sha="762c97eef7", lang="Python",
         tests=["tests/test_basic.py"],
         check="timeout 600 python -m pytest -q -p no:cacheprovider tests/test_basic.py",
         text="""The usage line that `--help` prints shows doubled square brackets for an optional argument whose type is a `Choice`:

```python
import click

@click.command()
@click.argument("method", type=click.Choice(["foo", "bar", "baz"]), nargs=-1)
def cli(method):
    pass
```

`cli --help` prints `Usage: cli [OPTIONS] [[foo|bar|baz]]...` -- the choices end up inside two pairs of brackets. The same happens with `required=False` instead of `nargs=-1`. It should read `[foo|bar|baz]...` (and `[foo|bar|baz]` for the optional one). Please fix this in click.

""" + PY_TESTS),
    dict(name="py-moreitertools-numeric-range", repo="more-itertools", sha="edb3346f83", lang="Python",
         tests=["tests/test_more.py"],
         check="timeout 600 python -m pytest -q -p no:cacheprovider tests/test_more.py",
         text="""`reversed()` does not work on an empty `numeric_range`:

```python
import more_itertools as mi
list(reversed(mi.numeric_range(0)))
```

This should give `[]`, like `list(reversed(range(0)))` does, but it raises an exception instead. Please fix `numeric_range` so reversing an empty range works.

""" + PY_TESTS),
    dict(name="c-cjson-minify-hang", repo="cJSON", sha="08d2bc766a", lang="C",
         tests=["tests/minify_tests.c"],
         check="gcc -std=c89 -Itests -o .grade_minify tests/minify_tests.c tests/unity/src/unity.c -lm && timeout 30 ./.grade_minify",
         text="""`cJSON_Minify` never returns for some inputs. For example:

```c
char json[] = "8 / 5\\n";
cJSON_Minify(json);   /* hangs forever */
```

A `/` that does not start a comment sends it into an endless loop. It should minify the text and return. Please fix this in cJSON.

The unit tests are C programs under `tests/` (Unity). With """ + CMAKE + """: `cmake -S . -B build -DENABLE_CJSON_TEST=On`, `cmake --build build`, then `ctest --test-dir build`."""),
    dict(name="c-cjson-getobject-array-crash", repo="cJSON", sha="be749d7efa", lang="C",
         tests=["tests/misc_tests.c"],
         check="gcc -std=c89 -Itests -o .grade_misc tests/misc_tests.c tests/unity/src/unity.c -lm && timeout 30 ./.grade_misc",
         text="""Asking a JSON array for an object member crashes the program:

```c
cJSON *array = cJSON_Parse("[1]");
cJSON *item = cJSON_GetObjectItemCaseSensitive(array, "name");   /* segmentation fault */
```

`cJSON_GetObjectItem` misbehaves on arrays in the same way. Both should simply return NULL when there is no member with that name. Please fix this in cJSON.

The unit tests are C programs under `tests/` (Unity). With """ + CMAKE + """: `cmake -S . -B build -DENABLE_CJSON_TEST=On`, `cmake --build build`, then `ctest --test-dir build`."""),
    dict(name="cpp-cxxopts-float-trailing-text", repo="cxxopts", sha="788a80e22b", lang="C++",
         tests=["test/options.cpp"],
         check="g++ -std=c++11 -Iinclude -Itest -o .grade_options test/main.cpp test/options.cpp && timeout 120 ./.grade_options",
         text="""Float options accept trailing garbage that integer options reject: `--int 123abc` throws `incorrect_argument_type`, but `--float 2.5typo` is accepted as 2.5 (and `nanXYZ` as nan). A floating-point value followed by other text should be rejected the same way an integer is. Please fix this in cxxopts.

The tests are in `test/` (Catch). With """ + CMAKE + """: `cmake -S . -B build -DCXXOPTS_BUILD_TESTS=ON`, `cmake --build build`, then `ctest --test-dir build`."""),
    dict(name="cpp-argparse-empty-argv", repo="argparse", sha="ce07b1398d", lang="C++",
         tests=["test/test_parse_args.cpp", "test/test_parse_known_args.cpp"],
         check="g++ -std=c++17 -Iinclude -Itest -DDOCTEST_CONFIG_IMPLEMENT_WITH_MAIN -c test/main.cpp -o .grade_main.o && g++ -std=c++17 -Iinclude -Itest -o .grade_args .grade_main.o test/test_parse_args.cpp test/test_parse_known_args.cpp && timeout 120 ./.grade_args",
         text="""Parsing an empty argument vector is broken:

```cpp
argparse::ArgumentParser program("test");
program.parse_args(std::vector<std::string>{});        // undefined behaviour
program.parse_known_args(std::vector<std::string>{});  // same
```

Both read past the end of the vector, so the program can crash or behave randomly. An empty vector should parse cleanly, as one holding only the program name does; a required argument that is missing should still be reported as an error. Please fix this in argparse.

The tests are in `test/` (doctest). With """ + CMAKE + """: `cmake -S . -B build -DARGPARSE_BUILD_TESTS=ON`, `cmake --build build`, then `ctest --test-dir build`."""),
    dict(name="java-gson-duplicate-key-null", repo="gson", sha="f4d371d29c", lang="Java",
         tests=["gson/src/test/java/com/google/gson/functional/MapTest.java",
                "gson/src/test/java/com/google/gson/functional/MapAsArrayTypeAdapterTest.java"],
         check="timeout 900 mvn -q test -pl gson -Dtest=MapTest,MapAsArrayTypeAdapterTest -Dsurefire.failIfNoSpecifiedTests=false",
         text="""Gson does not reject a duplicate key in a map when the first value is null:

```java
Type type = new TypeToken<Map<String, Integer>>() {}.getType();
gson.fromJson("{\\"a\\":null,\\"a\\":1}", type);   // returns {a=1}
gson.fromJson("{\\"a\\":1,\\"a\\":2}", type);      // throws JsonSyntaxException: duplicate key: a
```

A duplicate key should be rejected with `duplicate key: a` whatever the first value is. The same happens in the array-of-entries form that `enableComplexMapKeySerialization()` reads, e.g. `[["a",null],["a","x"]]`. Please fix this in Gson.

Tests run with Maven, offline: `mvn test -pl gson -Dtest=<TestClass>`."""),
    dict(name="java-jsoup-fragment-ioob", repo="jsoup", sha="c4bf5ba304", lang="Java",
         tests=["src/test/java/org/jsoup/integration/FuzzFixesTest.java"],
         check="timeout 900 mvn -q test -Dtest=FuzzFixesTest -Dsurefire.failIfNoSpecifiedTests=false",
         text="""Parsing this body fragment throws an exception (found by fuzzing):

```java
Parser.htmlParser().parseFragmentInput("<frameset>>l\\u0000<\\u0000<ditl>\\u0000< \\\\", new Element("colgroup"), "");
```

```
java.lang.IndexOutOfBoundsException: Index 1 out of bounds for length 0
	at java.base/java.util.ArrayList.get(ArrayList.java:427)
	at org.jsoup.parser.HtmlTreeBuilderState$7.inBodyStartTag(HtmlTreeBuilderState.java)
	at org.jsoup.parser.HtmlTreeBuilderState$7.process(HtmlTreeBuilderState.java)
	at org.jsoup.parser.HtmlTreeBuilder.process(HtmlTreeBuilder.java)
	at org.jsoup.parser.TreeBuilder.runParser(TreeBuilder.java)
	at org.jsoup.parser.TreeBuilder.parseFragment(TreeBuilder.java)
	at org.jsoup.parser.Parser.parseFragmentInput(Parser.java)
```

jsoup should never throw on malformed input; this should be handled as a parse error. Please fix it.

Tests run with Maven, offline: `mvn test -Dtest=<TestClass>`."""),
]


# ROUND 6 (rules in docs/AGENT_WORKFLOW_EVAL.md): twelve fixes merged after 2026-06-01, three per language,
# each a user-visible bug with the project's own regression test. Picked from scan/candidates2.py's lists
# for being deterministic: two fixes of undefined behaviour (json-c's INT64_MIN negation, utf8proc's heap
# overflow) were left out because without a sanitizer the code before the fix passes their tests too.
JSONC = "cmake and ctest are installed: `cmake -S . -B build`, `cmake --build build`, then `ctest --test-dir build`."
MVN = "Tests run with Maven, offline: `mvn test -Dtest=<TestClass>`."
ROUND6 = [
    dict(name="py-tomlkit-array-slice-delete", repo="tomlkit", sha="8c30fc17d9", lang="Python",
         tests=["tests/test_items.py"],
         check="timeout 600 python -m pytest -q -p no:cacheprovider tests/test_items.py",
         text="""Deleting a slice of a tomlkit array leaves the TOML it renders out of step with the list:

```python
import tomlkit
a = tomlkit.item([1, 2, 3])
del a[:0]          # deletes nothing
list(a)            # [1, 2, 3]
a.as_string()      # should still be "[1, 2, 3]", but the rendered array has lost its elements
```

Slices with negative bounds go wrong too: after `del a[-1:]` the list is `[1, 2]`, and after `del a[:-1]` it is `[3]`, but `as_string()` renders something else. What is rendered should always match what is left in the array. Please fix this in tomlkit.

""" + PY_TESTS),
    dict(name="py-marshmallow-url-fragment", repo="marshmallow", sha="902f99c415", lang="Python",
         tests=["tests/test_validate.py"],
         check="timeout 600 python -m pytest -q -p no:cacheprovider tests/test_validate.py",
         text="""`marshmallow.validate.URL()` rejects a valid URL whose fragment comes right after the host:

```python
from marshmallow import validate
validate.URL()("https://example.com/#frag")   # accepted
validate.URL()("https://example.com#frag")    # raises ValidationError: Not a valid URL.
```

RFC 3986 allows a fragment after an empty path, and a link to an anchor on a site's root page looks exactly like this. `https://example.com#frag` should be accepted -- and with `relative=True`, a bare `#frag` too, the way a bare `?query` already is. Please fix this in marshmallow.

""" + PY_TESTS),
    dict(name="py-toolz-interpose-empty", repo="toolz", sha="d2eba0374a", lang="Python",
         tests=["toolz/tests/test_itertoolz.py"],
         check="timeout 600 python -m pytest -q -p no:cacheprovider toolz/tests/test_itertoolz.py",
         text="""`toolz.interpose` fails on an empty sequence:

```python
from toolz import interpose
list(interpose("a", [1, 2, 3]))   # [1, 'a', 2, 'a', 3]
list(interpose("a", []))          # raises StopIteration
```

Interposing into an empty sequence should simply give nothing. Please fix this in toolz.

Tests run with `python -m pytest toolz/tests/` (pytest is installed)."""),
    dict(name="c-jsonc-patch-sibling-prefix", repo="json-c", sha="1b504e26ab", lang="C",
         tests=["tests/json_patch_tests.json", "tests/test_json_patch.expected"],
         check="cmake -S . -B .grade >/dev/null && cmake --build .grade -j4 >/dev/null && cd .grade && timeout 120 ctest -R '^test_json_patch$' --output-on-failure",
         text="""json-c's JSON Patch support refuses to move or copy a value between two sibling keys when one key's name starts with the other's:

```json
[ { "op": "move", "from": "/aa", "path": "/aab" } ]
```

applied to `{"aa": 1}` fails with "Invalid attempt to move parent under a child" -- but `/aab` is not inside `/aa`; it is a different key that happens to start with the same letters. `copy` fails the same way. RFC 6902 only forbids moving a location into one of its own children. Please fix this in json-c.

The tests are under `tests/` (the JSON Patch cases are in `tests/json_patch_tests.json`). With """ + JSONC),
    dict(name="c-jsonc-put-return-value", repo="json-c", sha="69be99ef8e", lang="C",
         tests=["tests/test_deep_nesting.c", "tests/test_deep_nesting.expected"],
         check="cmake -S . -B .grade >/dev/null && cmake --build .grade -j4 >/dev/null && cd .grade && timeout 120 ctest -R '^test_deep_nesting$' --output-on-failure",
         text="""`json_object_put()` no longer returns what its documentation in `json_object.h` promises. It should return 1 when the object was freed and 0 when a reference to it remains, but now it returns 0 when it frees a scalar, a string or an empty container:

```c
json_object_put(json_object_new_int(42));        /* returns 0, should be 1 */
json_object_put(json_object_new_string("foo"));  /* returns 0, should be 1 */
json_object_put(json_object_new_object());       /* returns 0, should be 1 */
```

This started with the rewrite that made freeing deeply nested objects iterative. Please fix this in json-c.

The tests are under `tests/`. With """ + JSONC),
    dict(name="c-cmark-reference-title-garbage", repo="cmark", sha="7042d9978b", lang="C",
         tests=["test/regression.txt"],
         check="cmake -S . -B .grade >/dev/null && cmake --build .grade -j4 >/dev/null && cd .grade && timeout 120 ctest -R '^regressiontest_executable$' --output-on-failure",
         text="""cmark keeps an invalid title on a link reference definition:

```markdown
[bla]: /bla
"The title" garbage
[bla]
```

renders as `<a href="/bla" title="The title">bla</a>`, but `"The title" garbage` is not a valid title -- there is text after it -- so the CommonMark spec (and its reference implementation) give the link no title at all, and that second line is ordinary paragraph text. Please fix this in cmark.

The tests are under `test/`: `test/spec_tests.py` runs a spec file such as `test/regression.txt` against a built `cmark`. With cmake and ctest installed: `cmake -S . -B build`, `cmake --build build`, then `ctest --test-dir build`."""),
    dict(name="cpp-fmt-negative-duration-sign", repo="fmt", sha="434e30f81f", lang="C++",
         tests=["test/chrono-test.cc"],
         check="cmake -S . -B .grade -DFMT_TEST=ON >/dev/null && cmake --build .grade -j4 --target chrono-test >/dev/null && cd .grade && timeout 300 ctest -R '^chrono-test$' --output-on-failure",
         text="""{fmt} gets the sign wrong when it formats a negative floating-point duration with more than one field:

```cpp
auto d = std::chrono::duration<double>(-61.25);
fmt::format("{:%S %M}", d);   // should be "-01.250000 01"
fmt::format("{:%S %Q}", d);   // should be "-01.250000 61.25"
```

The minus sign should appear once, at the start, as it does for integer durations; and a negative infinity should format as `-inf` (`{:%Q}`, `{:%S}`, `{:%H}`, ...). Please fix this in {fmt}.

The tests are in `test/` (Google Test, bundled). With cmake and ctest installed: `cmake -S . -B build -DFMT_TEST=ON`, `cmake --build build --target chrono-test`, then `ctest --test-dir build -R chrono`."""),
    dict(name="cpp-cli11-complex-exponent", repo="CLI11", sha="aadf041625", lang="C++",
         tests=["tests/NewParseTest.cpp"],
         # The test binary itself: CLI11's ctest run first starts helper programs only a full build makes.
         check="cmake -S . -B .grade -DCLI11_BUILD_TESTS=ON >/dev/null && cmake --build .grade -j4 --target NewParseTest >/dev/null && timeout 300 ./.grade/tests/NewParseTest",
         text="""A `std::complex` option in CLI11 cannot be given a value whose imaginary part has an exponent:

```cpp
std::complex<double> c;
app.add_option("-c,--complex", c);
// -c 1+2e-3i       ->  "Could not convert: -c = 1+2e-3i"
// -c 1.5e-1-2.5e-1i  and  -c 2e-3i  fail the same way
```

Exponents work for `double` options, and in the real part (`1e-3+2i`). They should work in the imaginary part too, without breaking hex input such as `0x1e+2i`. Please fix this in CLI11.

The tests are in `tests/` (Catch, bundled). With cmake and ctest installed: `cmake -S . -B build -DCLI11_BUILD_TESTS=ON`, `cmake --build build`, then `ctest --test-dir build`."""),
    dict(name="cpp-yamlcpp-base64-padding-space", repo="yaml-cpp", sha="8eb618e889", lang="C++",
         tests=["test/binary_test.cpp"],
         check="cmake -S . -B .grade -DYAML_CPP_BUILD_TESTS=ON >/dev/null && cmake --build .grade -j4 --target yaml-cpp-tests >/dev/null && timeout 300 ./.grade/test/yaml-cpp-tests --gtest_filter='BinaryTest.*'",
         text="""yaml-cpp decodes a `!!binary` value wrongly when whitespace separates its Base64 padding:

```cpp
YAML::DecodeBase64("TQ==");    // {'M'}
YAML::DecodeBase64("TQ= =");   // should also be {'M'}, but is not
```

YAML 1.1 binary scalars may contain whitespace anywhere, and PyYAML and libyaml decode `TQ= =` as the single byte `M`. Please fix this in yaml-cpp.

The tests are in `test/` (Google Test, bundled). With cmake installed: `cmake -S . -B build -DYAML_CPP_BUILD_TESTS=ON`, `cmake --build build --target yaml-cpp-tests`, then run `build/test/yaml-cpp-tests`."""),
    dict(name="java-commons-lang-plus-long", repo="commons-lang", sha="9a92df9fa3", lang="Java",
         tests=["src/test/java/org/apache/commons/lang3/math/NumberUtilsTest.java"],
         check="timeout 900 mvn -q test -Dtest=NumberUtilsTest -Dsurefire.failIfNoSpecifiedTests=false -Drat.skip=true -Djacoco.skip=true",
         text="""`NumberUtils.createNumber()` rejects a positive `long` literal written with an explicit `+` sign:

```java
NumberUtils.createNumber("12345L");    // 12345 (a Long)
NumberUtils.createNumber("-12345L");   // -12345
NumberUtils.createNumber("+12345L");   // throws NumberFormatException
```

The same goes for a lower-case `l`, and for values too large for a `long` (`"+12345678901234567890L"` should give a `BigInteger`); `NumberUtils.isCreatable()` disagrees with them in the same way. A leading `+` should be accepted wherever a leading `-` is. Please fix this in Commons Lang.

""" + MVN),
    dict(name="java-commons-text-csv-lone-quote", repo="commons-text", sha="e2eb7d1afd", lang="Java",
         tests=["src/test/java/org/apache/commons/text/translate/CsvTranslatorsTest.java"],
         check="timeout 900 mvn -q test -Dtest=CsvTranslatorsTest -Dsurefire.failIfNoSpecifiedTests=false -Drat.skip=true -Djacoco.skip=true",
         text="""Unescaping a CSV value that is a single double-quote throws instead of returning it:

```java
StringEscapeUtils.unescapeCsv("\\"");                       // StringIndexOutOfBoundsException
new CsvTranslators.CsvUnescaper().translate("\\"");         // the same
```

A lone `"` is not a quoted CSV value, so it should come back unchanged, as `"` -- the way other values that are not quoted do. Please fix this in Commons Text.

""" + MVN),
    dict(name="java-gson-number-overflow-zero", repo="gson", sha="004e7a4949", lang="Java",
         tests=["gson/src/test/java/com/google/gson/stream/JsonReaderTest.java"],
         check="timeout 900 mvn -q test -pl gson -Dtest=JsonReaderTest -Dsurefire.failIfNoSpecifiedTests=false",
         text="""Gson's `JsonReader` in strict mode rejects some valid numbers:

```java
JsonReader reader = new JsonReader(new StringReader("184467440737095516160"));
reader.setStrictness(Strictness.STRICT);
reader.peek();   // should be NUMBER; the number is rejected as malformed instead
```

`184467440737095516160` is 2^64 followed by a 0. The easiest way to hit this is to serialize a `BigInteger` with that value and read it back. Any digits without a leading zero are a valid JSON number. Please fix this in Gson.

Tests run with Maven, offline: `mvn test -pl gson -Dtest=<TestClass>`."""),
]
TASKS_DEF += ROUND6


def run(cmd, cwd=None, **kw):
    return subprocess.run(cmd, cwd=cwd, shell=isinstance(cmd, str), capture_output=True, text=True, **kw)


def fetch(name, sha):
    """Clone a task's repository the first time it is needed, and deepen a shallow clone (the
    candidate scan makes those) until the fix and its parent are in it."""
    path = os.path.join(REPOS, name)
    if not os.path.isdir(path):
        os.makedirs(REPOS, exist_ok=True)
        r = run(["git", "clone", "-q", REPO_URLS[name], path])
        assert r.returncode == 0, r.stderr
    if run(["git", "cat-file", "-e", sha + "^"], cwd=path).returncode:
        r = run(["git", "fetch", "-q", "--unshallow"], cwd=path)
        assert r.returncode == 0 or "complete repository" in r.stderr, r.stderr
    return path


def build(t):
    repo, sha = fetch(t["repo"], t["sha"]), t["sha"]
    out = os.path.join(TASKS, t["name"])
    shutil.rmtree(out, ignore_errors=True)
    ws = os.path.join(out, "workspace")
    os.makedirs(out)
    # The tree just before the fix, as a fresh repository of one commit.
    r = run(["git", "clone", "-q", "--no-checkout", "--shared", repo, ws])
    assert r.returncode == 0, r.stderr
    r = run(["git", "checkout", "-q", sha + "^"], cwd=ws)
    assert r.returncode == 0, r.stderr
    shutil.rmtree(os.path.join(ws, ".git"))
    env = dict(os.environ, GIT_AUTHOR_NAME="base", GIT_AUTHOR_EMAIL="base@example.invalid",
               GIT_COMMITTER_NAME="base", GIT_COMMITTER_EMAIL="base@example.invalid")
    for c in (["git", "init", "-q"], ["git", "add", "-A"], ["git", "commit", "-q", "-m", "base"]):
        r = run(c, cwd=ws, env=env)
        assert r.returncode == 0, (c, r.stderr)
    changed = run(["git", "diff", "--name-only", sha + "^", sha], cwd=repo).stdout.split()
    tests = [p for p in changed if p in t["tests"]]
    assert sorted(tests) == sorted(t["tests"]), (t["name"], changed)
    src = [p for p in changed if p not in t["tests"] and not p.endswith((".md", ".rst", ".txt")) and "CHANGES" not in p]
    with open(os.path.join(out, "hidden.patch"), "w") as f:
        f.write(run(["git", "diff", sha + "^", sha, "--", *tests], cwd=repo).stdout)
    with open(os.path.join(out, "solution.patch"), "w") as f:
        f.write(run(["git", "diff", sha + "^", sha, "--", *src], cwd=repo).stdout)
    with open(os.path.join(out, "task.txt"), "w") as f:
        f.write(t["text"].strip() + "\n")
    with open(os.path.join(out, "check.sh"), "w") as f:
        f.write("#!/bin/sh\n# check.sh <dir>: add the fix's own tests to <dir>, then run them. Exit 0 = pass.\n"
                "here=$(cd \"$(dirname \"$0\")\" && pwd)\ncd \"$1\" || exit 2\n"
                "# As SWE-bench does: the files the hidden tests change go back to the task's own\n"
                "# version first, so test edits the agent made there cannot stop them applying.\n"
                "base=$(git rev-list --max-parents=0 HEAD | tail -1)\n"
                "for f in $(git apply --numstat \"$here/hidden.patch\" | cut -f3); do\n"
                "  if git cat-file -e \"$base:$f\" 2>/dev/null; then git checkout -q \"$base\" -- \"$f\"; else rm -f \"$f\"; fi\n"
                "done\n"
                "git apply --whitespace=nowarn \"$here/hidden.patch\" || { echo 'GRADE: the hidden tests did not apply'; exit 3; }\n"
                + t["check"] + "\n")
    os.chmod(os.path.join(out, "check.sh"), 0o755)
    with open(os.path.join(out, "meta.txt"), "w") as f:
        f.write(f"repo={t['repo']}\nfix={sha}\nlang={t['lang']}\nsource={' '.join(src)}\ntests={' '.join(tests)}\n")
    return out, src


def grade(out, with_solution):
    d = tempfile.mkdtemp(prefix="grade-")
    try:
        shutil.copytree(os.path.join(out, "workspace"), d, dirs_exist_ok=True, symlinks=True)
        if with_solution:
            r = run(["git", "apply", os.path.join(out, "solution.patch")], cwd=d)
            if r.returncode:
                return False, "solution did not apply: " + r.stderr
        r = run([os.path.join(out, "check.sh"), d])
        return r.returncode == 0, (r.stdout + r.stderr)[-600:]
    finally:
        shutil.rmtree(d, ignore_errors=True)


if __name__ == "__main__":
    args = sys.argv[1:]
    validate = "--validate" in args
    names = [a for a in args if not a.startswith("--")]
    for t in TASKS_DEF:
        if names and t["name"] not in names:
            continue
        out, src = build(t)
        line = f"{t['name']:34s} {t['lang']:6s} source={','.join(src)}"
        if validate:
            bad, why_bad = grade(out, False)
            good, why_good = grade(out, True)
            ok = (not bad) and good
            line += f"  as given: {'PASS' if bad else 'fail'}  with the real fix: {'PASS' if good else 'fail'}  -> {'VALID' if ok else 'INVALID'}"
            if not ok:
                line += "\n    as given: " + why_bad.replace("\n", " | ")[-300:] + "\n    with fix: " + why_good.replace("\n", " | ")[-300:]
        print(line, flush=True)
