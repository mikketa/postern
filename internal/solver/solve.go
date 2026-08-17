// Package solver drives a browser tab until a challenge hands out a token.
//
// The loop is the same whichever vendor is involved: navigate to the target
// page so the origin is right, render a widget of our own with the site's key,
// wait, click if the widget is waiting to be clicked, and read the token back.
// What differs per vendor lives in provider.go and the bootstrap functions.
package solver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/browser"
	"github.com/mikketa/postern/internal/challenge"
	"github.com/mikketa/postern/internal/input"
)

const (
	// pollInterval is how often we read the token slot back out of the page.
	pollInterval = 200 * time.Millisecond

	// interactiveAfter is how long we let the widget settle before assuming it
	// wants a click. Non-interactive challenges resolve well inside this, so
	// waiting costs nothing and avoids clicking at a widget still laying out.
	interactiveAfter = 3 * time.Second

	// checkboxOffsetX is the distance from the widget's left edge to the middle
	// of its checkbox, in CSS pixels. Both vendors put it in the same place,
	// and neither lets us read into the frame to find it.
	checkboxOffsetX = 30

	// maxClicks bounds the retries. A challenge that has swallowed three
	// clicks is not going to yield to a fourth.
	maxClicks = 3

	// panelAfter is how long to wait before treating an open panel as a real
	// challenge, for a widget with nothing to click. It sits well past
	// interactiveAfter so an ordinary verification has had its chance to
	// complete first.
	panelAfter = 9 * time.Second

	// panelAfterClick is the same wait for a widget that had to be ticked, and
	// it is measured from the tick rather than from the navigation. A grid can
	// only exist because something asked for one, so that is the moment to
	// start counting from — and a panel with no tiles in it is not returned at
	// all, which is what the long absolute wait was really guarding against.
	//
	// Measured over three solves: the checkbox was ticked at 3.9-4.1s and the
	// grid was already up, but nothing looked at it until the ninth second.
	// Five seconds a solve, spent watching a challenge that was on screen.
	panelAfterClick = 1200 * time.Millisecond

	// maxPanels bounds how many grids we answer in one solve. reCAPTCHA will
	// hand out fresh ones indefinitely to a client it does not believe, and
	// dynamic grids legitimately take several rounds, so this is not tight.
	maxPanels = 5

	// clickRetryAfter is the wait before clicking again. The vendor takes a
	// moment to process a click, and clicking through that looks like a bot
	// mashing the box.
	clickRetryAfter = 8 * time.Second

	// navigateAttempts and navigateRetryAfter cover a navigation that failed
	// for a reason having nothing to do with the page.
	navigateAttempts   = 3
	navigateRetryAfter = 500 * time.Millisecond

	// gradedRounds is how many picture grids are worth answering before
	// concluding that the answers are not what is being judged. Twelve, from
	// twenty measured runs: every token came from a run that finished in six
	// or twelve rounds, and none at all from the thirteen runs that took some
	// other number — correct answers past that point were handed another grid
	// just the same.
	gradedRounds = 12

	// maxResets bounds how many times an expired challenge is started over.
	// Two, because the remaining budget is what really limits this — a reset
	// costs a fresh checkbox and whatever the widget serves next, and if that
	// keeps expiring the timeout is the honest answer.
	maxResets = 2
)

// errExpired is reCAPTCHA telling us the challenge outlived its session, which
// is a thing to start over rather than a thing to report.
const errExpired = "expired"

// transientNavigation is Chrome swapping its certificate verifier out from
// under a request. Nothing about the page caused it and nothing about the page
// fixes it; asking again does.
const transientNavigation = "ERR_CERT_VERIFIER_CHANGED"

// resetScript clears the widget back to an unticked checkbox. The bootstrap
// installs the function; a page rendered before it existed says so by being
// undefined, and the caller reports the original error instead.
const resetScript = `typeof window.__posternReset === 'function' && window.__posternReset()`

// Request describes one challenge to solve.
type Request struct {
	// Kind selects the vendor and mode. Empty means Turnstile.
	Kind Kind

	// URL is the page the widget belongs to. It decides the origin the vendor
	// sees, so it has to be the real target page, not a blank tab.
	URL string

	// SiteKey is the widget's sitekey, as found in the target page markup.
	SiteKey string

	// Action is the optional action label. Turnstile and reCAPTCHA v3 both
	// take one, and a token obtained without the one the site uses will be
	// rejected on validation.
	Action string

	// CData is Turnstile's optional customer data field.
	CData string

	// ImageSolver is the command that answers picture grids. Empty means
	// picture challenges are reported rather than attempted; see
	// internal/challenge for what the command receives and must print.
	ImageSolver string

	// SavePanels, when set, is a directory to keep every picture grid in, as
	// the PNG the solver was given and a JSON of what postern read off the
	// panel. A corpus of those is what lets a solver be calibrated offline,
	// and labelled, what lets a head be fitted for a category no model has a
	// class for.
	SavePanels string

	// Log, when set, records what the challenge did and what was answered.
	// Picture challenges are otherwise unreadable from the outside: a solve
	// either produces a token or it does not, and nothing says which prompt
	// came up, what the solver made of it, or what the panel objected to.
	Log *slog.Logger
}

// logger is the request's logger, or one that discards.
func (r Request) logger() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Result is a solved challenge.
type Result struct {
	Token   string
	Elapsed time.Duration
}

// state mirrors the window.__postern slot we poll.
type state struct {
	Token string `json:"token"`
	Error string `json:"error"`
}

// Solve opens a tab on req.URL, renders the widget itself, and waits for the
// token. Rendering our own widget rather than hunting the page's one keeps this
// independent of how the target site lays out its form.
func Solve(ctx context.Context, b *browser.Browser, req Request, timeout time.Duration) (*Result, error) {
	if req.URL == "" || req.SiteKey == "" {
		return nil, errors.New("solver: url and sitekey are required")
	}

	p, err := lookup(req.Kind)
	if err != nil {
		return nil, err
	}
	log := req.logger()

	bootstrap, err := p.bootstrap(req)
	if err != nil {
		return nil, err
	}

	tabCtx, closeTab, err := b.NewTab()
	if err != nil {
		return nil, err
	}
	defer closeTab()

	tabCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()

	start := time.Now()
	if err := open(tabCtx, req.URL, bootstrap); err != nil {
		return nil, fmt.Errorf("solver: bootstrap widget: %w", err)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// One finder for the tab: reaching a challenge frame in another process
	// means attaching to it, and that attachment is worth keeping.
	panels := challenge.NewFinder()

	clicks := 0
	// answers is what the solver said about each saved grid, kept until the
	// challenge ends. Only a run that produces a token gets to keep them: see
	// recordAnswers.
	answers := map[string][]int{}
	attempts := 0
	// Whether the vendor ever rendered its own frame. A widget that solves
	// itself never needs to, so this is not a failure on its own — but at the
	// end of a solve that produced nothing, it is the difference between "the
	// challenge beat us" and "there was never anything on screen to answer".
	sawVendorFrame := false
	resets := 0
	rounds := 0
	var lastClick time.Time

	for {
		select {
		case <-tabCtx.Done():
			// A parent cancellation is not the same failure as running out of
			// time on the challenge; only the latter is worth reporting as one.
			if errors.Is(context.Cause(tabCtx), context.DeadlineExceeded) {
				if clicks > 0 && !sawVendorFrame {
					return nil, fmt.Errorf("solver: no token after %s — the widget was clicked "+
						"%d times but %s never rendered its own frame, so there was nothing on "+
						"screen to answer. A sitekey that forces an interactive challenge does "+
						"this, and so does a vendor script the page would not load",
						timeout, clicks, req.kindOrDefault())
				}
				return nil, fmt.Errorf("solver: no token after %s", timeout)
			}
			return nil, tabCtx.Err()

		case <-ticker.C:
			var s state
			// The slot is undefined until the bootstrap runs, and evaluating it
			// then fails; that is expected, so keep polling.
			if err := chromedp.Run(tabCtx, chromedp.Evaluate(stateScript(p.tokenField), &s)); err != nil {
				continue
			}
			if s.Error != "" {
				// An expired session is not a refusal. reCAPTCHA gives a
				// challenge a couple of minutes, and a grid answered over
				// several rounds can outlive that — the page's own remedy is
				// the "please try again" the widget offers, which is a reset
				// and a fresh checkbox. Do that instead of reporting a failure
				// the browser has not actually hit.
				if s.Error == errExpired && resets < maxResets {
					var ok bool
					if err := chromedp.Run(tabCtx, chromedp.Evaluate(resetScript, &ok)); err != nil || !ok {
						return nil, fmt.Errorf("solver: %s error %s%s",
							req.kindOrDefault(), s.Error, hint(req.kindOrDefault(), s.Error))
					}
					resets++
					clicks, attempts = 0, 0
					lastClick = time.Time{}
					log.Info("the challenge expired, starting it over", "resets", resets)
					continue
				}
				return nil, fmt.Errorf("solver: %s error %s%s",
					req.kindOrDefault(), s.Error, hint(req.kindOrDefault(), s.Error))
			}
			if s.Token != "" {
				log.Info("token", "after", time.Since(start).Round(time.Millisecond),
					"challenges", attempts)
				// The token grades what was ticked, and only that. Measured on
				// a live run: a bus challenge produced a token with a school
				// bus sitting unticked in square 7 of two rounds, so reCAPTCHA
				// accepts an incomplete answer often enough to matter. Ticked
				// and accepted is therefore solid; not ticked means nothing at
				// all, and writing it down as "no bus here" would teach a head
				// that a school bus is not a bus. See recordAnswers.
				if err := recordAnswers(req.SavePanels, answers); err != nil {
					log.Info("could not write labels", "err", err)
				}
				return &Result{Token: s.Token, Elapsed: time.Since(start)}, nil
			}

			// A panel with tiles in it is a real challenge. reCAPTCHA keeps an
			// empty one around for every widget and flashes it open during
			// ordinary verifications too, so its mere presence proves nothing.
			if p.images && panelDue(p, start, lastClick) && attempts < maxPanels {
				panel, err := panels.Find(tabCtx)
				if err != nil {
					continue
				}
				if panel != nil {
					if req.ImageSolver == "" {
						return nil, errors.New("solver: an image challenge was served and no " +
							"image solver is configured — see -image-solver in the README")
					}

					attempts++
					done, err := challenge.Solve(tabCtx, panel, challenge.Options{
						Solver:     req.ImageSolver,
						SavePanels: req.SavePanels,
						OnAnswer: func(saved string, tiles []int) {
							answers[saved] = tiles
						},
					}, log)
					rounds += done
					if errors.Is(err, context.DeadlineExceeded) {
						// Running out of time mid-challenge is the same failure
						// as running out of time waiting, and reads better said
						// the same way.
						return nil, fmt.Errorf("solver: no token after %s", timeout)
					}
					if err != nil {
						return nil, fmt.Errorf("solver: %w", err)
					}

					// Past a certain point reCAPTCHA is not grading answers any
					// more. Measured over twenty runs: every token came from a
					// run that finished in six or twelve rounds, and not one
					// came from a longer one — correct answers got another grid
					// just the same. Carrying on spends the rest of the budget
					// to arrive at the same place, and reports "no token" for
					// something that was never about the answers.
					if rounds > gradedRounds {
						return nil, fmt.Errorf("solver: %d picture grids and still asking — "+
							"this address or profile is what is being refused, not the answers. "+
							"A profile with history, or an IP that is not a datacenter, is the "+
							"lever here; see the README on reputation", rounds)
					}

					// The verdict is not ours to read: a right answer produces
					// a token on the next poll, a wrong one produces another
					// grid, and either way the loop finds out.
					lastClick = time.Now()
				}
			}

			// An interactive challenge sits there until someone ticks the box.
			// A failed attempt usually means the widget has not been laid out
			// yet, so leave the counter alone and try again on the next tick.
			if p.clickable && readyToClick(start, lastClick, clicks) {
				if err := clickCheckbox(tabCtx, p.frameHost); err != nil {
					log.Debug("cannot click the checkbox yet", "why", err)
				} else {
					clicks++
					lastClick = time.Now()
					var box *rect
					if err := chromedp.Run(tabCtx,
						chromedp.Evaluate(rectScript(p.frameHost), &box)); err != nil || box == nil {
						box = &rect{}
					}
					log.Info("ticked the checkbox", "clicks", clicks,
						"after", time.Since(start).Round(time.Millisecond),
						"widget", fmt.Sprintf("%.0fx%.0f at %.0f,%.0f iframe=%v",
							box.W, box.H, box.X, box.Y, box.Iframe))
				}
			}
		}
	}
}

// open navigates to the page and installs the widget, retrying a navigation
// Chrome itself considers worth retrying.
//
// ERR_CERT_VERIFIER_CHANGED is Chrome reconfiguring certificate verification
// underneath a request — it happens on a cold profile, it has nothing to do
// with the page, and the documented remedy is to ask again. It cost a measured
// run for no reason at all.
func open(ctx context.Context, url, bootstrap string) error {
	var err error
	for attempt := range navigateAttempts {
		if attempt > 0 {
			if err := sleep(ctx, navigateRetryAfter); err != nil {
				return err
			}
		}
		err = chromedp.Run(ctx,
			chromedp.Navigate(url),
			chromedp.Evaluate(bootstrap, nil),
		)
		if err == nil || !strings.Contains(err.Error(), transientNavigation) {
			return err
		}
	}
	return err
}

// sleep waits, or gives up if the caller has.
func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// hint adds what a bare error code does not say. These cost nothing to carry
// and save the reader an hour: the code below sent this author looking for a
// content security policy for twenty minutes, when the real answer was that
// api.js refuses to serve a v3 loader for a v2 key.
func hint(kind Kind, code string) string {
	if kind == RecaptchaV3 && code == "api-script-blocked" {
		return " — reCAPTCHA refuses api.js?render= for a key that is not a v3 key, " +
			"so check that the sitekey is really a v3 one"
	}
	return ""
}

// kindOrDefault names the challenge for error messages.
func (r Request) kindOrDefault() Kind {
	if r.Kind == "" {
		return defaultKindValue
	}
	return r.Kind
}

// panelDue decides whether this tick should go looking for a picture grid.
//
// A widget that has to be ticked cannot serve one before it is ticked, so the
// wait runs from the tick. A widget with nothing to click asks for itself, and
// there is no moment to hang the wait on but the navigation.
func panelDue(p provider, start, lastClick time.Time) bool {
	if !p.clickable {
		return time.Since(start) >= panelAfter
	}
	return !lastClick.IsZero() && time.Since(lastClick) >= panelAfterClick
}

// readyToClick decides whether this tick should click: never before the widget
// has had its chance to solve itself, never more than maxClicks times, and
// never twice in quick succession.
func readyToClick(start, lastClick time.Time, clicks int) bool {
	if clicks >= maxClicks || time.Since(start) < interactiveAfter {
		return false
	}
	return clicks == 0 || time.Since(lastClick) >= clickRetryAfter
}

// stateScript reads the token slot, falling back to the hidden input the vendor
// writes alongside the callback. The callback is the documented path, but a
// widget that filled the field without firing it would otherwise look to us
// exactly like a widget that solved nothing.
func stateScript(tokenField string) string {
	if tokenField == "" {
		return `(() => window.__postern || { token: '', error: '' })()`
	}

	return fmt.Sprintf(`(() => {
  const s = window.__postern || { token: '', error: '' };
  if (s.token || s.error) return s;

  const field = document.querySelector('#postern-widget [name="%s"]');
  return { token: (field && field.value) || '', error: '' };
})()`, tokenField)
}

// rect is the widget's box in viewport coordinates.
type rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`

	// Iframe reports whether this is the vendor's own frame or our container
	// standing in for it.
	Iframe bool `json:"iframe"`
}

// rectScript locates what to aim at: the vendor's iframe if it is there, and
// our own container otherwise. Preferring the iframe means the click follows
// the widget if its position or size ever changes; the container is only a
// fallback for the window where the frame has not been inserted yet.
func rectScript(frameHost string) string {
	return fmt.Sprintf(`(() => {
  const host = document.querySelector('#postern-widget');
  if (!host) return null;

  for (const frame of host.querySelectorAll('iframe')) {
    const src = frame.src || frame.getAttribute('src') || '';
    if (!src.includes(%q)) continue;

    const r = frame.getBoundingClientRect();
    if (r.width > 50 && r.height > 20) {
      return { x: r.x, y: r.y, w: r.width, h: r.height, iframe: true };
    }
  }

  const r = host.getBoundingClientRect();
  return { x: r.x, y: r.y, w: r.width, h: r.height, iframe: false };
})()`, frameHost)
}

// clickCheckbox aims at the widget's checkbox and clicks it like a hand would.
func clickCheckbox(ctx context.Context, frameHost string) error {
	var box *rect
	if err := chromedp.Run(ctx, chromedp.Evaluate(rectScript(frameHost), &box)); err != nil {
		return err
	}
	if box == nil || box.W == 0 || box.H == 0 {
		return errors.New("solver: widget not laid out yet")
	}

	// Aim for the middle of the checkbox, but not for the same pixel every
	// time — nobody hits a target dead centre twice.
	target := input.Point{
		X: box.X + checkboxOffsetX + (rand.Float64()-0.5)*6,
		Y: box.Y + box.H/2 + (rand.Float64()-0.5)*6,
	}

	// Start somewhere below and to the right, so the pointer covers real
	// ground instead of materialising next to the target.
	origin := input.Point{
		X: box.X + box.W + 120 + rand.Float64()*200,
		Y: box.Y + box.H + 100 + rand.Float64()*180,
	}

	return chromedp.Run(ctx, input.Click(origin, target))
}

// hostSetup is the JavaScript every bootstrap runs first: it adds our container
// to the page at a known position, away from the edges so it never lands under
// a scrollbar, and above the site's own furniture so no cookie banner can
// swallow the click meant for the widget.
//
// It is centred vertically because of what opens next to it. reCAPTCHA sizes
// its picture panel to the room around the widget, and a widget pinned near the
// top of the window gets a panel squeezed to 480 pixels for a document that
// needs 530 — with the verify button in the 50 that were cut off, unreachable
// by any pointer. Given the middle of the window the panel opens at its full
// height and the button is simply there.
//
// The stacking order is a compromise rather than a maximum. reCAPTCHA opens its
// picture grid at a z-index in the billions; a container pinned to the very top
// sits in front of that grid, hiding the prompt an image solver needs to read
// and taking clicks meant for the tiles.
//
// It adds rather than replaces. Wiping the document with innerHTML also worked
// for Turnstile, but it silently broke reCAPTCHA v3: execute() needs the
// elements the API quietly created for itself, and clearing the page takes
// them with it. Leaving the page intact is also the more honest position —
// the widget then sits in the document it claims to belong to.
const hostSetup = `
  const previous = document.getElementById('postern-widget');
  if (previous) previous.remove();

  const host = document.createElement('div');
  host.id = 'postern-widget';
  host.style.cssText =
    'position:fixed;left:60px;top:50%;transform:translateY(-50%);z-index:999999';
  document.body.appendChild(host);
`

// recordAnswers adds this run's accepted answers to answers.json beside the
// panels.
//
// Deliberately not labels.json, and the difference is the whole point. A label
// file says what is in every square: the ones listed hold the thing and the
// rest do not. This file cannot say that. It is written only when a token was
// produced, so every square in it was ticked and accepted — but reCAPTCHA hands
// over tokens for incomplete answers, measured on a live run where a school bus
// sat unticked through two rounds of a bus challenge that passed. Treating the
// squares it does not mention as empty would be teaching a head the opposite of
// the truth.
//
// So: positives only, and no claim about anything else. examples/train-probe.py
// takes it with --answers, which adds the positives without inventing the
// negatives. A fleet left running still builds its own training set that way —
// the categories it can already answer pay for the ones it cannot — it is just
// a set of one-sided examples rather than a labelled corpus.
func recordAnswers(dir string, answers map[string][]int) error {
	if dir == "" || len(answers) == 0 {
		return nil
	}

	path := filepath.Join(dir, "answers.json")
	labels := map[string][]int{}
	if body, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(body, &labels); err != nil {
			return fmt.Errorf("solver: %s is not readable: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	for panel, tiles := range answers {
		// A round where nothing was ticked carries no information here: it
		// says the solver saw nothing, not that there was nothing. Only a
		// hand-checked label can say that, so it is left out entirely.
		if len(tiles) == 0 {
			continue
		}
		labels[panel] = tiles
	}
	if len(labels) == 0 {
		return nil
	}

	body, err := json.MarshalIndent(labels, "", " ")
	if err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
