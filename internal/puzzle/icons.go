package puzzle

import (
	"fmt"
	"image"
	"math"
	"sort"
)

// Finding icons in a picture, and pairing them with the ones asked for.
//
// This challenge shows a strip of pictograms and a picture to find them in.
// The picture's versions are redrawn — thick, coloured, textured, turned — so
// nothing matches pixel for pixel, and the vendor serves them from a path
// named for the network that made them. What is left to compare is the
// outline: its proportions, and its topology.

// Shape is a silhouette cut out of a picture.
type Shape struct {
	Mask          []bool // row-major, W by H
	W, H          int
	CentreX       int // in the source picture's coordinates
	CentreY       int
	Pixels        int
	MinX, MinY    int
	sourceW       int
	descriptorSet bool
	descriptor    descriptor
}

// descriptor is what two shapes are compared on.
type descriptor struct {
	hu       [7]float64
	holes    float64 // enclosed gaps: a face has them, a wave does not
	elongate float64 // longer side over shorter
	density  float64 // filled area over bounding box
}

// FindIcons segments a picture into the icons drawn on it.
//
// Colour is what separates them, but not any fixed colour: the background of
// one challenge is the icon colour of another. The dominant hue is measured
// and treated as the background — the majority always is — and what stands
// well away from it is an icon. Thresholding on saturation instead was tried
// first and swallowed the whole picture: these backgrounds are saturated too.
func FindIcons(img image.Image, minPixels int) []Shape {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	var hist [36]float64
	for y := range h {
		for x := range w {
			hue, sat, v := hsv(img, b.Min.X+x, b.Min.Y+y)
			if sat > 0.15 && v > 40 {
				hist[int(hue/10)%36] += sat
			}
		}
	}
	dom := 0
	for i, v := range hist {
		if v > hist[dom] {
			dom = i
		}
	}
	domHue := float64(dom)*10 + 5

	mask := make([]bool, w*h)
	for y := range h {
		for x := range w {
			hue, sat, v := hsv(img, b.Min.X+x, b.Min.Y+y)
			mask[y*w+x] = sat > 0.35 && v > 60 && hueApart(hue, domHue) > 60
		}
	}
	out := components(mask, w, h, minPixels)
	sort.Slice(out, func(i, j int) bool { return out[i].Pixels > out[j].Pixels })
	return out
}

// FindPictograms cuts the prompt strip into the shapes it is asking for.
//
// Split by columns rather than by connected regions: a pictogram is often
// drawn in separate pieces — a figure with a detached head — and treating
// each piece as a separate request would ask for the wrong things, in the
// wrong number.
func FindPictograms(img image.Image, minValue float64) []Shape {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	mask := make([]bool, w*h)
	for y := range h {
		for x := range w {
			if _, _, v := hsv(img, b.Min.X+x, b.Min.Y+y); v > minValue {
				mask[y*w+x] = true
			}
		}
	}

	full := make([]bool, w)
	for x := range w {
		for y := range h {
			if mask[y*w+x] {
				full[x] = true
				break
			}
		}
	}

	var out []Shape
	for x := 0; x < w; x++ {
		if !full[x] {
			continue
		}
		start, gap := x, 0
		for x++; x < w && gap < 3; x++ {
			if full[x] {
				gap = 0
			} else {
				gap++
			}
		}
		end := x - gap
		var cells []int
		for y := range h {
			for cx := start; cx <= end && cx < w; cx++ {
				if mask[y*w+cx] {
					cells = append(cells, y*w+cx)
				}
			}
		}
		if len(cells) > 10 {
			out = append(out, crop(cells, w))
		}
	}
	return out
}

// PairIcons says which icon answers each pictogram, in the order asked.
//
// Every assignment is scored and the best one wins: the boards are small — a
// handful of each — so this is enumerated rather than optimised, and the
// answer is the best whole arrangement rather than a series of independent
// best guesses that can claim the same icon twice.
func PairIcons(wanted, found []Shape) ([]int, error) {
	if len(wanted) == 0 {
		return nil, fmt.Errorf("puzzle: the prompt asks for nothing")
	}
	if len(found) < len(wanted) {
		return nil, fmt.Errorf("puzzle: the prompt asks for %d icons and the "+
			"picture yielded %d", len(wanted), len(found))
	}

	dw := make([]descriptor, len(wanted))
	for i := range wanted {
		dw[i] = describe(&wanted[i])
	}
	df := make([]descriptor, len(found))
	for i := range found {
		df[i] = describe(&found[i])
	}

	best, bestScore := []int(nil), math.Inf(1)
	used := make([]bool, len(found))
	cur := make([]int, 0, len(wanted))
	var walk func()
	walk = func() {
		if len(cur) == len(wanted) {
			var sc float64
			for i, j := range cur {
				sc += apart(dw[i], df[j])
			}
			if sc < bestScore {
				bestScore = sc
				best = append([]int(nil), cur...)
			}
			return
		}
		for j := range found {
			if used[j] {
				continue
			}
			used[j] = true
			cur = append(cur, j)
			walk()
			cur = cur[:len(cur)-1]
			used[j] = false
		}
	}
	walk()
	if best == nil {
		return nil, fmt.Errorf("puzzle: no arrangement of icons could be scored")
	}
	return best, nil
}

func describe(s *Shape) descriptor {
	if s.descriptorSet {
		return s.descriptor
	}
	d := descriptor{hu: huMoments(*s), holes: float64(holes(*s))}
	long, short := float64(max(s.W, s.H)), float64(min(s.W, s.H))
	if short > 0 {
		d.elongate = long / short
	}
	if s.W*s.H > 0 {
		d.density = float64(s.Pixels) / float64(s.W*s.H)
	}
	s.descriptor, s.descriptorSet = d, true
	return d
}

// apart is how unlike two shapes are.
//
// The moments carry the outline's proportions and the rest carry what they
// miss. Holes are weighted heavily because they are nearly binary and survive
// redrawing: a shape with gaps enclosed in it does not become one without.
func apart(a, b descriptor) float64 {
	var d float64
	// The seventh moment changes sign under reflection and these icons are
	// turned rather than mirrored, so six are compared.
	for i := range 6 {
		d += math.Abs(a.hu[i] - b.hu[i])
	}
	d += 4 * math.Abs(a.holes-b.holes)
	d += 3 * math.Abs(a.elongate-b.elongate)
	d += 3 * math.Abs(a.density-b.density)
	return d
}

// holes counts enclosed gaps: background regions inside the shape that do not
// reach its edge.
func holes(s Shape) int {
	seen := make([]bool, s.W*s.H)
	count := 0
	for y := range s.H {
		for x := range s.W {
			i := y*s.W + x
			if s.Mask[i] || seen[i] {
				continue
			}
			touchesEdge := false
			queue := []int{i}
			seen[i] = true
			for len(queue) > 0 {
				p := queue[len(queue)-1]
				queue = queue[:len(queue)-1]
				px, py := p%s.W, p/s.W
				if px == 0 || py == 0 || px == s.W-1 || py == s.H-1 {
					touchesEdge = true
				}
				for _, dd := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					nx, ny := px+dd[0], py+dd[1]
					if nx < 0 || ny < 0 || nx >= s.W || ny >= s.H {
						continue
					}
					q := ny*s.W + nx
					if !s.Mask[q] && !seen[q] {
						seen[q] = true
						queue = append(queue, q)
					}
				}
			}
			if !touchesEdge {
				count++
			}
		}
	}
	return count
}

// huMoments returns the seven invariant moments, log-compressed onto one scale.
func huMoments(s Shape) [7]float64 {
	var m00, m10, m01 float64
	for y := range s.H {
		for x := range s.W {
			if !s.Mask[y*s.W+x] {
				continue
			}
			m00++
			m10 += float64(x)
			m01 += float64(y)
		}
	}
	if m00 == 0 {
		return [7]float64{}
	}
	cx, cy := m10/m00, m01/m00

	central := func(p, q int) float64 {
		var v float64
		for y := range s.H {
			for x := range s.W {
				if !s.Mask[y*s.W+x] {
					continue
				}
				v += math.Pow(float64(x)-cx, float64(p)) * math.Pow(float64(y)-cy, float64(q))
			}
		}
		return v
	}
	n := func(p, q int) float64 {
		return central(p, q) / math.Pow(m00, 1+float64(p+q)/2)
	}
	n20, n02, n11 := n(2, 0), n(0, 2), n(1, 1)
	n30, n03, n21, n12 := n(3, 0), n(0, 3), n(2, 1), n(1, 2)

	var h [7]float64
	h[0] = n20 + n02
	h[1] = math.Pow(n20-n02, 2) + 4*n11*n11
	h[2] = math.Pow(n30-3*n12, 2) + math.Pow(3*n21-n03, 2)
	h[3] = math.Pow(n30+n12, 2) + math.Pow(n21+n03, 2)
	h[4] = (n30-3*n12)*(n30+n12)*(math.Pow(n30+n12, 2)-3*math.Pow(n21+n03, 2)) +
		(3*n21-n03)*(n21+n03)*(3*math.Pow(n30+n12, 2)-math.Pow(n21+n03, 2))
	h[5] = (n20-n02)*(math.Pow(n30+n12, 2)-math.Pow(n21+n03, 2)) +
		4*n11*(n30+n12)*(n21+n03)
	h[6] = (3*n21-n03)*(n30+n12)*(math.Pow(n30+n12, 2)-3*math.Pow(n21+n03, 2)) -
		(n30-3*n12)*(n21+n03)*(3*math.Pow(n30+n12, 2)-math.Pow(n21+n03, 2))

	for i := range h {
		if h[i] != 0 {
			h[i] = math.Copysign(math.Log10(math.Abs(h[i])), h[i])
		}
	}
	return h
}

// components extracts connected regions of a mask, four-way.
func components(mask []bool, w, h, minPixels int) []Shape {
	seen := make([]bool, w*h)
	var out []Shape
	for i := range mask {
		if !mask[i] || seen[i] {
			continue
		}
		var cells []int
		queue := []int{i}
		seen[i] = true
		for len(queue) > 0 {
			p := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			cells = append(cells, p)
			px, py := p%w, p/w
			for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := px+d[0], py+d[1]
				if nx < 0 || ny < 0 || nx >= w || ny >= h {
					continue
				}
				q := ny*w + nx
				if mask[q] && !seen[q] {
					seen[q] = true
					queue = append(queue, q)
				}
			}
		}
		if len(cells) >= minPixels {
			out = append(out, crop(cells, w))
		}
	}
	return out
}

func crop(cells []int, sourceW int) Shape {
	minX, minY, maxX, maxY := 1<<30, 1<<30, 0, 0
	for _, p := range cells {
		x, y := p%sourceW, p/sourceW
		minX, maxX = min(minX, x), max(maxX, x)
		minY, maxY = min(minY, y), max(maxY, y)
	}
	w, h := maxX-minX+1, maxY-minY+1
	s := Shape{
		Mask: make([]bool, w*h), W: w, H: h,
		CentreX: (minX + maxX) / 2, CentreY: (minY + maxY) / 2,
		Pixels: len(cells), MinX: minX, MinY: minY, sourceW: sourceW,
	}
	for _, p := range cells {
		x, y := p%sourceW, p/sourceW
		s.Mask[(y-minY)*w+(x-minX)] = true
	}
	return s
}

func hsv(img image.Image, x, y int) (hue, sat, value float64) {
	r, g, b, _ := img.At(x, y).RGBA()
	rf, gf, bf := float64(r>>8), float64(g>>8), float64(b>>8)
	mx := max(rf, max(gf, bf))
	mn := min(rf, min(gf, bf))
	if mx == mn || mx == 0 {
		return 0, 0, mx
	}
	d := mx - mn
	switch mx {
	case rf:
		hue = 60 * (gf - bf) / d
	case gf:
		hue = 60 * (2 + (bf-rf)/d)
	default:
		hue = 60 * (4 + (rf-gf)/d)
	}
	if hue < 0 {
		hue += 360
	}
	return hue, d / mx, mx
}

func hueApart(a, b float64) float64 {
	d := math.Abs(a - b)
	return math.Min(d, 360-d)
}
