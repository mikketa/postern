package browser_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/browser"
)

// probeJS reads back the properties headless Chrome is known to get wrong.
const probeJS = `(() => {
  let renderer = 'unavailable';
  try {
    const gl = document.createElement('canvas').getContext('webgl2');
    const dbg = gl && gl.getExtension('WEBGL_debug_renderer_info');
    if (dbg) renderer = gl.getParameter(dbg.UNMASKED_RENDERER_WEBGL);
  } catch (e) {
    renderer = 'error: ' + e;
  }
  return {
    userAgent: navigator.userAgent,
    webdriver: navigator.webdriver === true,
    screenWidth: screen.width,
    screenHeight: screen.height,
    innerWidth: innerWidth,
    innerHeight: innerHeight,
    renderer: renderer,
  };
})()`

type fingerprint struct {
	UserAgent    string `json:"userAgent"`
	Webdriver    bool   `json:"webdriver"`
	ScreenWidth  int    `json:"screenWidth"`
	ScreenHeight int    `json:"screenHeight"`
	InnerWidth   int    `json:"innerWidth"`
	InnerHeight  int    `json:"innerHeight"`
	Renderer     string `json:"renderer"`
}

const (
	testScreenWidth  = 1920
	testScreenHeight = 1080
)

// TestHeadlessFingerprint pins down what a headless tab looks like from inside
// the page. Every assertion here corresponds to something that was measurably
// wrong before it was fixed — treat a failure as a regression, not as a flake.
func TestHeadlessFingerprint(t *testing.T) {
	if testing.Short() {
		t.Skip("launches a browser")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	b, err := browser.Launch(ctx, browser.Options{
		Headless:     true,
		UserDataDir:  profileDir(t),
		ScreenWidth:  testScreenWidth,
		ScreenHeight: testScreenHeight,
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

	var fp fingerprint
	if err := chromedp.Run(tabCtx,
		chromedp.Navigate("about:blank"),
		chromedp.Evaluate(probeJS, &fp),
	); err != nil {
		t.Fatalf("probe: %v", err)
	}

	t.Logf("user agent: %s", fp.UserAgent)
	t.Logf("screen:     %dx%d", fp.ScreenWidth, fp.ScreenHeight)
	t.Logf("viewport:   %dx%d", fp.InnerWidth, fp.InnerHeight)
	t.Logf("renderer:   %s", fp.Renderer)

	if strings.Contains(fp.UserAgent, "Headless") {
		t.Errorf("user agent still announces headless: %s", fp.UserAgent)
	}
	if fp.Webdriver {
		t.Error("navigator.webdriver is true")
	}
	if fp.ScreenWidth != testScreenWidth || fp.ScreenHeight != testScreenHeight {
		t.Errorf("screen is %dx%d, want %dx%d",
			fp.ScreenWidth, fp.ScreenHeight, testScreenWidth, testScreenHeight)
	}
	if fp.InnerHeight <= 0 || fp.InnerHeight >= fp.ScreenHeight {
		t.Errorf("viewport height %d should sit below the screen height %d: a window "+
			"as tall as the screen means no browser UI at all",
			fp.InnerHeight, fp.ScreenHeight)
	}

	// Software rendering is expected on a machine without a usable GPU, which
	// includes most CI runners, so this reports rather than fails.
	if strings.Contains(fp.Renderer, "SwiftShader") {
		t.Logf("WARNING: software rendering — WebGL reports SwiftShader, which no desktop does")
	}
}

// profileDir returns a throwaway profile directory. t.TempDir is not used
// because Chrome is still flushing the profile when the test ends, and its
// cleanup would fail the test for it.
func profileDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "postern-test-profile-")
	if err != nil {
		t.Fatalf("temp profile: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
