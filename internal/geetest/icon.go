package geetest

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/png"
	"math/rand/v2"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/puzzle"
)

// The icon challenge: a strip of pictograms, and a picture to find them in.

// readIcon measures the two things that have to be captured.
const readIcon = `(() => {
  const box = sel => { const e = document.querySelector(sel); if (!e) return null;
    const r = e.getBoundingClientRect();
    if (!r.width) return null;
    return { X: r.x, Y: r.y, W: r.width, H: r.height }; };
  return { bg: box('[class*=geetest_bg]'), prompt: box('[class*=geetest_ques_tips]') };
})()`

// IconTries is how many pictures SolveIcon will answer before giving up.
//
// A refused answer is not the end of the challenge: the widget says to try
// again and puts up a fresh picture, which is the same offer it makes to a
// person who misread the first one. Answering that one too is the difference
// between the odds of one attempt and the odds of not failing four times over,
// and nothing else available moves the number that far.
//
// Four, not more. Every attempt past the first is one the vendor did not need
// to serve, and the returns fall off geometrically: at two attempts in three
// going right, a fourth is worth two challenges in a hundred.
const IconTries = 4

// SolveIcon clicks the icons the prompt asks for, in the order it asks, and
// answers the next picture when one is refused.
//
// It answers whatever picture it is given. Refreshing until a picture looked
// readable was tried — the widget's refresh button is an ordinary control, and
// a person who cannot make out a drawing does ask for another — but selecting
// on confidence needs a confidence worth the name, and this one was calibrated
// against two captured challenges, only one of which had a known answer. It
// measured 0/5 while tripling the requests made of the vendor, so it is gone.
// The confidence is still reported by PairIcons, for a caller that has the
// data to calibrate it. Answering a picture the widget has already refused is
// a different thing: it costs a request only when the last one was wrong, and
// it is the widget itself that offers the next picture.
func SolveIcon(ctx context.Context) error {
	var last error
	for try := range IconTries {
		if try > 0 {
			// The widget puts up the next picture on its own; this waits for
			// it to finish doing so rather than reading the old one again.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2500 * time.Millisecond):
			}
		}
		if err := solveIconOnce(ctx); err != nil {
			// A picture that could not be read at all is worth another
			// picture, not an abandoned challenge — but only if the widget
			// still has one to give.
			last = err
			if !iconIsWaiting(ctx) {
				return err
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(4 * time.Second):
		}
		done, said := Verdict(ctx)
		if done {
			return nil
		}
		last = fmt.Errorf("geetest: the icon challenge was refused: %s", said)
		if !iconIsWaiting(ctx) {
			return last
		}
	}
	return last
}

// iconIsWaiting says whether there is still a picture on screen to answer.
func iconIsWaiting(ctx context.Context) bool {
	var w struct{ Bg *Box }
	if err := evaluateInto(ctx, readIcon, &w); err != nil {
		return false
	}
	return w.Bg != nil
}

// Verdict reads what the widget says about the answer just given. Read from
// its words rather than from a class name: this version has no success element,
// so a selector reports failure on a challenge that visibly passed. Not
// specific to the icon challenge — every type reports itself the same way.
func Verdict(ctx context.Context) (bool, string) {
	var out struct {
		Success bool   `json:"success"`
		Said    string `json:"said"`
	}
	if err := evaluateInto(ctx, `(() => {
	  const t = [...document.querySelectorAll('[class*=geetest_]')]
	    .map(e => (e.innerText||'').trim()).filter(Boolean);
	  return {
	    success: t.some(s => /verification success/i.test(s)),
	    said: t.slice(0, 2).join(' | '),
	  };
	})()`, &out); err != nil {
		return false, err.Error()
	}
	return out.Success, out.Said
}

func solveIconOnce(ctx context.Context) error {
	var w struct {
		Bg     *Box `json:"bg"`
		Prompt *Box `json:"prompt"`
	}
	if err := evaluateInto(ctx, readIcon, &w); err != nil {
		return fmt.Errorf("geetest: measuring the icon challenge: %w", err)
	}
	if w.Bg == nil || w.Prompt == nil {
		return fmt.Errorf("geetest: the icon challenge is incomplete: bg=%v prompt=%v",
			w.Bg, w.Prompt)
	}

	picture, err := capture(ctx, *w.Bg)
	if err != nil {
		return fmt.Errorf("geetest: capturing the picture: %w", err)
	}
	prompt, err := capture(ctx, *w.Prompt)
	if err != nil {
		return fmt.Errorf("geetest: capturing the prompt: %w", err)
	}

	wanted := puzzle.FindPictograms(prompt, 140)
	// 200 pixels: smaller than any drawn icon and larger than the flecks of
	// stray colour these backgrounds carry.
	found := puzzle.FindIcons(picture, 200, len(wanted))
	pairing, err := puzzle.PairIcons(wanted, found)
	if err != nil {
		return fmt.Errorf("geetest: %w", err)
	}

	for _, j := range pairing.Order {
		icon := found[j]
		if err := Click(ctx, w.Bg.X+float64(icon.CentreX),
			w.Bg.Y+float64(icon.CentreY)); err != nil {
			return err
		}
		time.Sleep(time.Duration(300+rand.IntN(350)) * time.Millisecond)
	}

	// The picks are only submitted once they are all in.
	return clickIfPresent(ctx, "[class*=geetest_submit]:not([class*=geetest_disable])")
}

func capture(ctx context.Context, b Box) (image.Image, error) {
	raw, err := captureRaw(ctx, b)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	return img, err
}

// captureRaw returns the encoded screenshot of one element.
func captureRaw(ctx context.Context, b Box) ([]byte, error) {
	var raw []byte
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		var e error
		raw, e = page.CaptureScreenshot().WithClip(&page.Viewport{
			X: b.X, Y: b.Y, Width: b.W, Height: b.H, Scale: 1,
		}).Do(c)
		return e
	}))
	return raw, err
}

// clickIfPresent presses something when it is there, and says nothing when it
// is not: not every challenge has a submit button.
func clickIfPresent(ctx context.Context, sel string) error {
	var p struct{ X, Y, W float64 }
	js := fmt.Sprintf(`(() => { const e = document.querySelector(%q);
	  if (!e) return null; const r = e.getBoundingClientRect();
	  return { X: r.x + r.width/2, Y: r.y + r.height/2, W: r.width }; })()`, sel)
	if err := evaluateInto(ctx, js, &p); err != nil || p.W == 0 {
		return nil
	}
	return Click(ctx, p.X, p.Y)
}
