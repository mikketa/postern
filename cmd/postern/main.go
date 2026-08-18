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
	"github.com/mikketa/postern/internal/pool"
	"github.com/mikketa/postern/internal/solver"
)

const usage = `postern - captcha solver driving a real Chrome

usage:
  postern serve [flags]
  postern solve -url <page> -sitekey <key> [-kind <kind>] [flags]
  postern warm -pages <file> [flags]
  postern version

run "postern <command> -h" for the flags of a command.
`

// version is set by the release build. A binary built with "go build" says so
// rather than claiming a version nobody tagged.
var version = "devel"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "warm":
		err = runWarm(os.Args[2:])
	case "solve":
		err = runSolve(os.Args[2:])
	case "version", "-version", "--version":
		fmt.Println("postern", version)
		return
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
	fs.StringVar(&opts.Proxy, "proxy", "", "go out through this proxy, e.g. http://user:pass@host:port or socks5://host:port")
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

// runWarm gives the profile somewhere to have been.
//
// Kept as its own command rather than folded into solve: warming is slow by
// design and belongs on a schedule — once a day, say, from cron — not in front
// of every token. Doing it inline would also make every solve slower for a
// benefit that only accrues over days.
// buildFleet reads the identities file and pairs it with a pool.
//
// One identity per line, "name" or "name proxy". The profile is derived from
// the name next to the configured profile directory, so adding an identity is
// adding a line — nothing to create by hand, and the fleet warms a new profile
// on its first outing.
func buildFleet(path, pagesPath string, opts browser.Options, log *slog.Logger) (*pool.Fleet, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("serve: read %s: %w", path, err)
	}

	base := opts.UserDataDir
	if base == "" {
		base = defaultProfileDir()
	}

	var identities []*pool.Identity
	for line := range strings.SplitSeq(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 2 {
			return nil, fmt.Errorf("serve: %q is not \"name\" or \"name proxy\"", line)
		}
		identity := &pool.Identity{
			Name:    fields[0],
			Profile: filepath.Join(base, "fleet", fields[0]),
		}
		if len(fields) == 2 {
			// Taken in the form providers sell it, not just as a URL; see
			// pool.ParseProxy. A mistyped one is refused here rather than
			// becoming an identity that quarantines itself for no reason.
			proxy, err := pool.ParseProxy(fields[1])
			if err != nil {
				return nil, fmt.Errorf("serve: %s: %w", path, err)
			}
			identity.Proxy = proxy
		}
		identities = append(identities, identity)
	}
	if len(identities) == 0 {
		return nil, fmt.Errorf("serve: %s holds no identities", path)
	}

	var pages []string
	if pagesPath != "" {
		if pages, err = readPages(pagesPath); err != nil {
			return nil, err
		}
	}

	// Said once, at startup, rather than discovered in a month of flat token
	// rates. Not fatal: a fleet sharing one address is a legitimate thing to
	// run while proxies are still being sorted out, as long as nobody is under
	// the impression it is doing more than that.
	for _, problem := range pool.CheckFleet(identities) {
		log.Warn("fleet", "problem", problem)
	}

	p, err := pool.New(identities, filepath.Join(base, "fleet", "state.json"), pool.Settings{})
	if err != nil {
		return nil, err
	}
	return pool.NewFleet(p, opts, pages, log), nil
}

func runWarm(args []string) error {
	fs := flag.NewFlagSet("warm", flag.ExitOnError)
	opts, screen, mode := browserFlags(fs)
	list := fs.String("pages", "",
		"file of URLs to visit, one per line; blank lines and # comments ignored")
	timeout := fs.Duration("timeout", 10*time.Minute, "how long to spend browsing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyScreen(opts, *screen); err != nil {
		return err
	}
	if *list == "" {
		return errors.New("warm: -pages is required. There is no built-in list: a history " +
			"that looks ordinary depends on where this runs, and one baked into the binary " +
			"would be the same history for every postern in the world")
	}

	pages, err := readPages(*list)
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	b, closeBrowser, err := startBrowser(ctx, opts, display.Mode(*mode))
	if err != nil {
		return err
	}
	// Not deferred: the profile is only written when Chrome is closed, and an
	// error return that skipped it would throw away the whole point of running.
	err = b.Warm(ctx, pages, log)
	closeBrowser()
	if err != nil {
		return err
	}
	log.Info("profile warmed", "profile", opts.UserDataDir)
	return nil
}

// readPages reads the URL list, ignoring blanks and comments.
func readPages(path string) ([]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("warm: read %s: %w", path, err)
	}

	var pages []string
	for line := range strings.SplitSeq(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "http://") && !strings.HasPrefix(line, "https://") {
			return nil, fmt.Errorf("warm: %q is not an http url", line)
		}
		pages = append(pages, line)
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("warm: %s holds no urls", path)
	}
	return pages, nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	opts, screen, mode := browserFlags(fs)
	addr := fs.String("addr", "127.0.0.1:8099", "address to listen on")
	timeout := fs.Duration("timeout", 60*time.Second, "default per-solve timeout")
	concurrency := fs.Int("concurrency", 2, "solves running at the same time")
	identities := fs.String("identities", "",
		"file of identities, one \"name proxy\" per line: each gets its own profile, rests "+
			"between solves and is set aside when it stops working")
	warmPages := fs.String("warm-pages", "",
		"file of urls a new identity browses once before its first solve")
	imageSolver := fs.String("image-solver", "",
		"command answering picture grids: it receives a PNG path and prints one x,y per line")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyScreen(opts, *screen); err != nil {
		return err
	}

	// Refuse to hand a browser fleet to the network with nothing in front of
	// it. An error and not a warning on purpose: a warning printed at startup
	// is read once, on the day it is set up, and never again.
	//
	// Checked here rather than next to ListenAndServe so a misconfiguration
	// costs nothing — below this line the next thing that happens is Chrome
	// and an Xvfb starting, and it is galling to wait for them to come up only
	// to be told the address was wrong.
	if err := api.CheckReachable(*addr, api.Token()); err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A fleet starts its own browser per solve, one identity at a time, so the
	// shared one is only started when there is no fleet to start instead.
	var handler *api.Server
	if *identities == "" {
		b, closeBrowser, err := startBrowser(ctx, opts, display.Mode(*mode))
		if err != nil {
			return err
		}
		defer closeBrowser()
		handler = api.New(b, *timeout, *concurrency, *imageSolver, log)
	} else {
		// The fleet starts its browsers itself, so the screen they draw on has
		// to be started here and shared: one Xvfb for all of them, not one per
		// solve.
		if !opts.Headless {
			screen, err := display.Ensure(ctx, opts.ScreenWidth, opts.ScreenHeight, display.Mode(*mode))
			if err != nil {
				return err
			}
			defer screen.Close()
			opts.Env = screen.Env()
		}

		fleet, err := buildFleet(*identities, *warmPages, *opts, log)
		if err != nil {
			return err
		}
		log.Info("serving from a fleet", "identities", fleet.Size(), "ready", fleet.Ready())
		handler = api.NewFleet(fleet, *timeout, *concurrency, *imageSolver, log)
	}

	srv := newHTTPServer(*addr, handler.Handler(), *timeout)

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
		// Stop listening at once, then wait for what is already in flight.
		//
		// The window has to fit the work. It was five seconds, which is not
		// enough for anything this program does: a solve may legally run for
		// -timeout, so every rolling restart cut off whatever was mid-challenge
		// — the caller had already spent the wait, and the identity had spent a
		// challenge, for a connection that was dropped either way.
		drain := drainFor(*timeout)

		// Hand the signal back to the runtime, so a second one from an operator
		// who meant it terminates immediately instead of being swallowed.
		stop()
		log.Info("draining before shutdown", "for", drain,
			"note", "interrupt again to stop now")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), drain)
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
	savePanels := fs.String("save-panels", "",
		"keep a copy of every picture grid in this directory, to calibrate a solver against later")
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
		SavePanels:  *savePanels,
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

// newHTTPServer wraps the handler in the limits a listening socket needs.
//
// Every field below defaults to no limit at all in net/http, which is fine for
// a demo and wrong for anything reachable.
func newHTTPServer(addr string, h http.Handler, timeout time.Duration) *http.Server {
	return &http.Server{
		Addr:    addr,
		Handler: h,

		// Without these a connection that never finishes sending its headers
		// is held open for as long as it likes, and enough of them is all it
		// takes. ReadHeaderTimeout is the one that matters most: it is the
		// whole of the slow-headers attack. ReadTimeout can be short because a
		// solve request is a handful of fields, capped at 64KB by the handler.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,

		// WriteTimeout has to outlast the work, not the wire: it is measured
		// from the end of the request headers, so anything shorter than the
		// longest legal solve would cut the answer off mid-token. A request
		// cannot ask for more than -timeout — see api.effectiveTimeout — so
		// that, plus room to start a browser and write a reply, is the bound.
		WriteTimeout: timeout + 30*time.Second,
	}
}

// drainFor is how long to let in-flight solves finish after the listener
// closes: the longest one that could legally be running, plus room to write
// its answer.
//
// An orchestrator kills what has not exited by its own grace period — 30
// seconds by default in Kubernetes — so this number is also the one to size
// that against. It is deliberately derived from -timeout rather than fixed:
// raising the solve ceiling without raising the drain would quietly go back to
// dropping the longest solves, which are the ones that cost the most to lose.
func drainFor(timeout time.Duration) time.Duration {
	return timeout + 15*time.Second
}
