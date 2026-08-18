package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mikketa/postern/internal/solver"
)

// The classifier is a contract in two directions at once: callers branch on
// the code, and dashboards are built on the label. Both break silently if a
// class is wrong, so the mapping is pinned here rather than trusted.

func TestEachFailureIsClassifiedByItsSentinelNotItsWording(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want Reason
	}{
		{"nothing went wrong", nil, ReasonOK},
		{"the site's own challenge refused us",
			fmt.Errorf("solver: %w never let us through", solver.ErrCrossing), ReasonCrossing},
		{"the budget ran out",
			fmt.Errorf("solver: %w after 1m0s", solver.ErrNoToken), ReasonTimeout},
		{"a bare deadline is the same thing",
			context.DeadlineExceeded, ReasonTimeout},
		{"the widget reported its own error",
			fmt.Errorf("solver: %w: recaptcha-v2 error x", solver.ErrVendor), ReasonVendor},
		{"a grid arrived with nothing to read it",
			fmt.Errorf("solver: %w — see -image-solver", solver.ErrNoImageSolver), ReasonNoImageSolver},
		{"the vendor kept asking past the point it grades",
			fmt.Errorf("solver: %w: 14 picture grids", solver.ErrRefused), ReasonRefused},
		{"the request could not be attempted",
			fmt.Errorf("solver: %w: url required", solver.ErrInvalidRequest), ReasonInvalid},
		{"the caller hung up", context.Canceled, ReasonCancelled},
		{"anything else", errors.New("something new"), ReasonInternal},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := reasonFor(c.err); got != c.want {
				t.Errorf("reasonFor(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

// The point of classifying by sentinel: rewording a message for whoever reads
// it must not move anybody's dashboard.
func TestRewordingAMessageDoesNotChangeItsClass(t *testing.T) {
	before := fmt.Errorf("solver: %w never let us through — it took the click", solver.ErrCrossing)
	after := fmt.Errorf("solver: %w: refused, and here is completely different advice",
		solver.ErrCrossing)

	if reasonFor(before) != reasonFor(after) {
		t.Error("two wordings of the same failure classified differently — the " +
			"classifier is reading the message, which is free to change")
	}
}

func TestAFailedSolveAnswersWithItsCode(t *testing.T) {
	// A caller that has to match strings to tell "retry later" from "you
	// forgot to configure a vision solver" will get it wrong eventually.
	rec := &recorder{header: http.Header{}}
	writeFailure(rec, ReasonCrossing, "the challenge never let us through")

	if !strings.Contains(rec.body, `"code":"crossing_refused"`) {
		t.Errorf("no machine-readable code in %q", rec.body)
	}
	if rec.status != http.StatusBadGateway {
		t.Errorf("status %d, want %d", rec.status, http.StatusBadGateway)
	}
}

func TestEachReasonAnswersWithASensibleStatus(t *testing.T) {
	for _, c := range []struct {
		reason Reason
		want   int
	}{
		{ReasonInvalid, http.StatusBadRequest},
		{ReasonBusy, http.StatusServiceUnavailable},
		{ReasonNoImageSolver, http.StatusNotImplemented},
		{ReasonInternal, http.StatusInternalServerError},
		{ReasonCrossing, http.StatusBadGateway},
		{ReasonTimeout, http.StatusBadGateway},
		{ReasonVendor, http.StatusBadGateway},
	} {
		if got := c.reason.status(); got != c.want {
			t.Errorf("%q answers %d, want %d", c.reason, got, c.want)
		}
	}
}

func TestAFleetTurningCallersAwayIsCounted(t *testing.T) {
	// It used to return before anything recorded it, so the load that got a
	// 503 was invisible — exactly the load an operator needs to see.
	m := NewMetrics()
	m.Observe("turnstile", 0, false, ReasonBusy)
	m.Observe("turnstile", 3*time.Second, true, ReasonOK)

	var out strings.Builder
	m.Write(&out, nil)
	text := out.String()

	if !strings.Contains(text, `reason="busy"`) {
		t.Error("a 503 from an exhausted fleet is not counted")
	}
	// But it did no solving, so it must not drag the latency down.
	if count, _ := valueAfter(text, "postern_solve_duration_seconds_count "); count != 1 {
		t.Errorf("_count is %d, want 1 — a request that never solved is in the "+
			"latency histogram and every quantile is now wrong", count)
	}
}

// recorder is the smallest http.ResponseWriter that records what was written.
type recorder struct {
	header http.Header
	status int
	body   string
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(s int)   { r.status = s }
func (r *recorder) Write(b []byte) (int, error) {
	r.body += string(b)
	return len(b), nil
}

// TestEveryErrorResponseCarriesACode is the contract that makes the code worth
// depending on. One path that answers without it is enough to break a caller
// that branches on it, and it will be the path nobody tested.
func TestEveryErrorResponseCarriesACode(t *testing.T) {
	handler := guardedServer("s3cret")

	for _, c := range []struct {
		name string
		req  func() *http.Request
	}{
		{"no token", func() *http.Request {
			return httptest.NewRequest("GET", "/fleet", nil)
		}},
		{"body too large", func() *http.Request {
			r := httptest.NewRequest("POST", "/solve",
				strings.NewReader(`{"sitekey":"`+strings.Repeat("k", maxBodyBytes)+`"}`))
			r.Header.Set("Authorization", "Bearer s3cret")
			return r
		}},
		{"misspelt field", func() *http.Request {
			r := httptest.NewRequest("POST", "/solve",
				strings.NewReader(`{"url":"https://e.com","sitekey":"k","timeoutMs":5}`))
			r.Header.Set("Authorization", "Bearer s3cret")
			return r
		}},
		{"missing fields", func() *http.Request {
			r := httptest.NewRequest("POST", "/solve", strings.NewReader(`{}`))
			r.Header.Set("Authorization", "Bearer s3cret")
			return r
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, c.req())

			if rec.Code < 400 {
				t.Fatalf("expected an error, got %d", rec.Code)
			}
			var body struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not json: %q", rec.Body.String())
			}
			if body.Code == "" {
				t.Errorf("%d answered without a code: %q", rec.Code, rec.Body.String())
			}
			if body.Error == "" {
				t.Errorf("%d answered without a message", rec.Code)
			}
		})
	}
}
