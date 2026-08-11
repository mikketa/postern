// Package api exposes the solver over a small local HTTP interface, so that a
// script in any language can use Postern without linking against it.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/mikketa/postern/internal/browser"
	"github.com/mikketa/postern/internal/solver"
)

// Server answers solve requests using a single shared browser.
type Server struct {
	browser *browser.Browser
	timeout time.Duration
	log     *slog.Logger

	// imageSolver is passed to every solve; see internal/challenge.
	imageSolver string

	// slots caps how many tabs solve at once. One Chrome with a dozen tabs
	// grinding challenges is both slow and conspicuous.
	slots chan struct{}
}

// New builds a Server. maxConcurrent below 1 is treated as 1.
func New(b *browser.Browser, timeout time.Duration, maxConcurrent int, imageSolver string, log *slog.Logger) *Server {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Server{
		browser:     b,
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

	result, err := solver.Solve(r.Context(), s.browser, solver.Request{
		Kind:        solver.Kind(req.Kind),
		URL:         req.URL,
		SiteKey:     req.SiteKey,
		Action:      req.Action,
		CData:       req.CData,
		ImageSolver: s.imageSolver,
	}, timeout)
	if err != nil {
		// A client that walked away is not a solver failure worth logging.
		if r.Context().Err() == nil {
			s.log.Warn("solve failed", "url", req.URL, "err", err)
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

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
