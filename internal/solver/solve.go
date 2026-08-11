// Package solver drives a browser tab until a challenge hands out a token.
//
// The loop is the same whichever vendor is involved: navigate to the target
// page so the origin is right, render a widget of our own with the site's key,
// wait, click if the widget is waiting to be clicked, and read the token back.
// What differs per vendor lives in provider.go and the bootstrap functions.
package solver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
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
	// challenge. It sits well past interactiveAfter so an ordinary
	// verification has had its chance to complete first.
	panelAfter = 9 * time.Second

	// maxPanels bounds how many grids we answer in one solve. reCAPTCHA will
	// hand out fresh ones indefinitely to a client it does not believe, and
	// dynamic grids legitimately take several rounds, so this is not tight.
	maxPanels = 5

	// clickRetryAfter is the wait before clicking again. The vendor takes a
	// moment to process a click, and clicking through that looks like a bot
	// mashing the box.
	clickRetryAfter = 8 * time.Second
)

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
	if err := chromedp.Run(tabCtx,
		chromedp.Navigate(req.URL),
		chromedp.Evaluate(bootstrap, nil),
	); err != nil {
		return nil, fmt.Errorf("solver: bootstrap widget: %w", err)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	clicks := 0
	attempts := 0
	var lastClick time.Time

	for {
		select {
		case <-tabCtx.Done():
			// A parent cancellation is not the same failure as running out of
			// time on the challenge; only the latter is worth reporting as one.
			if errors.Is(context.Cause(tabCtx), context.DeadlineExceeded) {
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
				return nil, fmt.Errorf("solver: %s error %s%s",
					req.kindOrDefault(), s.Error, hint(req.kindOrDefault(), s.Error))
			}
			if s.Token != "" {
				log.Info("token", "after", time.Since(start).Round(time.Millisecond),
					"challenges", attempts)
				return &Result{Token: s.Token, Elapsed: time.Since(start)}, nil
			}

			// A panel with tiles in it is a real challenge. reCAPTCHA keeps an
			// empty one around for every widget and flashes it open during
			// ordinary verifications too, so its mere presence proves nothing.
			if p.images && time.Since(start) >= panelAfter && attempts < maxPanels {
				panel, err := challenge.Find(tabCtx)
				if err != nil {
					continue
				}
				if panel != nil {
					if req.ImageSolver == "" {
						panel.Close()
						return nil, errors.New("solver: an image challenge was served and no " +
							"image solver is configured — see -image-solver in the README")
					}

					attempts++
					err := challenge.Solve(tabCtx, panel, req.ImageSolver, log)
					panel.Close()
					if errors.Is(err, context.DeadlineExceeded) {
						// Running out of time mid-challenge is the same failure
						// as running out of time waiting, and reads better said
						// the same way.
						return nil, fmt.Errorf("solver: no token after %s", timeout)
					}
					if err != nil {
						return nil, fmt.Errorf("solver: %w", err)
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
				if err := clickCheckbox(tabCtx, p.frameHost); err == nil {
					clicks++
					lastClick = time.Now()
				}
			}
		}
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
