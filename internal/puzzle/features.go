package puzzle

import "math"

// What a pairing is judged on.
//
// Every comparison is reduced to a vector, and what each part is worth is
// fitted to challenges whose answers are known rather than guessed at by hand.
// Hand-picked weights were tried and they are how this got stuck: every
// adjustment traded one challenge for another.
//
// The vector went through a stage where it was worth nothing. Twelve
// measurements — how sparse a shape is, how elongated, how many gaps it
// encloses, how much of it is edge — and every one of them but the overlap
// landed within noise of picking at random, which with these candidate counts
// is 0.28. A fit over one useful column and eleven noisy ones cannot beat the
// useful column, and it did not: 0.650 against 0.661.
//
// What was missing was not a better fit but a different kind of measurement,
// and there were two.
//
// The sweep only ever turned the candidate. Reflecting it as well is worth
// more on its own than the whole upright sweep — 0.687 against 0.661 — whether
// because the vendor mirrors a glyph or simply because it is a second chance
// at an alignment.
//
// And a single overlap figure says how much two shapes agree while saying
// nothing about where. Sliced into eight rings out from the centre, the third
// ring alone ranks candidates as well as the entire overlap did, and the eight
// together say something the total cannot: a glyph and a candidate can agree
// on their outline and disagree completely about what is inside it, which is
// how a solid blob passes for a drawn ring. Splitting the disagreement into
// what the candidate adds and what it misses is the same idea — scenery tends
// to spill past the pictogram, a half-segmented icon tends to fall short, and
// the total treats those as the same failure.
//
// Measured over 220 labelled pictograms from 88 collected challenges,
// cross-validated by challenge:
//
//	silhouette overlap alone   0.664 per pictogram, 36 whole arrangements of 59
//	the fitted model           0.679 per pictogram, 43 whole arrangements of 59
//
// The gap is small per pictogram and large per challenge, which is the point:
// a challenge needs all three right, so a rule that is a little better on each
// one is a good deal better on the whole. Eight rings is where this stops
// paying — twelve and sixteen are no better — and three other ideas were tried
// against the same bench and dropped for moving nothing: wedges around the
// centre as well as rings, the shape of the match across the whole sweep
// rather than at its best angle, and sweeping a range of scales as well as
// angles, which was worse.

// FeatureCount is the length of a comparison vector.
const FeatureCount = 23

// FeatureNames label the vector, for reading a trained model back.
var FeatureNames = [FeatureCount]string{
	"overlap.best", "overlap.upright", "overlap.margin",
	"density.diff", "elongation.diff", "holes.diff",
	"stroke.wander", "size.relative", "hu.distance",
	"compact.diff", "fill.ratio", "chamfer.best",
	"fit.excess", "fit.missing",
	"ring.0", "ring.1", "ring.2", "ring.3",
	"ring.4", "ring.5", "ring.6", "ring.7",
	"bias",
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
	var aligned []bool
	// Turned through a full circle, and again reflected. Nothing said the
	// vendor only rotates its icons, and the reflected half of the sweep is
	// worth more than the upright half on its own: 0.687 of pictograms ranked
	// right against 0.661, measured over 115 labelled ones. Whether that is
	// the vendor mirroring a glyph or simply a second chance at an alignment
	// does not matter to a comparison that takes the best fit it can find.
	for i := range 2 * rotations {
		ng := normalise(rotate(gf, float64(i%rotations)*2*math.Pi/rotations))
		if i >= rotations {
			ng = flip(ng)
		}
		s := jaccard(nw, ng)
		if i == 0 {
			upright = s
		}
		if s > best {
			second, best = best, s
			aligned = ng
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

	// One overlap figure hides which way a candidate is wrong. A shape that
	// covers the pictogram and spills past it fails differently from one that
	// sits inside and leaves half of it bare, and the two are worth different
	// amounts: scenery tends to spill, a partly-segmented icon tends to fall
	// short. Jaccard is one minus their sum, so the split is what is new here,
	// not the total.
	//
	// And where the two agree matters as much as how much. Measured in rings
	// out from the centre, so it survives the turning: a glyph and a candidate
	// can agree on their outline and disagree entirely about what is inside
	// it, which is exactly how a solid blob passes for a drawn ring.
	if aligned != nil {
		f[12], f[13] = fitError(nw, aligned)
		r := ringAgreement(nw, aligned)
		for i, v := range r {
			f[14+i] = v
		}
	}
	f[22] = 1 // bias

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

// flip reflects a normalised silhouette left to right.
func flip(m []bool) []bool {
	const n = normalSize
	out := make([]bool, n*n)
	for y := range n {
		for x := range n {
			out[y*n+(n-1-x)] = m[y*n+x]
		}
	}
	return out
}

// rings is how finely the agreement is sliced from the centre outwards.
const rings = 8

// fitError splits disagreement into what the candidate adds and what it
// misses, both as a share of the two together.
func fitError(want, got []bool) (excess, missing float64) {
	var e, m, either float64
	for i := range want {
		switch {
		case want[i] && got[i]:
			either++
		case got[i]:
			e++
			either++
		case want[i]:
			m++
			either++
		}
	}
	if either == 0 {
		return 0, 0
	}
	return e / either, m / either
}

// ringAgreement is the overlap measured separately in four rings out from the
// centre of the grid.
func ringAgreement(want, got []bool) [rings]float64 {
	const n = normalSize
	var both, either [rings]float64
	c := float64(n-1) / 2
	// Scaled to the half-width, not the half-diagonal: a silhouette is fitted
	// to the grid, so it fills the inscribed circle and barely reaches the
	// corners. Scaling to the diagonal left the two outermost rings empty for
	// every shape, which is two of eight measurements saying nothing.
	maxR := c
	for y := range n {
		for x := range n {
			i := y*n + x
			if !want[i] && !got[i] {
				continue
			}
			k := min(int(math.Hypot(float64(x)-c, float64(y)-c)/maxR*rings), rings-1)
			either[k]++
			if want[i] && got[i] {
				both[k]++
			}
		}
	}
	var out [rings]float64
	for k := range out {
		if either[k] > 0 {
			out[k] = both[k] / either[k]
		} else {
			// Neither shape reaches this far. They agree about that, and
			// reporting it as total disagreement is simply false.
			out[k] = 1
		}
	}
	return out
}
