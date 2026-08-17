package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func guardedServer(token string) http.Handler {
	s := serverFor(&stubFleet{ready: 1})
	s.token = token
	return s.Handler()
}

func TestARouteThatNamesIdentitiesNeedsTheToken(t *testing.T) {
	// /fleet reports every identity, whether it is proxied and how it has
	// been doing. Left open it is a map of the operation for anyone who finds
	// the port.
	for _, c := range []struct {
		name, header string
		want         int
	}{
		{"no header at all", "", http.StatusUnauthorized},
		{"the wrong secret", "Bearer wrong", http.StatusUnauthorized},
		{"the right secret without its scheme", "s3cret", http.StatusUnauthorized},
		{"a prefix of the right secret", "Bearer s3cre", http.StatusUnauthorized},
		{"the right secret", "Bearer s3cret", http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/fleet", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			guardedServer("s3cret").ServeHTTP(rec, req)

			if rec.Code != c.want {
				t.Errorf("GET /fleet with %q returned %d, want %d",
					c.header, rec.Code, c.want)
			}
		})
	}
}

func TestSolvingNeedsTheToken(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/solve",
		strings.NewReader(`{"url":"https://example.com","sitekey":"0x4A"}`))
	guardedServer("s3cret").ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("an unauthenticated solve returned %d, want %d — anyone who "+
			"reaches the port can spend the fleet", rec.Code, http.StatusUnauthorized)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("no WWW-Authenticate header, so a client is not told how to authenticate")
	}
}

func TestTheHealthCheckStaysOpen(t *testing.T) {
	// A load balancer probing this usually cannot carry a secret, and the
	// answer says nothing a caller could use.
	rec := httptest.NewRecorder()
	guardedServer("s3cret").ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("GET /health returned %d with a token configured, want 200 — "+
			"health checks would fail and the service would look down", rec.Code)
	}
}

func TestNoTokenLeavesLocalhostAloneAndRefusesTheNetwork(t *testing.T) {
	for _, c := range []struct {
		addr    string
		token   string
		wantErr bool
	}{
		{"127.0.0.1:8099", "", false},
		{"[::1]:8099", "", false},
		{"127.0.0.1:8099", "s3cret", false},
		{"0.0.0.0:8099", "s3cret", false},
		{"0.0.0.0:8099", "", true},
		{":8099", "", true},
		{"192.168.1.10:8099", "", true},
	} {
		err := CheckReachable(c.addr, c.token)
		if (err != nil) != c.wantErr {
			t.Errorf("CheckReachable(%q, token=%t) error = %v, want error: %t",
				c.addr, c.token != "", err, c.wantErr)
		}
	}
}
