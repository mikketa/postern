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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
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

	// maxPasses is how many refusals in a row to accept before reporting that
	// this solver and this challenge are not going to agree.
	maxPasses = 3

	// maxStuckReloads is how many times to press the reload button and be given
	// the same grid back before accepting that the panel is not going to change
	// its mind. Two, because the first refusal is worth a second try and the
	// third is a loop: a run measured seventeen identical rounds — the same
	// picture, the same score to two decimal places — before the timeout ended
	// it. Failing in twenty seconds with a reason beats failing in two minutes
	// without one.
	maxStuckReloads = 2

	// buttonWait is how long to wait for the button to come back to life, and
	// buttonAttempts how many times.
	buttonWaitMin  = 300
	buttonWaitMax  = 500
	buttonAttempts = 6

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

	// solverErrLines is how much of a failing solver's output to quote back.
	solverErrLines = 3

	// paintAttempts is how many times to wait and photograph the grid again
	// while it is still being painted.
	paintAttempts = 6

	// blankTilesPercent is how much of the grid has to be one flat colour for
	// the whole thing to count as unpainted. Not all of it: a real grid can
	// hold a tile of plain sky, and a grid still arriving usually has one or
	// two tiles already in.
	blankTilesPercent = 60

	// blankRange is how much a tile's brightness may vary and still be called
	// flat, out of 65535, and blankStride how many pixels apart to sample.
	blankRange  = 900
	blankStride = 4

	// repaintTimeout is how long to wait for a second photograph before taking
	// the silence to mean the picture has stopped changing. Generous against a
	// capture that answers in about 100ms when there is anything to answer with.
	repaintTimeout = 1500 * time.Millisecond

	// settleWait is how long to let the layout settle after provoking a
	// repaint, before measuring where anything is.
	settleWaitMin = 120
	settleWaitMax = 260

	// settleRange is how far two photographs of the same grid may differ, on
	// average and out of 65535, and still count as the same picture. Generous
	// enough to ignore a repainted cursor or a hairline of antialiasing, and far
	// below what a fade moves.
	settleRange = 600
)

// errPass is that refusal, travelling back up to the round loop.
var errPass = errors.New("challenge: solver passed")

// Options is what to do with a picture challenge.
type Options struct {
	// Solver is the command to hand each grid to.
	Solver string

	// SavePanels, when set, is a directory to keep a copy of every grid in:
	// the PNG that was sent to the solver and a JSON of what postern read off
	// the panel beside it. This is how you build a corpus — to calibrate a
	// solver offline without burning live challenges, or to label tiles and
	// fit a head for a category no model has a class for. See
	// examples/train-probe.py.
	SavePanels string
}

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
func Solve(ctx context.Context, frame *Frame, opts Options, log *slog.Logger) (int, error) {
	solverCmd := opts.Solver
	if solverCmd == "" {
		return 0, fmt.Errorf("challenge: no image solver configured")
	}

	passes := 0
	stuck := 0
	// rounds is how many grids this challenge took, which is the caller's
	// only measure of how much reCAPTCHA is asking for.
	rounds := 0

	// fresh presses the reload button and insists on getting somewhere. A panel
	// that keeps handing back the same grid is not going to be talked round,
	// and every attempt costs a round trip to the solver.
	fresh := func(before []string) error {
		changed, err := reload(ctx, frame, before)
		if err != nil {
			return err
		}
		if changed {
			stuck = 0
			return nil
		}
		stuck++
		if stuck >= maxStuckReloads {
			return fmt.Errorf("challenge: asked %d times for a grid other than %q and got "+
				"the same one back, which the solver has nothing for", stuck, frame.View.Prompt)
		}
		log.Info("the panel kept the same grid", "attempts", stuck)
		return nil
	}

	for round := 0; round < maxRounds; round++ {
		rounds = round + 1
		// The panel closes the moment the challenge is over, either because it
		// was answered or because the widget gave up on it. Carrying on would
		// photograph whatever the page has where the panel used to be.
		if !frame.Open() {
			log.Info("panel closed", "round", round)
			return rounds, nil
		}

		if err := ready(ctx, frame); err != nil {
			return rounds, err
		}

		before := frame.View.Pictures()
		log.Info("challenge served",
			"round", round,
			"prompt", frame.View.Prompt,
			"grid", frame.View.Columns(),
			"notice", frame.View.Notice)

		points, err := inspect(ctx, frame, opts, log)
		if errors.Is(err, errPass) {
			// Reloading asks for a different challenge, but reCAPTCHA is under
			// no obligation to change the subject and often does not: it has
			// decided to ask about crosswalks and it will keep asking. Giving
			// up quickly beats burning the whole timeout on a category the
			// solver has already said it cannot answer.
			passes++
			if passes >= maxPasses {
				return rounds, fmt.Errorf("challenge: the solver has passed on %q %d times running "+
					"and reCAPTCHA keeps asking — it needs a model that answers this one",
					frame.View.Prompt, passes)
			}

			log.Info("solver passed, asking for another challenge", "passes", passes)
			if reloadErr := fresh(before); reloadErr != nil {
				return rounds, reloadErr
			}
			continue
		}
		passes = 0
		if err != nil {
			return rounds, err
		}
		log.Info("solver answered", "tiles", len(points))

		clicked := 0
		for _, p := range points {
			// A solver is somebody else's script, and a click outside the panel
			// lands on the target page — on a link, say, which navigates away
			// and takes the widget with it. Coordinates given in tile indices
			// rather than pixels are the usual way to end up here.
			if p.X < 0 || p.Y < 0 || p.X > frame.View.Width || p.Y > frame.View.Height {
				return rounds, fmt.Errorf("challenge: solver returned %.0f,%.0f, outside the "+
					"%.0fx%.0f panel it was given", p.X, p.Y, frame.View.Width, frame.View.Height)
			}

			// A tile is a toggle, so clicking one that is already ticked
			// unticks it. On a dynamic grid that is a loop with no exit: the
			// solver names the same tile every round because it is still the
			// right answer, and postern alternates between selecting and
			// deselecting it until the rounds run out. Seen on a bridge
			// challenge that spent all six rounds on tile 6.
			//
			// Naming a ticked tile means the solver agrees with what is
			// already there. Leave it alone.
			if tile := frame.View.TileAt(p.X, p.Y); tile != nil && tile.Selected {
				continue
			}

			if err := click(ctx, frame, p.X, p.Y); err != nil {
				return rounds, fmt.Errorf("challenge: click tile: %w", err)
			}
			clicked++
			if err := input.Pause(ctx, betweenClicksMin, betweenClicksMax); err != nil {
				return rounds, err
			}
		}
		if clicked < len(points) {
			log.Info("kept tiles the solver named again", "clicked", clicked, "named", len(points))
		}

		// An empty answer is an answer, not a failure: reCAPTCHA's own
		// instructions say to press the button when none of the pictures match,
		// and a dynamic grid ends exactly that way.
		//
		// Unless the panel has already complained about this grid. Submitting
		// without having changed anything, to a challenge that just said "check
		// the new images too", is resubmitting the answer it has already
		// refused — and the round after that is the same one again. Ask for a
		// different grid instead.
		//
		// This keys off clicks rather than coordinates: a solver naming only
		// tiles that are already ticked has changed nothing, whatever it
		// returned.
		if clicked == 0 && frame.View.Notice != "" {
			log.Info("nothing new on a grid already refused, asking for another",
				"notice", frame.View.Notice, "named", len(points))
			if err := fresh(before); err != nil {
				return rounds, err
			}
			continue
		}

		// A dynamic grid swaps every correct tile for a fresh picture, and is
		// only finished when nothing on screen matches any more. Submitting
		// before then is a half-answer, which reCAPTCHA rejects as surely as a
		// wrong one — so go round again and look at what replaced them.
		if clicked > 0 {
			replaced, err := await(ctx, frame, before, replaceAttempts)
			if err != nil {
				return rounds, err
			}
			if replaced {
				continue
			}
		}

		// Nothing left to click: submit, and wait to be told.
		if err := Verify(ctx, frame, log); err != nil {
			return rounds, err
		}
		if _, err := await(ctx, frame, before, verdictAttempts); err != nil {
			return rounds, err
		}
		if !frame.Open() {
			log.Info("challenge answered", "rounds", round+1)
			return rounds, nil
		}
		log.Info("another challenge", "notice", frame.View.Notice)
	}

	return rounds, nil
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
//
// The photograph is checked before it is sent anywhere. The document can say
// every picture has loaded while the grid on screen is still blank — reCAPTCHA
// fades them in, and an image that is `complete` with a decoded size is not yet
// an image that has been painted. Measured over 88 captured panels: fourteen of
// them, one in six, were entirely flat squares. The solver did what anyone
// would do with a blank grid and found nothing in it, and the round was spent.
//
// Blank is only the beginning of the fade, though, and the middle of it is
// worse: the pictures are there, washed out and mixed with the page behind
// them, so the grid looks answerable and the answers are wrong. No single
// photograph shows that it was taken mid-fade. Two do — a fading grid changes
// between them and a finished one does not — so this takes photographs until
// two in a row agree.
func inspect(ctx context.Context, frame *Frame, opts Options, log *slog.Logger) ([]point, error) {
	// Get the page painted before the first photograph too. The panel fades in
	// over the page, and under a virtual display that fade stops wherever it was
	// when the last frame was composited — measured, the opening grid of every
	// run came back at partial opacity with the form behind it showing through,
	// which is a grid nothing can answer and nothing about the document says is
	// wrong.
	//
	// Everything is measured again afterwards, because provoking a repaint
	// means changing the layout and the panel does not always come back exactly
	// where it was.
	if err := stir(ctx, frame); err != nil {
		return nil, err
	}
	if err := frame.Reread(ctx); err != nil {
		return nil, err
	}
	if !frame.Open() {
		return nil, nil
	}

	started := time.Now()
	shot, err := capture(ctx, frame)
	if err != nil {
		return nil, err
	}
	log.Debug("grid photographed", "attempt", 0, "took", time.Since(started))

	for attempt := range paintAttempts {
		// And again before looking again, or the page will sit exactly as it is
		// and the second photograph will never be answered.
		if err := stir(ctx, frame); err != nil {
			return nil, err
		}
		if err := input.Pause(ctx, loadWaitMin, loadWaitMax); err != nil {
			return nil, err
		}
		if err := frame.Reread(ctx); err != nil {
			return nil, err
		}
		if !frame.Open() {
			return nil, nil
		}

		started := time.Now()
		bounded, cancel := context.WithTimeout(ctx, repaintTimeout)
		again, err := capture(bounded, frame)
		cancel()

		// A screenshot request is only answered once the page has a frame to
		// give. A grid that has finished fading in has nothing left to paint,
		// so the request waits — measured, well past the whole timeout for the
		// solve. That silence is the answer: nothing has moved.
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			log.Debug("nothing repainted", "attempt", attempt+1)
			if !blank(shot, frame.View.Tiles) {
				break
			}
			continue
		}
		if err != nil {
			return nil, err
		}

		steady := settled(shot, again, frame.View.Tiles)
		shot = again
		log.Debug("grid photographed", "attempt", attempt+1, "steady", steady,
			"blank", blank(shot, frame.View.Tiles), "took", time.Since(started))
		if steady && !blank(shot, frame.View.Tiles) {
			break
		}
	}

	if opts.SavePanels != "" {
		if err := keep(opts.SavePanels, shot, frame.View); err != nil {
			// Worth saying, not worth abandoning a challenge over.
			return nil, fmt.Errorf("challenge: saving the panel: %w", err)
		}
	}

	path, cleanup, err := writeTemp(shot)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	return ask(ctx, opts.Solver, path, frame.View)
}

// keep writes the grid and what postern read off it, for calibrating a solver
// later against grids that cost nothing to replay.
func keep(dir string, shot []byte, view View) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Named for when it was taken, so a directory sorts into the order the
	// challenges were served.
	stem := filepath.Join(dir, strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.WriteFile(stem+".png", shot, 0o644); err != nil {
		return err
	}

	meta, err := json.Marshal(struct {
		Prompt  string `json:"prompt"`
		Columns string `json:"columns"`
		Tiles   string `json:"tiles"`
	}{view.Prompt, strconv.Itoa(view.Columns()), encodeTiles(view.Tiles)})
	if err != nil {
		return err
	}
	return os.WriteFile(stem+".json", meta, 0o644)
}

// reload asks the widget for a different challenge, for when the solver has
// said it cannot answer this one. The panel has a button for exactly this, and
// pressing it is what a person does when handed a puzzle in a language they do
// not read.
//
// It reports whether a different grid actually arrived. reCAPTCHA is under no
// obligation to produce one, and pressing the button at a panel that has
// decided to keep this grid is a loop with no exit: measured at seventeen
// identical rounds — the same picture, the same score to two decimal places —
// burning a two-minute budget that had nowhere to go.
func reload(ctx context.Context, frame *Frame, before []string) (bool, error) {
	if frame.View.Reload == nil {
		return false, fmt.Errorf("challenge: solver passed on %q and the panel offers no other",
			frame.View.Prompt)
	}

	x, y := frame.View.Reload.Center()
	if err := click(ctx, frame, x, y); err != nil {
		return false, fmt.Errorf("challenge: reload: %w", err)
	}

	// Wait for a genuinely different grid rather than photographing the old one
	// again, which would pass right back to the solver and stall the round.
	return await(ctx, frame, before, replaceAttempts)
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

	if err := buttonReady(ctx, frame); err != nil {
		return err
	}
	if frame.View.Button == nil {
		// Nothing to press. The panel is between states rather than broken, and
		// the caller will find it again on the next poll.
		return nil
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

		// await re-read the panel, so the button measured above may be gone.
		if frame.View.Button == nil {
			return nil
		}
	}

	focused, err := frame.Focus(verifySelector)
	if err != nil {
		return err
	}
	if !focused {
		// Out of good options: the button will not take the keyboard and the
		// document says a click would land elsewhere. Click at it anyway —
		// the reading may simply be stale — rather than abandoning a challenge
		// that is otherwise answered.
		log.Debug("verify button will not take focus, clicking at it anyway",
			"buttonY", frame.View.Button.Y, "panelHeight", frame.View.Height)

		x, y := frame.View.Button.Center()
		return click(ctx, frame, x, y)
	}

	log.Debug("verify by keyboard", "label", frame.View.Button.Label,
		"buttonY", frame.View.Button.Y, "panelHeight", frame.View.Height)
	return chromedp.Run(ctx, chromedp.ActionFunc(input.PressEnter))
}

// buttonReady waits for the panel's button to be worth pressing. reCAPTCHA
// disables it while it considers the previous answer, and a disabled button
// takes neither a click nor a keystroke.
func buttonReady(ctx context.Context, frame *Frame) error {
	for range buttonAttempts {
		if b := frame.View.Button; b != nil && !b.Disabled {
			return nil
		}
		if err := input.Pause(ctx, buttonWaitMin, buttonWaitMax); err != nil {
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

// stir moves the pointer across the panel without clicking anything, to get the
// page painted.
//
// Under a virtual display nothing composites a frame while the page is idle, so
// the panel's opening animation stops partway and stays there: measured, the
// first grid of every run came back washed out, mixed with the page behind it,
// while every later grid — after the pointer had moved to click something — was
// clean. Moving the pointer is what a person does before choosing anyway.
func stir(ctx context.Context, frame *Frame) error {
	fromX, fromY := frame.Point(frame.View.Width+70, frame.View.Height+50)
	toX, toY := frame.Point(frame.View.Width/2, frame.View.Height/2)

	if err := chromedp.Run(ctx, input.Move(
		input.Point{X: fromX, Y: fromY},
		input.Point{X: toX, Y: toY},
	)); err != nil {
		return err
	}

	// Pointer events change nothing on screen — no cursor is composited under
	// a virtual display — so on their own the frozen fade stays frozen. What
	// does get the page painted is a change to the layout, and a view one pixel
	// taller and back is the smallest one there is.
	//
	// Measured, in the order they were tried: moving the pointer, scrolling a
	// pixel, asking for a screencast, and overriding the page's backdrop colour
	// all left the fade exactly where it was. Only the resize moved it.
	var size struct {
		W int64 `json:"w"`
		H int64 `json:"h"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`({ w: window.innerWidth, h: window.innerHeight })`, &size)); err != nil {
		return err
	}

	if err := chromedp.Run(ctx,
		emulation.SetDeviceMetricsOverride(size.W, size.H+1, 1, false),
		emulation.ClearDeviceMetricsOverride(),
	); err != nil {
		return err
	}

	// The panel is laid out again after that, and reCAPTCHA does not always
	// put it back where it was. Measuring it before it has settled photographs
	// a rectangle holding half the grid and half the page behind it, which
	// looks exactly like a solver that cannot see.
	return input.Pause(ctx, settleWaitMin, settleWaitMax)
}

// capture screenshots just the panel, so the solver sees the prompt and the
// grid and nothing else.
//
// The clip is in page coordinates while everything else here is in viewport
// coordinates, so the scroll has to be added back. Nothing scrolls in the usual
// case — the widget is position:fixed — but a target page opened at an anchor,
// or one that restores its scroll, would otherwise hand the solver a picture of
// somewhere else entirely while the clicks went to the right place. A blank
// screenshot and a model that finds nothing look identical from here.
func capture(ctx context.Context, frame *Frame) ([]byte, error) {
	x, y, w, h := frame.Viewport()

	var scroll struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`({ x: window.scrollX, y: window.scrollY })`, &scroll)); err != nil {
		return nil, fmt.Errorf("challenge: read scroll: %w", err)
	}

	var shot []byte
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		shot, err = page.CaptureScreenshot().
			WithFormat(page.CaptureScreenshotFormatPng).
			WithClip(&page.Viewport{
				X:      x + scroll.X,
				Y:      y + scroll.Y,
				Width:  w,
				Height: h,
				Scale:  1,
			}).
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
	if len(fields) == 0 {
		return nil, fmt.Errorf("challenge: the image solver command is blank")
	}
	args := append(fields[1:], imagePath)

	cmd := exec.CommandContext(runCtx, fields[0], args...)
	cmd.Env = append(os.Environ(),
		"POSTERN_PROMPT="+view.Prompt,
		"POSTERN_COLUMNS="+strconv.Itoa(view.Columns()),
		"POSTERN_TILES="+encodeTiles(view.Tiles),
	)

	out, err := cmd.Output()
	if err != nil {
		// Running out of time kills the solver mid-look, and "signal: killed"
		// reads like the solver crashed. It is the caller's own deadline, and
		// it is worth saying so — the caller turns this into "no token after".
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("challenge: out of time while the solver was looking: %w", ctxErr)
		}

		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if exit.ExitCode() == passExitCode {
				return nil, errPass
			}
			// Whatever it printed is the only diagnosis its author gets.
			if said := lastLines(exit.Stderr, solverErrLines); said != "" {
				return nil, fmt.Errorf("challenge: solver %q failed: %w: %s",
					fields[0], err, said)
			}
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

// lastLines returns the tail of what the solver said, for the error message.
func lastLines(out []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "; "))
}

// encodeTiles renders the grid for the solver's environment.
func encodeTiles(tiles []Box) string {
	parts := make([]string, 0, len(tiles))
	for _, t := range tiles {
		parts = append(parts, fmt.Sprintf("%.0f,%.0f,%.0f,%.0f", t.X, t.Y, t.W, t.H))
	}
	return strings.Join(parts, ";")
}

// blank reports that the grid has not been painted yet.
//
// A tile that has arrived is a photograph, and a photograph has variation in
// it. A tile still waiting is one flat colour — white, or the grey reCAPTCHA
// parks there. The document is not a reliable witness to this: it reports the
// pictures as loaded while they are still fading in, so the panel is asked the
// only question that settles it, which is what it looks like.
func blank(shot []byte, tiles []Box) bool {
	if len(tiles) == 0 {
		return false
	}

	img, err := png.Decode(bytes.NewReader(shot))
	if err != nil {
		// Not our decision to make on a screenshot we cannot read.
		return false
	}

	flat := 0
	for _, tile := range tiles {
		if uniform(img, tile) {
			flat++
		}
	}
	return flat*100 >= len(tiles)*blankTilesPercent
}

// settled reports that two photographs of the grid, taken moments apart, show
// the same picture — which is how a finished grid is told from one still
// fading in.
//
// Averaged rather than thresholded per pixel: a fade moves every pixel of every
// tile a little, which averages high, while a redrawn cursor moves a handful of
// pixels a lot, which does not.
func settled(before, after []byte, tiles []Box) bool {
	if len(tiles) == 0 || len(before) == 0 {
		return false
	}

	first, err := png.Decode(bytes.NewReader(before))
	if err != nil {
		return false
	}
	second, err := png.Decode(bytes.NewReader(after))
	if err != nil {
		return false
	}
	if !first.Bounds().Eq(second.Bounds()) {
		return false
	}

	var total, samples uint64
	for _, tile := range tiles {
		bounds := first.Bounds()
		x0, y0 := bounds.Min.X+int(tile.X), bounds.Min.Y+int(tile.Y)
		x1, y1 := min(x0+int(tile.W), bounds.Max.X), min(y0+int(tile.H), bounds.Max.Y)

		for y := y0; y < y1; y += blankStride {
			for x := x0; x < x1; x += blankStride {
				total += uint64(diff(grey(first.At(x, y)), grey(second.At(x, y))))
				samples++
			}
		}
	}

	return samples > 0 && total/samples <= settleRange
}

func grey(c color.Color) uint32 {
	r, g, b, _ := c.RGBA()
	return (r*299 + g*587 + b*114) / 1000
}

func diff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

// uniform reports whether a tile is all one colour, give or take the noise a
// JPEG leaves behind.
func uniform(img image.Image, tile Box) bool {
	bounds := img.Bounds()
	x0, y0 := bounds.Min.X+int(tile.X), bounds.Min.Y+int(tile.Y)
	x1, y1 := x0+int(tile.W), y0+int(tile.H)
	if x1 > bounds.Max.X {
		x1 = bounds.Max.X
	}
	if y1 > bounds.Max.Y {
		y1 = bounds.Max.Y
	}
	if x1-x0 < blankStride || y1-y0 < blankStride {
		return false
	}

	// Sampled rather than exhaustive: a tile is thousands of pixels and this
	// runs on every round. A grid that is genuinely blank is blank everywhere,
	// so every few pixels is as good as all of them.
	var lo, hi uint32 = 65535, 0
	for y := y0; y < y1; y += blankStride {
		for x := x0; x < x1; x += blankStride {
			r, g, b, _ := img.At(x, y).RGBA()
			grey := (r*299 + g*587 + b*114) / 1000
			if grey < lo {
				lo = grey
			}
			if grey > hi {
				hi = grey
			}
			if hi-lo > blankRange {
				return false
			}
		}
	}
	return true
}
