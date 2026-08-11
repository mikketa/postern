// Package browser launches and holds a real Chrome instance.
//
// The whole premise of Postern is that the least detectable browser is an
// actual browser: a normal Chrome binary, a persistent profile that ages like
// a human's, and as few automation flags as we can get away with. Nothing here
// tries to emulate Chrome — it drives it.
package browser

import (
	"context"
	"errors"
	"fmt"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/patches"
)

// Options configures the Chrome instance Postern drives.
type Options struct {
	// UserDataDir is the Chrome profile directory. Keeping it across runs is
	// what makes the browser look lived-in; an empty value means a throwaway
	// profile, which is measurably worse.
	UserDataDir string

	// Headless runs without a window. Headless Chrome is still distinguishable
	// from headful in several ways, so this is off by default.
	Headless bool

	// ExecPath overrides Chrome autodetection.
	ExecPath string

	// Proxy is passed to --proxy-server, e.g. "http://user:pass@host:port".
	Proxy string
}

// Browser owns a Chrome process and hands out tabs.
type Browser struct {
	allocCtx context.Context
	cancel   context.CancelFunc
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
	return &Browser{allocCtx: allocCtx, cancel: cancel}, nil
}

// NewTab opens a tab with every patch installed, and returns it along with the
// function that closes it. Callers own the cancel func.
func (b *Browser) NewTab() (context.Context, context.CancelFunc, error) {
	if b.allocCtx == nil {
		return nil, nil, errors.New("browser: not launched")
	}

	tabCtx, cancel := chromedp.NewContext(b.allocCtx)
	if err := chromedp.Run(tabCtx, installPatches()); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("browser: open tab: %w", err)
	}
	return tabCtx, cancel, nil
}

// Close terminates Chrome.
func (b *Browser) Close() {
	if b.cancel != nil {
		b.cancel()
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
