package main

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sugarme/tokenizer/pretrained"
	ort "github.com/yalue/onnxruntime_go"

	"codeterminal/helper/helperproto"
)

// H.1 -- can a secret in a prompt become observable outside the daemon?
//
// Embed is the one function in this module on the RAW, PRE-SCRUB prompt path.
// daemon/server.go:507 hands gatherContext the unredacted prompt and retrieval
// hands text to this process, so everything here is about whether that text can
// come back out by any route other than a vector.
//
// THE CHAIN THAT WOULD CARRY IT, traced in code before it was tested:
//
//	Embed error -> dispatch -> helperproto.Response.Error, over the socket
//	  -> daemon/helperproc.go:380 "embedder helper returned an error: %s"
//	  -> similarChunks "retrieval error: %v" -> retrievalOutcome.Reason
//	  -> GroundingInfo, sent to the CLIENT before the first token
//
// An error string that names its input is therefore not a log-hygiene problem;
// it reaches a screen. Separately, the helper's stderr is copied into the
// daemon's own (helperproc.go:216), which /mcp-server puts on screen (R1.5).

const embedSentinel = "SENTINELskliveQQ51d0c9a7b3e4f2AKIA"

func modelDirForLeakTest(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	dir := filepath.Join(home, ".codeterminal", "models", "bge-small-en-v1.5-int8")
	if _, err := os.Stat(filepath.Join(dir, "tokenizer.json")); err != nil {
		t.Skipf("model not cached at %s: %v", dir, err)
	}
	return dir
}

// tokenizerOnly builds the half of OnnxEmbedder that touches raw text. The
// inference session is not needed to reach tokenize, and leaving it nil keeps
// this test runnable without the native runtime.
func tokenizerOnly(t *testing.T) *OnnxEmbedder {
	t.Helper()
	tk, err := pretrained.FromFile(filepath.Join(modelDirForLeakTest(t), "tokenizer.json"))
	if err != nil {
		t.Skipf("loading tokenizer: %v", err)
	}
	return &OnnxEmbedder{tokenizer: tk}
}

var ortOnce sync.Once

// fullEmbedder loads the real model and runtime, so the socket test drives
// production code rather than a stand-in.
func fullEmbedder(t *testing.T) *OnnxEmbedder {
	t.Helper()
	home, _ := os.UserHomeDir()
	lib := filepath.Join(home, ".codeterminal", "models", "onnxruntime-1.26.0", "libonnxruntime.so")
	if _, err := os.Stat(lib); err != nil {
		t.Skipf("onnxruntime not cached: %v", err)
	}
	var initErr error
	ortOnce.Do(func() {
		ort.SetSharedLibraryPath(lib)
		initErr = ort.InitializeEnvironment()
	})
	if initErr != nil {
		t.Skipf("initializing ONNX Runtime: %v", initErr)
	}
	e, err := NewOnnxEmbedder(modelDirForLeakTest(t), 0)
	if err != nil {
		t.Skipf("loading model: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// The tokenizer is the only component in Embed's path handed the raw text that
// also returns its error UNWRAPPED (onnxembedder.go:109), so it was the best
// candidate for a prompt leaking through an error string.
//
// IT LEAKS NOTHING, because it produces no error strings at all: of ten hostile
// inputs, six tokenize cleanly and four PANIC. That is a different and worse
// failure, and it is characterised here rather than asserted as a defect,
// because the wire encoding makes it unreachable -- see
// TestEmbed_TheWireEncodingIsWhatStopsTheTokenizerPanic. A permanently-red test
// over an unreachable crash would be noise; a green characterisation that fails
// when the library changes is a tripwire.
// hugeInputChars is the largest text the QUERY path can hand the tokenizer:
// daemon/lexicalstore.go's maxLexicalQueryChars. DERIVED, not chosen.
//
// It was 2,000,000 -- a round number picked for bigness -- and that made this
// test exceed the race gate's 600s timeout, because sugarme/tokenizer's
// BertNormalizer transforms per rune and the race detector instruments every
// one of those accesses. `make check` could not finish while it stood.
//
// WHAT THIS NO LONGER COVERS, stated rather than quietly dropped: a single text
// between 32 KiB and the 16 MiB wire cap (helper/main.go:154), which a client
// that is not the daemon could still send. That case is now untested here.
//
// Worth recording separately: maxSequenceLength (512) truncates the tokenizer's
// OUTPUT, in tokenize() at onnxembedder.go:117, AFTER EncodeSingle has already
// walked the whole input. The cap bounds what the model sees, not what the
// tokenizer does -- the same "capped bytes, nothing caps work" shape the
// lexical-query bound was added to close.
const hugeInputChars = 32768

func TestEmbed_TokenizerPanicsRatherThanErroringOnInvalidUTF8(t *testing.T) {
	e := tokenizerOnly(t)

	hostile := map[string]string{
		"plain":            embedSentinel,
		"nul":              embedSentinel + string(rune(0)),
		"lone-surrogate":   embedSentinel + string([]byte{0xed, 0xa0, 0x80}),
		"invalid-utf8":     embedSentinel + string([]byte{0xff, 0xfe}),
		"overlong-utf8":    embedSentinel + string([]byte{0xc0, 0xaf}),
		"truncated-utf8":   embedSentinel + string([]byte{0xe2, 0x82}),
		"huge":             embedSentinel + strings.Repeat("x", hugeInputChars),
		"control-soup":     embedSentinel + string(rune(0x1b)) + string(rune(0x9b)) + string(rune(0x7f)),
		"combining-bomb":   embedSentinel + strings.Repeat(string(rune(0x0301)), 10000),
		"single-codepoint": embedSentinel + strings.Repeat(string(rune(0x10FFFF)), 100),
	}

	var errored, ok, panics int
	for name, text := range hostile {
		t.Run(name, func(t *testing.T) {
			err, panicked := tokenizeCatchingPanic(e, text)
			switch {
			case panicked != nil:
				panics++
				t.Logf("PANICS (not an error): %v", panicked)
			case err != nil:
				errored++
				if strings.Contains(err.Error(), embedSentinel) {
					t.Errorf("the tokenizer's error carries its input verbatim, and that string reaches "+
						"the client's grounding message (helperproc.go:380 -> GroundingInfo):\n  %v", err)
				}
				t.Logf("errored WITHOUT leaking: %v", err)
			default:
				ok++
			}
		})
	}
	t.Logf("of %d hostile inputs: %d tokenized cleanly, %d errored, %d PANICKED", len(hostile), ok, errored, panics)

	// THE TRIPWIRE. If sugarme/tokenizer is upgraded and stops panicking, this
	// fails -- which is good news and means the wire encoding is no longer the
	// only thing standing between invalid UTF-8 and a dead helper process.
	// Re-measure, then simplify TestEmbed_TheWireEncodingIsWhatStopsTheTokenizerPanic.
	if panics == 0 {
		t.Errorf("no input panicked the tokenizer any more. This is an improvement: re-measure and "+
			"revisit the wire-encoding dependency documented in this file. (%d errored, %d clean)",
			errored, ok)
	}
	// The other direction: this test claimed the tokenizer produces no error
	// strings, which is what falsified the leak prediction. If it starts
	// producing them, they need checking for the sentinel again.
	if errored != 0 {
		t.Errorf("the tokenizer now returns %d error(s) where it previously returned none; those "+
			"strings reach the client's grounding message and must be re-checked for input echo",
			errored)
	}
}

// End to end over a real socket, against the real model: drive serveConn with a
// sentinel-bearing batch and grep everything observable.
func TestEmbed_NothingObservableCarriesThePrompt(t *testing.T) {
	e := fullEmbedder(t)

	var stderr strings.Builder
	var mu sync.Mutex
	srv := &server{embedder: e, logger: log.New(&lockedWriter{w: &stderr, mu: &mu}, "", 0)}

	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { srv.serveConn(server); server.Close(); close(done) }()

	req := helperproto.Request{
		Method: helperproto.MethodEmbed,
		Texts: []string{
			embedSentinel,
			"a harmless line of code",
			embedSentinel + string([]byte{0xff, 0xfe}),
		},
	}
	body, _ := json.Marshal(req)
	client.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := client.Write(append(body, '\n')); err != nil {
		t.Fatalf("write: %v", err)
	}
	client.SetReadDeadline(time.Now().Add(120 * time.Second))
	var raw json.RawMessage
	decErr := json.NewDecoder(client).Decode(&raw)
	client.Close()
	<-done

	mu.Lock()
	logged := stderr.String()
	mu.Unlock()

	// THIS ASSERTION IS A FORWARD GUARD, NOT A VERIFIED CONTROL, and saying so is
	// the point. Neutering dispatch to embed req.Texts into its error string did
	// NOT make this fail, because the error branch is never taken: Embed has no
	// reachable error path with a real model -- tokenize returns no errors (see
	// the panic characterisation above) and every other failure inside Embed is a
	// tensor or session error that a valid batch does not produce.
	//
	// It cannot be made load-bearing without changing production code: server's
	// embedder field is a concrete *OnnxEmbedder rather than an interface, so no
	// failing stand-in can be injected. That is why the branch has never been
	// exercised by anything. The stderr assertion below IS load-bearing --
	// neutering the decode path to log req.Texts fails it.
	if strings.Contains(string(raw), embedSentinel) {
		t.Errorf("the sentinel crossed back over the helper socket, which the daemon turns into a "+
			"retrieval error and shows the user: %s", truncateLeak(string(raw)))
	}
	if strings.Contains(logged, embedSentinel) {
		t.Errorf("the sentinel reached the helper's stderr, which the daemon copies into its own "+
			"(helperproc.go:216) and /mcp-server puts on screen (R1.5): %s", truncateLeak(logged))
	}
	// VACUITY FLOOR. A response that never arrived, or a batch that silently
	// produced nothing, would pass both checks above while proving nothing.
	var resp helperproto.Response
	if decErr != nil {
		t.Fatalf("no response decoded (%v); this test cannot have checked what crosses the socket", decErr)
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("response did not decode: %v", err)
	}
	if !resp.OK || len(resp.Vectors) != 3 {
		t.Fatalf("expected 3 vectors from a 3-text batch, got ok=%v vectors=%d error=%q; "+
			"the embed path did not actually run", resp.OK, len(resp.Vectors), resp.Error)
	}
	t.Logf("3 vectors returned, %d bytes on the wire, stderr %q", len(raw), logged)
}

// A malformed request body reaches the helper's own decoder, whose error text is
// logged. Go's JSON errors quote offsets rather than content, but "it does not
// today" is a measurement, not a property, so it is measured.
func TestEmbed_MalformedRequestLoggingDoesNotEchoTheBody(t *testing.T) {
	var stderr strings.Builder
	srv := &server{logger: log.New(&stderr, "", 0)}

	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { srv.serveConn(server); server.Close(); close(done) }()

	client.SetWriteDeadline(time.Now().Add(10 * time.Second))
	client.Write([]byte(`{"method":"embed","texts":["` + embedSentinel + `"],,,}` + "\n"))
	client.Close()
	<-done

	if strings.Contains(stderr.String(), embedSentinel) {
		t.Errorf("a decode error echoed the request body to stderr: %q", stderr.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("nothing was logged at all; this test cannot have checked the decode-error path")
	}
	t.Logf("logged: %q", strings.TrimSpace(stderr.String()))
}

// argv and the environment are what any process on this machine that can read
// /proc sees. Text arrives over a socket, so neither should be able to hold it
// -- asserted rather than assumed, because "it arrives by socket" is a claim
// about today's caller, not a property of this binary.
func TestEmbed_ArgvAndEnvironmentCannotHoldPromptText(t *testing.T) {
	for _, arg := range os.Args {
		if strings.Contains(arg, embedSentinel) {
			t.Errorf("argv carries prompt text: %q", arg)
		}
	}
	for _, kv := range os.Environ() {
		if strings.Contains(kv, embedSentinel) {
			t.Errorf("the environment carries prompt text: %q", kv)
		}
	}
	t.Logf("argv entries: %d, environment entries: %d", len(os.Args), len(os.Environ()))
}

type lockedWriter struct {
	w  *strings.Builder
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func truncateLeak(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

// tokenizeCatchingPanic runs tokenize and reports an error and a panic
// separately, because they are different failures with different blast radii:
// an error is returned to the caller, a panic ends the process.
func tokenizeCatchingPanic(e *OnnxEmbedder, text string) (err error, panicked any) {
	defer func() {
		if r := recover(); r != nil {
			panicked = r
		}
	}()
	_, err = e.tokenize(text)
	return err, nil
}

// WHAT THE PANIC COSTS, AND WHAT IS ACTUALLY HOLDING IT CLOSED.
//
// The tokenizer panics on invalid UTF-8 (above), and helper/main.go has no
// recover() anywhere -- handleConn and serveConn both lack one, while the
// daemon's equivalent has had one since daemon/server.go:289. So a panic here
// is not contained to a connection; it ends the process, and every later embed
// fails until the daemon respawns it.
//
// IT IS NOT REACHABLE TODAY, AND I PREDICTED THE WRONG REASON. The daemon's only
// content filter is sniffBinary (daemon/chunker.go:290), which looks for a NUL
// in the first 8 KiB -- invalid UTF-8 without a NUL sails through it, so I
// expected the crash to be live. It is not. encoding/json REPLACES every invalid
// UTF-8 byte with U+FFFD at MARSHAL time, silently, returning a nil error, so
// the bytes that panic the tokenizer cannot survive the trip across this socket.
//
// That protection is real and entirely incidental. Nothing in either module says
// "the wire encoding is what keeps the tokenizer alive", and until this test
// nothing checked it. A length-prefixed framing, a protobuf, or any future
// non-JSON route for the same text re-opens a process-killing crash with no
// other guard behind it. This test exists to make that dependency explicit and
// to fail if the encoder's behaviour ever changes.
func TestEmbed_TheWireEncodingIsWhatStopsTheTokenizerPanic(t *testing.T) {
	e := tokenizerOnly(t)

	// Windows-1252 smart quotes: invalid UTF-8, no NUL, so sniffBinary calls a
	// file containing them TEXT and the indexer sends them onward.
	raw := "he said " + string([]byte{0x93}) + "hi" + string([]byte{0x94})

	// 1. The payload really does panic when handed to the tokenizer directly.
	if _, panicked := tokenizeCatchingPanic(e, raw); panicked == nil {
		t.Fatal("the chosen payload no longer panics the tokenizer; this test would prove nothing " +
			"about what the wire encoding is protecting")
	}

	// 2. The wire encoding is what disarms it, and does so silently.
	body, err := json.Marshal(helperproto.Request{Method: helperproto.MethodEmbed, Texts: []string{raw}})
	if err != nil {
		t.Fatalf("marshalling reported an error: %v -- if the encoder starts REFUSING invalid UTF-8 "+
			"rather than replacing it, that is a better protection, but this test and the comment "+
			"above need rewriting", err)
	}
	var back helperproto.Request
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Texts[0] == raw {
		t.Fatalf("invalid UTF-8 now survives the wire intact.\n" +
			"THIS IS A CRASH, NOT A COSMETIC CHANGE: the bytes that arrive panic the tokenizer, " +
			"and helper/main.go still has no recover() above serveConn, so the helper process dies " +
			"and every later embed fails until it is respawned. Add a recover to handleConn, or " +
			"reject invalid UTF-8 before Embed.")
	}

	// 3. And the same payload, sent the way the daemon sends it, is harmless.
	//
	// THE REAL EMBEDDER, not the tokenizer-only stand-in. Once the wire encoding
	// has replaced the invalid bytes, the text tokenizes fine and Embed proceeds
	// to inference -- so a nil session nil-derefs, and that panic ALSO escaped
	// serveConn uncaught. Wrong cause, but it demonstrates the same containment
	// gap this test is about: any panic inside Embed ends the process.
	srv := &server{embedder: fullEmbedder(t), logger: log.New(&strings.Builder{}, "", 0)}
	client, server := net.Pipe()
	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }() // stands in for the process dying
		srv.serveConn(server)
	}()
	client.SetWriteDeadline(time.Now().Add(10 * time.Second))
	client.Write(append(body, '\n'))
	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	var respRaw json.RawMessage
	json.NewDecoder(client).Decode(&respRaw)
	client.Close()

	select {
	case r := <-panicked:
		if r != nil {
			t.Errorf("serveConn panicked and nothing caught it: %v", r)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serveConn neither returned nor panicked")
	}
}

// The reachability half, asserted against the daemon's own filter rather than
// described: bytes that panic the tokenizer must be shown to survive the only
// gate standing in front of it.
// The daemon's filter does NOT stop these bytes -- recorded so the dependency on
// the wire encoding is visible from both ends rather than only from the helper's.
func TestEmbed_InvalidUTF8WithoutNULIsNotFilteredAsBinary(t *testing.T) {
	// Mirrors daemon/chunker.go:290 sniffBinary exactly: NUL in the first 8 KiB.
	sniffBinaryEquivalent := func(b []byte) bool {
		if len(b) > 8192 {
			b = b[:8192]
		}
		for _, c := range b {
			if c == 0 {
				return true
			}
		}
		return false
	}
	e := tokenizerOnly(t)

	for name, content := range map[string][]byte{
		"latin-1 accented text": []byte("caf" + string([]byte{0xe9}) + " menu\n"),
		"windows-1252 quotes":   append([]byte("he said "), 0x93, 'h', 'i', 0x94, '\n'),
		"truncated utf-8":       append([]byte("prefix "), 0xe2, 0x82),
		"lone surrogate":        append([]byte("prefix "), 0xed, 0xa0, 0x80),
	} {
		t.Run(name, func(t *testing.T) {
			if sniffBinaryEquivalent(content) {
				t.Skip("this content IS filtered as binary, so it never reaches the tokenizer")
			}
			_, panicked := tokenizeCatchingPanic(e, string(content))
			t.Logf("not classified as binary; tokenizer panics on it: %v (bytes % x)", panicked != nil, content)
			if panicked == nil {
				return
			}
			// Reaching the tokenizer with these bytes would crash the process.
			// The only thing preventing it is the wire encoding -- pinned by
			// TestEmbed_TheWireEncodingIsWhatStopsTheTokenizerPanic, not by
			// anything in the daemon's filter.
			if body, _ := json.Marshal(helperproto.Request{Texts: []string{string(content)}}); strings.Contains(string(body), string(content)) {
				t.Errorf("these bytes survive marshalling AND panic the tokenizer: % x", content)
			}
		})
	}
}
