package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mikketa/postern/internal/browser"
	"github.com/mikketa/postern/internal/pool"
)

// stubFleet stands in for a real fleet: no browsers, just the bookkeeping the
// endpoints report on.
type stubFleet struct {
	identities []pool.Identity
	ready      int
	borrowErr  error
}

func (f *stubFleet) Borrow(context.Context) (*browser.Browser, func(bool), error) {
	if f.borrowErr != nil {
		return nil, nil, f.borrowErr
	}
	return nil, func(bool) {}, nil
}
func (f *stubFleet) Ready() int             { return f.ready }
func (f *stubFleet) Size() int              { return len(f.identities) }
func (f *stubFleet) Stats() []pool.Identity { return f.identities }

func serverFor(fleet Borrower) *Server {
	return NewFleet(fleet, time.Second, 1, "", slog.New(slog.DiscardHandler))
}

func TestFleetEndpointReportsEachIdentity(t *testing.T) {
	rested := time.Now().Add(2 * time.Minute)
	fleet := &stubFleet{ready: 1, identities: []pool.Identity{
		{Name: "alice", Solves: 3, Failures: 1, Warmed: true, Proxy: "http://u:p@gate:8000"},
		{Name: "bob", Failures: 3, Streak: 3, RestUntil: rested},
	}}

	recorder := httptest.NewRecorder()
	serverFor(fleet).Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/fleet", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /fleet returned %d", recorder.Code)
	}

	var body struct {
		Ready, Size int
		Identities  []struct {
			Name     string
			Solves   int
			Streak   int
			Resting  bool
			Proxied  bool
			Warmed   bool
			Failures int
		}
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if body.Size != 2 || body.Ready != 1 {
		t.Fatalf("ready %d of %d, want 1 of 2", body.Ready, body.Size)
	}
	if len(body.Identities) != 2 {
		t.Fatalf("reported %d identities", len(body.Identities))
	}
	if !body.Identities[0].Proxied || !body.Identities[0].Warmed {
		t.Fatalf("alice came back as %+v", body.Identities[0])
	}
	// Quarantine has to be visible: an identity set aside is the single most
	// useful thing to see when a fleet stops producing.
	if !body.Identities[1].Resting || body.Identities[1].Streak != 3 {
		t.Fatalf("bob's quarantine is invisible: %+v", body.Identities[1])
	}
}

// The password is in the identities file and must not come back out over HTTP.
func TestFleetEndpointDoesNotLeakProxyCredentials(t *testing.T) {
	fleet := &stubFleet{ready: 1, identities: []pool.Identity{
		{Name: "alice", Proxy: "http://user:hunter2@gate.example.com:8000"},
	}}

	recorder := httptest.NewRecorder()
	serverFor(fleet).Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/fleet", nil))

	for _, secret := range []string{"hunter2", "gate.example.com", "user:"} {
		if bytesContain(recorder.Body.Bytes(), secret) {
			t.Fatalf("/fleet leaked %q: %s", secret, recorder.Body.String())
		}
	}
}

func TestHealthSaysWhetherWorkCanBeTaken(t *testing.T) {
	recorder := httptest.NewRecorder()
	serverFor(&stubFleet{ready: 0, identities: []pool.Identity{{Name: "a"}}}).
		Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/health", nil))

	var body struct {
		Status      string
		Ready, Size int
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Everything resting is healthy but cannot take work, and a monitor has to
	// be able to tell that from a server that is broken.
	if body.Status != "ok" || body.Ready != 0 || body.Size != 1 {
		t.Fatalf("health reported %+v", body)
	}
}

func bytesContain(haystack []byte, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		indexOf(string(haystack), needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
