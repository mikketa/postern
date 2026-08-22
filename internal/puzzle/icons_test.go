package puzzle

import (
	"image"
	"image/color"
	"testing"
)

// Segmentation is the part that decides whether a challenge can be solved at
// all: a pictogram whose icon was never cut out of the picture cannot be
// matched to it later, however good the matching is. It is worth a fixture
// that does not need the vendor's artwork.

// collage draws a picture in the shape of the ones these challenges use: broad
// regions of flat colour, a colour that recurs all over the frame, and a few
// small drawings on top in colours of their own.
func collage(icons []image.Rectangle, iconColours []color.RGBA) image.Image {
	const w, h = 300, 200
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	bands := []color.RGBA{{220, 40, 110, 255}, {40, 90, 200, 255}, {240, 190, 60, 255}}
	for y := range h {
		for x := range w {
			img.Set(x, y, bands[x*len(bands)/w])
		}
	}
	// A colour that turns up everywhere: scenery, not a drawing. It covers
	// less of the picture than an icon does, so only where it sits gives it
	// away.
	speck := color.RGBA{30, 30, 30, 255}
	for y := 4; y < h; y += 11 {
		for x := 4; x < w; x += 13 {
			for dy := range 3 {
				for dx := range 3 {
					img.Set(x+dx, y+dy, speck)
				}
			}
		}
	}
	for i, r := range icons {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				img.Set(x, y, iconColours[i])
			}
		}
	}
	return img
}

// TestTheDrawingsAreFoundOnABusyBackground is the case that defeated the rule
// this replaced. Nothing here is close to the commonest colour, so "far from
// the background" returns the background.
func TestTheDrawingsAreFoundOnABusyBackground(t *testing.T) {
	icons := []image.Rectangle{
		image.Rect(30, 20, 55, 45),
		image.Rect(150, 90, 178, 118),
		image.Rect(240, 150, 266, 176),
	}
	colours := []color.RGBA{{0, 255, 130, 255}, {160, 0, 255, 255}, {255, 120, 0, 255}}

	found := FindIcons(collage(icons, colours), 200, len(icons))
	if len(found) < len(icons) {
		t.Fatalf("found %d candidates for %d drawings", len(found), len(icons))
	}

	for _, want := range icons {
		hit := false
		for _, s := range found {
			if s.CentreX >= want.Min.X && s.CentreX < want.Max.X &&
				s.CentreY >= want.Min.Y && s.CentreY < want.Max.Y {
				hit = true
			}
		}
		if !hit {
			t.Errorf("no candidate sits on the drawing at %v — a pictogram whose "+
				"icon was never cut out cannot be matched to it later", want)
		}
	}
}

// TestAColourFoundAllOverThePictureIsNotADrawing guards the other direction.
// The specks cover less of the picture than an icon does, so size alone would
// take them; what rules them out is that they are everywhere.
func TestAColourFoundAllOverThePictureIsNotADrawing(t *testing.T) {
	icons := []image.Rectangle{
		image.Rect(30, 20, 55, 45),
		image.Rect(150, 90, 178, 118),
		image.Rect(240, 150, 266, 176),
	}
	colours := []color.RGBA{{0, 255, 130, 255}, {160, 0, 255, 255}, {255, 120, 0, 255}}

	for _, s := range FindIcons(collage(icons, colours), 200, len(icons)) {
		// A candidate that swallowed the specks spans most of the frame.
		if s.W > 200 && s.H > 130 {
			t.Fatalf("a candidate %dx%d covers the whole picture: the recurring "+
				"colour was taken for a drawing", s.W, s.H)
		}
	}
}

// TestAskingForNothingIsAllowed covers the call the prompt reader makes when
// it could not count the pictograms: take whatever the strict pass yields.
func TestAskingForNothingIsAllowed(t *testing.T) {
	icons := []image.Rectangle{image.Rect(30, 20, 55, 45)}
	colours := []color.RGBA{{0, 255, 130, 255}}
	if got := FindIcons(collage(icons, colours), 200, 0); len(got) == 0 {
		t.Fatal("no candidate at all from a picture with a drawing on it")
	}
}
