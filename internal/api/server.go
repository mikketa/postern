// Package api exposes the solver over a small local HTTP interface, so that a
// script in any language can use Postern without linking against it.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/mikketa/postern/internal/browser"
	"github.com/mikketa/postern/internal/pool"
	"github.com/mikketa/postern/internal/solver"
)

// Borrower hands out a browser to solve with and takes it back afterwards.
//
// Two shapes fit: one browser shared by every request, which is what a single
// operator wants, and a fleet of identities taking turns, which is what any
// volume needs. The handler cannot tell the difference, and should not — see
// internal/pool for why the second exists.
type Borrower interface {
	Borrow(ctx context.Context) (*browser.Browser, func(solved bool), error)
	Ready() int
	Size() int
}

// Server answers solve requests.
type Server struct {
	fleet   Borrower
	timeout time.Duration
	log     *slog.Logger

	// imageSolver is passed to every solve; see internal/challenge.
	imageSolver string

	// slots caps how many tabs solve at once. One Chrome with a dozen tabs
	// grinding challenges is both slow and conspicuous.
	slots chan struct{}

	// token is the shared secret every route but the health check requires.
	// Empty leaves the server open, which CheckReachable only tolerates on
	// loopback.
	token string

	// metrics is what /metrics reports.
	metrics *Metrics
}

// New builds a Server over one shared browser. maxConcurrent below 1 is
// treated as 1.
func New(b *browser.Browser, timeout time.Duration, maxConcurrent int, imageSolver string, log *slog.Logger) *Server {
	return NewFleet(shared{browser: b}, timeout, maxConcurrent, imageSolver, log)
}

// shared is the one-browser Borrower: every request gets the same browser and
// giving it back does nothing.
type shared struct{ browser *browser.Browser }

func (s shared) Borrow(context.Context) (*browser.Browser, func(bool), error) {
	return s.browser, func(bool) {}, nil
}
func (s shared) Ready() int { return 1 }
func (s shared) Size() int  { return 1 }

// NewFleet builds a Server over anything that can lend a browser.
func NewFleet(fleet Borrower, timeout time.Duration, maxConcurrent int, imageSolver string, log *slog.Logger) *Server {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Server{
		fleet:       fleet,
		timeout:     timeout,
		imageSolver: imageSolver,
		log:         log,
		slots:       make(chan struct{}, maxConcurrent),
		token:       Token(),
		metrics:     NewMetrics(),
	}
}

// Handler returns the routes, behind the bearer check when one is configured.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /solve", s.handleSolve)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /fleet", s.handleFleet)
	mux.HandleFunc("GET /metrics", s.handleMetrics)

	guarded := authenticated(s.token, mux)
	routed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if openPath(r.URL.Path) {
			mux.ServeHTTP(w, r)
			return
		}
		guarded.ServeHTTP(w, r)
	})

	// Outermost first: an id exists before anything can log it, and the
	// recovery sits inside that so its log line can carry the id.
	return withRequestID(recovered(s.log, routed))
}

// maxBodyBytes bounds a solve request. The body is a handful of short fields;
// anything beyond this is a mistake or an attack, and it matters because
// json.Decoder reads the whole thing into memory before it decides it did not
// like it.
const maxBodyBytes = 64 << 10

type solveRequest struct {
	URL       string `json:"url"`
	SiteKey   string `json:"sitekey"`
	Kind      string `json:"kind,omitempty"`
	Action    string `json:"action,omitempty"`
	CData     string `json:"cdata,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

// effectiveTimeout is how long a request actually gets: what it asked for, or
// the operator's ceiling, whichever is shorter.
//
// A caller may ask for less than the operator allows, never more. The slot a
// request holds is a whole browser and there are only -concurrency of them, so
// an unbounded timeout_ms lets one client pin an identity for as long as it
// cares to name — and the fleet answers everyone else with 503 meanwhile.
func effectiveTimeout(askedMS int, ceiling time.Duration) time.Duration {
	if askedMS <= 0 {
		return ceiling
	}
	return min(time.Duration(askedMS)*time.Millisecond, ceiling)
}

type solveResponse struct {
	Token     string `json:"token"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

func (s *Server) handleSolve(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	var req solveRequest
	dec := json.NewDecoder(r.Body)
	// A misspelt field is otherwise silent: "timeoutMs" instead of
	// "timeout_ms" leaves the caller believing it set a timeout it did not,
	// and nothing in the response says so.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeFailure(w, ReasonTooLarge, "request body too large")
			return
		}
		writeFailure(w, ReasonInvalid, "invalid json body: "+err.Error())
		return
	}
	if req.URL == "" || req.SiteKey == "" {
		writeFailure(w, ReasonInvalid, "url and sitekey are required")
		return
	}

	timeout := effectiveTimeout(req.TimeoutMS, s.timeout)

	// Wait for a free slot, but give up if the client hangs up first.
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-r.Context().Done():
		return
	}

	chrome, give, err := s.fleet.Borrow(r.Context())
	if err != nil {
		// Everything is resting. That is the fleet working as intended under
		// more load than it has identities for, so say so plainly and let the
		// caller back off rather than pretending the solve failed.
		s.log.Info("no identity free", "ready", s.fleet.Ready(), "of", s.fleet.Size(),
			"request_id", requestIDFrom(r.Context()))
		w.Header().Set("Retry-After", "60")
		// Counted, because a fleet turning callers away is the single most
		// useful thing to see on a graph and it used to return before anything
		// recorded it — the load that got a 503 was invisible.
		s.metrics.Observe(req.Kind, 0, false, ReasonBusy)
		writeFailure(w, ReasonBusy, err.Error())
		return
	}
	solved := false
	reason := ReasonInternal
	started := time.Now()
	defer func() {
		give(solved)
		// Recorded on every path out, including the ones that returned an
		// error: a solver measured only on its successes reports a latency
		// that no user experiences. The reason rides along so that a rise in
		// failures says which kind, which is the difference between "the
		// address is being refused" and "nobody configured a vision solver".
		s.metrics.Observe(req.Kind, time.Since(started), solved, reason)
	}()

	result, err := solver.Solve(r.Context(), chrome, solver.Request{
		Kind:        solver.Kind(req.Kind),
		URL:         req.URL,
		SiteKey:     req.SiteKey,
		Action:      req.Action,
		CData:       req.CData,
		ImageSolver: s.imageSolver,
		Log:         s.log.With("url", req.URL, "request_id", requestIDFrom(r.Context())),
	}, timeout)
	if err != nil {
		reason = reasonFor(err)
		// A client that walked away is not a solver failure worth logging.
		if r.Context().Err() == nil {
			s.log.Warn("solve failed", "url", req.URL, "err", err, "code", reason,
				"request_id", requestIDFrom(r.Context()))
		}
		writeFailure(w, reason, err.Error())
		return
	}

	reason = ReasonOK
	solved = true
	s.log.Info("solved", "url", req.URL, "elapsed", result.Elapsed,
		"request_id", requestIDFrom(r.Context()))
	writeJSON(w, http.StatusOK, solveResponse{
		Token:     result.Token,
		ElapsedMS: result.Elapsed.Milliseconds(),
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	// Ready and size rather than a bare "ok": a server whose identities are all
	// resting is healthy and cannot take work, and a monitor needs to tell
	// those apart from a server that is broken.
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"ready":  s.fleet.Ready(),
		"size":   s.fleet.Size(),
	})
}

// handleFleet reports each identity and how it has been doing.
//
// A fleet that cannot be watched cannot be sized. The number that matters is
// not the total solved but the ratio per identity: identities that fail
// together are an address or a provider going bad, one that fails alone is its
// own profile burnt, and a pool permanently at zero ready needs more identities
// rather than more patience. None of that is visible from the outside without
// this.
func (s *Server) handleFleet(w http.ResponseWriter, _ *http.Request) {
	reporter, ok := s.fleet.(interface{ Stats() []pool.Identity })
	if !ok {
		writeFailure(w, ReasonNoFleet, "not serving from a fleet")
		return
	}

	type entry struct {
		Name      string    `json:"name"`
		Solves    int       `json:"solves"`
		Failures  int       `json:"failures"`
		Streak    int       `json:"streak"`
		Warmed    bool      `json:"warmed"`
		LastUsed  time.Time `json:"last_used,omitzero"`
		RestUntil time.Time `json:"rest_until,omitzero"`
		Resting   bool      `json:"resting"`
		Proxied   bool      `json:"proxied"`
	}

	now := time.Now()
	out := make([]entry, 0)
	for _, identity := range reporter.Stats() {
		out = append(out, entry{
			Name: identity.Name, Solves: identity.Solves, Failures: identity.Failures,
			Streak: identity.Streak, Warmed: identity.Warmed,
			LastUsed: identity.LastUsed, RestUntil: identity.RestUntil,
			Resting: now.Before(identity.RestUntil),
			// Whether, not which: the file holds passwords.
			Proxied: identity.Proxy != "",
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ready":      s.fleet.Ready(),
		"size":       s.fleet.Size(),
		"identities": out,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeFailure answers any error with its class alongside the message, so a
// caller can branch without matching strings.
//
// Every error path goes through here. A code that is present on most responses
// and absent on a few is worse than none at all: it invites a caller to depend
// on it and then fails them on the one path they did not test.
func writeFailure(w http.ResponseWriter, reason Reason, msg string) {
	writeJSON(w, reason.status(), map[string]string{
		"error": msg,
		"code":  string(reason),
	})
}

// handleMetrics renders the counters in Prometheus text format. It sits behind
// the same token as everything else: how many solves an operation runs, and
// how well, is not public information.
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.metrics.Write(w, map[string]int{
		"postern_fleet_identities_ready": s.fleet.Ready(),
		"postern_fleet_identities_total": s.fleet.Size(),
		"postern_solve_slots_in_use":     len(s.slots),
		"postern_solve_slots_total":      cap(s.slots),
	})
}
