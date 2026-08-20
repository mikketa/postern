package solver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/chromedp/chromedp"
)

// Reading the widget off the page, so that a caller does not have to.
//
// A sitekey is not a secret and it is not configuration: it is written in the
// markup of the page the caller already named. Asking for it anyway pushes the
// work of finding it onto every integration, and gets it wrong the day a site
// rotates its key.
//
// This runs after the crossing and before the widget is installed, which is
// the only moment the real page is on screen and untouched by us.

const (
	// detectWait bounds how long to keep looking. Widgets are usually rendered
	// by a script that arrives after the document, so the first look is often
	// too early; past this the page simply has no widget on it.
	detectWait = 10 * time.Second

	// detectEvery is how often to look again while waiting.
	detectEvery = 250 * time.Millisecond
)

// found is what the page says about its own widget.
type found struct {
	Kind Kind   `json:"kind"`
	Key  string `json:"key"`
	How  string `json:"how"`
}

// detectScript reads the widget out of the live document.
//
// Three sources, in order of how much they can be trusted. An explicit
// data-sitekey is what the site author wrote. An iframe's own URL is what the
// vendor's script decided to load, which is right even when the markup is
// generated. Both beat guessing from the shape of the key, which is only used
// when nothing else says which vendor it is.
const detectScript = `(() => {
  const hit = (kind, key, how) => key ? { kind, key, how } : null;

  // Turnstile keys begin 0x, reCAPTCHA keys begin 6L. Only consulted when the
  // markup around the key does not say which vendor rendered it.
  const byShape = k => k.startsWith('0x') ? 'turnstile' : 'recaptcha-v2';

  for (const el of document.querySelectorAll('[data-sitekey]')) {
    const key = el.getAttribute('data-sitekey');
    if (!key) continue;
    const around = [el.className, el.id, el.parentElement?.className]
      .filter(Boolean).join(' ');
    if (/turnstile/i.test(around)) return hit('turnstile', key, 'data-sitekey');
    if (/recaptcha/i.test(around)) {
      const invisible = el.getAttribute('data-size') === 'invisible';
      return hit(invisible ? 'recaptcha-v2-invisible' : 'recaptcha-v2', key, 'data-sitekey');
    }
    return hit(byShape(key), key, 'data-sitekey');
  }

  // The anchor frame carries the key as ?k=.
  for (const f of document.querySelectorAll('iframe[src*="recaptcha"]')) {
    const m = (f.src || '').match(/[?&]k=([^&]+)/);
    if (m) return hit('recaptcha-v2', decodeURIComponent(m[1]), 'recaptcha iframe');
  }

  // Turnstile puts the key in a path segment rather than a query parameter.
  for (const f of document.querySelectorAll('iframe[src*="challenges.cloudflare.com"]')) {
    const m = (f.src || '').match(/\/(0x[A-Za-z0-9_-]{10,})\//);
    if (m) return hit('turnstile', m[1], 'turnstile iframe');
  }

  return null;
})()`

// detect looks for a widget on the page already open in ctx.
//
// It waits, because a widget is nearly always rendered by a script that lands
// after the document does: looking once would report "no widget" on most of
// the pages that have one.
func detect(ctx context.Context, log *slog.Logger) (found, error) {
	deadline := time.Now().Add(detectWait)
	for {
		var raw json.RawMessage
		if err := chromedp.Run(ctx, chromedp.Evaluate(detectScript, &raw)); err != nil {
			return found{}, fmt.Errorf("solver: %w: reading the page for a widget: %w",
				ErrInvalidRequest, err)
		}

		var f found
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &f); err != nil {
				return found{}, fmt.Errorf("solver: %w: %w", ErrInvalidRequest, err)
			}
			if f.Key != "" {
				log.Info("found the widget on the page",
					"kind", f.Kind, "sitekey", f.Key, "how", f.How)
				return f, nil
			}
		}

		if time.Now().After(deadline) {
			// Named for what the caller can do about it. A page with no widget
			// is the ordinary case for a wrong url, and -sitekey is the way
			// past a widget this cannot see.
			return found{}, fmt.Errorf("solver: %w: no captcha widget found on the "+
				"page after %s. Pass the sitekey explicitly if the widget is "+
				"rendered in a way this cannot read", ErrInvalidRequest, detectWait)
		}
		if err := sleep(ctx, detectEvery); err != nil {
			return found{}, err
		}
	}
}
