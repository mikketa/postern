package main

import (
	"log/slog"
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

// TestDrainingOutlastsTheWorkItIsDraining is the rule a fixed number kept
// getting wrong. Shutdown stops the listener at once; the window after that
// has to fit the longest solve that could still be running, or a rolling
// restart drops exactly the requests that cost the most to lose.
func TestDrainingOutlastsTheWorkItIsDraining(t *testing.T) {
	for _, timeout := range []time.Duration{
		30 * time.Second, 60 * time.Second, 5 * time.Minute,
	} {
		if got := drainFor(timeout); got <= timeout {
			t.Errorf("drainFor(%s) = %s, which is not longer than the solve it "+
				"has to outlast — in-flight work would be cut off", timeout, got)
		}
	}

	// And it must track -timeout rather than sit at a constant: raising the
	// solve ceiling and leaving the drain behind is how this broke the first
	// time.
	if drainFor(5*time.Minute) <= drainFor(30*time.Second) {
		t.Error("drainFor does not grow with the solve timeout, so a longer " +
			"ceiling silently goes back to dropping the longest solves")
	}
}

func TestTheLogFormatIsAChoiceAndABadOneIsRefused(t *testing.T) {
	// Nothing that collects logs at scale parses anything but JSON, and a
	// deployment that has to regex its own log lines will eventually regex
	// them wrong.
	for _, format := range []string{"", "text", "json"} {
		if _, err := newLogger(format, slog.LevelInfo); err != nil {
			t.Errorf("newLogger(%q) = %v, want a logger", format, err)
		}
	}
	// And a typo is refused at startup rather than silently falling back:
	// discovering the format was wrong by finding no parseable logs, during
	// the incident the logs were for, is the wrong time.
	if _, err := newLogger("jsonn", slog.LevelInfo); err == nil {
		t.Error("a misspelt -log was accepted, and would be discovered as an " +
			"absence of parseable logs at the worst possible moment")
	}
}
