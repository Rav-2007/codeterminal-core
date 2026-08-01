package main

// Tests for the two things the soak script reads (P3.4).
//
// The soak asserts "limiter buckets are swept" against the rate_limiter_buckets
// gauge. A gauge that does not track reality would make a 30-minute run report
// health it never verified, so the gauge is tested here at unit speed.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestBucketCountTracksSweeping pins that the number the soak reads actually
// falls when sweepLocked reclaims buckets. Without this, a stuck-at-zero gauge
// would make an unbounded leak look like a clean run.
func TestBucketCountTracksSweeping(t *testing.T) {
	l := newRateLimiter(keyRatePerSecond, keyBurst)
	base := time.Now()
	l.now = func() time.Time { return base }

	const distinctKeys = 50
	for i := 0; i < distinctKeys; i++ {
		l.allow(strings.Repeat("k", i+1))
	}
	if got := l.bucketCount(); got != distinctKeys {
		t.Fatalf("bucketCount = %d after %d distinct keys, want %d", got, distinctKeys, distinctKeys)
	}

	// Well past bucketIdleTTL, and past bucketSweepE so the sweep is due. The
	// sweep runs opportunistically inside allow, so one more call drives it.
	l.now = func() time.Time { return base.Add(bucketIdleTTL + bucketSweepE + time.Minute) }
	l.allow("trigger-the-sweep")

	got := l.bucketCount()
	if got > 1 {
		t.Errorf("bucketCount = %d after every bucket aged past bucketIdleTTL, want <= 1 "+
			"(just the key that triggered the sweep). Buckets are the limiter's one "+
			"unbounded dimension -- one per distinct caller -- so a sweep that does not "+
			"reclaim them is a memory leak that grows with traffic diversity", got)
	}
}

// TestLimiterBucketsGaugeIsExposed pins that the gauge reaches a scrape at all,
// and sums across limiters rather than reporting only one.
func TestLimiterBucketsGaugeIsExposed(t *testing.T) {
	p := newTestProxy("http://unused.invalid", "http://unused.invalid")

	// Touch three different limiters so a sum is distinguishable from any one of
	// them. keyTokens is driven with charge rather than available on purpose:
	// available deliberately creates NO bucket for an unseen key ("never seen: a
	// full bucket by construction"), so it would contribute nothing to the count.
	p.keyRate.allow("key-a")
	p.keyTokens.charge("key-b", 1)
	p.preAuthPerSource.allow("1.2.3.4")

	rec := httptest.NewRecorder()
	p.metrics.writeTo(rec)

	var scraped map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &scraped); err != nil {
		t.Fatalf("metrics scrape is not valid JSON: %v\nbody=%s", err, rec.Body.String())
	}

	raw, ok := scraped["rate_limiter_buckets"]
	if !ok {
		t.Fatalf("rate_limiter_buckets is absent from a scrape, so the soak test would "+
			"assert on a field that does not exist; keys=%v", keysOf(scraped))
	}

	var buckets int
	if err := json.Unmarshal(raw, &buckets); err != nil {
		t.Fatalf("rate_limiter_buckets is not a number: %s", raw)
	}
	if buckets < 3 {
		t.Errorf("rate_limiter_buckets = %d after touching three limiters, want >= 3 -- "+
			"the gauge is not summing across every limiter", buckets)
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
