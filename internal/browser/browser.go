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
	"net/url"
	"strings"
	"sync"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
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

// shutdownTimeout bounds the polite shutdown. Chrome normally goes in well
// under a second; the point of the bound is that a browser refusing to close
// must not hold up the caller, since the kill that follows will end it anyway.
const shutdownTimeout = 5 * time.Second

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

	// Proxy is where Chrome goes out through, as "http://host:port",
	// "socks5://host:port" or a bare "host:port". A password may be given as
	// "http://user:pass@host:port": Chrome cannot take one on the command
	// line, so it is stripped off and replayed over CDP instead. Which address
	// a solve comes from is the largest single factor in whether reCAPTCHA
	// hands over a token.
	Proxy string

	// Env adds environment entries for the Chrome process, which is how a
	// windowed browser is pointed at a virtual display.
	Env []string
}

// Browser owns a Chrome process and hands out tabs.
type Browser struct {
	// browserCtx is the context Chrome itself runs under. Tabs are derived
	// from it, never from the allocator: a context taken straight off the
	// allocator starts its own Chrome, which then finds the profile already
	// locked, hands over to the running instance and exits — leaving the
	// caller with a browser that died on arrival.
	browserCtx    context.Context
	browserCancel context.CancelFunc
	allocCancel   context.CancelFunc

	// width and height are the screen size, remembered so every tab gets a
	// window sized from it, not just the one Chrome opens at startup.
	width  int
	height int

	// mu guards the user agent lookup, which happens once on the first tab and
	// is reused by every tab after it.
	mu         sync.Mutex
	uaResolved bool
	uaOverride string

	// proxyUser and proxyPass are what Chrome could not be told on the command
	// line; see splitProxy. Empty for a proxy that wants no password, and for
	// no proxy at all.
	proxyUser string
	proxyPass string
}

// splitProxy separates the address Chrome is given from the credentials it
// cannot use.
//
// --proxy-server takes no password. Chrome drops whatever is in front of the
// @, asks the proxy anyway, gets 407 back, and puts up the sign-in dialog that
// a browser nobody is sitting at will never answer — so the page hangs and the
// only sign of it is a navigation that never finishes. The credentials have to
// be replayed over CDP instead, which is what Browser holds them for. Keeping
// them out of the command line is worth something on its own: /proc is
// world-readable, so a password in a flag is a password every account on the
// machine can read.
//
// A bare "host:port" is accepted because that is the form proxy lists come in;
// without a scheme url.Parse reads the whole thing as a path.
func splitProxy(proxy string) (address, user, pass string, err error) {
	if proxy == "" {
		return "", "", "", nil
	}

	scheme := ""
	rest := proxy
	if at := strings.Index(proxy, "://"); at >= 0 {
		scheme, rest = proxy[:at+3], proxy[at+3:]
	}

	parsed, err := url.Parse("//" + rest)
	if err != nil {
		return "", "", "", fmt.Errorf("proxy %q: %w", proxy, err)
	}
	if parsed.User != nil {
		user = parsed.User.Username()
		pass, _ = parsed.User.Password()
		parsed.User = nil
	}

	return scheme + strings.TrimPrefix(parsed.String(), "//"), user, pass, nil
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

	// Nothing here tries to stop Chrome throttling the renderer, and that is a
	// measurement rather than an oversight.
	//
	// A panel that photographs as a flat white rectangle — the document has the
	// grid, every picture loaded and decoded, the pixels never painted — looks
	// exactly like a window Chrome has decided nobody is looking at, and the
	// standard answer is --disable-backgrounding-occluded-windows,
	// --disable-renderer-backgrounding and --disable-background-timer-throttling.
	// Measured over eight runs on the demo page, alternating with and without
	// them so that Google's mood drifting could not be mistaken for an effect:
	// unpainted grids per run came out 3, 7, 0 and 6 with the flags, and 7, 7, 7
	// and 0 without. The spread between runs is larger than any difference
	// between the two arms, so they bought nothing and are not carried.

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
	proxyAddress, proxyUser, proxyPass, err := splitProxy(opts.Proxy)
	if err != nil {
		return nil, fmt.Errorf("browser: %w", err)
	}
	if proxyAddress != "" {
		flags = append(flags, chromedp.ProxyServer(proxyAddress))
	}
	if len(opts.Env) > 0 {
		flags = append(flags, chromedp.Env(opts.Env...))

		// Being handed an X display means being asked to use it. Emptying
		// WAYLAND_DISPLAY is most of that, but Chrome's backend choice also
		// answers to a hint that can be set by policy or by a wrapper script,
		// and a windowed browser that lands on the operator's own desktop is
		// not a cosmetic failure: it takes their pointer and their focus.
		for _, entry := range opts.Env {
			if strings.HasPrefix(entry, "DISPLAY=") && len(entry) > len("DISPLAY=") {
				flags = append(flags, chromedp.Flag("ozone-platform", "x11"))
				break
			}
		}
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, flags...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)

	// Start Chrome here rather than on the first solve: a bad binary, a locked
	// profile or a missing display should be reported by Launch, not surface
	// later as a failed request.
	if err := chromedp.Run(browserCtx); err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("browser: start chrome: %w", err)
	}

	return &Browser{
		browserCtx:    browserCtx,
		browserCancel: browserCancel,
		allocCancel:   allocCancel,
		width:         opts.ScreenWidth,
		height:        opts.ScreenHeight,
		proxyUser:     proxyUser,
		proxyPass:     proxyPass,
	}, nil
}

// NewTab opens a tab with the user agent corrected and every patch installed,
// and returns it along with the function that closes it. Callers own the
// cancel func.
func (b *Browser) NewTab() (context.Context, context.CancelFunc, error) {
	if b.browserCtx == nil {
		return nil, nil, errors.New("browser: not launched")
	}

	// Each solve gets its own window rather than another tab in a shared one.
	// Background tabs are not painted, and a widget that never renders never
	// solves — which showed up as most concurrent requests timing out while
	// the foreground one sailed through.
	// Creating a target is a browser-level command, so it has to be addressed
	// to the browser rather than to some page's session.
	chrome := chromedp.FromContext(b.browserCtx)
	if chrome == nil || chrome.Browser == nil {
		return nil, nil, errors.New("browser: not launched")
	}
	browserExec := cdp.WithExecutor(b.browserCtx, chrome.Browser)

	targetID, err := target.CreateTarget("about:blank").WithNewWindow(true).Do(browserExec)
	if err != nil {
		return nil, nil, fmt.Errorf("browser: open window: %w", err)
	}

	tabCtx, cancel := chromedp.NewContext(b.browserCtx, chromedp.WithTargetID(targetID))

	// The window is ours to clean up. chromedp's own cancel would do it, but
	// only as a side effect of tearing the attachment down — and that same
	// behaviour is why a context attached to a *frame's* target must never be
	// cancelled: closing a frame target closes the page holding it. See
	// challenge.Finder.
	closeTab := func() {
		// The browser may already be gone, in which case there is nothing left
		// to close and the error is not interesting.
		_ = target.CloseTarget(targetID).Do(browserExec)
		cancel()
	}

	if err := b.answerProxy(tabCtx); err != nil {
		closeTab()
		return nil, nil, err
	}

	if err := chromedp.Run(tabCtx, b.hideHeadless(), b.sizeWindow(), focusPage(), bypassCSP(), installPatches()); err != nil {
		closeTab()
		return nil, nil, fmt.Errorf("browser: open tab: %w", err)
	}
	return tabCtx, closeTab, nil
}

// answerProxy signs the tab in to an authenticated proxy.
//
// Which address a solve goes out from is not a detail. Measured over one
// evening on one connection, unchanged code went from three tokens in five to
// none in five after about twenty-five solves — reputation, not logic. The way
// out of that is somebody else's address, and a proxy worth using wants a
// password, which Chrome cannot be handed on the command line (see splitProxy):
// it asks, gets 407, and raises a dialog no one is there to fill in. Without
// this the navigation dies as ERR_INVALID_AUTH_CREDENTIALS.
//
// Answering means turning on Fetch, and Fetch pauses every request whether or
// not it is the one being challenged, so each one has to be waved through. That
// is a round trip per request, which is why it is only turned on when there is
// actually a password to give.
//
// This is the tab's own session, so it covers the page and everything the page
// loads. A challenge iframe is a target of its own and does not inherit it —
// Chrome remembers proxy credentials across the network session once they are
// accepted, so in practice the first sign-in covers what comes after, but that
// is Chrome's behaviour rather than something proven here. If an out-of-process
// frame is ever seen failing with ERR_INVALID_AUTH_CREDENTIALS, this is where
// the second Fetch.enable belongs, on the frame's session.
func (b *Browser) answerProxy(ctx context.Context) error {
	if b.proxyUser == "" && b.proxyPass == "" {
		return nil
	}

	user, pass := b.proxyUser, b.proxyPass
	chromedp.ListenTarget(ctx, func(event any) {
		// Both arms run in their own goroutine: the listener is called on the
		// connection's read loop, and issuing a command from there would wait
		// for a reply that cannot be read until the listener returns.
		switch e := event.(type) {
		case *fetch.EventAuthRequired:
			go func() {
				_ = chromedp.Run(ctx, fetch.ContinueWithAuth(e.RequestID,
					&fetch.AuthChallengeResponse{
						Response: fetch.AuthChallengeResponseResponseProvideCredentials,
						Username: user,
						Password: pass,
					}))
			}()
		case *fetch.EventRequestPaused:
			go func() {
				_ = chromedp.Run(ctx, fetch.ContinueRequest(e.RequestID))
			}()
		}
	})

	if err := chromedp.Run(ctx, fetch.Enable().WithHandleAuthRequests(true)); err != nil {
		return fmt.Errorf("browser: sign in to the proxy: %w", err)
	}
	return nil
}

// focusPage makes the tab believe it is the window someone is looking at.
//
// A virtual display has no window manager, so nothing ever gives Chrome's
// window the focus: measured on the display postern draws on,
// `document.hasFocus()` comes back false while `visibilityState` is "visible".
// A page that is visible, receives clicks on a checkbox, and is not focused is
// a combination no one sitting at a computer produces — and focus is not
// obscure, it is what a page uses to know whether to keep playing a video.
//
// This asks the renderer to treat the page as focused, which is the same thing
// a window manager would arrange, rather than overriding hasFocus in page
// JavaScript where the lie would be visible in the property descriptor.
func focusPage() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		return emulation.SetFocusEmulationEnabled(true).Do(ctx)
	})
}

// bypassCSP lets postern render its own widget on pages that forbid it.
//
// Postern solves a challenge by putting the vendor's widget on the target page
// itself, which means loading the vendor's script — and a page's content
// security policy is entitled to refuse. Sites that use reCAPTCHA naturally
// allow Google's script and never notice this; sites that use one vendor and
// are asked for another refuse outright, and postern would report
// "api-script-blocked" for a page it can plainly reach.
//
// The relaxation is invisible to the page: it is applied by the browser to our
// own session, not written into the document, and nothing about the origin,
// referrer or request the vendor sees changes.
func bypassCSP() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		return page.SetBypassCSP(true).Do(ctx)
	})
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
	// Ask Chrome to shut down, and wait for it, rather than cancelling the
	// context and killing it.
	//
	// Cookies and history survive a kill — those are SQLite and land on disk as
	// they happen — but the profile's own state does not. Measured over two
	// runs each way, a killed Chrome left no Default/Preferences at all, and a
	// closed one wrote it every time, along with nine other files. Preferences
	// is where the profile's settled state lives, so without it every run reads
	// as a browser that has never once been closed: no language settled, no
	// permissions remembered, none of the accumulated small state a profile
	// picks up. For a program whose case is that the profile ages like a
	// person's, that is the part that was not ageing.
	if b.browserCtx != nil {
		done, cancel := context.WithTimeout(context.WithoutCancel(b.browserCtx), shutdownTimeout)
		if err := chromedp.Cancel(done); err != nil {
			// Nothing useful to do about it — the cancels below still stop the
			// process — but a profile that will not save is worth a line.
			_ = err
		}
		cancel()
	}

	if b.browserCancel != nil {
		b.browserCancel()
	}
	if b.allocCancel != nil {
		b.allocCancel()
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
