package browser

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/input"
)

// Warming the profile is the one lever on reCAPTCHA that is neither the vision
// model nor the address it comes from, and on the measurements here it is the
// larger of the three.
//
// The evidence is in the round counts. Over twenty runs, every token came from
// a run that finished in exactly six or twelve rounds and none came from a run
// that took any other number — thirteen runs, no tokens. A grid answered well
// does not produce that shape. A decision taken before the pictures does: some
// sessions are asked one easy challenge and waved through, others are asked
// until they give up, and which one you get is settled by what the browser
// looks like on arrival. A profile that has visited nothing, holds no cookies
// and has never been closed is the worst version of that.
//
// So this browses. Not to fake a fingerprint — postern's whole approach is to
// drive a real browser rather than dress one up — but to give the real browser
// a real past: pages actually fetched, cookies actually set by the sites that
// set them, history actually written, all at a pace a person might read at.
//
// It is deliberately small and boring: a handful of ordinary public pages, one
// visit each, a scroll and a pause. Nothing is submitted, nothing is logged
// into, nothing is fetched in bulk. A warm-up that hammered a site would be
// both rude and self-defeating, since the point is to look unremarkable.

const (
	// dwellMin and dwellMax are how long to stay on a page. Long enough to be a
	// read rather than a fetch, short enough that warming a profile is a coffee
	// break and not an afternoon.
	dwellMin = 2500
	dwellMax = 6500

	// scrollPause is the beat between the two scrolls on a page.
	scrollPauseMin = 400
	scrollPauseMax = 1200

	// pageTimeout bounds one visit. A slow site should cost one page, not the
	// whole warm-up.
	pageTimeout = 25 * time.Second
)

// Warm visits pages in the profile's own browser so that the profile has been
// somewhere.
//
// The caller supplies the list. There is no built-in one on purpose: what an
// unremarkable browsing history looks like depends on where the solver runs and
// who it is meant to resemble, and a list baked into the binary would be the
// same list for everybody using postern — which is its own signal.
func (b *Browser) Warm(ctx context.Context, pages []string, log *slog.Logger) error {
	if len(pages) == 0 {
		return fmt.Errorf("browser: nothing to visit")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	tabCtx, closeTab, err := b.NewTab()
	if err != nil {
		return err
	}
	defer closeTab()

	// Not in the order they were given: a profile that visits the same sites in
	// the same sequence every time is a pattern, and the whole point is not to
	// be one.
	order := rand.Perm(len(pages))

	visited := 0
	for _, at := range order {
		address := pages[at]
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err := visit(tabCtx, address); err != nil {
			// One page that will not load is not a reason to abandon the rest;
			// a warm profile with a gap in it is still a warm profile.
			log.Info("skipped a page", "url", address, "err", err)
			continue
		}
		visited++
		log.Info("visited", "url", address, "visited", visited, "of", len(pages))
	}

	if visited == 0 {
		return fmt.Errorf("browser: none of the %d pages would load", len(pages))
	}
	return nil
}

// visit reads one page: load it, look down it, stay a while.
func visit(ctx context.Context, address string) error {
	bounded, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()

	// Navigate rather than chromedp.Navigate, which waits for the load event.
	// Two of six ordinary news and reference pages never fired one inside
	// twenty-five seconds — there is always one more tracker still connecting —
	// and a page nobody waits for is a page not visited at all. A reader starts
	// reading when the text is there, so that is what this waits for.
	if err := chromedp.Run(bounded, chromedp.ActionFunc(func(ctx context.Context) error {
		_, _, _, _, err := page.Navigate(address).Do(ctx)
		return err
	})); err != nil {
		return err
	}
	if err := chromedp.Run(bounded, chromedp.WaitReady("body", chromedp.ByQuery)); err != nil {
		return err
	}

	// Two scrolls rather than one jump to the bottom, which is not a thing
	// anybody's wheel does, and a pause between them.
	for range 2 {
		if err := input.Pause(bounded, scrollPauseMin, scrollPauseMax); err != nil {
			return err
		}
		if err := chromedp.Run(bounded, chromedp.Evaluate(
			`window.scrollBy({ top: 300 + Math.random() * 500, behavior: 'smooth' })`,
			nil)); err != nil {
			return err
		}
	}

	return input.Pause(bounded, dwellMin, dwellMax)
}
