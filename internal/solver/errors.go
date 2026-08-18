package solver

import "errors"

// The failure classes a caller can act on, so that acting on them does not
// mean matching strings against a message that is free to be rewritten.
//
// Deliberately few. Each one exists because the right response to it is
// different: retry later, fix your configuration, change the address, or give
// up. A class nobody would branch on is a class that belongs in the message.
var (
	// ErrCrossing is a challenge in front of the site that never let us
	// through. The lever is the address, not the code.
	ErrCrossing = errors.New("the challenge in front of the page")

	// ErrNoToken is the budget running out with the widget still unsatisfied.
	// Worth retrying; worth raising -timeout if it is the common answer.
	ErrNoToken = errors.New("no token before the deadline")

	// ErrVendor is the widget reporting a failure of its own.
	ErrVendor = errors.New("the widget reported an error")

	// ErrNoImageSolver is a picture grid served with nothing configured to
	// read it. A configuration problem, and retrying changes nothing.
	ErrNoImageSolver = errors.New("an image challenge was served and no image solver is configured")

	// ErrRefused is the vendor going on serving grids past the point where it
	// grades them. Measured: no token has ever come from a run this long. The
	// answers are not what is being refused.
	ErrRefused = errors.New("the challenge kept asking past the point it grades answers")

	// ErrInvalidRequest is a request that could not be attempted at all.
	ErrInvalidRequest = errors.New("invalid request")
)
