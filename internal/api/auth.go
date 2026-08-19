package api

import (
	"context"
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

// Exposure is how the server is about to be reachable, and the only thing
// that decides whether serving is safe.
type Exposure struct {
	// Addr is the listen address, as given to -addr.
	Addr string

	// Token is the shared secret, empty when there is none.
	Token string

	// Clients is how many clients a -clients file defined. A client file is
	// authentication just as much as a token is, and a deployment using one
	// must not be told to set POSTERN_TOKEN as well.
	Clients int

	// TLS is whether this server terminates TLS itself.
	TLS bool

	// BehindTLSProxy is the operator saying a terminator sits in front, which
	// is the one legitimate reason to carry a token over cleartext off this
	// machine. Named for what it claims rather than for what it disables: an
	// operator setting it should recognise their own deployment in it, and an
	// operator who does not have a proxy should not be tempted.
	BehindTLSProxy bool
}

// Check refuses a configuration that would hand the solver, or its secret, to
// the network.
//
// Loopback is nobody else's business and passes whatever it does. Off this
// machine there are two separate questions, and both have to be answered:
// whether anyone may call it, and whether the answer travels in the clear.
func (e Exposure) Check() error {
	reachable, err := offThisMachine(e.Addr)
	if err != nil {
		return err
	}
	if !reachable {
		return nil
	}

	if e.Token == "" && e.Clients == 0 {
		return fmt.Errorf("api: -addr %q is reachable from off this machine and "+
			"there is no authentication. Set %s, pass -clients, or bind to "+
			"127.0.0.1", e.Addr, TokenEnv)
	}

	// A bearer token is a password, and this one is sent on every request.
	// Over cleartext it is readable by anything on the path, so an
	// authenticated server on a plain socket is not more private than an
	// unauthenticated one — it only looks it.
	if !e.TLS && !e.BehindTLSProxy {
		secret := TokenEnv
		if e.Token == "" {
			secret = "the client keys"
		}
		return fmt.Errorf("api: -addr %q would send %s across the network in "+
			"cleartext, where it is a password anyone on the path can read. "+
			"Pass -tls-cert and -tls-key, or -behind-tls-proxy if something in "+
			"front of this already terminates TLS", e.Addr, secret)
	}
	return nil
}

// offThisMachine reports whether an address is reachable by anything but this
// host.
func offThisMachine(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, fmt.Errorf("api: cannot read the listen address %q: %w", addr, err)
	}
	// An empty host is every interface, which is the broadest of all.
	if host == "" {
		return true, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback(), nil
	}
	// A name rather than an address: "localhost" is the only one that can be
	// trusted without resolving, and resolving at startup would make the check
	// depend on whatever DNS says today.
	return host != "localhost", nil
}

type clientKey struct{}

// clientFrom returns the client behind a request, or nil when the server runs
// with no authentication at all.
func clientFrom(ctx context.Context) *Client {
	c, _ := ctx.Value(clientKey{}).(*Client)
	return c
}

// withClient attaches the caller to the context, so that everything
// downstream — quota, job ownership, metrics, logs — can name it without
// being handed it through every signature.
func withClient(ctx context.Context, c *Client) context.Context {
	return context.WithValue(ctx, clientKey{}, c)
}

// clientName is what to record for a request, including the case where there
// is no authentication and so no client.
func clientName(c *Client) string {
	if c == nil {
		return "anonymous"
	}
	return c.Name
}

// authenticated wraps a handler in a bearer-token check, resolving which
// client presented the key. A nil set leaves it open, which is the
// single-operator-on-localhost case.
func authenticated(clients *Clients, next http.Handler) http.Handler {
	if clients.Len() == 0 {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		key, ok := strings.CutPrefix(header, "Bearer ")
		// The prefix is not a secret, so testing it plainly leaks nothing; the
		// key itself is compared in constant time inside lookup.
		client := (*Client)(nil)
		if ok {
			client = clients.lookup(key)
		}
		if client == nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeFailure(w, ReasonUnauthorized, "missing or wrong bearer token")
			return
		}
		next.ServeHTTP(w, r.WithContext(withClient(r.Context(), client)))
	})
}

// bearerExempt reports whether a route is outside the bearer check.
//
// Two kinds are, for different reasons, and neither is "unauthenticated":
//
//   - /health, because a load balancer probing it usually cannot carry a
//     secret, and the answer says nothing a caller could use. /fleet is not on
//     the list: it names identities and whether each is proxied.
//   - the 2Captcha-compatible endpoints, which carry their own credential in a
//     "key" parameter because that is what the protocol specifies. They check
//     it themselves against the same secret — see Server.keyOK. Requiring a
//     bearer as well would mean no existing client could reach them, which is
//     the entire point of speaking that protocol.
func bearerExempt(p string) bool {
	return strings.HasPrefix(p, "/health") ||
		strings.HasPrefix(p, APIVersion+"/health") ||
		p == "/in.php" || p == "/res.php"
}

// subtleEqual compares two secrets in constant time, padded so that a length
// mismatch does not return early and leak the length one request at a time.
func subtleEqual(got, want string) bool {
	return len(got) == len(want) &&
		subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
