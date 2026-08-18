package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPanicBecomesAStatusRatherThanASilentDrop(t *testing.T) {
	// net/http already keeps a panicking handler from taking the process down,
	// but what reaches the caller is a closed connection with no status at
	// all — indistinguishable from the network failing, and it sends whoever
	// is debugging to the wrong place entirely.
	boom := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("the solver exploded")
	})

	rec := httptest.NewRecorder()
	handler := withRequestID(recovered(slog.New(slog.DiscardHandler), boom))
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/fleet", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("a panicking handler returned %d, want %d",
			rec.Code, http.StatusInternalServerError)
	}
	if rec.Body.Len() == 0 {
		t.Error("no body, so the caller cannot tell a crash from a dropped connection")
	}
}

func TestEveryRequestCarriesAnIdItCanBeFoundBy(t *testing.T) {
	rec := httptest.NewRecorder()
	guardedServer("").ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))

	if rec.Header().Get(RequestIDHeader) == "" {
		t.Fatalf("no %s on the response — a caller reporting a failure has "+
			"nothing to quote, and two requests for the same url a second "+
			"apart are identical in the log", RequestIDHeader)
	}
}

func TestACallersOwnRequestIdIsKept(t *testing.T) {
	// A client with its own tracing already has an id. Minting a second one
	// for the same request means the two systems cannot be joined.
	const theirs = "caller-side-trace-42"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/health", nil)
	req.Header.Set(RequestIDHeader, theirs)
	guardedServer("").ServeHTTP(rec, req)

	if got := rec.Header().Get(RequestIDHeader); got != theirs {
		t.Errorf("echoed %q, want the caller's own %q", got, theirs)
	}
}

func TestTwoRequestsDoNotShareAnId(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		rec := httptest.NewRecorder()
		guardedServer("").ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
		id := rec.Header().Get(RequestIDHeader)
		if seen[id] {
			t.Fatalf("id %q handed out twice — an id that repeats identifies nothing", id)
		}
		seen[id] = true
	}
}
