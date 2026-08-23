package puzzle

import "math"

// Looking at a shape the way that makes turning it harmless.
//
// The vendor turns its icons, and every comparison so far has paid for that by
// trying the candidate at every angle: seventy-two alignments per pair, and
// the answer is whichever fitted best. That works and it is expensive, and it
// puts the burden on the search rather than on the description.
//
// Sampled around the centre instead — a ring of angles at each of a series of
// radii — a rotation of the shape becomes a shift along the angle axis and
// nothing else. Anything that reads the rows in a way that does not care where
// they start is then invariant to it by construction, with no search at all.
// Reflection is a reversal of the same axis, which is just as cheap to check.

const (
	// polarRadii is how many rings the shape is sampled at, from its centre
	// out to its furthest pixel.
	polarRadii = 16

	// polarAngles is how finely each ring is sampled. Rotation resolves to
	// this: 32 angles is 11 degrees, about what the old sweep managed.
	polarAngles = 32

	// polarSubsamples is the grid used inside each cell, so that a thin stroke
	// registers as partly covering a cell rather than as present or absent.
	polarSubsamples = 2
)

// polarMap samples a shape into polarRadii rings of polarAngles cells, each
// holding how much of that cell the shape covers.
//
// The radius is scaled to the shape's own reach, so the description does not
// change when the same drawing arrives larger or smaller.
func polarMap(s Shape) []float64 {
	out := make([]float64, polarRadii*polarAngles)
	if s.Pixels == 0 || s.W == 0 || s.H == 0 {
		return out
	}

	var sx, sy float64
	for y := range s.H {
		for x := range s.W {
			if s.Mask[y*s.W+x] {
				sx += float64(x)
				sy += float64(y)
			}
		}
	}
	cx, cy := sx/float64(s.Pixels), sy/float64(s.Pixels)

	reach := 0.0
	for y := range s.H {
		for x := range s.W {
			if s.Mask[y*s.W+x] {
				reach = math.Max(reach, math.Hypot(float64(x)-cx, float64(y)-cy))
			}
		}
	}
	if reach < 1 {
		reach = 1
	}

	const sub = polarSubsamples
	for ri := range polarRadii {
		for ti := range polarAngles {
			hit := 0
			for a := range sub {
				for b := range sub {
					r := (float64(ri) + (float64(a)+0.5)/sub) / polarRadii * reach
					t := (float64(ti) + (float64(b)+0.5)/sub) / polarAngles * 2 * math.Pi
					x := int(math.Round(cx + r*math.Cos(t)))
					y := int(math.Round(cy + r*math.Sin(t)))
					if x >= 0 && y >= 0 && x < s.W && y < s.H && s.Mask[y*s.W+x] {
						hit++
					}
				}
			}
			out[ri*polarAngles+ti] = float64(hit) / (sub * sub)
		}
	}
	return out
}

// polarTurn shifts a map along its angle axis, which is what turning the shape
// it came from does to it.
func polarTurn(m []float64, by int) []float64 {
	out := make([]float64, len(m))
	for ri := range polarRadii {
		for ti := range polarAngles {
			out[ri*polarAngles+(ti+by+polarAngles)%polarAngles] = m[ri*polarAngles+ti]
		}
	}
	return out
}

// polarFlip reflects the map about the vertical, which is what mirroring the
// shape it came from does to it.
//
// The angle a cell stands for goes to pi minus itself, which is a reversal of
// the axis and a half turn, not a reversal alone. Reversing alone is also a
// reflection — about the horizontal — and the two differ by exactly half a
// turn, which is easy to write by accident and hard to see afterwards.
func polarFlip(m []float64) []float64 {
	out := make([]float64, len(m))
	for ri := range polarRadii {
		for ti := range polarAngles {
			to := (polarAngles/2 - 1 - ti + 2*polarAngles) % polarAngles
			out[ri*polarAngles+to] = m[ri*polarAngles+ti]
		}
	}
	return out
}
