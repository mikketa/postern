// Command postern solves Turnstile and reCAPTCHA challenges with a real browser.
//
//	postern serve                       # local HTTP API
//	postern solve -url ... -sitekey ...  # one shot, token on stdout
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mikketa/postern/internal/api"
	"github.com/mikketa/postern/internal/browser"
	"github.com/mikketa/postern/internal/display"
	"github.com/mikketa/postern/internal/solver"
)

const usage = `postern - captcha solver driving a real Chrome

usage:
  postern serve [flags]
  postern solve -url <page> -sitekey <key> [-kind <kind>] [flags]

run "postern <command> -h" for the flags of a command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "solve":
		err = runSolve(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "postern:", err)
		os.Exit(1)
	}
}

// browserFlags registers the flags every command shares. The screen size and
// display mode come back unparsed, since flag parsing has not run yet when
// this is called.
func browserFlags(fs *flag.FlagSet) (*browser.Options, *string, *string) {
	opts := &browser.Options{}
	fs.StringVar(&opts.UserDataDir, "profile", defaultProfileDir(), "Chrome profile directory, kept across runs")
	fs.BoolVar(&opts.Headless, "headless", false,
		"run Chrome in headless mode — measurably more detectable; by default a windowed "+
			"Chrome runs on a virtual display instead, which shows nothing on screen either")
	screen := fs.String("screen", "1920x1080", "virtual screen size, WxH — the window is sized from it")
	mode := fs.String("display", string(display.Virtual),
		"where Chrome draws: virtual (an Xvfb of our own, nothing on screen) or host (your session, visible)")
	fs.StringVar(&opts.ExecPath, "chrome", "", "path to the Chrome binary (default: autodetect)")
	fs.StringVar(&opts.Proxy, "proxy", "", "proxy passed to Chrome, e.g. http://user:pass@host:port")
	return opts, screen, mode
}

// startBrowser gets Chrome running, with a screen for it to draw on unless the
// caller asked for headless. Windowed Chrome on a virtual display is the
// default because headless is refused by real challenges; see internal/display.
func startBrowser(ctx context.Context, opts *browser.Options, mode display.Mode) (*browser.Browser, func(), error) {
	var screen *display.Display

	if !opts.Headless {
		var err error
		screen, err = display.Ensure(ctx, opts.ScreenWidth, opts.ScreenHeight, mode)
		if err != nil {
			return nil, nil, err
		}
		opts.Env = screen.Env()
	}

	b, err := browser.Launch(ctx, *opts)
	if err != nil {
		screen.Close()
		return nil, nil, err
	}

	return b, func() {
		b.Close()
		screen.Close()
	}, nil
}

// applyScreen parses a WxH string into the options.
func applyScreen(opts *browser.Options, screen string) error {
	var w, h int
	if _, err := fmt.Sscanf(screen, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return fmt.Errorf("invalid -screen %q, expected something like 1920x1080", screen)
	}
	opts.ScreenWidth, opts.ScreenHeight = w, h
	return nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	opts, screen, mode := browserFlags(fs)
	addr := fs.String("addr", "127.0.0.1:8099", "address to listen on")
	timeout := fs.Duration("timeout", 60*time.Second, "default per-solve timeout")
	concurrency := fs.Int("concurrency", 2, "solves running at the same time")
	imageSolver := fs.String("image-solver", "",
		"command answering picture grids: it receives a PNG path and prints one x,y per line")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyScreen(opts, *screen); err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	b, closeBrowser, err := startBrowser(ctx, opts, display.Mode(*mode))
	if err != nil {
		return err
	}
	defer closeBrowser()

	srv := &http.Server{
		Addr:    *addr,
		Handler: api.New(b, *timeout, *concurrency, *imageSolver, log).Handler(),
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", *addr, "profile", opts.UserDataDir)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func runSolve(args []string) error {
	fs := flag.NewFlagSet("solve", flag.ExitOnError)
	opts, screen, mode := browserFlags(fs)
	url := fs.String("url", "", "page the widget belongs to (required)")
	sitekey := fs.String("sitekey", "", "sitekey, as found in the target page (required)")
	kind := fs.String("kind", string(solver.Turnstile),
		"challenge kind: "+strings.Join(solver.Kinds(), ", "))
	action := fs.String("action", "", "action parameter, if the site sets one")
	cdata := fs.String("cdata", "", "Turnstile cData parameter, if the site sets one")
	imageSolver := fs.String("image-solver", "",
		"command answering picture grids: it receives a PNG path and prints one x,y per line")
	timeout := fs.Duration("timeout", 60*time.Second, "give up after this long")
	verbose := fs.Bool("v", false, "report what the challenge did, on stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *url == "" || *sitekey == "" {
		return errors.New("-url and -sitekey are required")
	}
	if err := applyScreen(opts, *screen); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	b, closeBrowser, err := startBrowser(ctx, opts, display.Mode(*mode))
	if err != nil {
		return err
	}
	defer closeBrowser()

	req := solver.Request{
		Kind:        solver.Kind(*kind),
		URL:         *url,
		SiteKey:     *sitekey,
		Action:      *action,
		CData:       *cdata,
		ImageSolver: *imageSolver,
	}
	if *verbose {
		req.Log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	result, err := solver.Solve(ctx, b, req, *timeout)
	if err != nil {
		return err
	}

	fmt.Println(result.Token)
	return nil
}

// defaultProfileDir keeps the profile out of the way but stable, since a fresh
// profile on every run defeats the point of using a real browser.
func defaultProfileDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "postern-profile")
	}
	return filepath.Join(base, "postern", "profile")
}
