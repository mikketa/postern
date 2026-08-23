package puzzle

import (
	"math"
	"testing"
)

// The whole point of the polar description is that turning a shape shifts its
// map along one axis and does nothing else. That is a claim about the code,
// not a hope, so it is checked.

// wedge draws a shape with no symmetry, so that a wrong shift cannot pass for
// a right one.
func wedge() Shape {
	const n = 64
	s := Shape{Mask: make([]bool, n*n), W: n, H: n}
	c := float64(n-1) / 2
	for y := range n {
		for x := range n {
			dx, dy := float64(x)-c, float64(y)-c
			r := math.Hypot(dx, dy)
			a := math.Atan2(dy, dx)
			// A disc with a bite taken out of one side and a spur on another.
			in := r < 22 && !(a > 0.4 && a < 1.4 && r > 8)
			if !in && r < 28 && a > -2.6 && a < -2.2 {
				in = true
			}
			if in {
				s.Mask[y*n+x] = true
				s.Pixels++
			}
		}
	}
	return s
}

func mapDistance(a, b []float64) float64 {
	var d float64
	for i := range a {
		d += math.Abs(a[i] - b[i])
	}
	return d / float64(len(a))
}

// TestTurningAShapeShiftsItsMap is the invariance the network is built on. A
// turn of one whole angular cell has to land on a shift of exactly one cell.
func TestTurningAShapeShiftsItsMap(t *testing.T) {
	s := wedge()
	upright := polarMap(s)

	for _, cells := range []int{1, 5, 8, 17} {
		theta := float64(cells) / polarAngles * 2 * math.Pi
		turned := polarMap(rotate(s, theta))

		want := polarTurn(upright, cells)
		got := mapDistance(turned, want)

		// Against the best wrong shift, which is the comparison that means
		// something: a description that matched everything equally would pass
		// a threshold on its own.
		worst := math.Inf(1)
		for k := range polarAngles {
			if k == cells%polarAngles {
				continue
			}
			worst = math.Min(worst, mapDistance(turned, polarTurn(upright, k)))
		}
		if got >= worst {
			t.Errorf("turned by %d cells: the right shift is %.4f away and some "+
				"wrong shift is %.4f — the map is not tracking the turn",
				cells, got, worst)
		}
		if got > 0.05 {
			t.Errorf("turned by %d cells: %.4f from the shift it should be, "+
				"which is more than resampling explains", cells, got)
		}
	}
}

// TestReflectingAShapeReversesItsMap covers the other symmetry, which the
// comparison checks rather than learns.
func TestReflectingAShapeReversesItsMap(t *testing.T) {
	s := wedge()
	flipped := polarMap(mirrorShape(s))
	want := polarFlip(polarMap(s))
	// The reversal lands between two angular cells, so a whole cell of slack.
	best := math.Inf(1)
	for k := -1; k <= 1; k++ {
		best = math.Min(best, mapDistance(flipped, polarTurn(want, k)))
	}
	if best > 0.05 {
		t.Errorf("a reflected shape is %.4f from the reversal of its map", best)
	}
}

// TestScalingAShapeLeavesItsMapAlone is the other invariance the radius
// normalisation is there for: the same drawing, larger, is the same drawing.
func TestScalingAShapeLeavesItsMapAlone(t *testing.T) {
	s := wedge()
	big := upscale(s, 3)
	if d := mapDistance(polarMap(big), polarMap(s)); d > 0.05 {
		t.Errorf("the same shape drawn three times larger is %.4f away", d)
	}
}
