package solver

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/input"
)

// TestClickIsBothFastAndReal covers the wait that is not worth having.
//
// Chrome acknowledges an input event only once the renderer under the pointer
// has processed it, and under a virtual display that acknowledgement can take
// seconds — measured at 43s for a single click on a page with nothing on it,
// while the same page answered every other command instantly. The event itself
// is delivered when the command is sent, so postern does not wait for the
// reply. This checks that the click still lands, and still lands quickly.
func TestClickIsBothFastAndReal(t *testing.T) {
	if testing.Short() {
		t.Skip("launches a browser and needs network")
	}

	tabCtx, done := newVirtualTab(t)
	defer done()

	if err := chromedp.Run(tabCtx, chromedp.Navigate("https://example.com")); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	// The page has exactly one link.
	var link struct{ X, Y float64 }
	if err := chromedp.Run(tabCtx, chromedp.Evaluate(`(() => {
	  const r = document.querySelector('a').getBoundingClientRect();
	  return { X: r.x + r.width / 2, Y: r.y + r.height / 2 };
	})()`, &link)); err != nil {
		t.Fatalf("find link: %v", err)
	}

	start := time.Now()
	from := input.Point{X: link.X + 320, Y: link.Y + 240}
	if err := chromedp.Run(tabCtx, input.Click(from, input.Point{X: link.X, Y: link.Y})); err != nil {
		t.Fatalf("click: %v", err)
	}
	elapsed := time.Since(start)

	// A click is a pointer walking a short path with human pauses. Anything
	// past a couple of seconds is waiting on something, and a solve makes
	// dozens of them.
	if elapsed > 3*time.Second {
		t.Errorf("the click took %s, which is time spent waiting rather than clicking", elapsed)
	}

	var url string
	for i := 0; i < 10; i++ {
		time.Sleep(time.Second)
		if err := chromedp.Run(tabCtx, chromedp.Evaluate(`location.href`, &url)); err != nil {
			continue
		}
		if url != "" && url != "https://example.com/" {
			t.Logf("clicked in %s and the link followed to %s", elapsed.Round(time.Millisecond), url)
			return
		}
	}
	t.Errorf("still on %q after clicking its only link: the click did not land", url)
}
