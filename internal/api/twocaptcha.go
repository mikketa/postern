package api

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/mikketa/postern/internal/solver"
)

// The 2Captcha legacy interface, spoken well enough that a client written
// against any of the paid services points at postern by changing one base URL.
//
// That is the whole reason it exists. Postern's own /solve is a better shape —
// one request, one answer, a real status code — but nobody rewrites a working
// integration to try an unknown solver. This one they can try in a minute.
//
// It is a protocol, not a model: everything below translates and hands the
// work to solveOnce, so both front ends queue the same way, count the same
// way, and cannot drift apart.

// The error strings are part of the interface, spelling included.
// ERROR_NO_SUCH_CAPCHA_ID is misspelt at the source; clients match on it.
const (
	errWrongKey    = "ERROR_KEY_DOES_NOT_EXIST"
	errNoSlot      = "ERROR_NO_SLOT_AVAILABLE"
	errUnsolvable  = "ERROR_CAPTCHA_UNSOLVABLE"
	errNoSuchID    = "ERROR_NO_SUCH_CAPCHA_ID"
	errNoMethod    = "ERROR_NO_SUCH_METHOD"
	errBadParams   = "ERROR_BAD_PARAMETERS"
	errPageURL     = "ERROR_PAGEURL"
	errBadSitekey  = "ERROR_RECAPTCHA_INVALID_SITEKEY"
	notReady       = "CAPCHA_NOT_READY"
	compatBodyCap  = 64 << 10
	pollAdviceSecs = 5
)

// twoCaptchaRoutes adds the compatibility endpoints.
func (s *Server) twoCaptchaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/in.php", s.handleIn)
	mux.HandleFunc("/res.php", s.handleRes)
}

// handleIn accepts a captcha and answers with an id to collect it by.
//
// Both GET and POST, and form or query either way: the clients in the wild do
// all four, and a compatibility layer that is particular about which is not
// compatible with anything.
func (s *Server) handleIn(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, compatBodyCap)
	_ = r.ParseForm()
	q := r.Form
	asJSON := truthy(q.Get("json"))

	if !s.keyOK(q.Get("key")) {
		compatError(w, asJSON, errWrongKey)
		return
	}

	req, errCode := translate(q)
	if errCode != "" {
		compatError(w, asJSON, errCode)
		return
	}

	// The queue is bounded by what the fleet can actually get through: past
	// this, more waiting only means more timeouts. ERROR_NO_SLOT_AVAILABLE is
	// the protocol's own word for it, and clients written against the real
	// service already back off on it.
	id, ok := s.jobs.start(s.queueDepth)
	if !ok {
		s.metrics.Observe(req.Kind, 0, false, ReasonBusy)
		compatError(w, asJSON, errNoSlot)
		return
	}

	// The submitting request ends here; the solve outlives it. So it gets a
	// context of its own — using the request's would cancel the work at the
	// moment we answered, which is the one thing this protocol must not do.
	s.detached.Add(1)
	go func() {
		defer s.detached.Done()
		// This goroutine is outside the request, so it is outside the recovery
		// middleware too: a panic in here has nothing between it and the
		// process. Recovering is not tidiness, it is the difference between
		// one failed captcha and every in-flight one.
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic solving a submitted captcha",
					"id", id, "panic", v, "stack", string(debug.Stack()))
				s.jobs.finish(id, "", ReasonInternal, fmt.Errorf("internal error: %v", v))
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(),
			effectiveTimeout(req.TimeoutMS, s.timeout)+30*time.Second)
		defer cancel()

		result, reason, err := s.solveOnce(ctx, req, "2captcha/"+id)
		if err != nil {
			s.jobs.finish(id, "", reason, err)
			return
		}
		s.jobs.finish(id, result.Token, ReasonOK, nil)
	}()

	compatOK(w, asJSON, id)
}

// handleRes answers with a token, a not-ready, or an error.
func (s *Server) handleRes(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	q := r.Form
	asJSON := truthy(q.Get("json"))

	if !s.keyOK(q.Get("key")) {
		compatError(w, asJSON, errWrongKey)
		return
	}

	switch q.Get("action") {
	case "get", "":
	case "getbalance":
		// Nothing is metered here, and answering with a number invites a
		// client to believe there is an account behind it. A large one is the
		// honest answer to "may I keep going": yes.
		compatOK(w, asJSON, "999999")
		return
	default:
		compatError(w, asJSON, errNoMethod)
		return
	}

	entry, found := s.jobs.collect(q.Get("id"))
	if !found {
		compatError(w, asJSON, errNoSuchID)
		return
	}
	if !entry.finished {
		compatError(w, asJSON, notReady)
		return
	}
	if entry.err != nil {
		compatError(w, asJSON, compatCode(entry.reason))
		return
	}
	compatOK(w, asJSON, entry.token)
}

// keyOK checks the client key. With no token configured every key passes,
// which matches the rest of the server: authentication is something an
// operator turns on.
func (s *Server) keyOK(key string) bool {
	return s.token == "" || subtleEqual(key, s.token)
}

// translate turns 2Captcha parameters into a solve request. It returns the
// protocol's own error string when it cannot.
func translate(q map[string][]string) (solveRequest, string) {
	get := func(names ...string) string {
		for _, n := range names {
			if vs := q[n]; len(vs) > 0 && vs[0] != "" {
				return vs[0]
			}
		}
		return ""
	}

	// pageurl is the documented name; url is what several clients send.
	pageURL := get("pageurl", "url")
	if pageURL == "" {
		return solveRequest{}, errPageURL
	}
	// googlekey for reCAPTCHA, sitekey for Turnstile. Accept either for both:
	// telling a caller its key is in the wrong field helps nobody.
	sitekey := get("googlekey", "sitekey")
	if sitekey == "" {
		return solveRequest{}, errBadSitekey
	}

	req := solveRequest{
		URL:     pageURL,
		SiteKey: sitekey,
		Action:  get("action"),
		CData:   get("data", "cdata"),
	}

	switch strings.ToLower(get("method")) {
	case "turnstile":
		req.Kind = string(solver.Turnstile)
	case "userrecaptcha":
		switch {
		case strings.EqualFold(get("version"), "v3"):
			req.Kind = string(solver.RecaptchaV3)
			if req.Action == "" {
				// The service's own default when a caller does not say.
				req.Action = "verify"
			}
		case truthy(get("invisible")):
			req.Kind = string(solver.RecaptchaInvis)
		default:
			req.Kind = string(solver.RecaptchaV2)
		}
	case "":
		return solveRequest{}, errBadParams
	default:
		// hcaptcha, funcaptcha, geetest and the rest. Saying so plainly beats
		// accepting the job and failing it five seconds later.
		return solveRequest{}, errNoMethod
	}

	// action on a v2 request is 2Captcha's own field, not the vendor's, and
	// passing it through would change the token the widget asks for.
	if req.Kind == string(solver.RecaptchaV2) || req.Kind == string(solver.RecaptchaInvis) {
		req.Action = ""
	}
	return req, ""
}

// compatCode maps a failure to the string this protocol uses for it.
//
// The set on the other side is coarser than ours: a caller here can tell
// "try again" from "stop", and nothing finer. Everything that means the
// challenge was attempted and did not yield becomes unsolvable, which is what
// a client written against the real service already handles.
func compatCode(r Reason) string {
	switch r {
	case ReasonBusy:
		return errNoSlot
	case ReasonInvalid:
		return errBadParams
	case ReasonNoImageSolver:
		// The operator is missing a piece of their own configuration. There is
		// no code for that here, and unsolvable is at least true of the job.
		return errUnsolvable
	default:
		return errUnsolvable
	}
}

// compatOK writes a success, in whichever of the two formats was asked for.
func compatOK(w http.ResponseWriter, asJSON bool, payload string) {
	if asJSON {
		writeJSON(w, http.StatusOK, map[string]any{"status": 1, "request": payload})
		return
	}
	writePlain(w, "OK|"+payload)
}

// compatError writes a failure. Note the status: this protocol answers 200 and
// puts the outcome in the body, so a client that checks the status code sees
// success and reads the body — which is what it was written to do.
func compatError(w http.ResponseWriter, asJSON bool, code string) {
	if asJSON {
		body := map[string]any{"status": 0, "request": code}
		if code == notReady {
			body["error_text"] = fmt.Sprintf("not solved yet, ask again in %ds", pollAdviceSecs)
		}
		writeJSON(w, http.StatusOK, body)
		return
	}
	writePlain(w, code)
}

func writePlain(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(s))
}

// truthy reads the protocol's several spellings of yes.
func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n != 0
	}
	return false
}
