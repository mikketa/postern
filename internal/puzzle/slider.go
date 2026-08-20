// Package puzzle reads a slider captcha off a picture.
//
// A slider challenge shows a piece and a background with the piece's silhouette
// stamped into it. Solving it is knowing how far along that notch sits; the
// dragging is somebody else's problem.
package puzzle

import (
	"fmt"
	"image"
	"math"
)

// Notch is where the piece belongs.
type Notch struct {
	// CentreX is the notch's centre, in pixels from the left of the picture.
	CentreX int

	// Confidence is how well the notch matched the piece, from -1 to 1. It is
	// reported rather than acted on: a caller that solves in a loop can use a
	// weak match to decide to ask for a different picture instead of spending
	// an attempt on a guess.
	Confidence float64
}

// FindNotch locates the piece's silhouette in the background.
//
// Brightness is the obvious signal and the wrong one. A picture with something
// dark in it — a navy cube, a shadow — has a darkest square that is not the
// notch, and aiming at it puts the piece tens of pixels short: measured at 44px
// out on a strip whose notch had been confirmed by eye.
//
// What is actually distinctive is the shape. The piece and the notch are the
// same silhouette, drawn twice, and the piece is right there in the picture to
// be used as a template. Matching is done on gradients rather than on colour
// because the piece is rendered light and the notch dark: their interiors have
// nothing in common, while their outlines are the same curve.
func FindNotch(img image.Image, piece int) (Notch, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if piece <= 0 || piece > w || piece > h {
		return Notch{}, fmt.Errorf("puzzle: piece of %dpx does not fit a %dx%d picture",
			piece, w, h)
	}

	py := pieceBand(img, piece)
	tpl := edges(img, b.Min.X, b.Min.Y+py, piece, piece)

	// The notch is at the same height as the piece — a slider only moves
	// sideways — so the search is over x, with a little slack in y for
	// rounding and for a piece whose band was found a pixel off.
	bestX, best := -1, -2.0
	for dy := -3; dy <= 3; dy++ {
		y := py + dy
		if y < 0 || y+piece > h {
			continue
		}
		// The piece occupies the left edge, so the notch is always to its
		// right. Skipping that band stops the search matching the piece
		// against itself, which is a perfect score and a useless answer.
		for x := piece + 8; x+piece <= w; x++ {
			if s := ncc(tpl, edges(img, b.Min.X+x, b.Min.Y+y, piece, piece)); s > best {
				bestX, best = x, s
			}
		}
	}
	if bestX < 0 {
		return Notch{}, fmt.Errorf("puzzle: no notch found in a %dx%d picture", w, h)
	}
	return Notch{CentreX: bestX + piece/2, Confidence: best}, nil
}

// luminance is perceived brightness, the weighting the eye uses.
func luminance(img image.Image, x, y int) float64 {
	r, g, b, _ := img.At(x, y).RGBA()
	return 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8)
}

// edges is the gradient magnitude over a region: the outlines, without the
// colours that fill them.
func edges(img image.Image, x0, y0, w, h int) []float64 {
	b := img.Bounds()
	at := func(x, y int) float64 {
		return luminance(img,
			min(max(x0+x, b.Min.X), b.Max.X-1),
			min(max(y0+y, b.Min.Y), b.Max.Y-1))
	}
	out := make([]float64, w*h)
	for y := range h {
		for x := range w {
			out[y*w+x] = math.Hypot(at(x+1, y)-at(x-1, y), at(x, y+1)-at(x, y-1))
		}
	}
	return out
}

// ncc is normalised cross-correlation: how alike two edge maps are, whatever
// their contrast. Without the normalisation a bland region of the picture would
// lose to a busy one that happens to be nothing like the piece.
func ncc(a, c []float64) float64 {
	var ma, mc float64
	for i := range a {
		ma += a[i]
		mc += c[i]
	}
	ma /= float64(len(a))
	mc /= float64(len(c))

	var num, da, dc float64
	for i := range a {
		u, v := a[i]-ma, c[i]-mc
		num += u * v
		da += u * u
		dc += v * v
	}
	if da == 0 || dc == 0 {
		return 0
	}
	return num / math.Sqrt(da*dc)
}

// pieceBand finds the piece down the left edge, so a caller does not have to
// say where it is. The piece is the busiest square there: it is a cut-out with
// a lit border, against whatever the background happens to be.
func pieceBand(img image.Image, piece int) int {
	b := img.Bounds()
	best, bestE := 0, -1.0
	for y := 0; y+piece <= b.Dy(); y++ {
		var s float64
		for _, v := range edges(img, b.Min.X, b.Min.Y+y, piece, piece) {
			s += v
		}
		if s > bestE {
			best, bestE = y, s
		}
	}
	return best
}
