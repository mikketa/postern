// Package solver drives a browser tab until Cloudflare hands out a token.
package solver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/browser"
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
	// of its checkbox, in CSS pixels. We cannot read into the iframe to find
	// the box, but its internal layout does not move.
	checkboxOffsetX = 30

	// maxClicks bounds the retries. A challenge that has swallowed three
	// clicks is not going to yield to a fourth.
	maxClicks = 3

	// clickRetryAfter is the wait before clicking again. Cloudflare takes a
	// moment to process a click, and clicking through that looks like a bot
	// mashing the box.
	clickRetryAfter = 8 * time.Second
)

// Request describes one challenge to solve.
type Request struct {
	// URL is the page the widget belongs to. It decides the origin Cloudflare
	// sees, so it has to be the real target page, not a blank tab.
	URL string

	// SiteKey is the widget's sitekey, as found in the target page markup.
	SiteKey string

	// Action and CData are the optional Turnstile parameters. When the target
	// site sets them, a token obtained without them will be rejected.
	Action string
	CData  string
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

	tabCtx, closeTab, err := b.NewTab()
	if err != nil {
		return nil, err
	}
	defer closeTab()

	tabCtx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()

	bootstrap, err := renderScript(req)
	if err != nil {
		return nil, err
	}

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
			if err := chromedp.Run(tabCtx, chromedp.Evaluate(stateScript, &s)); err != nil {
				continue
			}
			if s.Error != "" {
				return nil, fmt.Errorf("solver: turnstile error %s", s.Error)
			}
			if s.Token != "" {
				return &Result{Token: s.Token, Elapsed: time.Since(start)}, nil
			}

			// An interactive challenge sits there until someone ticks the box.
			// A failed attempt usually means the widget has not been laid out
			// yet, so leave the counter alone and try again on the next tick.
			if readyToClick(start, lastClick, clicks) {
				if err := clickCheckbox(tabCtx); err == nil {
					clicks++
					lastClick = time.Now()
				}
			}
		}
	}
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

// stateScript reads the token slot, falling back to the hidden input Turnstile
// writes alongside the callback. The callback is the documented path, but a
// widget that filled the field without firing it would otherwise look to us
// exactly like a widget that solved nothing.
const stateScript = `(() => {
  const s = window.__postern || { token: '', error: '' };
  if (s.token || s.error) return s;

  const field = document.querySelector('#postern-widget [name="cf-turnstile-response"]');
  return { token: (field && field.value) || '', error: '' };
})()`

// rect is the widget's box in viewport coordinates.
type rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`

	// Iframe reports whether this is Cloudflare's own frame or our container
	// standing in for it.
	Iframe bool `json:"iframe"`
}

// rectScript locates what to aim at: Cloudflare's iframe if it is there, and
// our own container otherwise. Preferring the iframe means the click follows
// the widget if its position or size ever changes; the container is only a
// fallback for the window where the frame has not been inserted yet.
const rectScript = `(() => {
  const host = document.querySelector('#postern-widget');
  if (!host) return null;

  for (const frame of host.querySelectorAll('iframe')) {
    const src = frame.src || frame.getAttribute('src') || '';
    if (!src.includes('challenges.cloudflare.com')) continue;

    const r = frame.getBoundingClientRect();
    if (r.width > 50 && r.height > 20) {
      return { x: r.x, y: r.y, w: r.width, h: r.height, iframe: true };
    }
  }

  const r = host.getBoundingClientRect();
  return { x: r.x, y: r.y, w: r.width, h: r.height, iframe: false };
})()`

// clickCheckbox aims at the widget's checkbox and clicks it like a hand would.
func clickCheckbox(ctx context.Context) error {
	var box *rect
	if err := chromedp.Run(ctx, chromedp.Evaluate(rectScript, &box)); err != nil {
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

// renderScript builds the in-page bootstrap: it wipes the document, drops a
// widget in, and parks the result on window.__postern for us to poll.
func renderScript(req Request) (string, error) {
	params := map[string]string{"sitekey": req.SiteKey}
	if req.Action != "" {
		params["action"] = req.Action
	}
	if req.CData != "" {
		params["cData"] = req.CData
	}

	encoded, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("solver: encode widget params: %w", err)
	}

	return fmt.Sprintf(`(() => {
  window.__postern = { token: '', error: '' };
  // Placed at a fixed spot so the checkbox is at a known viewport coordinate,
  // and away from the edges so it never lands under a scrollbar.
  document.documentElement.innerHTML =
    '<head></head><body><div id="postern-widget" style="position:fixed;left:60px;top:90px"></div></body>';

  window.__posternRender = () => {
    window.turnstile.render('#postern-widget', Object.assign(%s, {
      callback: (token) => { window.__postern.token = token; },
      'error-callback': (code) => { window.__postern.error = String(code || 'unknown'); },
    }));
  };

  // render=explicit keeps the API from rendering anything it finds on the
  // page; the only widget we want is the one __posternRender puts up.
  const script = document.createElement('script');
  script.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?onload=__posternRender&render=explicit';
  script.async = true;
  script.onerror = () => { window.__postern.error = 'api-script-blocked'; };
  document.head.appendChild(script);
})()`, encoded), nil
}
