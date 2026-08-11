package solver

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/challenge"
)

// TestChallengeIsReadableFromTheInside asks Google's demo for a real picture
// challenge and checks that postern can measure it rather than guess at it.
// Everything the image path depends on is in this one reading: the prompt as
// text, a grid of a known size, and a button with a position.
//
// It needs the network and a live challenge, so it is skipped in short mode.
// reCAPTCHA occasionally waves a fresh browser through without a challenge; a
// run that never sees a panel says so rather than failing.
func TestChallengeIsReadableFromTheInside(t *testing.T) {
	if testing.Short() {
		t.Skip("launches a browser and needs network")
	}

	tabCtx, done := newVirtualTab(t)
	defer done()

	bootstrap, err := recaptchaV2Bootstrap(Request{URL: demoV2URL, SiteKey: demoV2Key})
	if err != nil {
		t.Fatalf("bootstrap script: %v", err)
	}
	if err := chromedp.Run(tabCtx,
		chromedp.Navigate(demoV2URL),
		chromedp.Evaluate(bootstrap, nil),
	); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	time.Sleep(interactiveAfter)
	if err := clickCheckbox(tabCtx, "google.com/recaptcha"); err != nil {
		t.Fatalf("click checkbox: %v", err)
	}

	panels := challenge.NewFinder()
	var panel *challenge.Frame
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if panel, _ = panels.Find(tabCtx); panel != nil {
			break
		}
		time.Sleep(time.Second)
	}
	if panel == nil {
		t.Skip("no picture challenge was served this run")
	}

	view := panel.View
	t.Logf("prompt %q, %d tiles, button %+v", view.Prompt, len(view.Tiles), view.Button)

	if view.Prompt == "" {
		t.Error("no prompt: an image solver would be left OCR-ing the screenshot")
	}
	if view.Columns() == 0 {
		t.Errorf("%d tiles is neither a 3x3 nor a 4x4 grid", len(view.Tiles))
	}
	if view.Button == nil {
		t.Fatal("no verify button: the challenge could be answered but never submitted")
	}

	// The button has to be inside the panel, or no pointer can reach it.
	if bottom := view.Button.Y + view.Button.H; bottom > view.Height {
		t.Errorf("button ends at y=%.0f, past the panel's %.0f", bottom, view.Height)
	}

	// And the panel has to be on screen, or the screenshot is of nothing.
	x, y, w, h := panel.Viewport()
	if w <= 0 || h <= 0 || x < 0 || y < 0 {
		t.Errorf("panel sits at %.0f,%.0f %.0fx%.0f", x, y, w, h)
	}
}
