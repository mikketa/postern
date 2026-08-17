package solver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/mikketa/postern/internal/input"
)

// A site can sit behind a challenge of its own rather than carrying a widget in
// a form: Cloudflare's managed challenge answers every request with an
// interstitial page and only serves the site once that page is satisfied. It is
// not the same product as the widget postern renders — it is stricter, it is
// decided per request, and until it is crossed there is no target page to put a
// widget on at all.
//
// Crossing it is one click, and finding where to click is the whole problem:
// the widget lives in a closed shadow root, so it has no iframe to look for, no
// element to query and nothing to read. What the page does expose is the
// element the shadow root hangs off — and that element gives itself away by
// occupying the space of a widget while containing not one character of text.
//
// This cost most of a night to find because the obvious instrument lies:
// document.querySelectorAll('iframe') returns zero on a page that is displaying
// the checkbox perfectly well, which reads exactly like a challenge refusing to
// render. It is not. Measure this one on the screen, never through the DOM.

const (
	// interstitialBudget is how long to spend on the whole crossing. Measured
	// on a live managed challenge: the click is accepted at once, the widget
	// spins for four to eight seconds, and the page it was hiding arrives
	// inside fifteen.
	interstitialBudget = 45 * time.Second

	// verdictWait is how long a click is given to be answered. A crossing
	// shows in under two seconds — measured again on 2026-08-21, 1.8s from the
	// meaningful click to the site.
	//
	// This was eighteen seconds, calibrated to catch a refused challenge
	// putting its checkbox back at about sixteen. That signal is not worth its
	// price: the answer to a refusal is to reload for another challenge, and
	// waiting to watch the checkbox come back first buys nothing. Measured on
	// one refused crossing, the old value spent 76 of its 89 seconds sitting in
	// four of these waits. Six leaves a threefold margin over the slowest
	// crossing ever observed.
	verdictWait = 6 * time.Second

	// ignoredWait is what the first click gets instead, because the first
	// click is not answered at all — see settleBeforeClick. Waiting eighteen
	// seconds for a verdict on a click that was never taken is fourteen
	// seconds of every crossing spent on nothing.
	ignoredWait = 4 * time.Second

	// instances is how many challenges to answer before giving up. A challenge
	// that has refused will go on refusing however many times its checkbox is
	// clicked: measured, a run clicked the same accepted-elsewhere coordinate
	// twice more and was refused both times. What gets a different answer is a
	// different challenge, which is what reloading asks for.
	instances = 2

	// settleBeforeClick is how long the widget is given between being drawn and
	// being clicked. It is drawn before it is listening: measured over twelve
	// crossings, the first click was ignored every single time and the second
	// crossed in 1.8s — with the coordinates swapped between runs, so it was
	// never the aim. Waiting here costs three seconds; not waiting costs the
	// eighteen it takes to tell an ignored click from a refused one.
	settleBeforeClick = 3 * time.Second

	// checkboxInset is where the checkbox sits from the left edge of the
	// element hosting the shadow root, in CSS pixels — the same thirty as the
	// widget postern renders itself, which is worth knowing: the interstitial
	// is a different product but it draws the same checkbox in the same place.
	//
	// Measured, and the measurement is the point: reading the offset off a
	// screenshot by eye gave 41, every crossing then took two clicks and
	// eighteen seconds of waiting out the first, and 30 crossed in 1.8s every
	// time.
	checkboxInset = 30

	// widgetWait is how long the checkbox is given to be drawn. The challenge
	// page arrives before the script that draws its widget, and a run that
	// asked once and gave up sat out the whole budget in front of a checkbox
	// that appeared a second later.
	widgetWait = 12 * time.Second

	// minimumInstance is the least time another challenge is worth starting
	// in: the settle, a first click that is never answered, and one verdict.
	// With less than that on the clock it can only end the way it began,
	// having spent what the widget still needs.
	minimumInstance = settleBeforeClick + ignoredWait + verdictWait
)

// errCrossing marks a failure to get past a challenge standing in front of the
// site. A solve reports its own failures as a widget that would not install,
// which this is not — the widget was never reached.
var errCrossing = errors.New("the challenge in front of the page")

// checkboxAlternates is where to click, in order. Twice at the vendor's own
// inset: the first click is never answered, the second is the real one.
//
// It used to carry 41 and 55 after those two, kept on the theory that an inset
// is a layout detail worth hedging. It is not, and the hedge was expensive:
// measured on a refused crossing, those two attempts clicked at 553,338 and
// 567,338 — beside a checkbox that had already been clicked correctly at
// 542,338 — and cost 38 seconds of verdict waiting between them. Both numbers
// came from the eye-measurement above, which was wrong. A challenge that will
// not take a correct click wants a different challenge, not a different pixel.
var checkboxAlternates = []float64{checkboxInset, checkboxInset}

// interstitialScript reports whether this page is a challenge rather than the
// site, and where its checkbox is.
//
// The challenge platform script is the tell. A title in the local language is
// not — "Un instant…", "Just a moment...", one per language Cloudflare speaks —
// and neither is any id or class on the page: they are regenerated per request
// (`div#mZiFs3` on one load, something else on the next).
const interstitialScript = `(() => {
  if (!document.querySelector('script[src*="/cdn-cgi/challenge-platform/"]')) {
    return JSON.stringify({ challenge: false });
  }

  // The host of a closed shadow root cannot be recognised by what is inside it
  // — nothing can see inside — but by the absence of anything: a box the size
  // of a widget carrying no text at all. Its wrappers share its box to the
  // pixel, so the deepest one, which is the last in document order, is the one.
  const hosts = [...document.querySelectorAll('body *')].filter(el => {
    if (el.textContent.trim()) return false;
    const r = el.getBoundingClientRect();
    return r.width >= 150 && r.height >= 40 && r.height <= 120;
  });
  if (!hosts.length) return JSON.stringify({ challenge: true });

  const r = hosts[hosts.length - 1].getBoundingClientRect();
  return JSON.stringify({ challenge: true, x: r.x, y: r.y, w: r.width, h: r.height });
})()`

// interstitial is what the page said about itself.
type interstitial struct {
	Challenge bool    `json:"challenge"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	W         float64 `json:"w"`
	H         float64 `json:"h"`
}

// look asks the page whether it is a challenge and where its checkbox is.
func look(ctx context.Context) (interstitial, error) {
	var raw string
	if err := chromedp.Run(ctx, chromedp.Evaluate(interstitialScript, &raw)); err != nil {
		// Mid-navigation, or no document yet. Not a challenge, and not a
		// failure worth ending a solve over.
		return interstitial{}, nil
	}

	var page interstitial
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		return interstitial{}, fmt.Errorf("solver: read interstitial: %w", err)
	}
	return page, nil
}

// left reports how long the crossing may spend, which is its own budget or
// whatever the solve has left, whichever is shorter.
//
// A solve carries one deadline for everything it does, and crossing happens
// before the widget is even on the page. Helping itself to a fixed budget here
// meant a single refused challenge could eat a whole default -timeout and
// report a widget that would not install, which is not what happened.
func left(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return interstitialBudget
	}
	return min(time.Until(deadline), interstitialBudget)
}

// cross gets past a challenge standing between us and the target page. It
// returns with the site loaded, or with an error naming what it was still
// looking at.
//
// A page that is not a challenge returns immediately, which is every page on
// every site that does not do this.
func cross(ctx context.Context, log *slog.Logger) error {
	crossed := 0
	for instance := range instances {
		if instance > 0 {
			// This challenge has refused. Clicking it again gets the same
			// answer, so ask for another one — a reload is a fresh challenge
			// with a fresh decision behind it. Only if there is time: what is
			// left belongs to the widget that still has to be solved.
			if left(ctx) < minimumInstance {
				break
			}
			log.Info("asking for another challenge", "refused", instance)
			if err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
				return fmt.Errorf("solver: reload the challenge: %w", err)
			}
		}

		done, err := answer(ctx, log)
		if err != nil {
			// Including the deadline running out mid-crossing, which is the
			// common way this fails and which arrives here as a bare context
			// error. Left unmarked it was reported as a widget that would not
			// install — measured on a live run, and the whole reason the
			// caller cannot simply trust the wrapping it does itself.
			return fmt.Errorf("solver: %w: %w", errCrossing, err)
		}
		if done {
			return nil
		}
		crossed++
	}

	if left(ctx) < minimumInstance {
		return fmt.Errorf("solver: %w ran out of the solve's own budget after %d "+
			"attempt(s). Crossing costs about 13s when it goes well and the whole "+
			"budget when it does not, and the widget still has to be solved "+
			"afterwards — raise -timeout for a site behind a managed challenge",
			errCrossing, crossed)
	}

	return fmt.Errorf("solver: %w never let us through — it took the click and put its "+
		"checkbox back, over %d challenges. That is the challenge refusing this browser "+
		"or this address rather than missing the click. A residential address is the "+
		"lever here; measured, the same code crossed on the second click from another one",
		errCrossing, crossed)
}

// answer deals with one challenge. It reports whether the site is now loaded.
func answer(ctx context.Context, log *slog.Logger) (bool, error) {
	page, err := look(ctx)
	if err != nil {
		return false, err
	}
	if !page.Challenge {
		return true, nil
	}

	// The checkbox is not on screen the instant the challenge page is: it is
	// drawn by a script that has to arrive first. Measured, a run that asked
	// once and immediately concluded there was nothing to click sat out the
	// whole budget in front of a widget that appeared a second later.
	drawn := min(widgetWait, left(ctx))
	for waited := time.Duration(0); page.W == 0 && waited < drawn; waited += 500 * time.Millisecond {
		if err := input.Pause(ctx, 500, 500); err != nil {
			return false, err
		}
		if page, err = look(ctx); err != nil {
			return false, err
		}
		if !page.Challenge {
			return true, nil
		}
	}

	if page.W == 0 {
		// A challenge that solves itself without asking. It has nothing on
		// screen to click, so the only thing to do is let it finish.
		log.Info("a challenge stands in front of the page, with nothing to click")
		return true, settle(ctx, log)
	}

	// The widget is on screen; that does not mean it is listening yet.
	if err := input.Pause(ctx, int(settleBeforeClick.Milliseconds()),
		int(settleBeforeClick.Milliseconds())); err != nil {
		return false, err
	}

	// The aim is kept, only the inset varies, and the vendor's own thirty goes
	// first. Looking again between attempts was worse than useless: with the
	// widget mid-redraw the same rule picks some other empty box on the page,
	// and a measured run spent its third attempt clicking at 55,181 — the
	// corner of the header, nowhere near a challenge.
	deadline := time.Now().Add(left(ctx))
	for attempt, inset := range checkboxAlternates {
		if time.Now().After(deadline) {
			break
		}

		target := input.Point{X: page.X + inset, Y: page.Y + page.H/2}
		// Come from outside the widget, so the pointer covers real ground
		// rather than materialising on what it is about to click.
		from := input.Point{X: page.X + page.W + 160, Y: page.Y + page.H + 140}

		log.Info("crossing the challenge in front of the page",
			"at", fmt.Sprintf("%.0f,%.0f", target.X, target.Y))
		if err := chromedp.Run(ctx, input.Click(from, target)); err != nil {
			return false, fmt.Errorf("solver: click the challenge: %w", err)
		}

		// The first click of a challenge is never answered, so waiting out a
		// verdict on it is time spent on nothing.
		wait := verdictWait
		if attempt == 0 {
			wait = ignoredWait
		}
		crossed, err := waitOut(ctx, wait)
		if err != nil {
			return false, err
		}
		if crossed {
			log.Info("the challenge let us through")
			return true, nil
		}
	}

	// Refused. The caller decides whether another challenge is worth asking for.
	return false, nil
}

// settle waits for a challenge to finish deciding on its own.
func settle(ctx context.Context, log *slog.Logger) error {
	crossed, err := waitOut(ctx, left(ctx))
	if err != nil {
		return err
	}
	if !crossed {
		return fmt.Errorf("solver: %w had nothing to click and never resolved", errCrossing)
	}
	log.Info("the challenge let us through")
	return nil
}

// waitOut reports whether the challenge is gone before the budget runs out.
func waitOut(ctx context.Context, budget time.Duration) (bool, error) {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if err := input.Pause(ctx, 500, 500); err != nil {
			return false, err
		}
		page, err := look(ctx)
		if err != nil {
			return false, err
		}
		if !page.Challenge {
			return true, nil
		}
	}
	return false, nil
}
