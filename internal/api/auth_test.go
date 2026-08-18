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

func TestWhatMayBeServedAndWhatMayNot(t *testing.T) {
	// Two separate questions once the socket is reachable from off this
	// machine: may anyone call it, and does the answer travel in the clear.
	// Both have to be answered, and the second is the one that is easy to
	// forget — an authenticated server on a plain socket is not more private
	// than an unauthenticated one, it only looks it.
	for _, c := range []struct {
		name    string
		e       Exposure
		wantErr bool
	}{
		{"loopback needs nothing", Exposure{Addr: "127.0.0.1:8099"}, false},
		{"ipv6 loopback too", Exposure{Addr: "[::1]:8099"}, false},
		{"localhost by name", Exposure{Addr: "localhost:8099"}, false},
		{"loopback with a token is still fine", Exposure{Addr: "127.0.0.1:8099", Token: "s"}, false},

		{"every interface, no token", Exposure{Addr: ":8099"}, true},
		{"a routable address, no token", Exposure{Addr: "192.168.1.10:8099"}, true},
		{"all interfaces, no token", Exposure{Addr: "0.0.0.0:8099"}, true},

		{"a token in cleartext off this machine", Exposure{Addr: "0.0.0.0:8099", Token: "s"}, true},
		{"the same, over TLS", Exposure{Addr: "0.0.0.0:8099", Token: "s", TLS: true}, false},
		{"the same, behind a terminator", Exposure{Addr: "0.0.0.0:8099", Token: "s", BehindTLSProxy: true}, false},

		{"a proxy in front does not excuse having no token",
			Exposure{Addr: "0.0.0.0:8099", BehindTLSProxy: true}, true},
		{"nor does TLS", Exposure{Addr: "0.0.0.0:8099", TLS: true}, true},

		{"a nonsense address is an error", Exposure{Addr: "not-an-address"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := c.e.Check(); (err != nil) != c.wantErr {
				t.Errorf("Check() error = %v, want error: %t", err, c.wantErr)
			}
		})
	}
}
