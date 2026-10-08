package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// What a request can cost the helper, and what a vector depends on. Both are
// properties of running ONE text per inference (see OnnxEmbedder.Embed), and
// both were measured broken on 2026-10-08 before these tests existed.

// fullLengthText is a text of well over maxSequenceLength tokens, different
// for each n, so every one of them costs the model its full 512 positions.
func fullLengthText(n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "daemon/file%d.go\n", n)
	for line := 0; b.Len() < 6000; line++ {
		fmt.Fprintf(&b, "\tif attempt%d > limit%d { return retry(ctx, request%d, backoff*%d) }\n", n, line, n+line, line+2)
	}
	return b.String()
}

// residentMB is this process's resident memory, from the kernel. Linux only:
// the other platforms have no /proc, and the bound is not theirs to differ on.
func residentMB(t *testing.T) int {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("resident memory is read from /proc, which only Linux has")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skipf("reading /proc/self/status: %v", err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			kb, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")))
			if err != nil {
				t.Fatalf("VmRSS line %q: %v", line, err)
			}
			return kb / 1024
		}
	}
	t.Fatal("no VmRSS line in /proc/self/status")
	return 0
}

// growthAllowedMB is how much the process may grow over a request, however
// many texts it holds. One more text in the same inference cost 40 MB when
// this was measured, so the 48 below cost 1.8 GB before the bound and the 16
// at once cost 480 MB; what is left after it is the allocator's own slack.
const growthAllowedMB = 200

// THE REGRESSION TEST FOR THE 6 GB HELPER. One save of a 1,700-line file sent
// 58 chunks in one request and the helper kept 2.3 GB for it; the number here
// is a little under that file.
func TestABigRequestCostsNoMoreMemoryThanASmallOne(t *testing.T) {
	e := fullEmbedder(t)

	// One text first, so what is measured is the growth a BIG request adds and
	// not the session's first use.
	if _, err := e.Embed([]string{fullLengthText(0)}); err != nil {
		t.Fatal(err)
	}
	before := residentMB(t)

	texts := make([]string, 48)
	for i := range texts {
		texts[i] = fullLengthText(i + 1)
	}
	vecs, err := e.Embed(texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("%d vectors for %d texts", len(vecs), len(texts))
	}
	if grew := residentMB(t) - before; grew > growthAllowedMB {
		t.Errorf("a request of %d texts grew the process by %d MB (from %d MB); it may grow by at most %d MB, "+
			"because memory that follows the size of a request is how the helper reached 6 GB",
			len(texts), grew, before, growthAllowedMB)
	}
}

// The same bill by another route: many small requests at the same moment.
func TestRequestsArrivingTogetherCostNoMoreThanOne(t *testing.T) {
	e := fullEmbedder(t)
	if _, err := e.Embed([]string{fullLengthText(0)}); err != nil {
		t.Fatal(err)
	}
	before := residentMB(t)

	const together = 16
	errs := make([]error, together)
	var wg sync.WaitGroup
	for i := range together {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.Embed([]string{fullLengthText(100 + i)})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if grew := residentMB(t) - before; grew > growthAllowedMB {
		t.Errorf("%d requests at once grew the process by %d MB (from %d MB); at most %d MB is allowed, "+
			"because inferences that run side by side each need their own working memory",
			together, grew, before, growthAllowedMB)
	}
}

// A vector is a function of its text. It was not: the model is quantised and
// takes its ranges from the whole input tensor, so the same chunk embedded
// beside different chunks came out as far apart as cosine 0.994.
func TestATextsVectorDoesNotDependOnWhatItArrivedWith(t *testing.T) {
	e := fullEmbedder(t)
	texts := []string{
		fullLengthText(1),
		"func main() {}",
		"Represent this sentence for searching relevant passages: where is the retry limit set",
		fullLengthText(2),
	}
	together, err := e.Embed(texts)
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range texts {
		alone, err := e.Embed([]string{text})
		if err != nil {
			t.Fatal(err)
		}
		if len(alone) != 1 || len(alone[0]) != embedDim {
			t.Fatalf("text %d alone: %d vectors", i, len(alone))
		}
		for j := range alone[0] {
			if alone[0][j] != together[i][j] {
				t.Fatalf("text %d: component %d is %v alone and %v beside three others; "+
					"a text's vector must not depend on what it arrived with", i, j, alone[0][j], together[i][j])
			}
		}
	}

	// And not on the order either.
	reversed, err := e.Embed([]string{texts[3], texts[2], texts[1], texts[0]})
	if err != nil {
		t.Fatal(err)
	}
	for i := range texts {
		for j := range together[i] {
			if reversed[len(texts)-1-i][j] != together[i][j] {
				t.Fatalf("text %d: component %d changed when the request was reversed", i, j)
			}
		}
	}
}
