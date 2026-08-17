package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
