package solver

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/browser"
)

// interactiveKey is Cloudflare's dummy sitekey that always forces a visible,
// interactive challenge — the case the click path exists for.
const interactiveKey = "3x00000000000000000000FF"

// TestStateScriptReadsHiddenField covers the fallback: Turnstile writes the
// token into a hidden field next to the widget as well as passing it to the
// callback, and a token in the field with no callback must not read as failure.
func TestStateScriptReadsHiddenField(t *testing.T) {
	if testing.Short() {
		t.Skip("launches a browser")
	}

	tabCtx, done := newTab(t)
	defer done()

	const plant = `(() => {
  document.body.innerHTML =
    '<div id="postern-widget"><input name="cf-turnstile-response" value="token-from-field"></div>';
  return true;
})()`

	var planted bool
	var s state
	if err := chromedp.Run(tabCtx,
		chromedp.Navigate("about:blank"),
		chromedp.Evaluate(plant, &planted),
		chromedp.Evaluate(stateScript("cf-turnstile-response"), &s),
	); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if s.Token != "token-from-field" {
		t.Errorf("token read as %q, want it picked up from the hidden field", s.Token)
	}
}

// TestClickTargetIsResolvable checks that once the widget is up there is
// something to aim at, wide enough for the checkbox offset to land inside it.
//
// It does not assert which of the two targets is used. Dummy sitekeys render no
// iframe at all — just the container and a hidden field — so the iframe branch
// only ever runs against a production sitekey, and asserting on it here would
// be asserting on a case this test cannot reach. Needs network: the widget
// script is loaded from challenges.cloudflare.com.
func TestClickTargetIsResolvable(t *testing.T) {
	if testing.Short() {
		t.Skip("launches a browser and needs network")
	}

	tabCtx, done := newTab(t)
	defer done()

	bootstrap, err := turnstileBootstrap(Request{URL: "https://example.com", SiteKey: interactiveKey})
	if err != nil {
		t.Fatalf("render script: %v", err)
	}
	if err := chromedp.Run(tabCtx,
		chromedp.Navigate("https://example.com"),
		chromedp.Evaluate(bootstrap, nil),
	); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var box *rect
		if err := chromedp.Run(tabCtx, chromedp.Evaluate(rectScript("challenges.cloudflare.com"), &box)); err != nil || box == nil || box.W == 0 {
			time.Sleep(300 * time.Millisecond)
			continue
		}

		t.Logf("aiming at %.0fx%.0f (cloudflare iframe: %t)", box.W, box.H, box.Iframe)
		if box.W <= checkboxOffsetX {
			t.Errorf("target is %.0f wide, too narrow for a checkbox at x+%d", box.W, checkboxOffsetX)
		}
		return
	}

	t.Fatal("widget never produced anything to click")
}

// newTab launches a headless browser and opens one tab, cleaning both up.
func newTab(t *testing.T) (context.Context, func()) {
	t.Helper()

	dir, err := os.MkdirTemp("", "postern-test-profile-")
	if err != nil {
		t.Fatalf("temp profile: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)

	b, err := browser.Launch(ctx, browser.Options{
		Headless:     true,
		UserDataDir:  dir,
		ScreenWidth:  1920,
		ScreenHeight: 1080,
	})
	if err != nil {
		cancel()
		t.Fatalf("launch: %v", err)
	}

	tabCtx, closeTab, err := b.NewTab()
	if err != nil {
		b.Close()
		cancel()
		t.Fatalf("new tab: %v", err)
	}

	return tabCtx, func() {
		closeTab()
		b.Close()
		cancel()
		_ = os.RemoveAll(dir)
	}
}
