package solver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/challenge"
)

// Google's always-passing test key, which any domain may use. It never serves
// a picture challenge, which is the point: what is under test here is whether
// the challenge frame can be reached at all on somebody else's site, not what
// is inside it.
const testKey = "6LeIxAcTAAAAAJcZVRqyHh71UMIEGNQ_MXjiZKhI"

// TestChallengeFrameIsReachableAcrossOrigins covers the case that decides
// whether postern works anywhere but Google's own demo.
//
// Chrome runs a cross-site iframe in a process of its own. Such a frame is
// invisible to the page's session — it appears in the frame tree as an empty
// about:blank — and turns up instead as a separate target on the browser. On
// google.com the challenge frame is same-site and the frame tree is enough;
// everywhere else it is not, and a solver that only knew the first route would
// work on the demo and nowhere real.
func TestChallengeFrameIsReachableAcrossOrigins(t *testing.T) {
	if testing.Short() {
		t.Skip("launches a browser and needs network")
	}

	tabCtx, done := newVirtualTab(t)
	defer done()

	const host = "https://example.com/"
	bootstrap, err := recaptchaV2Bootstrap(Request{URL: host, SiteKey: testKey})
	if err != nil {
		t.Fatalf("bootstrap script: %v", err)
	}
	if err := chromedp.Run(tabCtx,
		chromedp.Navigate(host),
		chromedp.Evaluate(bootstrap, nil),
	); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	time.Sleep(8 * time.Second)

	var widget struct {
		Error  string   `json:"error"`
		Frames []string `json:"frames"`
	}
	if err := chromedp.Run(tabCtx, chromedp.Evaluate(`(() => ({
	  error: (window.__postern || {}).error || '',
	  frames: [...document.querySelectorAll('iframe')].map(f => f.src || ''),
	}))()`, &widget)); err != nil {
		t.Fatalf("page state: %v", err)
	}
	if widget.Error != "" {
		t.Fatalf("widget did not render: %s", widget.Error)
	}

	var rendered bool
	for _, src := range widget.Frames {
		rendered = rendered || strings.Contains(src, "/recaptcha/api2/bframe")
	}
	if !rendered {
		t.Skipf("no challenge frame this run: %v", widget.Frames)
	}

	// The page's own session cannot see into it...
	local := 0
	if err := chromedp.Run(tabCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		tree, err := page.GetFrameTree().Do(ctx)
		if err != nil {
			return err
		}
		for _, child := range tree.ChildFrames {
			if strings.Contains(child.Frame.URL, "/recaptcha/api2/bframe") {
				local++
			}
		}
		return nil
	})); err != nil {
		t.Fatalf("frame tree: %v", err)
	}

	// ...so it has to be reached as a target, and evaluated in from there.
	reached := 0
	if err := chromedp.Run(tabCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		list, err := target.GetTargets().Do(ctx)
		if err != nil {
			return err
		}

		for _, info := range list {
			if info.Type != "iframe" || !strings.Contains(info.URL, "/recaptcha/api2/bframe") {
				continue
			}

			// The cancel is deliberately dropped: chromedp tears an attachment
			// down by closing the target, and closing a frame's target closes
			// the page holding it. The tab context releases this one.
			frameCtx, _ := chromedp.NewContext(tabCtx, chromedp.WithTargetID(info.TargetID))
			err := chromedp.Run(frameCtx, chromedp.ActionFunc(func(fctx context.Context) error {
				tree, err := page.GetFrameTree().Do(fctx)
				if err != nil {
					return err
				}

				world, err := page.CreateIsolatedWorld(tree.Frame.ID).WithWorldName("postern").Do(fctx)
				if err != nil {
					return err
				}

				result, exception, err := runtime.Evaluate(`window.innerWidth > 0`).
					WithContextID(world).WithReturnByValue(true).Do(fctx)
				if err != nil {
					return err
				}
				if exception != nil {
					return context.Canceled
				}
				if string(result.Value) == "true" {
					reached++
				}
				return nil
			}))
			if err != nil {
				t.Logf("frame target %s: %v", info.TargetID, err)
			}
		}
		return nil
	})); err != nil {
		t.Fatalf("targets: %v", err)
	}

	t.Logf("challenge frames: %d in the page's own tree, %d reachable as targets", local, reached)
	if reached == 0 {
		t.Fatal("no cross-origin challenge frame could be evaluated in: picture " +
			"challenges would only ever work on google.com")
	}

	// And the real entry point should survive the same page without complaint.
	// It finds nothing here — the test key serves no challenge — but "nothing"
	// and "error" are different answers and only one of them is acceptable.
	if _, err := challenge.NewFinder().Find(tabCtx); err != nil {
		t.Errorf("Find: %v", err)
	}
}
