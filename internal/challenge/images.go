// Package challenge answers the picture grids reCAPTCHA puts up, by asking
// something else to look at them.
//
// Postern deliberately ships no vision model. Which one to use is not a
// decision a captcha solver should make for you — a local model, a hosted API
// and a human squinting at a PNG all have their place, and embedding any of
// them would drag a large dependency into a binary whose whole appeal is that
// it has none. So the grid is captured, handed to a command you nominate, and
// whatever that command says gets clicked.
//
// The protocol is deliberately dumb, so that writing a solver is a twenty-line
// script:
//
//	stdin  — nothing
//	argv   — the path to a PNG of the challenge panel, prompt included
//	stdout — one "x,y" pair per line, in pixels within that image; empty means
//	         nothing to click
//	exit 0 — anything else is treated as a failure to solve
//
// Coordinates rather than tile indices, because the grid is 3x3 or 4x4
// depending on the challenge and the panel is a cross-origin iframe we cannot
// measure from the inside. Whatever is looking at the picture can see the
// layout; we should not have to guess it.
package challenge

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/input"
)

const (
	// solverTimeout bounds the external command. Generous on purpose: a vision
	// model running on CPU, or a human being asked to look, both take longer
	// than an API call would.
	solverTimeout = 90 * time.Second

	// verifyFromRight and verifyFromBottom locate the verify button, measured
	// off screenshots of live challenges. Both are taken from the bottom-right
	// corner rather than the top-left, because the panel is not one fixed size:
	// it came out 400x580 for one challenge type and 300x480 for another, and
	// an offset anchored on the left edge missed the button entirely on the
	// narrower one.
	verifyFromRight  = 58
	verifyFromBottom = 29

	// betweenClicks is the pause between tile clicks. Selecting nine tiles in
	// nine milliseconds is not something a hand does.
	betweenClicksMin = 180
	betweenClicksMax = 520
)

// Panel is the challenge iframe, in viewport coordinates.
type Panel struct {
	X, Y, W, H float64
}

// Solve captures the panel, asks the external solver what to click, clicks it,
// and presses verify. It reports whether the solver was able to answer at all,
// not whether the answer was right — only the widget knows that, and the caller
// finds out by watching for the token.
func Solve(ctx context.Context, panel Panel, solverCmd string) error {
	if solverCmd == "" {
		return fmt.Errorf("challenge: no image solver configured")
	}

	shot, err := capture(ctx, panel)
	if err != nil {
		return err
	}

	path, cleanup, err := writeTemp(shot)
	if err != nil {
		return err
	}
	defer cleanup()

	points, err := ask(ctx, solverCmd, path)
	if err != nil {
		return err
	}
	if len(points) == 0 {
		return fmt.Errorf("challenge: solver selected nothing")
	}

	for _, p := range points {
		// The solver works in image pixels; the pointer works in the viewport.
		target := input.Point{X: panel.X + p.X, Y: panel.Y + p.Y}
		origin := input.Point{X: panel.X + panel.W + 90, Y: panel.Y + panel.H + 70}

		if err := chromedp.Run(ctx, input.Click(origin, target)); err != nil {
			return fmt.Errorf("challenge: click tile: %w", err)
		}
		if err := input.Pause(ctx, betweenClicksMin, betweenClicksMax); err != nil {
			return err
		}
	}

	verify := input.Point{
		X: panel.X + panel.W - verifyFromRight,
		Y: panel.Y + panel.H - verifyFromBottom,
	}
	origin := input.Point{X: panel.X + panel.W + 120, Y: panel.Y + panel.H + 90}
	if err := chromedp.Run(ctx, input.Click(origin, verify)); err != nil {
		return fmt.Errorf("challenge: click verify: %w", err)
	}
	return nil
}

// capture screenshots just the panel, so the solver sees the prompt and the
// grid and nothing else.
func capture(ctx context.Context, panel Panel) ([]byte, error) {
	var shot []byte

	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		shot, err = page.CaptureScreenshot().
			WithFormat(page.CaptureScreenshotFormatPng).
			WithClip(&page.Viewport{
				X:      panel.X,
				Y:      panel.Y,
				Width:  panel.W,
				Height: panel.H,
				Scale:  1,
			}).Do(ctx)
		return err
	}))
	if err != nil {
		return nil, fmt.Errorf("challenge: capture panel: %w", err)
	}
	return shot, nil
}

// writeTemp puts the capture somewhere the solver can read it.
func writeTemp(shot []byte) (string, func(), error) {
	dir, err := os.MkdirTemp("", "postern-challenge-")
	if err != nil {
		return "", nil, fmt.Errorf("challenge: temp dir: %w", err)
	}

	path := filepath.Join(dir, "challenge.png")
	if err := os.WriteFile(path, shot, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("challenge: write capture: %w", err)
	}
	return path, func() { os.RemoveAll(dir) }, nil
}

// point is a click position within the captured image.
type point struct {
	X, Y float64
}

// ask runs the solver command and parses what it says to click.
func ask(ctx context.Context, solverCmd, imagePath string) ([]point, error) {
	runCtx, cancel := context.WithTimeout(ctx, solverTimeout)
	defer cancel()

	// The command is split on spaces so that flags can be given inline; a path
	// with spaces in it should be wrapped in a shell script rather than fought
	// with quoting rules here.
	fields := strings.Fields(solverCmd)
	args := append(fields[1:], imagePath)

	out, err := exec.CommandContext(runCtx, fields[0], args...).Output()
	if err != nil {
		return nil, fmt.Errorf("challenge: solver %q failed: %w", fields[0], err)
	}

	var points []point
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		x, y, ok := strings.Cut(line, ",")
		if !ok {
			return nil, fmt.Errorf("challenge: solver returned %q, want \"x,y\"", line)
		}

		px, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return nil, fmt.Errorf("challenge: solver returned %q, want \"x,y\"", line)
		}
		py, err := strconv.ParseFloat(strings.TrimSpace(y), 64)
		if err != nil {
			return nil, fmt.Errorf("challenge: solver returned %q, want \"x,y\"", line)
		}
		points = append(points, point{X: px, Y: py})
	}
	return points, scanner.Err()
}
