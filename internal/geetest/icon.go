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

// SolveIcon clicks the icons the prompt asks for, in the order it asks.
func SolveIcon(ctx context.Context) error {
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

	// 200 pixels: smaller than any drawn icon and larger than the flecks of
	// stray colour these backgrounds carry.
	found := puzzle.FindIcons(picture, 200)
	wanted := puzzle.FindPictograms(prompt, 140)
	order, err := puzzle.PairIcons(wanted, found)
	if err != nil {
		return fmt.Errorf("geetest: %w", err)
	}

	for _, j := range order {
		icon := found[j]
		if err := Click(ctx, w.Bg.X+float64(icon.CentreX), w.Bg.Y+float64(icon.CentreY)); err != nil {
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
