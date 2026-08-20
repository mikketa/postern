package puzzle

import "math"

// Comparing two drawings of the same thing.
//
// Invariant moments were the first attempt and they are too coarse: they
// reduce a silhouette to seven numbers, and two shapes that are nothing alike
// can agree on all seven. What discriminates is comparing the silhouettes
// themselves — laid over one another, at the same size, turned to their best
// alignment — and counting how much of the two coincides.
//
// Two things have to happen before that comparison means anything. An icon
// drawn as an outline is mostly background inside its own border, while the
// pictogram asking for it is solid; filling the enclosed gaps makes them the
// same object. And the vendor turns its icons, so the comparison is repeated
// through a full rotation and the best alignment is the one that counts.

// normalSize is the grid every silhouette is resampled onto. Small enough that
// a full rotation sweep over several candidates stays cheap, large enough to
// keep the features that separate a wave from a face.
const normalSize = 32

// rotations is how many angles are tried. Ten degrees is finer than the error
// left after resampling to a 32-pixel grid.
const rotations = 36

// Resembles scores how alike two silhouettes are, from 0 to 1.
//
// The score is the best overlap found across the rotation sweep: how much of
// the two shapes coincides, over how much they cover between them.
func Resembles(a, b Shape) float64 {
	ma := normalise(fillEnclosed(a))
	best := 0.0
	for i := range rotations {
		mb := normalise(rotate(fillEnclosed(b), float64(i)*2*math.Pi/rotations))
		var both, either int
		for j := range ma {
			if ma[j] && mb[j] {
				both++
			}
			if ma[j] || mb[j] {
				either++
			}
		}
		if either == 0 {
			continue
		}
		if s := float64(both) / float64(either); s > best {
			best = s
		}
	}
	return best
}

// fillEnclosed fills gaps that do not reach the edge, turning an outline into
// the solid shape it draws.
func fillEnclosed(s Shape) Shape {
	out := Shape{Mask: append([]bool(nil), s.Mask...), W: s.W, H: s.H,
		CentreX: s.CentreX, CentreY: s.CentreY, MinX: s.MinX, MinY: s.MinY}

	// Flood from the border: everything the flood misses is enclosed.
	outside := make([]bool, s.W*s.H)
	var queue []int
	push := func(x, y int) {
		i := y*s.W + x
		if !s.Mask[i] && !outside[i] {
			outside[i] = true
			queue = append(queue, i)
		}
	}
	for x := range s.W {
		push(x, 0)
		push(x, s.H-1)
	}
	for y := range s.H {
		push(0, y)
		push(s.W-1, y)
	}
	for len(queue) > 0 {
		p := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		px, py := p%s.W, p/s.W
		for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			nx, ny := px+d[0], py+d[1]
			if nx >= 0 && ny >= 0 && nx < s.W && ny < s.H {
				push(nx, ny)
			}
		}
	}
	for i := range out.Mask {
		if !outside[i] {
			out.Mask[i] = true
		}
	}
	out.Pixels = 0
	for _, on := range out.Mask {
		if on {
			out.Pixels++
		}
	}
	return out
}

// rotate turns a silhouette about its centre, growing the frame so nothing is
// cut off.
func rotate(s Shape, theta float64) Shape {
	if theta == 0 {
		return s
	}
	sin, cos := math.Sin(theta), math.Cos(theta)
	w := int(math.Ceil(math.Abs(float64(s.W)*cos) + math.Abs(float64(s.H)*sin)))
	h := int(math.Ceil(math.Abs(float64(s.W)*sin) + math.Abs(float64(s.H)*cos)))
	if w < 1 || h < 1 {
		return s
	}
	out := Shape{Mask: make([]bool, w*h), W: w, H: h}

	// Sampled backwards — for each destination pixel, where it came from — so
	// the result has no holes, which forward mapping leaves.
	scx, scy := float64(s.W)/2, float64(s.H)/2
	dcx, dcy := float64(w)/2, float64(h)/2
	for y := range h {
		for x := range w {
			dx, dy := float64(x)-dcx, float64(y)-dcy
			sx := int(math.Round(dx*cos + dy*sin + scx))
			sy := int(math.Round(-dx*sin + dy*cos + scy))
			if sx >= 0 && sy >= 0 && sx < s.W && sy < s.H && s.Mask[sy*s.W+sx] {
				out.Mask[y*w+x] = true
				out.Pixels++
			}
		}
	}
	return out
}

// normalise resamples a silhouette onto a fixed grid, keeping its proportions.
//
// Proportions are kept rather than stretched away: how long a shape is against
// how wide is one of the few things that survives redrawing, and stretching
// every candidate to a square throws it away.
func normalise(s Shape) []bool {
	out := make([]bool, normalSize*normalSize)

	// Tighten to the silhouette first: a rotation leaves empty margins, and
	// scaling those in would shrink the shape by a different amount at every
	// angle.
	minX, minY, maxX, maxY := s.W, s.H, -1, -1
	for y := range s.H {
		for x := range s.W {
			if s.Mask[y*s.W+x] {
				minX, maxX = min(minX, x), max(maxX, x)
				minY, maxY = min(minY, y), max(maxY, y)
			}
		}
	}
	if maxX < 0 {
		return out
	}
	w, h := maxX-minX+1, maxY-minY+1

	scale := float64(normalSize) / float64(max(w, h))
	offX := (normalSize - int(float64(w)*scale)) / 2
	offY := (normalSize - int(float64(h)*scale)) / 2

	for y := range normalSize {
		for x := range normalSize {
			sx := int(float64(x-offX)/scale) + minX
			sy := int(float64(y-offY)/scale) + minY
			if sx >= minX && sy >= minY && sx <= maxX && sy <= maxY && s.Mask[sy*s.W+sx] {
				out[y*normalSize+x] = true
			}
		}
	}
	return out
}
