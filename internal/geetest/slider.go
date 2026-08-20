package geetest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"math"
	"math/rand/v2"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/puzzle"
)

// The slider challenge: a piece to drag into the notch it was cut from.

// Box is an element's position on screen.
type Box struct {
	X, Y, W, H float64
}

// widget is the parts of the slider that have to be found.
type widget struct {
	Bg     *Box `json:"bg"`
	Piece  *Box `json:"piece"`
	Handle *Box `json:"handle"`
}

// readWidget measures the puzzle.
//
// The handle is picked by walking the candidates rather than with a selector:
// the button that starts the challenge also carries "geetest_btn" and comes
// first in the document, so a selector silently grabs the wrong element. Class
// is read with getAttribute because on an SVG node className is an
// SVGAnimatedString, and a class filter written the obvious way passes every
// SVG element through.
const readWidget = `(() => {
  const box = sel => { const e = document.querySelector(sel); if (!e) return null;
    const r = e.getBoundingClientRect();
    if (!r.width) return null;
    return { X: r.x, Y: r.y, W: r.width, H: r.height }; };
  return {
    bg:    box('[class*=geetest_bg]'),
    piece: box('[class*=geetest_slice]'),
    handle: (() => {
      const e = [...document.querySelectorAll('[class*=geetest_btn]')]
        .find(n => {
          const cls = n.getAttribute('class') || '';
          const r = n.getBoundingClientRect();
          return !/btn_click/.test(cls) && r.width > 0 && r.width < 150;
        });
      if (!e) return null;
      const r = e.getBoundingClientRect();
      return { X: r.x, Y: r.y, W: r.width, H: r.height };
    })(),
  };
})()`

// pieceCentre reports where the piece sits relative to the picture. Read live
// rather than computed: it is the thing being aimed.
const pieceCentre = `(() => {
  const bg = document.querySelector('[class*=geetest_bg]');
  const p = document.querySelector('[class*=geetest_slice]');
  if (!bg || !p) return null;
  const b = bg.getBoundingClientRect(), r = p.getBoundingClientRect();
  return r.x - b.x + r.width / 2;
})()`

// SolveSlider drags the piece into its notch.
func SolveSlider(ctx context.Context) error {
	var w widget
	if err := evaluateInto(ctx, readWidget, &w); err != nil {
		return fmt.Errorf("geetest: measuring the slider: %w", err)
	}
	if w.Bg == nil || w.Piece == nil || w.Handle == nil {
		return fmt.Errorf("geetest: the slider is incomplete: bg=%v piece=%v handle=%v",
			w.Bg, w.Piece, w.Handle)
	}

	var shot []byte
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		var err error
		shot, err = page.CaptureScreenshot().WithClip(&page.Viewport{
			X: w.Bg.X, Y: w.Bg.Y, Width: w.Bg.W, Height: w.Bg.H, Scale: 1,
		}).Do(c)
		return err
	})); err != nil {
		return fmt.Errorf("geetest: capturing the picture: %w", err)
	}

	img, _, err := image.Decode(bytes.NewReader(shot))
	if err != nil {
		return fmt.Errorf("geetest: decoding the picture: %w", err)
	}
	notch, err := puzzle.FindNotch(img, int(w.Piece.W))
	if err != nil {
		return fmt.Errorf("geetest: %w", err)
	}
	return dragTo(ctx, *w.Handle, float64(notch.CentreX))
}

// dragTo carries the handle until the piece lands on want.
//
// The widget does not only check where the piece stops: landing it dead on the
// notch still failed while the gesture was wrong. Two things matter. The
// stroke is asymmetric — a hand accelerates hard and spends most of its time
// arriving — and it overshoots and comes back, because it is aiming at
// something. An earlier version measured the handle-to-piece ratio mid-drag,
// which meant stopping in the middle of a stroke; the ratio came back 1.000
// every time, so it is a constant and reading it cost more than it bought.
func dragTo(ctx context.Context, handle Box, want float64) error {
	sx, sy := handle.X+handle.W/2, handle.Y+handle.H/2

	var startAt float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(pieceCentre, &startAt)); err != nil {
		return fmt.Errorf("geetest: reading the piece: %w", err)
	}
	dist := want - startAt

	if err := chromedp.Run(ctx,
		chromedp.MouseEvent(input.MouseMoved, sx-40+rand.Float64()*12, sy-18+rand.Float64()*9),
		chromedp.Sleep(time.Duration(90+rand.IntN(120))*time.Millisecond),
		chromedp.MouseEvent(input.MouseMoved, sx, sy),
		chromedp.Sleep(time.Duration(120+rand.IntN(140))*time.Millisecond),
		chromedp.MouseEvent(input.MousePressed, sx, sy,
			chromedp.Button("left"), chromedp.ClickCount(1)),
		chromedp.Sleep(time.Duration(70+rand.IntN(110))*time.Millisecond),
	); err != nil {
		return err
	}

	// Vertical drift as a smoothed random walk: fresh noise on every sample
	// reads as a machine adding jitter, while a hand wanders and comes back.
	drift := 0.0
	for _, s := range strokes(dist) {
		drift = drift*0.82 + (rand.Float64()-0.5)*0.9
		if err := chromedp.Run(ctx, chromedp.MouseEvent(input.MouseMoved,
			sx+s.at, sy+drift,
			chromedp.Button("left"), chromedp.ButtonModifiers(1))); err != nil {
			return err
		}
		time.Sleep(s.in)
	}

	time.Sleep(time.Duration(120+rand.IntN(160)) * time.Millisecond)
	return chromedp.Run(ctx, chromedp.MouseEvent(input.MouseReleased, sx+dist, sy+drift,
		chromedp.Button("left"), chromedp.ClickCount(1)))
}

// stroke is one sample of the gesture: where to be, and how long to wait.
type stroke struct {
	at float64
	in time.Duration
}

// strokes builds the whole gesture up front, so it can be read without moving
// a mouse.
func strokes(dist float64) []stroke {
	steps := 34 + rand.IntN(12)
	over := 3 + rand.Float64()*5

	out := make([]stroke, 0, steps+3)
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		// Most of the distance early, most of the time late.
		e := 1 - math.Pow(1-t, 3)
		gap := 8 + rand.IntN(10)
		if rand.IntN(9) == 0 {
			gap += 25 + rand.IntN(45) // a hand is not a metronome
		}
		out = append(out, stroke{at: (dist + over) * e, in: time.Duration(gap) * time.Millisecond})
	}
	// Settle onto the target in decreasing corrections.
	for _, back := range []float64{0.55, 0.22, 0} {
		out = append(out, stroke{
			at: dist + over*back,
			in: time.Duration(45+rand.IntN(70)) * time.Millisecond,
		})
	}
	return out
}

func evaluateInto(ctx context.Context, js string, out any) error {
	var raw json.RawMessage
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &raw)); err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
