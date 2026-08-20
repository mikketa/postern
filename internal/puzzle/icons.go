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
	Mask       []bool // row-major, W by H
	W, H       int
	CentreX    int // in the source picture's coordinates
	CentreY    int
	Pixels     int
	MinX, MinY int
	sourceW    int
}

// FindIcons segments a picture into the icons drawn on it.
//
// Colour is what separates them, but not any fixed colour: the background of
// one challenge is the icon colour of another. The dominant hue is measured
// and treated as the background — the majority always is — and what stands
// well away from it is an icon. Thresholding on saturation instead was tried
// first and swallowed the whole picture: these backgrounds are saturated too.
// want is how many icons the prompt asks for: the search loosens until it has
// at least that many, because fewer is a guaranteed failure. Pass 0 to take
// whatever the strictest pass yields.
func FindIcons(img image.Image, minPixels, want int) []Shape {
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

	// Progressively looser thresholds, until there are at least as many
	// candidates as the prompt asks for.
	//
	// Measured over twelve collected challenges, the strict rule found fewer
	// than the three icons asked for on five of them — and too few candidates
	// cannot be recovered from later, while too many are merely harder to
	// choose between. An earlier version only loosened when it found nothing
	// at all, which never fired: the failing cases yielded one or two.
	//
	// The strict pass still runs first, so challenges that separate cleanly
	// are not made harder.
	var out []Shape
	for _, look := range []struct{ sat, value, away float64 }{
		{0.35, 60, 60},
		{0.28, 50, 45},
		{0.22, 40, 32},
		{0.15, 30, 22},
		{0.10, 25, 15},
	} {
		mask := make([]bool, w*h)
		for y := range h {
			for x := range w {
				hue, sat, v := hsv(img, b.Min.X+x, b.Min.Y+y)
				mask[y*w+x] = sat > look.sat && v > look.value &&
					hueApart(hue, domHue) > look.away
			}
		}
		got := dropLettering(mergeNested(components(mask, w, h, minPixels)))
		if len(got) > len(out) {
			out = got
		}
		if len(out) >= want && len(out) > 0 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pixels > out[j].Pixels })
	return out
}

// dropLettering removes runs of shapes that are text rather than icons.
//
// These backgrounds carry the vendor's name across them in large coloured
// letters, and letters segment exactly as well as icons do: on one challenge
// four of the ten candidates were the letters of a word, evenly spaced and all
// the same size. That regularity is what gives them away — icons are scattered
// and sized independently, while letters sit on a line, share a height, and
// repeat at a steady pitch.
//
// Three is the shortest run that establishes a pitch; two shapes side by side
// are a coincidence, and dropping them would throw away icons.
func dropLettering(in []Shape) []Shape {
	if len(in) < 3 {
		return in
	}
	byLine := map[int][]int{}
	for i, s := range in {
		// Bucket by baseline, loosely: letters of one word share it.
		byLine[s.CentreY/12] = append(byLine[s.CentreY/12], i)
	}

	drop := map[int]bool{}
	for _, band := range byLine {
		if len(band) < 3 {
			continue
		}
		sort.Slice(band, func(a, b int) bool { return in[band[a]].CentreX < in[band[b]].CentreX })

		// A run is letters when consecutive shapes keep the same height and
		// the same gap between them.
		run := []int{band[0]}
		for k := 1; k < len(band); k++ {
			prev, cur := in[band[k-1]], in[band[k]]
			sameHeight := math.Abs(float64(prev.H-cur.H)) <= float64(max(prev.H, cur.H))*0.25
			pitch := cur.CentreX - prev.CentreX
			steady := true
			if len(run) >= 2 {
				last := in[run[len(run)-1]].CentreX - in[run[len(run)-2]].CentreX
				steady = math.Abs(float64(pitch-last)) <= float64(max(pitch, last))*0.3
			}
			if sameHeight && steady && pitch < max(prev.W, cur.W)*3 {
				run = append(run, band[k])
				continue
			}
			if len(run) >= 3 {
				for _, i := range run {
					drop[i] = true
				}
			}
			run = []int{band[k]}
		}
		if len(run) >= 3 {
			for _, i := range run {
				drop[i] = true
			}
		}
	}

	out := in[:0:0]
	for i, s := range in {
		if !drop[i] {
			out = append(out, s)
		}
	}
	// Never hand back nothing: if the whole picture looked like lettering, the
	// rule is wrong for this challenge and the candidates are better than none.
	if len(out) == 0 {
		return in
	}
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

// Pairing is an answer, with how much it should be trusted.
//
// The confidence is the point of it. Recognising these icons works about a
// third of the time, and a solver that clicks regardless is wrong twice for
// every time it is right. A solver that knows when it is guessing can ask for
// a different picture instead — which is what the widget's refresh button is
// for, and what a person does when they cannot make out the drawing.
type Pairing struct {
	// Order[i] is the icon answering pictogram i.
	Order []int

	// Mean is the average resemblance of the chosen arrangement.
	Mean float64

	// Margin is how much better this arrangement scored than the next best.
	// A picture where two arrangements score alike is a picture that has not
	// been understood, however good the winner looks on its own.
	Margin float64
}

// PairIcons says which icon answers each pictogram, in the order asked.
//
// Every assignment is scored and the best one wins: the boards are small — a
// handful of each — so this is enumerated rather than optimised, and the
// answer is the best whole arrangement rather than a series of independent
// best guesses that can claim the same icon twice.
func PairIcons(wanted, found []Shape) (Pairing, error) {
	if len(wanted) == 0 {
		return Pairing{}, fmt.Errorf("puzzle: the prompt asks for nothing")
	}
	if len(found) < len(wanted) {
		return Pairing{}, fmt.Errorf("puzzle: the prompt asks for %d icons and "+
			"the picture yielded %d", len(wanted), len(found))
	}

	// Every pictogram against every candidate, scored on how much the two
	// silhouettes coincide once filled, resized and turned into alignment.
	// Invariant moments were tried first and were too coarse to separate a
	// redrawn icon from a letter of the vendor's own logo.
	cost := make([][]float64, len(wanted))
	for i := range wanted {
		cost[i] = make([]float64, len(found))
		for j := range found {
			cost[i][j] = 1 - Resembles(wanted[i], found[j])
		}
	}

	best, bestScore, runnerUp := []int(nil), math.Inf(1), math.Inf(1)
	used := make([]bool, len(found))
	cur := make([]int, 0, len(wanted))
	var walk func()
	walk = func() {
		if len(cur) == len(wanted) {
			var sc float64
			for i, j := range cur {
				sc += cost[i][j]
			}
			// The icons of one challenge are drawn by one process at one
			// scale — measured at 49x49, 49x50 and 50x51 on a challenge that
			// solved — while the decorations they hide among are sized
			// independently. An arrangement that mixes a large shape with a
			// small one is usually picking up scenery.
			sc += spread(found, cur)
			// Prefer candidates that look drawn rather than solid: the
			// scenery in these pictures is lettering and photographed
			// objects, and both are solid where an icon is a stroke.
			for _, j := range cur {
				sc += 0.45 * (1 - Drawn(found[j]))
			}
			switch {
			case sc < bestScore:
				bestScore, runnerUp = sc, bestScore
				best = append([]int(nil), cur...)
			case sc < runnerUp:
				runnerUp = sc
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
		return Pairing{}, fmt.Errorf("puzzle: no arrangement of icons could be scored")
	}

	var mean float64
	for i, j := range best {
		mean += 1 - cost[i][j]
	}
	mean /= float64(len(best))

	margin := 0.0
	if !math.IsInf(runnerUp, 1) {
		margin = runnerUp - bestScore
	}
	return Pairing{Order: best, Mean: mean, Margin: margin}, nil
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

// spread penalises an arrangement whose icons are not sized alike, in the same
// units as the resemblance cost so the two can simply be added.
func spread(found []Shape, pick []int) float64 {
	if len(pick) < 2 {
		return 0
	}
	smallest, largest := math.Inf(1), 0.0
	for _, j := range pick {
		d := math.Hypot(float64(found[j].W), float64(found[j].H))
		smallest = math.Min(smallest, d)
		largest = math.Max(largest, d)
	}
	if smallest <= 0 {
		return 0
	}
	// 1.0 for shapes of equal size, rising with the ratio between them.
	return 0.35 * (largest/smallest - 1)
}

// mergeNested collapses candidates that sit inside one another.
//
// An icon drawn as an outline segments twice over — once as its border and
// again as whatever the border encloses — and both arrive as candidates. On a
// collected challenge that cost two of the three places available, leaving the
// real answer nowhere to go. Whichever is larger stands for both.
func mergeNested(in []Shape) []Shape {
	inside := func(a, b Shape) bool {
		// a within b, allowing a small overhang for ragged edges.
		const slack = 4
		return a.MinX >= b.MinX-slack && a.MinY >= b.MinY-slack &&
			a.MinX+a.W <= b.MinX+b.W+slack && a.MinY+a.H <= b.MinY+b.H+slack
	}
	drop := make([]bool, len(in))
	for i := range in {
		for j := range in {
			if i == j || drop[i] || drop[j] {
				continue
			}
			if inside(in[i], in[j]) && in[i].W*in[i].H < in[j].W*in[j].H {
				drop[i] = true
			}
		}
	}
	out := in[:0:0]
	for i, s := range in {
		if !drop[i] {
			out = append(out, s)
		}
	}
	return out
}

// Drawn reports how much a shape looks hand-drawn rather than solid, from 0 to
// 1.
//
// The icons in these challenges are drawn in outline — a wandering stroke that
// leaves most of its own bounding box empty — while the scenery they hide
// among is solid: letters cut from card, photographed objects. Two cheap
// measures separate them. A stroke fills little of the box it occupies, and
// its border is long for the area it encloses, because it wanders.
func Drawn(s Shape) float64 {
	if s.W == 0 || s.H == 0 || s.Pixels == 0 {
		return 0
	}
	density := float64(s.Pixels) / float64(s.W*s.H)

	// Border pixels: filled cells with at least one empty neighbour.
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
	// A solid blob's border grows as the square root of its area; a stroke's
	// grows with the area itself. The ratio is near 0 for one and near 1 for
	// the other.
	wander := float64(border) / float64(s.Pixels)

	sparse := 1 - math.Min(density/0.55, 1)
	return math.Min(0.5*sparse+0.5*math.Min(wander/0.8, 1), 1)
}
