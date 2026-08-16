package challenge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// hostPage stands in for a site that carries its own reCAPTCHA beside the one
// postern renders. Both widgets own a bframe, both bframes have the same src,
// and reCAPTCHA parks the closed one off the top of the page — which is the
// shape that made postern click at negative coordinates.
const hostPage = `<!doctype html>
<title>deux widgets</title>
<body style="margin:0">
<iframe id="parked" src="%[1]s"
        style="position:absolute;left:1px;top:-9999px;visibility:hidden"
        width="300" height="150"></iframe>
<iframe id="deployed" src="%[1]s"
        style="position:absolute;left:112px;top:150px;visibility:visible"
        width="400" height="580"></iframe>
</body>`

// TestHostPositionPrefersTheDeployedFrame is a regression test with a measured
// origin: before it, hostPosition answered the parked frame's -9999 and every
// click of the round was dispatched off screen.
func TestHostPositionPrefersTheDeployedFrame(t *testing.T) {
	if testing.Short() {
		t.Skip("launches a browser")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	frameURL, pageURL, stop := servePage(t)
	defer stop()

	tabCtx, closeTab := headlessTab(ctx, t)
	defer closeTab()

	if err := chromedp.Run(tabCtx, chromedp.Navigate(pageURL)); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	x, y, deployed, err := hostPosition(tabCtx, frameURL)
	if err != nil {
		t.Fatalf("host position: %v", err)
	}

	// The deployed frame's corner, not the parked one's.
	if x != 112 || y != 150 {
		t.Errorf("origin = %v,%v, want 112,150 — the parked frame sits at 1,-9999", x, y)
	}
	if !deployed {
		t.Error("deployed = false, want true: one of the two frames is on screen")
	}
}

// TestHostPositionFallsBackWhenEveryFrameIsParked keeps the fallback honest:
// between rounds reCAPTCHA parks them all, and a stale origin is still better
// than reporting no panel.
func TestHostPositionFallsBackWhenEveryFrameIsParked(t *testing.T) {
	if testing.Short() {
		t.Skip("launches a browser")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	frameURL, _, stop := servePage(t)
	defer stop()

	tabCtx, closeTab := headlessTab(ctx, t)
	defer closeTab()

	parked := fmt.Sprintf(`<!doctype html><body style="margin:0">
<iframe src=%q style="position:absolute;left:7px;top:-9999px;visibility:hidden"
        width="300" height="150"></iframe></body>`, frameURL)

	if err := chromedp.Run(tabCtx,
		chromedp.Navigate("about:blank"),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return chromedp.Evaluate(fmt.Sprintf(
				`document.write(%q); document.close(); true`, parked), nil).Do(ctx)
		}),
	); err != nil {
		t.Fatalf("write page: %v", err)
	}

	x, y, deployed, err := hostPosition(tabCtx, frameURL)
	if err != nil {
		t.Fatalf("host position: %v", err)
	}
	if x != 7 || y != -9999 {
		t.Errorf("origin = %v,%v, want the only frame at 7,-9999", x, y)
	}

	// And the caller has to be told, because a click aimed here goes nowhere.
	if deployed {
		t.Error("deployed = true, want false: every frame on the page is parked")
	}
}

// TestParkedPanelIsNotOpen separates the two questions that used to be one. The
// panel's own document is identical whether or not reCAPTCHA has moved the
// iframe off screen, so only the outside measurement can tell, and clicking
// while parked is what sent the pointer to y=-9448.
func TestParkedPanelIsNotOpen(t *testing.T) {
	live := View{
		Height: 580,
		Tiles:  []Box{{W: 100, H: 100}, {W: 100, H: 100}},
	}

	deployed := &Frame{View: live}
	if !deployed.Open() {
		t.Error("a panel on screen with tiles should be open")
	}

	parked := &Frame{View: live, Parked: true}
	if parked.Open() {
		t.Error("a parked panel reports the same view from the inside, but nothing " +
			"can be clicked in it")
	}
	if !parked.hasPanel() {
		t.Error("hasPanel should still see the tiles: that is how Reread knows to " +
			"measure the frame again instead of giving up on it")
	}
}

// servePage returns the url a fake bframe answers on, the page embedding two of
// them, and a stop function.
func servePage(t *testing.T) (frameURL, pageURL string, stop func()) {
	t.Helper()

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)

	// The marker postern looks for is in the path, so the stand-in has to carry
	// it too.
	frameURL = server.URL + frameMarker + "?k=test"
	mux.HandleFunc(frameMarker, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<!doctype html><body style="margin:0;background:#eee">`)
	})
	mux.HandleFunc("/host", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, hostPage, frameURL)
	})

	return frameURL, server.URL + "/host", server.Close
}

// headlessTab opens a browser for one test.
func headlessTab(ctx context.Context, t *testing.T) (context.Context, func()) {
	t.Helper()

	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.Flag("headless", true))

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	tabCtx, cancelTab := chromedp.NewContext(allocCtx)

	if err := chromedp.Run(tabCtx); err != nil {
		cancelTab()
		cancelAlloc()
		t.Skipf("no browser to test against: %v", err)
	}

	return tabCtx, func() { cancelTab(); cancelAlloc() }
}
