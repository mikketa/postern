package solver

import (
	"context"
	"strings"
	"sync"

	cdplog "github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// What the browser said, kept for the moment a widget fails without saying why.
//
// A vendor's error callback fires with no argument. Everything that would
// explain it — a rejected origin, a blocked request, a script that threw — is
// written to the console and to the browser's own log, and then thrown away
// because nobody was listening. This listens.

// consoleKeep bounds what is held. A page left running writes a great deal
// that has nothing to do with us, and an error message that quotes fifty lines
// of somebody's analytics is not a better error message.
const consoleKeep = 12

// watcher collects console output and browser log entries for one tab.
type watcher struct {
	mu   sync.Mutex
	seen []string
}

// watchConsole starts collecting on a tab. It is best-effort: a browser that
// will not enable the log domain is not a reason to fail a solve that might
// otherwise work.
func watchConsole(ctx context.Context) *watcher {
	w := &watcher{}

	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			if e.Type != "error" && e.Type != "warning" {
				return
			}
			w.add(strings.TrimSpace(joinArgs(e.Args)))

		case *runtime.EventExceptionThrown:
			if e.ExceptionDetails != nil {
				w.add(e.ExceptionDetails.Text)
			}

		// The browser's own log, which carries what no page script sees: a
		// refused origin, a blocked subresource, a failed request.
		case *cdplog.EventEntryAdded:
			if e.Entry == nil || (e.Entry.Level != "error" && e.Entry.Level != "warning") {
				return
			}
			w.add(strings.TrimSpace(e.Entry.Text + " " + e.Entry.URL))
		}
	})

	// Enabled in the background: chromedp.Run inside a listener would deadlock,
	// and a failure here only costs the diagnosis, never the solve.
	go func() {
		_ = chromedp.Run(ctx, cdplog.Enable(), runtime.Enable())
	}()
	return w
}

// add records one line, ignoring blanks and repeats.
func (w *watcher) add(line string) {
	if line == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.seen) >= consoleKeep {
		return
	}
	for _, s := range w.seen {
		if s == line {
			return
		}
	}
	w.seen = append(w.seen, line)
}

// telling returns the lines that look like they explain a captcha failure,
// most useful first.
//
// Filtered rather than dumped whole: the point is to turn an unexplained
// failure into a sentence someone can act on, and a page's unrelated warnings
// bury that instead of carrying it.
func (w *watcher) telling() []string {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	var out []string
	for _, s := range w.seen {
		l := strings.ToLower(s)
		if strings.Contains(l, "recaptcha") || strings.Contains(l, "captcha") ||
			strings.Contains(l, "site key") || strings.Contains(l, "sitekey") ||
			strings.Contains(l, "invalid domain") || strings.Contains(l, "turnstile") ||
			strings.Contains(l, "blocked") || strings.Contains(l, "refused") {
			out = append(out, s)
		}
	}
	return out
}

// said renders the collected explanation, or nothing.
func (w *watcher) said() string {
	lines := w.telling()
	if len(lines) == 0 {
		return ""
	}
	return ". The browser said: " + strings.Join(lines, " | ")
}

// joinArgs flattens console arguments into one line.
func joinArgs(args []*runtime.RemoteObject) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		switch {
		case a == nil:
		case len(a.Value) > 0:
			parts = append(parts, strings.Trim(string(a.Value), `"`))
		case a.Description != "":
			parts = append(parts, a.Description)
		}
	}
	return strings.Join(parts, " ")
}
