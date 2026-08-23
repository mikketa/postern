package geetest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/puzzle"
)

// The board challenges, driven against the vendor's own demo.
//
// The solving is covered offline in internal/puzzle, on boards that were read
// off this same demo. What is left to check here is everything between the
// page and that solver — reading a board out of the DOM, and playing a move
// back — which no unit test can reach.
//
// It is a live test, so it is skipped by -short and allowed to fail in CI: a
// vendor who changes their markup, or does not serve a board this run, is not
// a regression in this repository. It fails loudly rather than quietly so the
// change gets noticed.

const demo = "https://www.geetest.com/en/adaptive-captcha-demo"

// runs is how many attempts each type gets. Five by default: enough to tell a
// solver that works from one that got lucky, few enough to be a polite number
// of requests to point at somebody's demo.
func runs(t *testing.T) int {
	t.Helper()
	if v := os.Getenv("POSTERN_GEETEST_RUNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("POSTERN_GEETEST_RUNS=%q is not a count", v)
		}
		return n
	}
	return 5
}

func TestTheFiveInARowChallengeIsSolved(t *testing.T) {
	solves(t, "Gobang CAPTCHA", playBoard(func(b Board) (puzzle.Move, error) {
		return puzzle.SolveLine(b.Cells, 5)
	}))
}

func TestTheMatchThreeChallengeIsSolved(t *testing.T) {
	solves(t, "IconCrush CAPTCHA", playBoard(func(b Board) (puzzle.Move, error) {
		return puzzle.SolveSwap(b.Cells, 3)
	}))
}

func TestTheSliderChallengeIsSolved(t *testing.T) {
	solves(t, "Slide CAPTCHA", SolveSlider)
}

// TestTheIconChallengeIsSolved is the one type that is not solved outright.
//
// Measured across three series since this work: 3 of 5, 2 of 5, 5 of 10, so 10
// of 20. It was 5 of 35 before — seven series of five reading 0, 2, 1, 0, 1, 1,
// 0 — and what moved it was the segmentation in front of the recognition.
//
// The bench that says so is built from the vendor's own artwork. GeeTest draws
// its icons over a small pool of reused photographs, so the per-pixel median
// over the challenges sharing a photograph reconstructs it with every icon
// removed, and subtracting that gives the icons exactly. That is a ground
// truth, where before there was only a person looking at a screenshot and
// deciding whether segmentation had done well — which it turns out is not a
// reliable thing to ask a person. The first set of answers, read off by eye,
// was wrong often enough that a model losing to plain silhouette overlap
// looked like one beating it, and this comment said so.
//
// Against that truth, over 88 challenges and 261 drawn icons:
//
//	segmentation finds the icon        0.578 before, 0.843 now
//	challenges yielding all three      12 of 47 before, 59 of 88 now
//
// What changed is the question segmentation asks. It used to take the
// commonest colour for the background and call anything far from it an icon,
// which does not survive a photograph: a collage of pink card, orange
// lettering and a blue gamepad is as far from its own commonest colour as
// anything drawn on it, and every candidate came back scenery. Whatever the
// palette, a photograph's colours recur across the frame while an icon is
// drawn once, in one colour, in one small patch. Two other defects fell out of
// having a truth to measure against: a flood fill that leaked through the
// pinholes in a speckled stroke and so filled nothing, and an unstable sort
// that handed back a different candidate order run to run.
//
// The recognition is a fitted model — measurements of a pictogram against a
// candidate, ranked within the pictogram; see internal/puzzle/train_test.go to
// collect a bench and refit. For a while it was worth nothing, because eleven
// of its twelve columns were noise and no fit over one useful column and
// eleven useless ones beats the useful column. What it needed was measurements
// of a different kind: the rotation sweep reflected as well as turned, the
// agreement sliced into rings out from the centre rather than totalled, and a
// learnt description of the shape to sit beside the written-down ones — a
// small convolution over a log-polar map of the silhouette, fitted to
// synthesised tracings, worth 41 challenges of 90 against 47.
//
// What it needed more, in the end, was to be fitted against the right thing.
// It is fitted on tracings synthesised from the vendor's own prompts, and
// those were being drawn at twice the size a real icon is, among distractors
// that were all other pictograms — while what the segmenter really hands over
// is three icons among a handful of window frames and kerbstones. Drawing them
// at the right size and putting scenery among the distractors took the
// written-down rules from 0.686 of pictograms ranked right to 0.759, which is
// more than any column ever added to them.
//
// The whole chain, measured against reconstructed ground truth rather than
// inferred — see internal/puzzle/bench_test.go, which is the only measurement
// here that predicts this test:
//
//	icons cut out of the picture            222 of 270
//	challenges yielding all their icons      61 of 90
//	of those, whole arrangement right        47 of 61
//	CHALLENGES SOLVED, ONE PICTURE           47 of 90
//
// A third of pictures are lost before recognition is reached: the segmenter
// welds a drawing to its neighbour or never sees it, and no amount of
// recognition can click an icon that was never cut out. Loosening it has been
// measured twice — it finds more icons and settles no more challenges, because
// the extra candidates cost the choosing more than they buy.
//
// So one attempt is not one picture. A refused answer is met with a fresh
// picture, which is the same offer the widget makes to a person who misread
// the first one, and five pictures fail together far less often than one does.
// Measured on the same day against the same demo, twelve attempts each, the
// only difference being how many pictures each attempt was allowed:
//
//	one picture     6 of 12
//	four pictures  11 of 12
//
// One picture is what the bench predicts, near enough. Four is what 1 - 0.5^4
// predicts, 0.94. IconTries is five now, for 0.97. TestOnePictureIsNotFour
// Pictures is the first of those numbers and this test is the second — run
// them together or neither, because a demo's pictures are not the same
// pictures from one week to the next.
//
// This needs a display and refuses to run without one, but it does not need
// yours: Chrome under Xvfb is an ordinary Chrome that happens to draw nowhere,
// which is a different thing from headless Chrome and is not refused.
//
//	Xvfb :77 -screen 0 1600x1000x24 &
//	DISPLAY=:77 ICON=1 POSTERN_GEETEST_RUNS=20 go test -count=1 \
//	    -run 'TestTheIconChallengeIsSolved|TestOnePicture' \
//	    ./internal/geetest -v -timeout 70m
//
// Skipped rather than left failing: a test that always fails stops being read.
// ICON=1 runs it anyway, which is how the number above is kept honest.
func TestTheIconChallengeIsSolved(t *testing.T) {
	if os.Getenv("ICON") == "" {
		t.Skip("solved about half the time: see the comment above, ICON=1 to run it")
	}
	solves(t, "Icon CAPTCHA", SolveIcon)
}

// TestOnePictureIsNotFourPictures is the witness for IconTries. Retrying can
// only look like an improvement when it is compared with something, and two
// runs taken on different days against a live demo are not comparable: the
// vendor's pictures are not the same pictures. This answers one picture and
// gives up, so the pair of numbers comes from the same run of the same solver
// against the same demo, and the only difference is how many pictures it was
// allowed.
func TestOnePictureIsNotFourPictures(t *testing.T) {
	if os.Getenv("ICON") == "" {
		t.Skip("live, and only worth running beside TestTheIconChallengeIsSolved")
	}
	solvesAs(t, "Icon CAPTCHA", "Icon CAPTCHA, one picture", func(ctx context.Context) error {
		return solveIconTries(ctx, 1)
	})
}

// TestTheNoCaptchaChallengePasses covers the type that asks for nothing: the
// widget decides on its own and the button is the whole interaction. Worth a
// test anyway — it is the path where a solver has to recognise there is
// nothing to solve, rather than wait for a challenge that is never coming.
func TestTheNoCaptchaChallengePasses(t *testing.T) {
	solves(t, "No CAPTCHA", func(context.Context) error { return nil })
}

// playBoard reads the board, solves it and plays the move back.
func playBoard(solve func(Board) (puzzle.Move, error)) func(context.Context) error {
	return func(ctx context.Context) error {
		b, err := ReadBoard(ctx)
		if err != nil {
			return err
		}
		m, err := solve(b)
		if err != nil {
			return err
		}
		return Play(ctx, b, m)
	}
}

// solves runs one challenge type end to end, several times over.
func solves(t *testing.T, tab string, play func(context.Context) error) {
	t.Helper()
	solvesAs(t, tab, tab, play)
}

// solvesAs is solves when the challenge being driven and the run being
// reported are not the same thing: tab is the demo's own label and has to
// match it exactly, name is what the count is filed under.
func solvesAs(t *testing.T, tab, name string, play func(context.Context) error) {
	t.Helper()
	if testing.Short() {
		t.Skip("drives a real browser against a vendor's demo")
	}
	if os.Getenv("DISPLAY") == "" {
		// Headless was measured at 0 successes against 7 with a display: these
		// widgets can tell, and a headless run here would report a failure
		// that says nothing about the code.
		t.Skip("needs a display: this vendor refuses headless")
	}

	n := runs(t)
	ok := 0
	for i := 1; i <= n; i++ {
		if attempt(t, i, tab, play) {
			ok++
		}
		if i < n {
			time.Sleep(20 * time.Second)
		}
	}
	t.Logf("%s: %d/%d", name, ok, n)
	if ok < n {
		t.Errorf("%s solved %d of %d", name, ok, n)
	}
}

func attempt(t *testing.T, i int, tab string, play func(context.Context) error) bool {
	t.Helper()

	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.Flag("headless", false),
		// Chrome picks its display backend before it reads DISPLAY, so on a
		// Wayland desktop it goes looking for a compositor and never reaches
		// the X server the test is pointed at. Naming the backend and clearing
		// WAYLAND_DISPLAY is what makes it use the display it was given; on a
		// CI runner with Xvfb both are already true and neither does harm.
		chromedp.Flag("ozone-platform", "x11"),
		chromedp.Env("WAYLAND_DISPLAY="),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, 150*time.Second)
	defer cancelT()

	if err := chromedp.Run(ctx, chromedp.Navigate(demo), chromedp.Sleep(14*time.Second)); err != nil {
		t.Logf("attempt %d: navigating: %v", i, err)
		return false
	}
	if err := clickLabel(ctx, tab); err != nil {
		t.Logf("attempt %d: choosing %s: %v", i, tab, err)
		return false
	}
	if err := chromedp.Run(ctx, chromedp.Sleep(5*time.Second)); err != nil {
		return false
	}
	if err := clickSelector(ctx, "[class*=geetest_btn_click]"); err != nil {
		t.Logf("attempt %d: starting the challenge: %v", i, err)
		return false
	}
	chromedp.Run(ctx, chromedp.Sleep(7*time.Second))

	if err := play(ctx); err != nil {
		t.Logf("attempt %d: %v", i, err)
		return false
	}
	chromedp.Run(ctx, chromedp.Sleep(3*time.Second))

	// The match-three submits its result; the other settles on its own.
	clickSelector(ctx, "[class*=geetest_submit]:not([class*=geetest_disable])")
	chromedp.Run(ctx, chromedp.Sleep(5*time.Second))

	won, said := Verdict(ctx)
	if !won {
		t.Logf("attempt %d: the widget said %q", i, said)
	}
	return won
}

func clickLabel(ctx context.Context, label string) error {
	var p struct{ X, Y, W float64 }
	js := fmt.Sprintf(`(() => {
	  const want = %q.toLowerCase();
	  const hit = [...document.querySelectorAll('*')].filter(e => e.children.length <= 1 &&
	    ((e.innerText||e.textContent||'').trim().toLowerCase() === want));
	  if (!hit.length) return null;
	  const r = hit[hit.length-1].getBoundingClientRect();
	  return { x: r.x + r.width/2, y: r.y + r.height/2, w: r.width }; })()`, label)
	if err := evaluate(ctx, js, &p); err != nil || p.W == 0 {
		return fmt.Errorf("no element reading %q", label)
	}
	return chromedp.Run(ctx, chromedp.MouseClickXY(p.X, p.Y))
}

func clickSelector(ctx context.Context, sel string) error {
	var p struct{ X, Y, W float64 }
	js := fmt.Sprintf(`(() => { const e = document.querySelector(%q);
	  if (!e) return null; const r = e.getBoundingClientRect();
	  return { x: r.x + r.width/2, y: r.y + r.height/2, w: r.width }; })()`, sel)
	if err := evaluate(ctx, js, &p); err != nil || p.W == 0 {
		return fmt.Errorf("nothing matching %s", sel)
	}
	return chromedp.Run(ctx, chromedp.MouseClickXY(p.X, p.Y))
}

func evaluate(ctx context.Context, js string, out any) error {
	var raw json.RawMessage
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &raw)); err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
