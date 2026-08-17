package main

import (
	"net/http"
	"testing"
	"time"
)

// TestTheListeningSocketHasLimits locks the timeouts on the HTTP server.
//
// net/http defaults every one of these to no limit, so leaving the struct bare
// is not a neutral choice: a connection that never finishes sending its
// headers is then held for as long as it cares to, and enough of them is the
// whole attack. This test exists because that is invisible in review — the
// absent field looks exactly like the field nobody needed.
func TestTheListeningSocketHasLimits(t *testing.T) {
	const solveTimeout = 60 * time.Second
	srv := newHTTPServer("127.0.0.1:0", http.NotFoundHandler(), solveTimeout)

	for _, c := range []struct {
		name string
		got  time.Duration
		why  string
	}{
		{"ReadHeaderTimeout", srv.ReadHeaderTimeout, "slow headers hold a connection open indefinitely"},
		{"ReadTimeout", srv.ReadTimeout, "a slow body does the same"},
		{"IdleTimeout", srv.IdleTimeout, "kept-alive connections accumulate"},
	} {
		if c.got <= 0 {
			t.Errorf("%s is unset — %s", c.name, c.why)
		}
	}

	if srv.MaxHeaderBytes <= 0 || srv.MaxHeaderBytes > 1<<20 {
		t.Errorf("MaxHeaderBytes = %d, want a bound under net/http's 1MB default",
			srv.MaxHeaderBytes)
	}

	// The one that is easy to get backwards: too short and it truncates a
	// perfectly good answer, which looks like the solver failing.
	if srv.WriteTimeout <= solveTimeout {
		t.Errorf("WriteTimeout = %s, which is not longer than the %s a solve may "+
			"legally take — a slow token would be cut off mid-flight and read "+
			"as a solver failure", srv.WriteTimeout, solveTimeout)
	}
}
