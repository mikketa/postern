package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A histogram written by hand is worth testing at the format level: a bucket
// that is not cumulative, or a missing +Inf, is silently wrong — the scrape
// succeeds and every quantile computed from it is nonsense.

func TestBucketsAreCumulativeAndEndAtInfinity(t *testing.T) {
	m := NewMetrics()
	for _, d := range []time.Duration{
		500 * time.Millisecond, 3 * time.Second, 12 * time.Second, 200 * time.Second,
	} {
		m.Observe("alice", "turnstile", d, true, ReasonOK)
	}

	var out strings.Builder
	m.Write(&out, nil)
	text := out.String()

	// Cumulative: every bucket holds everything at or below its bound, so the
	// counts never go down as the bound grows.
	previous := int64(-1)
	for _, bound := range durationBounds {
		want := "postern_solve_duration_seconds_bucket{le=\"" +
			strconv.FormatFloat(bound, 'g', -1, 64) + "\"} "
		got, ok := valueAfter(text, want)
		if !ok {
			t.Fatalf("no bucket for le=%v — a missing edge makes every quantile wrong", bound)
		}
		if got < previous {
			t.Errorf("bucket le=%v holds %d, less than the bound below it (%d): "+
				"buckets are cumulative", bound, got, previous)
		}
		previous = got
	}

	inf, ok := valueAfter(text, "postern_solve_duration_seconds_bucket{le=\"+Inf\"} ")
	if !ok {
		t.Fatal("no +Inf bucket, which the format requires")
	}
	if inf != 4 {
		t.Errorf("+Inf holds %d, want 4 — it has to equal the total count", inf)
	}
	if count, _ := valueAfter(text, "postern_solve_duration_seconds_count "); count != inf {
		t.Errorf("_count is %d and +Inf is %d; they must agree", count, inf)
	}

	// The 200s observation is past every bound, so it belongs to +Inf alone.
	if last, _ := valueAfter(text, "postern_solve_duration_seconds_bucket{le=\"120\"} "); last != 3 {
		t.Errorf("le=120 holds %d, want 3 — a solve past the last bound must not "+
			"be counted into it", last)
	}
}

func TestAFailedSolveIsCountedToo(t *testing.T) {
	// A solver measured only on its successes reports a latency and a rate
	// that nobody experiences.
	m := NewMetrics()
	m.Observe("alice", "recaptcha-v2", 5*time.Second, true, ReasonOK)
	m.Observe("alice", "recaptcha-v2", 90*time.Second, false, ReasonTimeout)

	var out strings.Builder
	m.Write(&out, nil)
	text := out.String()

	for _, want := range []string{
		`postern_solves_total{kind="recaptcha-v2",outcome="token",reason="ok"} 1`,
		`postern_solves_total{kind="recaptcha-v2",outcome="failed",reason="timeout"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	if count, _ := valueAfter(text, "postern_solve_duration_seconds_count "); count != 2 {
		t.Errorf("_count is %d, want 2 — the failure was not timed", count)
	}
}

func TestMetricsAreNotPublic(t *testing.T) {
	rec := httptest.NewRecorder()
	guardedServer("s3cret").ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /metrics returned %d without a token, want %d — how much an "+
			"operation solves is not public", rec.Code, http.StatusUnauthorized)
	}
}

// valueAfter reads the integer following a prefix, on the line that carries it.
func valueAfter(text, prefix string) (int64, bool) {
	i := strings.Index(text, prefix)
	if i < 0 {
		return 0, false
	}
	rest := text[i+len(prefix):]
	if end := strings.IndexByte(rest, '\n'); end >= 0 {
		rest = rest[:end]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
	return n, err == nil
}

// TestObservingFromEveryHandlerAtOnceIsSafe gives -race something to find.
// Every solve writes here from its own goroutine, and /metrics reads while
// they do; without the lock that is a data race the tests would otherwise
// never exercise.
func TestObservingFromEveryHandlerAtOnceIsSafe(t *testing.T) {
	m := NewMetrics()
	kinds := []string{"turnstile", "recaptcha-v2", "recaptcha-v3"}

	done := make(chan struct{})
	for i := range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := range 50 {
				m.Observe("alice", kinds[j%len(kinds)], time.Duration(j)*time.Second, j%2 == 0, ReasonOK)
			}
		}()
		_ = i
	}
	// Scrape while they write, which is what Prometheus does.
	for range 20 {
		m.Write(io.Discard, map[string]int{"postern_fleet_identities_ready": 1})
	}
	for range 8 {
		<-done
	}

	var out strings.Builder
	m.Write(&out, nil)
	if count, _ := valueAfter(out.String(), "postern_solve_duration_seconds_count "); count != 400 {
		t.Errorf("_count is %d after 8 goroutines of 50 observations, want 400", count)
	}
}
