// Package api exposes the solver over a small local HTTP interface, so that a
// script in any language can use Postern without linking against it.
package api

import (
	"context"
	"encoding/json"
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
	}
}

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /solve", s.handleSolve)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /fleet", s.handleFleet)
	return mux
}

type solveRequest struct {
	URL       string `json:"url"`
	SiteKey   string `json:"sitekey"`
	Kind      string `json:"kind,omitempty"`
	Action    string `json:"action,omitempty"`
	CData     string `json:"cdata,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

type solveResponse struct {
	Token     string `json:"token"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

func (s *Server) handleSolve(w http.ResponseWriter, r *http.Request) {
	var req solveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.URL == "" || req.SiteKey == "" {
		writeError(w, http.StatusBadRequest, "url and sitekey are required")
		return
	}

	timeout := s.timeout
	if req.TimeoutMS > 0 {
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}

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
		s.log.Info("no identity free", "ready", s.fleet.Ready(), "of", s.fleet.Size())
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	solved := false
	defer func() { give(solved) }()

	result, err := solver.Solve(r.Context(), chrome, solver.Request{
		Kind:        solver.Kind(req.Kind),
		URL:         req.URL,
		SiteKey:     req.SiteKey,
		Action:      req.Action,
		CData:       req.CData,
		ImageSolver: s.imageSolver,
		Log:         s.log.With("url", req.URL),
	}, timeout)
	if err != nil {
		// A client that walked away is not a solver failure worth logging.
		if r.Context().Err() == nil {
			s.log.Warn("solve failed", "url", req.URL, "err", err)
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	solved = true
	s.log.Info("solved", "url", req.URL, "elapsed", result.Elapsed)
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
		writeError(w, http.StatusNotFound, "not serving from a fleet")
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

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
