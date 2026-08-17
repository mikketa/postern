package pool

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/mikketa/postern/internal/browser"
)

// Fleet turns identities into running browsers.
//
// One browser per solve, started when the identity is taken and closed when it
// is given back. That is the opposite of what a server usually wants — starting
// Chrome costs a second or so — and it is deliberate on both counts.
//
// Closing is how a profile is written at all: a browser that is killed leaves
// no settled state behind (see browser.Close), so a fleet that kept browsers
// open for speed would be a fleet of profiles that never age. And keeping one
// browser per identity alive would cost a few hundred megabytes each while they
// sit through their rest, which is most of the time by design. Bounding memory
// by how many solves run at once, rather than by how many identities exist, is
// what lets the pool be large enough to matter.
type Fleet struct {
	pool  *Pool
	opts  browser.Options
	pages []string
	log   *slog.Logger
}

// NewFleet pairs a pool with the browser options every identity starts from.
// The profile and proxy in opts are ignored: those come from the identity.
func NewFleet(p *Pool, opts browser.Options, warmPages []string, log *slog.Logger) *Fleet {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Fleet{pool: p, opts: opts, pages: warmPages, log: log}
}

// Borrow starts a browser for the identity that has rested longest, warming its
// profile first if it has never been anywhere.
//
// The returned function must be called with whether the solve produced a token:
// that is what decides how long this identity rests, and whether it is set
// aside altogether.
func (f *Fleet) Borrow(ctx context.Context) (*browser.Browser, func(solved bool), error) {
	identity, err := f.pool.Take()
	if err != nil {
		return nil, nil, err
	}

	opts := f.opts
	opts.UserDataDir = identity.Profile
	opts.Proxy = identity.Proxy

	chrome, err := browser.Launch(ctx, opts)
	if err != nil {
		// A browser that will not start is the identity's fault as far as the
		// pool is concerned — a bad proxy, a locked profile — so it counts as a
		// failure and the identity rests rather than being retried at once.
		f.pool.Release(identity, false)
		return nil, nil, fmt.Errorf("fleet: identity %s: %w", identity.Name, err)
	}

	if !identity.Warmed && len(f.pages) > 0 {
		// Once per profile, on its first outing. Doing it here rather than in a
		// separate command is what makes a new identity self-sufficient: add a
		// line to the identities file and it takes itself browsing before it
		// ever answers a captcha.
		f.log.Info("warming a new profile", "identity", identity.Name, "pages", len(f.pages))
		if err := chrome.Warm(ctx, f.pages, f.log); err != nil {
			f.log.Info("warming did not finish", "identity", identity.Name, "err", err)
		} else {
			f.pool.Warmed(identity)
		}
	}

	f.log.Info("borrowed", "identity", identity.Name, "ready", f.pool.Ready(), "of", f.pool.Size())

	var once sync.Once
	return chrome, func(solved bool) {
		once.Do(func() {
			// Close before Release: closing is what writes the profile, and the
			// identity must not be handed to another solve while its Chrome is
			// still holding the profile lock.
			chrome.Close()
			f.pool.Release(identity, solved)
			f.log.Info("returned", "identity", identity.Name, "solved", solved,
				"ready", f.pool.Ready(), "of", f.pool.Size())
		})
	}, nil
}

// Ready is how many identities could be borrowed right now.
func (f *Fleet) Ready() int { return f.pool.Ready() }

// Size is how many identities the fleet has.
func (f *Fleet) Size() int { return f.pool.Size() }

// Stats is every identity and how it has done.
func (f *Fleet) Stats() []Identity { return f.pool.Stats() }
