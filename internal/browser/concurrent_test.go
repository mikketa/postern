package browser_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/browser"
)

// TestConcurrentTabsAreVisible covers two failures that no unit test saw and
// that only showed up under load against a live service.
//
// The first: tabs used to be derived from the allocator, so every one of them
// started its own Chrome, found the profile locked, handed over to the running
// instance and died — every concurrent solve failed.
//
// The second: tabs in a shared window are backgrounded, and a backgrounded page
// is not painted, so its widget never runs. Most concurrent solves timed out
// while the foreground one succeeded. Hence one window per tab, and hence this
// assertion on visibilityState, which is exactly what a backgrounded tab gets
// wrong.
func TestConcurrentTabsAreVisible(t *testing.T) {
	if testing.Short() {
		t.Skip("launches a browser")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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

	const tabs = 3

	var wg sync.WaitGroup
	states := make([]string, tabs)
	errs := make([]error, tabs)

	for i := range tabs {
		wg.Add(1)
		go func() {
			defer wg.Done()

			tabCtx, closeTab, err := b.NewTab()
			if err != nil {
				errs[i] = err
				return
			}
			defer closeTab()

			errs[i] = chromedp.Run(tabCtx,
				chromedp.Navigate("about:blank"),
				chromedp.Evaluate(`document.visibilityState`, &states[i]),
			)
		}()
	}
	wg.Wait()

	for i := range tabs {
		if errs[i] != nil {
			t.Errorf("tab %d: %v", i, errs[i])
			continue
		}
		if states[i] != "visible" {
			t.Errorf("tab %d is %q, want visible: a hidden page is not painted and its widget never runs",
				i, states[i])
		}
	}
}
