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
	solves(t, "Gobang CAPTCHA", func(b Board) (puzzle.Move, error) {
		return puzzle.SolveLine(b.Cells, 5)
	})
}

func TestTheMatchThreeChallengeIsSolved(t *testing.T) {
	solves(t, "IconCrush CAPTCHA", func(b Board) (puzzle.Move, error) {
		return puzzle.SolveSwap(b.Cells, 3)
	})
}

// solves runs one challenge type end to end, several times over.
func solves(t *testing.T, tab string, solve func(Board) (puzzle.Move, error)) {
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
		if attempt(t, i, tab, solve) {
			ok++
		}
		if i < n {
			time.Sleep(20 * time.Second)
		}
	}
	t.Logf("%s: %d/%d", tab, ok, n)
	if ok < n {
		t.Errorf("%s solved %d of %d", tab, ok, n)
	}
}

func attempt(t *testing.T, i int, tab string, solve func(Board) (puzzle.Move, error)) bool {
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

	b, err := ReadBoard(ctx)
	if err != nil {
		t.Logf("attempt %d: %v", i, err)
		return false
	}
	m, err := solve(b)
	if err != nil {
		t.Logf("attempt %d: %v", i, err)
		return false
	}
	if err := Play(ctx, b, m); err != nil {
		t.Logf("attempt %d: playing %s: %v", i, m, err)
		return false
	}
	chromedp.Run(ctx, chromedp.Sleep(3*time.Second))

	// The match-three submits its result; the other settles on its own.
	clickSelector(ctx, "[class*=geetest_submit]:not([class*=geetest_disable])")
	chromedp.Run(ctx, chromedp.Sleep(5*time.Second))

	won, said := verdict(ctx)
	if !won {
		t.Logf("attempt %d: played %s and the widget said %q", i, m, said)
	}
	return won
}

// verdict reads what the widget says about the attempt. Read from its words
// rather than from a class name: there is no success element in this version,
// so a selector reports failure on a challenge that visibly passed.
func verdict(ctx context.Context) (bool, string) {
	var out struct {
		Success bool   `json:"success"`
		Said    string `json:"said"`
	}
	var raw json.RawMessage
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
	  const t = [...document.querySelectorAll('[class*=geetest_]')]
	    .map(e => (e.innerText||'').trim()).filter(Boolean);
	  return {
	    success: t.some(s => /verification success/i.test(s)),
	    said: t.slice(0, 2).join(' | '),
	  };
	})()`, &raw)); err != nil {
		return false, err.Error()
	}
	json.Unmarshal(raw, &out)
	return out.Success, out.Said
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
