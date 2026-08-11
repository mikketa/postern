package browser_test

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/browser"
)

// cdpProbe is the published way to notice an attached inspector: when the
// Runtime domain is enabled, anything passed to console.* gets serialised for
// the debugger, and serialising an Error reads its stack. A page that never
// opens devtools sees that getter stay untouched.
//
// On Chrome 151 it does not fire even with Runtime explicitly enabled — the
// object is handed over by reference and only serialised if a frontend asks
// for it, so the getter is never reached. The test therefore skips rather than
// passes: this measures nothing today, and reporting it as a pass would be
// claiming an all-clear that was never established. Left in place as a
// starting point for a probe that does work.
const cdpProbe = `(() => {
  let read = false;
  const err = new Error();
  Object.defineProperty(err, 'stack', {
    configurable: false,
    enumerable: false,
    get() { read = true; return ''; },
  });
  console.debug(err);
  window.__cdpDetected = read;
})()`

func TestCDPDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("launches a browser")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	b, err := browser.Launch(ctx, browser.Options{
		Headless:     true,
		UserDataDir:  profileDir(t),
		ScreenWidth:  1920,
		ScreenHeight: 1080,
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer b.Close()

	tabCtx, closeTab, err := b.NewTab()
	if err != nil {
		t.Fatalf("new tab: %v", err)
	}
	defer closeTab()

	var detected bool
	if err := chromedp.Run(tabCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(cdpProbe).Do(ctx)
			return err
		}),
		chromedp.Navigate("about:blank"),
		chromedp.Evaluate(`window.__cdpDetected === true`, &detected),
	); err != nil {
		t.Fatalf("probe: %v", err)
	}

	// Positive control. A negative result only means something if the same
	// probe fires when the Runtime domain is definitely enabled — otherwise it
	// could just as well be a broken probe.
	var detectedWithRuntime bool
	if err := chromedp.Run(tabCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			return runtime.Enable().Do(ctx)
		}),
		chromedp.Navigate("about:blank"),
		chromedp.Evaluate(`window.__cdpDetected === true`, &detectedWithRuntime),
	); err != nil {
		t.Fatalf("control: %v", err)
	}

	t.Logf("detectable as we run:            %t", detected)
	t.Logf("detectable with Runtime enabled: %t", detectedWithRuntime)

	if !detectedWithRuntime {
		t.Skip("probe does not fire even with Runtime enabled — it proves nothing here")
	}
	if detected {
		t.Error("the page can tell an inspector is attached")
	}
}
