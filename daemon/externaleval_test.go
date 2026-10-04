//go:build eval

// THE OUTSIDE REPOSITORIES: retrieval measured on code this daemon was not
// tuned on.
//
// WHY THIS FILE EXISTS. Every retrieval number this project has is measured on
// this repository -- Go, written by the people who tuned the ranker, indexed by
// the eval that scores it. Several choices in the pipeline are visibly shaped
// by that: test files are recognised only as _test.go (fileclass.go), the
// setup-file down-weight names only Go files (rerank.go), and constructs are
// found at column zero, which is where Go puts its methods and Java, Python,
// Rust and TypeScript do not (chunkcontext.go). None of that can show up in an
// eval whose corpus is Go. These questions run the same pipeline over four
// public repositories in four other languages.
//
// HOW THEY WERE WRITTEN, honestly. Unlike heldout_eval_test.go, these could
// not be worded before the code was opened: a question about an unfamiliar
// repository needs its answer located before its anchor can be written. The
// plain-language questions are therefore worded from what the code DOES, in a
// user's words, and avoid its identifiers; the agent-shaped ones use
// identifiers on purpose, because that is how a model searches.
//
// THE SPLIT, fixed before any run: every third question of each repository
// (by position) is held out. Levers may be chosen on the other ten; the held-out
// five are only ever asked whether a choice holds.
//
// THE REPOSITORIES are pinned in testdata/evalrepos/repos.txt and fetched by
// scripts/fetch-eval-repos.sh into $MOCHIII_EVAL_REPOS (default
// ~/.cache/mochiii-eval-repos). Nothing of theirs is committed here. A missing
// clone skips; a clone at the wrong commit fails, because the anchors name
// lines of exactly the pinned trees.
//
//	scripts/fetch-eval-repos.sh
//	go test -tags eval -count=1 -run TestExternalEvalGroundTruth -v ./
//	go test -tags eval -count=1 -timeout 90m -run TestExternalRepoRetrieval -v ./
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

var externalEvalQuestions = map[string][]rerankEvalQuery{
	"flask": {
		{"how is the session cookie signed so that cookies made with an older secret key still work",
			[]string{"src/flask/sessions.py"}, []string{"keys.append(app.secret_key)"}, shapeImpl},
		{"how does the command line tool find the application object inside a module when no name is given",
			[]string{"src/flask/cli.py"}, []string{"def find_best_app(module: ModuleType) -> Flask:"}, shapeImpl},
		{"load settings from environment variables that share a prefix, including nested keys",
			[]string{"src/flask/config.py"}, []string{"def from_prefixed_env("}, shapeImpl},
		{"which error handler runs when a view inside a blueprint raises an exception",
			[]string{"src/flask/sansio/app.py"}, []string{"def _find_error_handler("}, shapeImpl},
		{"where is the maximum request body size enforced and can one view change it",
			[]string{"src/flask/wrappers.py"}, []string{"def max_content_length(self) -> int | None:"}, shapeImpl},
		{"how are url prefixes combined when a blueprint is registered inside another blueprint",
			[]string{"src/flask/sansio/blueprints.py"}, []string{`state.url_prefix.rstrip("/") + "/" + bp_url_prefix.lstrip("/")`}, shapeImpl},
		{"how is it decided whether templates are reloaded automatically when they change on disk",
			[]string{"src/flask/app.py"}, []string{`auto_reload = self.config["TEMPLATES_AUTO_RELOAD"]`}, shapeImpl},
		{"how are coroutine view functions run by a synchronous server",
			[]string{"src/flask/app.py"}, []string{"def async_to_sync("}, shapeImpl},
		{"how does the session keep tuples, bytes and dates intact when it is stored as JSON",
			[]string{"src/flask/json/tag.py"}, []string{"class TaggedJSONSerializer:"}, shapeImpl},
		{"when is an unhandled exception raised again instead of turning into a 500 page",
			[]string{"src/flask/app.py"}, []string{`propagate = self.config["PROPAGATE_EXCEPTIONS"]`}, shapeImpl},
		{"url_for _external _scheme ValueError",
			[]string{"src/flask/app.py"}, []string{`raise ValueError("When specifying '_scheme', '_external' must be True.")`}, shapeAgent},
		{"MethodView dispatch_request HEAD falls back to get",
			[]string{"src/flask/views.py"}, []string{"meth = getattr(self, request.method.lower(), None)"}, shapeAgent},
		{"load_dotenv .flaskenv .env precedence",
			[]string{"src/flask/cli.py"}, []string{"def load_dotenv("}, shapeAgent},
		{"stream_with_context generator app context",
			[]string{"src/flask/helpers.py"}, []string{"if (ctx := _cv_app.get(None)) is None:"}, shapeAgent},
		{"test that a session survives rotating the secret key using fallback keys",
			[]string{"tests/test_basic.py"}, []string{"def test_session_secret_key_fallbacks("}, shapeTest},
	},
	"hono": {
		{"how does the request size limit handle an upload that is chunked and has no content length",
			[]string{"src/middleware/body-limit/index.ts"}, []string{"const rawReader = c.req.raw.body.getReader()"}, shapeImpl},
		{"how are cross-site form submissions rejected using the origin and fetch metadata headers",
			[]string{"src/middleware/csrf/index.ts"}, []string{"// denied always when origin header is not present"}, shapeImpl},
		{"how does the router that wraps several routers decide which one to keep using",
			[]string{"src/router/smart-router/router.ts"}, []string{"if (e instanceof UnsupportedPathError) {"}, shapeImpl},
		{"what response is sent when a handler throws an http exception compared with any other error",
			[]string{"src/hono-base.ts"}, []string{"return c.text('Internal Server Error', 500)"}, shapeImpl},
		{"how is the signature of a signed cookie checked",
			[]string{"src/utils/cookie.ts"}, []string{"const verifySignature = async ("}, shapeImpl},
		{"when verifying a token what stops the header from switching to a different signing algorithm",
			[]string{"src/utils/jwt/jwt.ts"}, []string{"if (header.alg !== alg) {"}, shapeImpl},
		{"how does static file serving refuse request paths that try to climb out of the root directory",
			[]string{"src/middleware/serve-static/index.ts"}, []string{`if (/(?:^|[\/\\])\.{1,2}(?:$|[\/\\])|[\/\\]{2,}|\\/.test(filename)) {`}, shapeImpl},
		{"how does the middleware decide to answer 304 not modified from the request headers",
			[]string{"src/middleware/etag/index.ts"}, []string{"function etagMatches(etag: string, ifNoneMatch: string | null) {"}, shapeImpl},
		{"how are all registered routes compiled into one regular expression",
			[]string{"src/router/reg-exp-router/trie.ts"}, []string{"buildRegExp(): [RegExp, ReplacementMap, ReplacementMap] {"}, shapeImpl},
		{"how does mounting a sub application under a path prefix work",
			[]string{"src/hono-base.ts"}, []string{"const subApp = this.basePath(path)"}, shapeImpl},
		{"parseBody multipart formData all dot options",
			[]string{"src/utils/body.ts"}, []string{"export const parseBody: ParseBody = async ("}, shapeAgent},
		{"streamSSE writeSSE event id retry",
			[]string{"src/helper/streaming/sse.ts"}, []string{"async writeSSE(message: SSEMessage) {"}, shapeAgent},
		{"ipRestriction denyList allowList",
			[]string{"src/middleware/ip-restriction/index.ts"}, []string{"export const ipRestriction = ("}, shapeAgent},
		{"bearerAuth verifyToken prefix header",
			[]string{"src/middleware/bearer-auth/index.ts"}, []string{"export const bearerAuth = <E extends Env = Env>("}, shapeAgent},
		{"test for the default cors preflight response headers",
			[]string{"src/middleware/cors/index.test.ts"}, []string{"it('Preflight default', async () => {"}, shapeTest},
	},
	"ripgrep": {
		{"how does the searcher stop reading a file once it sees a NUL byte",
			[]string{"crates/searcher/src/line_buffer.rs"}, []string{"BinaryDetection::Quit(byte) => {"}, shapeImpl},
		{"when several ignore rules match a path which one wins and how are negated rules handled",
			[]string{"crates/ignore/src/gitignore.rs"}, []string{"for &i in matches.iter().rev() {"}, shapeImpl},
		{"how does the parallel directory walk share work between threads",
			[]string{"crates/ignore/src/walk.rs"}, []string{"/// A work-stealing stack."}, shapeImpl},
		{"how is a glob pattern turned into a regular expression",
			[]string{"crates/globset/src/glob.rs"}, []string{"fn to_regex_with(&self, options: &GlobOptions) -> String {"}, shapeImpl},
		{"when does smart case make a pattern match case insensitively",
			[]string{"crates/regex/src/config.rs"}, []string{"analysis.any_literal() && !analysis.any_uppercase()"}, shapeImpl},
		{"under what conditions is a file read through a memory map",
			[]string{"crates/searcher/src/searcher/mmap.rs"}, []string{"match unsafe { Mmap::map(file) } {"}, shapeImpl},
		{"which external programs are run to search compressed files",
			[]string{"crates/cli/src/decompress.rs"}, []string{"fn default_decompression_commands() -> Vec<DecompressionCommand> {"}, shapeImpl},
		{"how are files over the maximum size skipped while walking directories",
			[]string{"crates/ignore/src/walk.rs"}, []string{"fn skip_filesize("}, shapeImpl},
		{"how is each line of the machine readable output format written",
			[]string{"crates/printer/src/json.rs"}, []string{"json::to_writer_pretty(&mut self.wtr, message)?;"}, shapeImpl},
		{"how are the placeholders in a hyperlink format filled in with the path, line and column",
			[]string{"crates/printer/src/hyperlink/mod.rs"}, []string{"fn interpolate_to("}, shapeImpl},
		{"DEFAULT_TYPES file type globs",
			[]string{"crates/ignore/src/default_types.rs"}, []string{"pub(crate) const DEFAULT_TYPES: &[(&[&str], &[&str])] = &["}, shapeAgent},
		{"pcre2 jit_if_available max_jit_stack_size",
			[]string{"crates/core/flags/hiargs.rs"}, []string{".max_jit_stack_size(Some(10 * (1 << 20)));"}, shapeAgent},
		{"DecodeReaderBytesBuilder strip_bom utf16 transcoding",
			[]string{"crates/searcher/src/searcher/mod.rs"}, []string{"let mut decode_builder = DecodeReaderBytesBuilder::new();"}, shapeAgent},
		{"gitconfig_excludes_path core.excludesFile",
			[]string{"crates/ignore/src/gitignore.rs"}, []string{"pub fn gitconfig_excludes_path() -> Option<PathBuf> {"}, shapeAgent},
		{"regression test that a global gitignore still applies when run from a subdirectory",
			[]string{"tests/regression.rs"}, []string{"rgtest!(r3179_global_gitignore_cwd, |dir: Dir, mut cmd: TestCommand| {"}, shapeTest},
	},
	"gson": {
		{"how does it decide to skip a field when serializing, such as transient or static fields",
			[]string{"gson/src/main/java/com/google/gson/internal/Excluder.java"}, []string{"public boolean excludeField(Field field, boolean serialize) {"}, shapeImpl},
		{"how is an object created for a class that has no constructor without arguments",
			[]string{"gson/src/main/java/com/google/gson/internal/ConstructorConstructor.java"}, []string{"private <T> ObjectConstructor<T> newUnsafeAllocator(Class<? super T> rawType) {"}, shapeImpl},
		{"how is a camel case java field name turned into a lower case name separated by underscores",
			[]string{"gson/src/main/java/com/google/gson/FieldNamingPolicy.java"}, []string{"static String separateCamelCase(String name, char separator) {"}, shapeImpl},
		{"what happens when the adapter for a type is requested again while it is still being built, for recursive types",
			[]string{"gson/src/main/java/com/google/gson/Gson.java"}, []string{"FutureTypeAdapter<T> call = new FutureTypeAdapter<>();"}, shapeImpl},
		{"where does the reader reject non-standard input like comments unless lenient parsing is on",
			[]string{"gson/src/main/java/com/google/gson/stream/JsonReader.java"}, []string{"private void checkLenient() throws MalformedJsonException {"}, shapeImpl},
		{"how does the streaming reader recognise a number and decide whether it fits in a long",
			[]string{"gson/src/main/java/com/google/gson/stream/JsonReader.java"}, []string{"private int peekNumber() throws IOException {"}, shapeImpl},
		{"how are characters like angle brackets and ampersands escaped when writing strings in html safe mode",
			[]string{"gson/src/main/java/com/google/gson/stream/JsonWriter.java"}, []string{"HTML_SAFE_REPLACEMENT_CHARS = REPLACEMENT_CHARS.clone();"}, shapeImpl},
		{"how are maps whose keys are objects written out as arrays of key and value pairs",
			[]string{"gson/src/main/java/com/google/gson/internal/bind/MapTypeAdapterFactory.java"}, []string{"hasComplexKeys |= keyElement.isJsonArray() || keyElement.isJsonObject();"}, shapeImpl},
		{"how are date strings in the international standard format with time zones parsed",
			[]string{"gson/src/main/java/com/google/gson/internal/bind/util/ISO8601Utils.java"}, []string{"public static Date parse(String date, ParsePosition pos) throws ParseException {"}, shapeImpl},
		{"how does an anonymous subclass capture a generic type such as a list of strings at runtime",
			[]string{"gson/src/main/java/com/google/gson/reflect/TypeToken.java"}, []string{"private Type getTypeTokenTypeArgument() {"}, shapeImpl},
		{"ReflectionAccessFilter BLOCK_INACCESSIBLE_JAVA",
			[]string{"gson/src/main/java/com/google/gson/ReflectionAccessFilter.java"}, []string{"ReflectionAccessFilter BLOCK_INACCESSIBLE_JAVA ="}, shapeAgent},
		{"LinkedTreeMap rebalance rotate",
			[]string{"gson/src/main/java/com/google/gson/internal/LinkedTreeMap.java"}, []string{"private void rebalance(Node<K, V> unbalanced, boolean insert) {"}, shapeAgent},
		{"SerializedName alternate field names",
			[]string{"gson/src/main/java/com/google/gson/internal/bind/ReflectiveTypeAdapterFactory.java"}, []string{"alternates = Arrays.asList(annotation.alternate());"}, shapeAgent},
		{"JsonTreeReader peek JsonElement stack",
			[]string{"gson/src/main/java/com/google/gson/internal/bind/JsonTreeReader.java"}, []string{"public JsonToken peek() throws IOException {"}, shapeAgent},
		{"test that serializing objects which reference each other in a loop fails",
			[]string{"gson/src/test/java/com/google/gson/functional/CircularReferenceTest.java"}, []string{"public void testCircularSerialization() {"}, shapeTest},
	},
}

// splitExternal returns a repository's tuning and held-out questions: every
// third, by position, is held out. Positional on purpose -- the split was fixed
// before any run, and position knows nothing about which questions are hard.
func splitExternal(qs []rerankEvalQuery) (tuning, heldOut []rerankEvalQuery) {
	for i, q := range qs {
		if i%3 == 2 {
			heldOut = append(heldOut, q)
		} else {
			tuning = append(tuning, q)
		}
	}
	return tuning, heldOut
}

// externalRepo is one pinned repository from testdata/evalrepos/repos.txt.
type externalRepo struct {
	name, language, url, sha string
}

func loadExternalRepos(t *testing.T) []externalRepo {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "evalrepos", "repos.txt"))
	if err != nil {
		t.Fatalf("the repository manifest: %v", err)
	}
	defer f.Close()
	var repos []externalRepo
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 {
			t.Fatalf("manifest line %q: want name, language, url, sha", line)
		}
		repos = append(repos, externalRepo{fields[0], fields[1], fields[2], fields[3]})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	// The manifest and the questions must name the same repositories, or a
	// repository would be fetched and never asked about -- or asked about and
	// never fetched, which would skip silently.
	var names []string
	for _, r := range repos {
		names = append(names, r.name)
		if len(externalEvalQuestions[r.name]) == 0 {
			t.Errorf("%s is pinned in the manifest and has no questions", r.name)
		}
	}
	for name := range externalEvalQuestions {
		if !slices.Contains(names, name) {
			t.Errorf("%s has questions and is not pinned in the manifest", name)
		}
	}
	return repos
}

// externalRepoRoot returns where a repository's clone lives, skipping the test
// when it is absent and failing it when the clone is at another commit.
func externalRepoRoot(t *testing.T, r externalRepo) string {
	t.Helper()
	dir := os.Getenv("MOCHIII_EVAL_REPOS")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		dir = filepath.Join(home, ".cache", "mochiii-eval-repos")
	}
	root := filepath.Join(dir, r.name)
	if _, err := os.Stat(root); err != nil {
		t.Skipf("%s is not fetched (%v); run scripts/fetch-eval-repos.sh", r.name, err)
	}
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("%s: reading its commit: %v", r.name, err)
	}
	if got := strings.TrimSpace(string(out)); got != r.sha {
		t.Fatalf("%s is at %s, pinned %s: the anchors name lines of the pinned tree. "+
			"Run scripts/fetch-eval-repos.sh", r.name, got, r.sha)
	}
	return root
}

// TestExternalEvalGroundTruth resolves every outside question's anchors in its
// pinned clone, through the indexer's own scan -- so an answer file the index
// refuses to read (the secret-name gate, a pruned directory, the size cap)
// fails here, in seconds, rather than reading as a retrieval miss in the
// hour-long run.
func TestExternalEvalGroundTruth(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range loadExternalRepos(t) {
		qs := externalEvalQuestions[r.name]
		if len(qs) != 15 {
			t.Errorf("%s has %d questions; the split and the rule assume 15", r.name, len(qs))
		}
		for _, q := range qs {
			if seen[q.query] {
				t.Errorf("%q is asked twice", q.query)
			}
			seen[q.query] = true
		}
		root := externalRepoRoot(t, r)
		scan, err := ScanWorkspace(root)
		if err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		resolveExactChunks(t, qs, scan.Chunks)
		t.Logf("%s (%s): %d files, %d chunks, every anchor resolved", r.name, r.language, scan.FilesScanned, len(scan.Chunks))
	}
}

var (
	externalCorporaMu sync.Mutex
	externalCorpora   = map[string]*evalCorpus{}
)

// externalCorpus builds a repository's index once per process. The whole tree
// is kept: nothing in an outside repository quotes these questions.
func externalCorpus(t *testing.T, r externalRepo) *evalCorpus {
	t.Helper()
	root := externalRepoRoot(t, r)
	externalCorporaMu.Lock()
	defer externalCorporaMu.Unlock()
	if c, ok := externalCorpora[r.name]; ok {
		return c
	}
	c, err := buildEvalCorpus(root, func(string) bool { return true })
	if err != nil {
		t.Fatalf("building the %s corpus: %v", r.name, err)
	}
	externalCorpora[r.name] = c
	return c
}

// TestExternalRepoRetrieval runs each outside repository through the same
// functions TestRerankEvalRetrievalRanking does -- retrieveTopK, then
// deliverWithinBudget at production's defaults -- and reports, per
// repository, what was retrieved and what was delivered on the tuning and
// held-out questions, and how long a retrieval took.
//
// NOT GATED. A floor needs a second machine to agree with the first, and this
// does not run in CI yet.
func TestExternalRepoRetrieval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eval test in -short mode")
	}
	ctx := context.Background()
	var totRet, totDel, totHeldRet, totHeldDel, totTune, totHeld int
	for _, r := range loadExternalRepos(t) {
		c := externalCorpus(t, r)
		tuning, heldOut := splitExternal(externalEvalQuestions[r.name])
		exactTune := resolveExactChunks(t, tuning, c.Scan.Chunks)
		exactHeld := resolveExactChunks(t, heldOut, c.Scan.Chunks)
		if t.Failed() {
			t.Fatalf("%s: ground truth did not resolve", r.name)
		}

		// Latency, warm: one retrieval per question, model and index loaded.
		var took []time.Duration
		for _, q := range externalEvalQuestions[r.name] {
			start := time.Now()
			if _, err := retrieveTopK(ctx, q.query, defaultK, c.Embedder, c.Store, c.LexicalStore, true); err != nil {
				t.Fatalf("%s: retrieveTopK(%q): %v", r.name, q.query, err)
			}
			took = append(took, time.Since(start))
		}
		slices.Sort(took)
		p50, p95 := took[len(took)/2], took[min(len(took)-1, len(took)*95/100)]

		ret, _, del, _, _ := runEvalPass(ctx, t, c.Embedder, c.Store, c.LexicalStore, tuning, exactTune, nil, c.RepoRoot, r.name+" (tuning)")
		hret, _, hdel, _, _ := runEvalPass(ctx, t, c.Embedder, c.Store, c.LexicalStore, heldOut, exactHeld, nil, c.RepoRoot, r.name+" (held-out)")

		fmt.Printf("EVALEXTERNAL repo=%s lang=%s chunks=%d embed=%s retrieved=%d/%d delivered=%d/%d "+
			"heldout_retrieved=%d/%d heldout_delivered=%d/%d p50=%s p95=%s\n",
			r.name, r.language, c.Chunks, c.EmbedTime.Round(time.Second),
			countTrue(ret), len(tuning), countTrue(del), len(tuning),
			countTrue(hret), len(heldOut), countTrue(hdel), len(heldOut),
			p50.Round(time.Millisecond), p95.Round(time.Millisecond))
		totRet += countTrue(ret)
		totDel += countTrue(del)
		totHeldRet += countTrue(hret)
		totHeldDel += countTrue(hdel)
		totTune += len(tuning)
		totHeld += len(heldOut)
	}
	fmt.Printf("EVALEXTERNAL total retrieved=%d/%d delivered=%d/%d heldout_retrieved=%d/%d heldout_delivered=%d/%d\n",
		totRet, totTune, totDel, totTune, totHeldRet, totHeld, totHeldDel, totHeld)
}
