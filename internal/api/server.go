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
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
