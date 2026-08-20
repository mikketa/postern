package puzzle

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// The pictures are built here rather than checked in. Fixtures cut from a
// vendor's live challenge would be their artwork sitting in this repository,
// and they would also be a handful of samples standing in for every picture the
// vendor owns. Drawing them means the awkward cases can be asked for by name —
// including the one that actually broke this.

const (
	picW, picH = 300, 200
	pieceSize  = 60
	pieceTop   = 70
)

// silhouette is the piece's shape: a square with a tab on one side and a bite
// out of the other. Deliberately not a plain square — a square matches every
// square in the picture equally well, which would let a broken matcher pass.
func silhouette(x, y int) bool {
	if x < 0 || y < 0 || x >= pieceSize || y >= pieceSize {
		return false
	}
	const r = pieceSize / 5
	cx, cy := float64(x), float64(y)

	// A bite taken out of the left edge.
	if math.Hypot(cx, cy-pieceSize/2) < r {
		return false
	}
	// A tab standing off the right edge is part of the shape...
	if cx > pieceSize-2 {
		return math.Hypot(cx-pieceSize, cy-pieceSize/2) < r
	}
	return true
}

// background is a deterministic pattern with structure at several scales, so
// the gradients it produces are as varied as a photograph's.
func background(x, y int) color.RGBA {
	v := 120 +
		40*math.Sin(float64(x)/17) +
		30*math.Cos(float64(y)/11) +
		20*math.Sin(float64(x+y)/7)
	return color.RGBA{uint8(clamp(v)), uint8(clamp(v * 0.8)), uint8(clamp(v * 1.1)), 255}
}

func clamp(v float64) float64 { return math.Max(0, math.Min(255, v)) }

// strip draws a challenge: the piece on the left, the notch at notchX, and
// optionally a decoy — a large region darker than the notch itself.
func strip(notchX int, decoy bool) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, picW, picH))
	for y := range picH {
		for x := range picW {
			img.Set(x, y, background(x, y))
		}
	}

	// The decoy: darker than the notch and much bigger, placed away from it.
	// This is the shape of the bug this package was written to fix — the
	// darkest square in the picture is not the notch, and aiming at it put the
	// piece 44px short on a real challenge.
	if decoy {
		for y := 30; y < 130; y++ {
			for x := 95; x < 165; x++ {
				img.Set(x, y, color.RGBA{6, 8, 20, 255})
			}
		}
	}

	// The notch: the silhouette darkened, with a lit rim, exactly as a slider
	// challenge renders it.
	for y := range pieceSize {
		for x := range pieceSize {
			if !silhouette(x, y) {
				continue
			}
			px, py := notchX+x, pieceTop+y
			if !inside(px, py) {
				continue
			}
			c := background(px, py)
			if rim(x, y) {
				img.Set(px, py, color.RGBA{235, 235, 235, 255})
			} else {
				img.Set(px, py, color.RGBA{c.R / 3, c.G / 3, c.B / 3, 255})
			}
		}
	}

	// The piece, drawn light against the left edge.
	for y := range pieceSize {
		for x := range pieceSize {
			if !silhouette(x, y) {
				continue
			}
			px, py := x, pieceTop+y
			c := background(notchX+x, pieceTop+y)
			if rim(x, y) {
				img.Set(px, py, color.RGBA{255, 255, 255, 255})
			} else {
				img.Set(px, py, color.RGBA{
					uint8(clamp(float64(c.R)*1.4 + 40)),
					uint8(clamp(float64(c.G)*1.4 + 40)),
					uint8(clamp(float64(c.B)*1.4 + 40)), 255})
			}
		}
	}
	return img
}

func inside(x, y int) bool { return x >= 0 && y >= 0 && x < picW && y < picH }

// rim is true on the silhouette's outline.
func rim(x, y int) bool {
	for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		if !silhouette(x+d[0], y+d[1]) {
			return true
		}
	}
	return false
}

func TestTheNotchIsFound(t *testing.T) {
	// Spread across the picture, including hard against the right edge where
	// the search window runs out.
	for _, at := range []int{90, 130, 180, 210, picW - pieceSize} {
		n, err := FindNotch(strip(at, false), pieceSize)
		if err != nil {
			t.Fatalf("notch at %d: %v", at, err)
		}
		if want := at + pieceSize/2; abs(n.CentreX-want) > 4 {
			t.Errorf("notch at %d: found centre %d, want %d (±4) — a slider "+
				"puzzle allows a few pixels and no more", at, n.CentreX, want)
		}
	}
}

// TestSomethingDarkerThanTheNotchDoesNotWin is the regression. Brightness was
// the first thing tried and it fails exactly here: the decoy is far darker than
// the notch and far larger, so any darkest-region search picks it.
func TestSomethingDarkerThanTheNotchDoesNotWin(t *testing.T) {
	const at = 200
	n, err := FindNotch(strip(at, true), pieceSize)
	if err != nil {
		t.Fatalf("FindNotch: %v", err)
	}
	want := at + pieceSize/2
	if abs(n.CentreX-want) > 4 {
		t.Errorf("found centre %d, want %d (±4) — the darkest region of the "+
			"picture is a decoy at x=95..165, and matching brightness instead "+
			"of shape lands there", n.CentreX, want)
	}
}

// TestAConfidenceIsReported covers the number a caller uses to decide whether
// to spend an attempt or ask for a different picture.
func TestAConfidenceIsReported(t *testing.T) {
	n, err := FindNotch(strip(180, false), pieceSize)
	if err != nil {
		t.Fatalf("FindNotch: %v", err)
	}
	if n.Confidence <= 0 {
		t.Errorf("confidence %.3f on a picture whose notch was found", n.Confidence)
	}
}

func TestAPieceTooBigForThePictureIsRefused(t *testing.T) {
	if _, err := FindNotch(strip(180, false), picW+1); err == nil {
		t.Error("a piece wider than the picture was accepted")
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
