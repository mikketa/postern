package challenge

import (
	"os"
	"testing"
)

// A real capture from a live run: the panel is where it should be, the document
// describes a 3x3 grid of 130px tiles, and every pixel of it is white.
//
// TestBlankSpotsAnUnpaintedGrid covers the shapes this can take with panels
// drawn here. This one covers the bytes that actually came off a run, because
// they are what the detector failed to keep out of the solver: a blank
// photograph is one the solver can answer — it finds nothing, which is a legal
// answer to a dynamic grid — so postern ticked nothing and pressed verify,
// submitting an empty answer to a grid nobody had seen. Measured over two runs
// on the demo page, 8 rounds of 11 under headless Chrome and 5 of 13 under a
// virtual display were photographs of exactly this.
func TestUnpaintedGridFromALiveRunIsRecognised(t *testing.T) {
	shot, err := os.ReadFile("testdata/unpainted-panel.png")
	if err != nil {
		t.Fatal(err)
	}

	var tiles []Box
	for _, y := range []float64{125, 255, 385} {
		for _, x := range []float64{5, 135, 265} {
			tiles = append(tiles, Box{X: x, Y: y, W: 130, H: 130})
		}
	}

	if !blank(shot, tiles) {
		t.Fatal("a panel captured entirely white was not recognised as unpainted")
	}
}
