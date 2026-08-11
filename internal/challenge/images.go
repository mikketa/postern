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
//	argv   — the path to a PNG of the challenge panel, prompt included
//	stdin  — nothing
//	stdout — one "x,y" pair per line, in pixels within that image; empty means
//	         nothing to click
//	exit 0 — answered
//	exit 2 — cannot answer this one; postern asks the widget for a different
//	         challenge and comes back
//	 other — a failure, which ends the solve
//
// The difference between exit 0 with no coordinates and exit 2 is the
// difference between "none of these are buses" and "I do not know what a
// crosswalk looks like", and it matters: the first is an answer worth
// submitting, the second is a guess that will be marked wrong. A solver that
// says so gets handed another grid instead, which is how a model that knows
// ten kinds of thing still gets through a challenge that asks about twenty.
//
// The environment carries what postern already knows, so a solver does not
// have to work it out from the picture:
//
//	POSTERN_PROMPT  — the instruction, as text
//	POSTERN_COLUMNS — 3 or 4, the width of the grid
//	POSTERN_TILES   — "x,y,w,h;..." one per tile, in image pixels
//
// A solver may ignore all three and read the picture alone; postern still
// clicks whatever coordinates come back. But they are exact where a screenshot
// is a guess, and the difference between OCR-ing a prompt and being told it is
// the difference between usually and always.
package challenge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
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

	// betweenClicks is the pause between tile clicks. Selecting nine tiles in
	// nine milliseconds is not something a hand does.
	betweenClicksMin = 180
	betweenClicksMax = 520

	// beforeVerify is the pause before pressing the button. The grid animates
	// each selection, and a click that lands mid-animation is one the panel is
	// not ready for.
	beforeVerifyMin = 400
	beforeVerifyMax = 900

	// maxRounds bounds a single dynamic challenge. Each correct click brings a
	// fresh picture that has to be looked at too, and a grid that keeps
	// producing matches past this is one we are not going to finish.
	maxRounds = 6

	// replaceWait is how long a replaced tile is given to arrive. reCAPTCHA
	// fades the old picture out and the new one in, and the grid is worth
	// photographing only once that has finished.
	replaceWait = 900 * time.Millisecond

	// replaceAttempts is how many of those to wait before concluding that
	// nothing is going to be replaced and the grid was a static one. Too few
	// and a slow swap reads as a static grid, which submits an answer the
	// challenge was not finished asking for.
	replaceAttempts = 5

	// loadWait is how long to wait between checks that the pictures have
	// arrived, and loadAttempts how many of those to make.
	loadWaitMin  = 350
	loadWaitMax  = 600
	loadAttempts = 14

	// pointerAttempts is how long to wait for a pointer submission to register
	// before falling back to the keyboard.
	pointerAttempts = 3

	// verdictAttempts is how long the panel is given to answer a submission.
	// reCAPTCHA takes a moment to decide, and the difference between "still
	// thinking" and "here is another grid" is only visible afterwards. Reading
	// too early sees the answered grid still standing and treats it as a fresh
	// challenge, which burns an attempt on a question already answered.
	verdictAttempts = 9

	// passExitCode is how a solver says it cannot answer this challenge.
	passExitCode = 2
)

// errPass is that refusal, travelling back up to the round loop.
var errPass = errors.New("challenge: solver passed")

// Solve captures the panel, asks the external solver what to click, clicks it,
// and presses the button. It reports whether the solver was able to answer at
// all, not whether the answer was right — only the widget knows that, and the
// caller finds out by watching for the token.
//
// It answers a challenge in as many rounds as the challenge takes. A static
// grid is one round: tick what matches, press verify. A dynamic grid replaces
// every correct tile with a fresh picture and is only finished when none of
// what is on screen matches any more — pressing verify before then submits a
// half-answer, which reCAPTCHA rejects as surely as a wrong one.
func Solve(ctx context.Context, frame *Frame, solverCmd string, log *slog.Logger) error {
	if solverCmd == "" {
		return fmt.Errorf("challenge: no image solver configured")
	}

	for round := 0; round < maxRounds; round++ {
		// The panel closes the moment the challenge is over, either because it
		// was answered or because the widget gave up on it. Carrying on would
		// photograph whatever the page has where the panel used to be.
		if !frame.Open() {
			log.Info("panel closed", "round", round)
			return nil
		}

		if err := ready(ctx, frame); err != nil {
			return err
		}

		before := frame.View.Pictures()
		log.Info("challenge served",
			"round", round,
			"prompt", frame.View.Prompt,
			"grid", frame.View.Columns(),
			"notice", frame.View.Notice)

		points, err := inspect(ctx, frame, solverCmd)
		if errors.Is(err, errPass) {
			log.Info("solver passed, asking for another challenge")
			if reloadErr := reload(ctx, frame, before); reloadErr != nil {
				return reloadErr
			}
			continue
		}
		if err != nil {
			return err
		}
		log.Info("solver answered", "tiles", len(points))

		for _, p := range points {
			if err := click(ctx, frame, p.X, p.Y); err != nil {
				return fmt.Errorf("challenge: click tile: %w", err)
			}
			if err := input.Pause(ctx, betweenClicksMin, betweenClicksMax); err != nil {
				return err
			}
		}

		// A dynamic grid swaps every correct tile for a fresh picture, and is
		// only finished when nothing on screen matches any more. Submitting
		// before then is a half-answer, which reCAPTCHA rejects as surely as a
		// wrong one — so go round again and look at what replaced them.
		if len(points) > 0 {
			replaced, err := await(ctx, frame, before, replaceAttempts)
			if err != nil {
				return err
			}
			if replaced {
				continue
			}
		}

		// Nothing left to click: submit, and wait to be told.
		if err := Verify(ctx, frame, log); err != nil {
			return err
		}
		if _, err := await(ctx, frame, before, verdictAttempts); err != nil {
			return err
		}
		if !frame.Open() {
			log.Info("challenge answered", "rounds", round+1)
			return nil
		}
		log.Info("another challenge", "notice", frame.View.Notice)
	}

	return nil
}

// ready waits for the grid to be worth looking at: pictures loaded, nothing
// mid-animation. A grid photographed while its images are still arriving is a
// grid of blank squares, and a solver asked to find cars in it will rightly
// find none.
func ready(ctx context.Context, frame *Frame) error {
	for range loadAttempts {
		if !frame.View.Loading && !frame.View.Settling {
			return nil
		}
		if err := input.Pause(ctx, loadWaitMin, loadWaitMax); err != nil {
			return err
		}
		if err := frame.Reread(ctx); err != nil {
			return err
		}
		if !frame.Open() {
			return nil
		}
	}
	return nil
}

// inspect photographs the grid as it stands and asks what to click.
func inspect(ctx context.Context, frame *Frame, solverCmd string) ([]point, error) {
	shot, err := capture(ctx, frame)
	if err != nil {
		return nil, err
	}

	path, cleanup, err := writeTemp(shot)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	return ask(ctx, solverCmd, path, frame.View)
}

// reload asks the widget for a different challenge, for when the solver has
// said it cannot answer this one. The panel has a button for exactly this, and
// pressing it is what a person does when handed a puzzle in a language they do
// not read.
func reload(ctx context.Context, frame *Frame, before []string) error {
	if frame.View.Reload == nil {
		return fmt.Errorf("challenge: solver passed on %q and the panel offers no other",
			frame.View.Prompt)
	}

	x, y := frame.View.Reload.Center()
	if err := click(ctx, frame, x, y); err != nil {
		return fmt.Errorf("challenge: reload: %w", err)
	}

	// Wait for a genuinely different grid rather than photographing the old one
	// again, which would pass right back to the solver and stall the round.
	if _, err := await(ctx, frame, before, replaceAttempts); err != nil {
		return err
	}
	return nil
}

// await waits for the panel to become something other than what it was: a
// different grid, replaced tiles, or no panel at all. It waits out the whole
// budget rather than stopping at the first quiet moment: reCAPTCHA takes a
// beat to decide, and a grid read too early looks exactly like a grid that is
// never going to change.
func await(ctx context.Context, frame *Frame, before []string, tries int) (bool, error) {
	for range tries {
		if err := input.Pause(ctx, int(replaceWait.Milliseconds()), int(replaceWait.Milliseconds())); err != nil {
			return false, err
		}
		if err := frame.Reread(ctx); err != nil {
			return false, err
		}
		if !frame.Open() {
			return true, nil
		}
		if frame.View.Settling {
			continue
		}
		if changed(before, frame.View.Pictures()) {
			return true, nil
		}
	}
	return false, nil
}

// changed reports whether the grid is showing anything new.
func changed(before, after []string) bool {
	if len(before) != len(after) {
		return true
	}
	for i := range before {
		if before[i] != after[i] {
			return true
		}
	}
	return false
}

// verifySelector is the panel's submit button, whatever its wording.
const verifySelector = "#recaptcha-verify-button"

// Verify submits the answer. Its label changes with the round — "Vérifier" once
// something is selected, "Ignorer" when nothing matches, "Suivant" between the
// images of a multi-step challenge — but it is always the way forward.
//
// It goes by pointer when it can and by keyboard when it cannot. reCAPTCHA
// lays the panel out taller than the space it gives it, and on a short panel
// the button ends up below a container that clips it: the browser knows the
// rectangle, paints none of it, and a click sent there lands on the page
// behind. Widening the frame does not help — the clipping is inside the
// document — but a focused button still answers Enter.
func Verify(ctx context.Context, frame *Frame, log *slog.Logger) error {
	if err := input.Pause(ctx, beforeVerifyMin, beforeVerifyMax); err != nil {
		return err
	}

	// Re-read first: selecting tiles can resize the panel and move the button,
	// and the position measured before the clicks is then the old one.
	if err := frame.Reread(ctx); err != nil {
		return err
	}
	if !frame.Open() {
		return nil
	}

	if frame.View.Button == nil {
		return fmt.Errorf("challenge: panel has no verify button")
	}

	if frame.View.Button.Hittable {
		x, y := frame.View.Button.Center()
		log.Debug("verify by pointer", "label", frame.View.Button.Label)

		before := frame.View.Pictures()
		if err := click(ctx, frame, x, y); err != nil {
			return err
		}

		// A button that was hittable when measured may not be by the time the
		// pointer arrives — the panel relabels and relays itself mid-round, and
		// a click into that gap is swallowed silently. If nothing moved, fall
		// through to the keyboard rather than leaving the answer unsubmitted.
		taken, err := await(ctx, frame, before, pointerAttempts)
		if err != nil || taken || !frame.Open() {
			return err
		}
		log.Debug("pointer click was swallowed, trying the keyboard")
	}

	focused, err := frame.Focus(ctx, verifySelector)
	if err != nil {
		return err
	}
	if !focused {
		return fmt.Errorf("challenge: verify button is clipped at y=%.0f in a %.0f-tall "+
			"panel and will not take focus", frame.View.Button.Y, frame.View.Height)
	}

	log.Debug("verify by keyboard", "label", frame.View.Button.Label,
		"buttonY", frame.View.Button.Y, "panelHeight", frame.View.Height)
	return chromedp.Run(ctx, chromedp.ActionFunc(input.PressEnter))
}

// click aims the real pointer at a position inside the challenge document.
// Everything is measured through CDP and clicked through the mouse; nothing is
// ever dispatched by script, which would arrive untrusted and be ignored.
func click(ctx context.Context, frame *Frame, x, y float64) error {
	targetX, targetY := frame.Point(x, y)
	target := input.Point{X: targetX, Y: targetY}

	// Come from outside the panel, so the pointer covers real ground instead
	// of materialising on top of what it is about to click.
	originX, originY := frame.Point(frame.View.Width+90, frame.View.Height+70)
	origin := input.Point{X: originX, Y: originY}

	return chromedp.Run(ctx, input.Click(origin, target))
}

// capture screenshots just the panel, so the solver sees the prompt and the
// grid and nothing else.
func capture(ctx context.Context, frame *Frame) ([]byte, error) {
	x, y, w, h := frame.Viewport()

	var shot []byte
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		shot, err = page.CaptureScreenshot().
			WithFormat(page.CaptureScreenshotFormatPng).
			WithClip(&page.Viewport{X: x, Y: y, Width: w, Height: h, Scale: 1}).
			Do(ctx)
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
func ask(ctx context.Context, solverCmd, imagePath string, view View) ([]point, error) {
	runCtx, cancel := context.WithTimeout(ctx, solverTimeout)
	defer cancel()

	// The command is split on spaces so that flags can be given inline; a path
	// with spaces in it should be wrapped in a shell script rather than fought
	// with quoting rules here.
	fields := strings.Fields(solverCmd)
	args := append(fields[1:], imagePath)

	cmd := exec.CommandContext(runCtx, fields[0], args...)
	cmd.Env = append(os.Environ(),
		"POSTERN_PROMPT="+view.Prompt,
		"POSTERN_COLUMNS="+strconv.Itoa(view.Columns()),
		"POSTERN_TILES="+encodeTiles(view.Tiles),
	)

	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == passExitCode {
			return nil, errPass
		}
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

// encodeTiles renders the grid for the solver's environment.
func encodeTiles(tiles []Box) string {
	parts := make([]string, 0, len(tiles))
	for _, t := range tiles {
		parts = append(parts, fmt.Sprintf("%.0f,%.0f,%.0f,%.0f", t.X, t.Y, t.W, t.H))
	}
	return strings.Join(parts, ";")
}
