package solver

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/challenge"
	"github.com/mikketa/postern/internal/input"
)

// TestVerifyReachesTheButton checks the one step that cannot be seen from
// outside: whether submitting an answer does anything at all.
//
// It deliberately ignores whether the answer is right. A right answer and a
// wrong one both make the panel move — a fresh grid, a complaint, or a closed
// panel — while a submission that never reached the button leaves the grid
// exactly as it was, ticks and all. That is the failure this covers, and it is
// invisible in a solve log: an unanswered challenge and a wrongly answered one
// look identical from the outside.
func TestVerifyReachesTheButton(t *testing.T) {
	if testing.Short() {
		t.Skip("launches a browser and needs network")
	}

	tabCtx, done := newVirtualTab(t)
	defer done()

	bootstrap, err := recaptchaV2Bootstrap(Request{URL: demoV2URL, SiteKey: demoV2Key})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
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
	for i := 0; i < 25 && panel == nil; i++ {
		time.Sleep(time.Second)
		panel, _ = panels.Find(tabCtx)
	}
	if panel == nil {
		t.Skip("no picture challenge was served this run")
	}

	t.Logf("panel %.0fx%.0f, document %.0f tall, button %+v",
		panel.View.Width, panel.View.Height, panel.View.DocHeight, panel.View.Button)

	// Tick one tile, so there is something to submit.
	tile := panel.View.Tiles[0]
	x, y := tile.Center()
	px, py := panel.Point(x, y)
	origin := input.Point{X: px + panel.View.Width, Y: py + panel.View.Height}
	if err := chromedp.Run(tabCtx, input.Click(origin, input.Point{X: px, Y: py})); err != nil {
		t.Fatalf("click tile: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)

	if err := panel.Reread(tabCtx); err != nil {
		t.Fatalf("reread: %v", err)
	}
	before := panel.View.Pictures()

	if err := challenge.Verify(tabCtx, panel, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))); err != nil {
		t.Fatalf("verify: %v", err)
	}

	for i := 0; i < 12; i++ {
		time.Sleep(time.Second)
		if err := panel.Reread(tabCtx); err != nil {
			t.Fatalf("reread: %v", err)
		}
		if !panel.Open() {
			t.Log("panel closed: the answer was accepted")
			return
		}
		if panel.View.Notice != "" {
			t.Logf("panel answered: %q", panel.View.Notice)
			return
		}
		if !samePictures(before, panel.View.Pictures()) {
			t.Log("grid changed: the submission was taken")
			return
		}
	}

	t.Fatalf("the grid is unchanged %ds after verify: the button was never "+
		"activated (panel %.0f tall, button at y=%.0f)",
		12, panel.View.Height, panel.View.Button.Y)
}

func samePictures(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
