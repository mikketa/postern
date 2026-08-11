// Command postern solves Cloudflare Turnstile challenges with a real browser.
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
	"syscall"
	"time"

	"github.com/mikketa/postern/internal/api"
	"github.com/mikketa/postern/internal/browser"
	"github.com/mikketa/postern/internal/solver"
)

const usage = `postern - Cloudflare Turnstile solver driving a real Chrome

usage:
  postern serve [flags]
  postern solve -url <page> -sitekey <key> [flags]

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

// browserFlags registers the flags every command shares. The window size is
// returned unparsed, since flag parsing has not run yet when this is called.
func browserFlags(fs *flag.FlagSet) (*browser.Options, *string) {
	opts := &browser.Options{}
	fs.StringVar(&opts.UserDataDir, "profile", defaultProfileDir(), "Chrome profile directory, kept across runs")
	fs.BoolVar(&opts.Headless, "headless", true, "run without a window (-headless=false for a windowed browser)")
	window := fs.String("window", "1920x1080", "browser window size, WxH")
	fs.StringVar(&opts.ExecPath, "chrome", "", "path to the Chrome binary (default: autodetect)")
	fs.StringVar(&opts.Proxy, "proxy", "", "proxy passed to Chrome, e.g. http://user:pass@host:port")
	return opts, window
}

// applyWindow parses a WxH string into the options.
func applyWindow(opts *browser.Options, window string) error {
	var w, h int
	if _, err := fmt.Sscanf(window, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return fmt.Errorf("invalid -window %q, expected something like 1920x1080", window)
	}
	opts.ScreenWidth, opts.ScreenHeight = w, h
	return nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	opts, window := browserFlags(fs)
	addr := fs.String("addr", "127.0.0.1:8099", "address to listen on")
	timeout := fs.Duration("timeout", 60*time.Second, "default per-solve timeout")
	concurrency := fs.Int("concurrency", 2, "solves running at the same time")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := applyWindow(opts, *window); err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	b, err := browser.Launch(ctx, *opts)
	if err != nil {
		return err
	}
	defer b.Close()

	srv := &http.Server{
		Addr:    *addr,
		Handler: api.New(b, *timeout, *concurrency, log).Handler(),
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
	opts, window := browserFlags(fs)
	url := fs.String("url", "", "page the widget belongs to (required)")
	sitekey := fs.String("sitekey", "", "Turnstile sitekey (required)")
	action := fs.String("action", "", "Turnstile action parameter, if the site sets one")
	cdata := fs.String("cdata", "", "Turnstile cData parameter, if the site sets one")
	timeout := fs.Duration("timeout", 60*time.Second, "give up after this long")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *url == "" || *sitekey == "" {
		return errors.New("-url and -sitekey are required")
	}
	if err := applyWindow(opts, *window); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	b, err := browser.Launch(ctx, *opts)
	if err != nil {
		return err
	}
	defer b.Close()

	result, err := solver.Solve(ctx, b, solver.Request{
		URL:     *url,
		SiteKey: *sitekey,
		Action:  *action,
		CData:   *cdata,
	}, *timeout)
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
