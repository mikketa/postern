// Package browser launches and holds a real Chrome instance.
//
// The whole premise of Postern is that the least detectable browser is an
// actual browser: a normal Chrome binary, a persistent profile that ages like
// a human's, and as few automation flags as we can get away with. Nothing here
// tries to emulate Chrome — it drives it.
//
// Headless is the default, which costs a few things a windowed browser gets for
// free. Every one of them is corrected here rather than papered over in
// JavaScript: the user agent stops announcing HeadlessChrome, the GPU is turned
// back on so WebGL reports the real adapter instead of SwiftShader, and the
// window is given a size an actual monitor might have.
package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/patches"
)

// headlessMarker is what Chrome puts in its user agent when running headless.
const headlessMarker = "HeadlessChrome/"

// chromeUIHeight is roughly what the tab strip and address bar cost a desktop
// Chrome window. Headless draws no browser UI at all, so without subtracting
// it the viewport would be exactly as tall as the screen — something that only
// happens in fullscreen, and one of the cheapest headless tells going.
const chromeUIHeight = 111

// Options configures the Chrome instance Postern drives.
type Options struct {
	// UserDataDir is the Chrome profile directory. Keeping it across runs is
	// what makes the browser look lived-in; an empty value means a throwaway
	// profile, which is measurably worse.
	UserDataDir string

	// Headless runs without a window. On by default; see the package comment
	// for what that costs and how it is paid back.
	Headless bool

	// ScreenWidth and ScreenHeight size the browser window. Headless Chrome
	// otherwise reports an 800x600 screen, a size nobody has had in years.
	ScreenWidth  int
	ScreenHeight int

	// ExecPath overrides Chrome autodetection.
	ExecPath string

	// Proxy is passed to --proxy-server, e.g. "http://user:pass@host:port".
	Proxy string
}

// Browser owns a Chrome process and hands out tabs.
type Browser struct {
	allocCtx context.Context
	cancel   context.CancelFunc

	// width and height are the screen size, remembered so every tab gets a
	// window sized from it, not just the one Chrome opens at startup.
	width  int
	height int

	// mu guards the user agent lookup, which happens once on the first tab and
	// is reused by every tab after it.
	mu         sync.Mutex
	uaResolved bool
	uaOverride string
}

// Launch starts Chrome. The process lives until Close is called or ctx is done.
func Launch(ctx context.Context, opts Options) (*Browser, error) {
	flags := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	flags = append(flags,
		// Drops the CDP-injected properties Blink would otherwise expose.
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		// chromedp enables this by default; it is the "Chrome is being
		// controlled by automated test software" bit.
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("headless", opts.Headless),
	)

	if opts.Headless {
		// Headless falls back to software rendering, and SwiftShader in the
		// WebGL renderer string is not something a desktop ever reports. On a
		// machine with no usable GPU Chrome falls back on its own anyway, so
		// this is safe to ask for everywhere.
		flags = append(flags, chromedp.Flag("enable-gpu", true))
	}

	if opts.ScreenWidth > 0 && opts.ScreenHeight > 0 {
		if opts.Headless {
			// There is no monitor to inherit from, so Chrome invents an 800x600
			// one and --window-size does not change it. This does.
			flags = append(flags, chromedp.Flag("screen-info",
				fmt.Sprintf("{%dx%d}", opts.ScreenWidth, opts.ScreenHeight)))
		}
		flags = append(flags, chromedp.WindowSize(opts.ScreenWidth, opts.ScreenHeight-chromeUIHeight))
	}
	if opts.UserDataDir != "" {
		flags = append(flags, chromedp.UserDataDir(opts.UserDataDir))
	}
	if opts.ExecPath != "" {
		flags = append(flags, chromedp.ExecPath(opts.ExecPath))
	}
	if opts.Proxy != "" {
		flags = append(flags, chromedp.ProxyServer(opts.Proxy))
	}

	allocCtx, cancel := chromedp.NewExecAllocator(ctx, flags...)
	return &Browser{
		allocCtx: allocCtx,
		cancel:   cancel,
		width:    opts.ScreenWidth,
		height:   opts.ScreenHeight,
	}, nil
}

// NewTab opens a tab with the user agent corrected and every patch installed,
// and returns it along with the function that closes it. Callers own the
// cancel func.
func (b *Browser) NewTab() (context.Context, context.CancelFunc, error) {
	if b.allocCtx == nil {
		return nil, nil, errors.New("browser: not launched")
	}

	tabCtx, cancel := chromedp.NewContext(b.allocCtx)
	if err := chromedp.Run(tabCtx, b.hideHeadless(), b.sizeWindow(), installPatches()); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("browser: open tab: %w", err)
	}
	return tabCtx, cancel, nil
}

// sizeWindow resizes the tab's window after the fact.
//
// --window-size only sizes the window Chrome opens at startup; tabs created
// over CDP get their own, and headless defaults those to 800x600. Resizing the
// window rather than overriding device metrics keeps us out of Chrome's device
// emulation mode, which brings its own inconsistencies to be spotted.
func (b *Browser) sizeWindow() chromedp.ActionFunc {
	return func(ctx context.Context) error {
		if b.width <= 0 || b.height <= 0 {
			return nil
		}

		targetID := chromedp.FromContext(ctx).Target.TargetID
		windowID, _, err := cdpbrowser.GetWindowForTarget().WithTargetID(targetID).Do(ctx)
		if err != nil {
			return fmt.Errorf("browser: find window: %w", err)
		}

		bounds := &cdpbrowser.Bounds{
			Width:       int64(b.width),
			Height:      int64(b.height - chromeUIHeight),
			WindowState: cdpbrowser.WindowStateNormal,
		}
		if err := cdpbrowser.SetWindowBounds(windowID, bounds).Do(ctx); err != nil {
			return fmt.Errorf("browser: resize window: %w", err)
		}
		return nil
	}
}

// Close terminates Chrome.
func (b *Browser) Close() {
	if b.cancel != nil {
		b.cancel()
	}
}

// hideHeadless strips the HeadlessChrome token from the user agent.
//
// The replacement is derived from whatever this Chrome actually reports rather
// than hardcoded, so it stays correct across browser updates. Client hints are
// left alone: Chrome already reports plain "Google Chrome" there even headless,
// so overriding the user agent is what makes the two agree.
func (b *Browser) hideHeadless() chromedp.ActionFunc {
	return func(ctx context.Context) error {
		b.mu.Lock()
		if !b.uaResolved {
			_, _, _, ua, _, err := cdpbrowser.GetVersion().Do(ctx)
			if err != nil {
				b.mu.Unlock()
				return fmt.Errorf("browser: read user agent: %w", err)
			}
			if strings.Contains(ua, headlessMarker) {
				b.uaOverride = strings.Replace(ua, headlessMarker, "Chrome/", 1)
			}
			b.uaResolved = true
		}
		override := b.uaOverride
		b.mu.Unlock()

		// Windowed Chrome has nothing to hide, and an override that changes
		// nothing is one more thing that can go wrong.
		if override == "" {
			return nil
		}
		return emulation.SetUserAgentOverride(override).Do(ctx)
	}
}

// installPatches registers every embedded script to run before page scripts do.
func installPatches() chromedp.ActionFunc {
	return func(ctx context.Context) error {
		scripts, err := patches.All()
		if err != nil {
			return fmt.Errorf("browser: read patches: %w", err)
		}
		for _, s := range scripts {
			if _, err := page.AddScriptToEvaluateOnNewDocument(s.Source).Do(ctx); err != nil {
				return fmt.Errorf("browser: install patch %s: %w", s.Name, err)
			}
		}
		return nil
	}
}
