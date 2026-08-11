// Package solver drives a browser tab until Cloudflare hands out a token.
package solver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/browser"
)

// pollInterval is how often we read the token slot back out of the page.
const pollInterval = 200 * time.Millisecond

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
			if err := chromedp.Run(tabCtx, chromedp.Evaluate(`window.__postern`, &s)); err != nil {
				continue
			}
			if s.Error != "" {
				return nil, fmt.Errorf("solver: turnstile error %s", s.Error)
			}
			if s.Token != "" {
				return &Result{Token: s.Token, Elapsed: time.Since(start)}, nil
			}
		}
	}
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
  document.documentElement.innerHTML = '<head></head><body><div id="postern-widget"></div></body>';

  window.__posternRender = () => {
    window.turnstile.render('#postern-widget', Object.assign(%s, {
      callback: (token) => { window.__postern.token = token; },
      'error-callback': (code) => { window.__postern.error = String(code || 'unknown'); },
    }));
  };

  const script = document.createElement('script');
  script.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?onload=__posternRender';
  script.async = true;
  script.onerror = () => { window.__postern.error = 'api-script-blocked'; };
  document.head.appendChild(script);
})()`, encoded), nil
}
