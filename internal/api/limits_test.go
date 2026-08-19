package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mikketa/postern/internal/pool"
)

// The endpoint is reachable by anything that can open a socket to it, so what
// a request is allowed to ask for has to be bounded before any of it is acted
// on. These lock the three bounds: how much body, which fields, and how long.

func TestABodyTooLargeIsRefusedRatherThanBuffered(t *testing.T) {
	// json.Decoder reads the whole body into memory before it decides it did
	// not like it, so an unbounded body is an unbounded allocation.
	body := `{"url":"https://example.com","sitekey":"` +
		strings.Repeat("k", maxBodyBytes) + `"}`

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/solve", strings.NewReader(body))
	serverFor(&stubFleet{ready: 1}).Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a %d-byte body returned %d, want %d — nothing is stopping a "+
			"client from making the server allocate whatever it likes",
			len(body), recorder.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestAMisspeltFieldIsRefusedRatherThanIgnored(t *testing.T) {
	// The dangerous case is not a nonsense field, it is a near-miss: a caller
	// that writes timeoutMs believes it set a timeout, gets the default, and
	// has nothing in the response telling it otherwise.
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/solve", strings.NewReader(
		`{"url":"https://example.com","sitekey":"0x4A","timeoutMs":5000}`))
	serverFor(&stubFleet{ready: 1}).Handler().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("a misspelt field returned %d, want %d — the caller is left "+
			"believing it set something it did not", recorder.Code, http.StatusBadRequest)
	}
}

func TestACallerCannotAskForMoreTimeThanTheOperatorAllows(t *testing.T) {
	const ceiling = 60 * time.Second

	for _, c := range []struct {
		name  string
		asked int
		want  time.Duration
	}{
		{"nothing asked takes the operator's own", 0, ceiling},
		{"a negative is not an instruction", -1, ceiling},
		{"less than the ceiling is granted", 5_000, 5 * time.Second},
		{"more than the ceiling is cut to it", 3_600_000, ceiling},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := effectiveTimeout(c.asked, ceiling); got != c.want {
				t.Errorf("effectiveTimeout(%d, %s) = %s, want %s — a request "+
					"holds a whole browser, and there are only -concurrency of them",
					c.asked, ceiling, got, c.want)
			}
		})
	}
}

// TestTheHealthCheckCanSayNo is the whole point of the endpoint. It used to
// answer 200 whatever had happened, so a server whose Chrome had died went on
// reporting that it was fine — nothing restarted it, and every request behind
// the green probe failed. A liveness check that cannot fail is a false
// assurance, which is worse than an absent one.
func TestTheHealthCheckCanSayNo(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		rec := httptest.NewRecorder()
		serverFor(&stubFleet{ready: 1}).Handler().
			ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))

		if rec.Code != http.StatusOK {
			t.Errorf("a working server answered %d", rec.Code)
		}
	})

	t.Run("the browser is gone", func(t *testing.T) {
		rec := httptest.NewRecorder()
		serverFor(&stubFleet{unhealthy: errors.New("chrome is not running")}).Handler().
			ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))

		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("a broken server answered %d, want %d — an orchestrator "+
				"watching this would never restart it",
				rec.Code, http.StatusServiceUnavailable)
		}
		if !strings.Contains(rec.Body.String(), "chrome is not running") {
			t.Errorf("the reason is not in the body: %q", rec.Body.String())
		}
	})

	t.Run("everyone resting is busy, not broken", func(t *testing.T) {
		// The distinction that matters: a fleet with nothing free is working
		// as designed and must not be restarted for it.
		rec := httptest.NewRecorder()
		serverFor(&stubFleet{ready: 0, identities: make([]pool.Identity, 3)}).Handler().
			ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))

		if rec.Code != http.StatusOK {
			t.Errorf("a fully-rested fleet answered %d — restarting it would "+
				"throw away every profile's history", rec.Code)
		}
	})
}
