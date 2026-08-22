package puzzle

import "math"

// What a pairing is judged on.
//
// Every comparison is reduced to a vector, and what each part is worth is
// fitted to challenges whose answers are known rather than guessed at by hand.
// Hand-picked weights were tried and they are how this got stuck: every
// adjustment traded one challenge for another.
//
// What the vector is worth, measured over 115 labelled pictograms: not much
// beyond its first column. Ranking by silhouette overlap alone picks the right
// candidate 0.661 of the time; by chamfer distance alone, also 0.661; the
// fitted model, cross-validated by challenge, 0.650, and the two agree on the
// whole arrangement 21 challenges out of 32. Every other measurement here —
// how sparse a shape is, how many gaps it encloses, how elongated, how much of
// it is edge — lands within noise of picking at random, which is 0.28.
//
// That is worth stating plainly rather than leaving to be rediscovered: these
// twelve measurements carry one piece of information between them, which is
// how well two silhouettes agree, and no fit over them will beat measuring
// that agreement well. The vector and the fitting stay because they are how
// the next measurement gets judged — added, refitted, and kept only if the
// number moves.

// FeatureCount is the length of a comparison vector.
const FeatureCount = 13

// FeatureNames label the vector, for reading a trained model back.
var FeatureNames = [FeatureCount]string{
	"overlap.best", "overlap.upright", "overlap.margin",
	"density.diff", "elongation.diff", "holes.diff",
	"stroke.wander", "size.relative", "hu.distance",
	"compact.diff", "fill.ratio", "chamfer.best", "bias",
}

// Features compares one pictogram against one candidate.
//
// context carries the other candidates, so measurements that only mean
// something in relation to the rest of the picture — is this one unusually
// large, unusually solid — can be expressed.
func Features(want, got Shape, context []Shape) [FeatureCount]float64 {
	var f [FeatureCount]float64

	wf, gf := fillEnclosed(want), fillEnclosed(got)
	nw := normalise(wf)

	// Best overlap across the rotation sweep, and the overlap without turning
	// anything: an icon presented upright is a different kind of evidence from
	// one that only matches upside down.
	best, upright := 0.0, 0.0
	second := 0.0
	// Overlap counts a pixel as agreeing or not and nothing in between, which
	// is harsh on a shape traced by hand: a stroke a pixel or two off its mark
	// scores as if it were somewhere else entirely. The chamfer distance asks
	// instead how far each pixel is from the other shape, so a near miss reads
	// as a near miss. Both are measured; which is worth more is for the fitting
	// to decide.
	dw := distances(nw)
	chamfer := math.Inf(1)
	for i := range rotations {
		ng := normalise(rotate(gf, float64(i)*2*math.Pi/rotations))
		s := jaccard(nw, ng)
		if i == 0 {
			upright = s
		}
		if s > best {
			second, best = best, s
		} else if s > second {
			second = s
		}
		if d := chamferBoth(nw, dw, ng); d < chamfer {
			chamfer = d
		}
	}
	if math.IsInf(chamfer, 1) {
		chamfer = float64(normalSize)
	}
	f[0] = best
	f[1] = upright
	// How much the best angle beats the runner-up: a shape that matches
	// equally at every angle is round, and matches nothing in particular.
	f[2] = best - second

	f[3] = math.Abs(density(want) - density(got))
	f[4] = math.Abs(elongation(want) - elongation(got))
	f[5] = math.Abs(float64(holes(wf) - holes(gf)))

	// How much of the candidate is edge. A solid blob's border grows as the
	// square root of its area, a drawn stroke's grows with the area itself, so
	// this separates an icon from a letter cut out of card. Handed over raw:
	// squashing it into a tidy 0-to-1 verdict first, with a threshold chosen by
	// hand, throws away the part the model is meant to weigh.
	f[6] = wander(got)

	// Size against the other candidates: one challenge's icons are drawn at
	// one scale, so an outlier is usually scenery.
	f[7] = relativeSize(got, context)

	f[8] = huDistance(wf, gf)
	f[9] = math.Abs(compactness(want) - compactness(got))

	// What filling the candidate added. An icon drawn as an outline encloses
	// far more than it covers, so this sits well below one; scenery that was
	// already solid sits at one. Measured against the shape's own area rather
	// than its bounding box, which a diagonal stroke inflates.
	f[10] = 0
	if gf.Pixels > 0 {
		f[10] = float64(got.Pixels) / float64(gf.Pixels)
	}
	f[11] = chamfer
	f[12] = 1 // bias

	return f
}

func jaccard(a, b []bool) float64 {
	var both, either float64
	for i := range a {
		if a[i] && b[i] {
			both++
		}
		if a[i] || b[i] {
			either++
		}
	}
	if either == 0 {
		return 0
	}
	return both / either
}

func density(s Shape) float64 {
	if s.W*s.H == 0 {
		return 0
	}
	return float64(s.Pixels) / float64(s.W*s.H)
}

func elongation(s Shape) float64 {
	long, short := float64(max(s.W, s.H)), float64(min(s.W, s.H))
	if short == 0 {
		return 1
	}
	return long / short
}

// compactness is perimeter squared over area, scaled: near 1 for a disc, large
// for a wandering stroke.
func compactness(s Shape) float64 {
	if s.Pixels == 0 {
		return 0
	}
	border := 0
	for y := range s.H {
		for x := range s.W {
			if !s.Mask[y*s.W+x] {
				continue
			}
			for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := x+d[0], y+d[1]
				if nx < 0 || ny < 0 || nx >= s.W || ny >= s.H || !s.Mask[ny*s.W+nx] {
					border++
					break
				}
			}
		}
	}
	return float64(border*border) / (4 * math.Pi * float64(s.Pixels))
}

func relativeSize(s Shape, context []Shape) float64 {
	if len(context) == 0 {
		return 1
	}
	var total float64
	for _, c := range context {
		total += math.Hypot(float64(c.W), float64(c.H))
	}
	mean := total / float64(len(context))
	if mean == 0 {
		return 1
	}
	return math.Hypot(float64(s.W), float64(s.H)) / mean
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
			edge := false
			queue := []int{i}
			seen[i] = true
			for len(queue) > 0 {
				p := queue[len(queue)-1]
				queue = queue[:len(queue)-1]
				px, py := p%s.W, p/s.W
				if px == 0 || py == 0 || px == s.W-1 || py == s.H-1 {
					edge = true
				}
				for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					nx, ny := px+d[0], py+d[1]
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
			if !edge {
				count++
			}
		}
	}
	return count
}

// huDistance is the distance between two shapes' invariant moments. On its own
// this was too coarse to pair anything; as one measurement among twelve it
// still carries something the others do not.
func huDistance(a, b Shape) float64 {
	ha, hb := huMoments(a), huMoments(b)
	var d float64
	for i := range 6 {
		d += math.Abs(ha[i] - hb[i])
	}
	return d / 6
}

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

// wander is the share of a shape's pixels that lie on its border.
func wander(s Shape) float64 {
	if s.Pixels == 0 {
		return 0
	}
	border := 0
	for y := range s.H {
		for x := range s.W {
			if !s.Mask[y*s.W+x] {
				continue
			}
			for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := x+d[0], y+d[1]
				if nx < 0 || ny < 0 || nx >= s.W || ny >= s.H || !s.Mask[ny*s.W+nx] {
					border++
					break
				}
			}
		}
	}
	return float64(border) / float64(s.Pixels)
}

// distances is the distance from every cell to the nearest set cell, in steps
// of a chess king. Unset shapes get the grid's width, which is further than
// anything inside it.
func distances(m []bool) []float64 {
	const n = normalSize
	d := make([]float64, n*n)
	queue := make([]int, 0, n*n)
	for i, on := range m {
		if on {
			d[i] = 0
			queue = append(queue, i)
		} else {
			d[i] = float64(n)
		}
	}
	for head := 0; head < len(queue); head++ {
		i := queue[head]
		x, y := i%n, i/n
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				nx, ny := x+dx, y+dy
				if nx < 0 || ny < 0 || nx >= n || ny >= n {
					continue
				}
				j := ny*n + nx
				if d[j] > d[i]+1 {
					d[j] = d[i] + 1
					queue = append(queue, j)
				}
			}
		}
	}
	return d
}

// chamferBoth is the mean distance from each shape to the other, symmetrised
// so that neither shape can win by being small enough to hide inside the other.
func chamferBoth(a []bool, da []float64, b []bool) float64 {
	db := distances(b)
	var sa, sb, na, nb float64
	for i := range a {
		if a[i] {
			sa += db[i]
			na++
		}
		if b[i] {
			sb += da[i]
			nb++
		}
	}
	if na == 0 || nb == 0 {
		return float64(normalSize)
	}
	return 0.5 * (sa/na + sb/nb)
}
