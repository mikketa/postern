package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/mikketa/postern/internal/solver"
)

// Reason is the machine-readable failure class returned as "code" and used as
// the outcome label on the metrics.
//
// The set is small and closed on purpose. As a metric label it becomes a time
// series per value, so a class derived from an error message would grow one
// series per distinct message and eventually cost more to store than the
// solves it describes. As an API field it is a contract: a caller branches on
// it, so it may gain values but existing ones do not change meaning.
type Reason string

const (
	ReasonOK Reason = "ok"

	// ReasonBusy is every identity resting. Retry after Retry-After.
	ReasonBusy Reason = "busy"

	// ReasonCrossing is a challenge in front of the site that refused us. The
	// lever is the address, and retrying from the same one rarely helps.
	ReasonCrossing Reason = "crossing_refused"

	// ReasonTimeout is the budget running out. Worth a retry, and worth
	// raising -timeout if it is the usual answer.
	ReasonTimeout Reason = "timeout"

	// ReasonVendor is the widget reporting a failure of its own.
	ReasonVendor Reason = "vendor_error"

	// ReasonNoImageSolver is a picture grid with nothing configured to read
	// it. Configuration, not luck: retrying changes nothing.
	ReasonNoImageSolver Reason = "no_image_solver"

	// ReasonRefused is the vendor serving grids past the point it grades them,
	// which is the address or the profile being refused rather than the
	// answers.
	ReasonRefused Reason = "challenge_refused"

	// ReasonInvalid is a request that could not be attempted.
	ReasonInvalid Reason = "invalid_request"

	// ReasonUnauthorized is a missing or wrong bearer token.
	ReasonUnauthorized Reason = "unauthorized"

	// ReasonQuota is this client's own rate limit, not the server's capacity.
	// Distinct from ReasonBusy on purpose: busy means come back when the fleet
	// frees up, and quota means come back when your own allowance refills. A
	// caller that cannot tell them apart cannot tell "the server is loaded"
	// from "you are asking for more than you bought".
	ReasonQuota Reason = "quota_exceeded"

	// ReasonTooLarge is a request body past what will be read.
	ReasonTooLarge Reason = "body_too_large"

	// ReasonNoFleet is a fleet endpoint on a server running one shared
	// browser, which has no identities to report on.
	ReasonNoFleet Reason = "no_fleet"

	// ReasonCancelled is the caller hanging up.
	ReasonCancelled Reason = "client_gone"

	// ReasonInternal is everything unclassified. A rising count here means a
	// class is missing, not that the world got stranger.
	ReasonInternal Reason = "internal"
)

// reasonFor classifies a solve failure.
//
// By errors.Is against the solver's own sentinels, never by matching the
// message: the message is free to be rewritten for whoever reads it, and a
// classifier that reads it turns every wording change into a silent break in
// someone's dashboard.
func reasonFor(err error) Reason {
	switch {
	case err == nil:
		return ReasonOK
	case errors.Is(err, solver.ErrCrossing):
		return ReasonCrossing
	case errors.Is(err, solver.ErrNoImageSolver):
		return ReasonNoImageSolver
	case errors.Is(err, solver.ErrRefused):
		return ReasonRefused
	case errors.Is(err, solver.ErrVendor):
		return ReasonVendor
	case errors.Is(err, solver.ErrNoToken),
		errors.Is(err, context.DeadlineExceeded):
		return ReasonTimeout
	case errors.Is(err, solver.ErrInvalidRequest):
		return ReasonInvalid
	case errors.Is(err, context.Canceled):
		return ReasonCancelled
	default:
		return ReasonInternal
	}
}

// status is the HTTP status a reason answers with.
func (r Reason) status() int {
	switch r {
	case ReasonInvalid:
		return http.StatusBadRequest
	case ReasonUnauthorized:
		return http.StatusUnauthorized
	case ReasonTooLarge:
		return http.StatusRequestEntityTooLarge
	case ReasonNoFleet:
		return http.StatusNotFound
	case ReasonBusy:
		return http.StatusServiceUnavailable
	case ReasonQuota:
		// 429 and not 503: the server has capacity, this caller has spent its
		// allowance. A client that retries on 503 and gives up on 429 is
		// behaving correctly in both cases only if we tell them apart.
		return http.StatusTooManyRequests
	case ReasonNoImageSolver:
		// The server is missing a piece of its own configuration. Nothing the
		// caller sent is wrong, and repeating the request will not fix it.
		return http.StatusNotImplemented
	case ReasonInternal:
		return http.StatusInternalServerError
	default:
		// The challenge was attempted and did not yield: crossing refused,
		// timeout, vendor error. The upstream is the vendor, not us.
		return http.StatusBadGateway
	}
}
