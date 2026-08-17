package api

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
)

// TokenEnv is where the shared secret is read from.
//
// An environment variable rather than a flag, for the same reason proxy
// credentials are stripped off -proxy: anything on a command line is in
// /proc/<pid>/cmdline, which every user on the machine can read. A secret that
// leaks to `ps` is not a secret.
const TokenEnv = "POSTERN_TOKEN"

// Token returns the configured shared secret, empty when there is none.
func Token() string { return os.Getenv(TokenEnv) }

// CheckReachable refuses a configuration that would expose the solver to the
// network with nothing in front of it.
//
// Binding to localhost without a token is a reasonable single-operator setup.
// Binding to anything else without one hands a browser fleet to whoever finds
// the port, so it is an error rather than a warning: a warning at startup is
// read once and never again.
func CheckReachable(addr, token string) error {
	if token != "" {
		return nil
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("api: cannot read the listen address %q: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("api: -addr %q listens on every interface with no "+
			"authentication. Set %s, or bind to 127.0.0.1", addr, TokenEnv)
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		return fmt.Errorf("api: -addr %q is reachable from off this machine and "+
			"there is no authentication. Set %s, or bind to 127.0.0.1", addr, TokenEnv)
	}
	return nil
}

// authenticated wraps a handler in a bearer-token check. An empty token leaves
// it open, which is the single-operator-on-localhost case.
func authenticated(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte("Bearer " + token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		// Constant time, and length-padded: subtle.ConstantTimeCompare returns
		// early on a length mismatch, so comparing straight would leak the
		// length of the secret one request at a time.
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "missing or wrong bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// openPath reports whether a route is reachable without a token. Only the
// health check is: a load balancer probing it usually cannot carry a secret,
// and it says nothing an unauthenticated caller could use. /fleet is not on
// the list — it names identities and whether each is proxied.
func openPath(p string) bool { return strings.HasPrefix(p, "/health") }
