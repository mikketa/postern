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

	// The background colour: the commonest colour in the picture, full stop.
	//
	// Weighting the vote by how colourful a pixel is looks sensible and
	// inverts the whole result on a pale background. There the only saturated
	// pixels in the picture ARE the icons, so they win the vote, the icons are
	// taken for the background, and the pale scenery standing out from them is
	// returned as the icons. Measured on collected challenges with a light
	// grey backdrop: every candidate landed on the lettering and not one of
	// the three icons was found.
	//
	// So every pixel votes, in a coarse three-dimensional histogram. A
	// desaturated background is a colour like any other and wins on numbers,
	// which is what being the background means.
	const hueBins, satBins, valBins = 12, 4, 4
	hist := make([]int, hueBins*satBins*valBins)
	bucket := func(hue, sat, v float64) int {
		hi := min(int(hue/360*hueBins), hueBins-1)
		si := min(int(sat*satBins), satBins-1)
		vi := min(int(v/256*valBins), valBins-1)
		return (hi*satBins+si)*valBins + vi
	}
	for y := range h {
		for x := range w {
			hist[bucket(hsv(img, b.Min.X+x, b.Min.Y+y))]++
		}
	}
	dom := 0
	for i, n := range hist {
		if n > hist[dom] {
			dom = i
		}
	}
	domHue := (float64(dom/(satBins*valBins)) + 0.5) * 360 / hueBins
	domSat := (float64(dom/valBins%satBins) + 0.5) / satBins
	domVal := (float64(dom%valBins) + 0.5) * 256 / valBins

	// How far a colour is from the background, across all three axes.
	//
	// Hue alone loses icons outright: a cyan icon on a blue background is
	// thirty degrees away and never clears a hue threshold, however much
	// brighter and more saturated it plainly is. But hue also has to be
	// discounted when either colour is close to grey, because the hue of a
	// grey is arbitrary — that is what makes a pale background pick fights
	// with everything.
	apartFrom := func(hue, sat, v float64) float64 {
		weight := math.Min(math.Min(sat, domSat)/0.4, 1)
		dh := hueApart(hue, domHue) / 180 * weight
		ds := math.Abs(sat - domSat)
		dv := math.Abs(v-domVal) / 255
		return math.Sqrt(dh*dh + 0.6*ds*ds + 0.3*dv*dv)
	}

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
	// The scale is the colour distance above, so these start high: a threshold
	// low enough to catch a cyan icon on a blue background also lets the
	// background through, and the picture arrives as one blob. Strict first,
	// loosening only while there are too few candidates.
	for _, minApart := range []float64{0.62, 0.52, 0.44, 0.37, 0.31, 0.26, 0.21} {
		mask := make([]bool, w*h)
		for y := range h {
			for x := range w {
				mask[y*w+x] = apartFrom(hsv(img, b.Min.X+x, b.Min.Y+y)) > minApart
			}
		}
		raw := components(mask, w, h, minPixels)

		// Welding the pieces of a drawing together is usually right — an icon
		// is not always drawn in one stroke — but it can also weld an icon to
		// its neighbour and leave too few candidates. So it is tried first and
		// dropped when it costs more than it buys.
		got := dropLettering(mergeNested(mergeTouching(raw)))
		if len(got) < want {
			if loose := dropLettering(mergeNested(raw)); len(loose) > len(got) {
				got = loose
			}
		}
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
// Splitting on empty columns was the obvious way and it miscounts: on a bench
// of twelve collected prompts it found three only eight times, once finding a
// single shape where there were plainly three, and once four. Both directions
// are fatal — the number of pictograms is the number of icons to click, so
// getting it wrong fails the challenge before any recognition happens.
//
// The strip is laid out regularly instead: square pictograms, evenly spaced,
// filling its height. So the count comes from the geometry — how many
// pictogram-heights fit across the occupied width — and the strip is then cut
// into that many equal parts. A pictogram drawn in separate pieces, a figure
// with a detached head, stays one shape rather than becoming two.
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

	// The occupied box: the strip is usually wider than what is drawn on it.
	minX, maxX, minY, maxY := w, -1, h, -1
	for y := range h {
		for x := range w {
			if mask[y*w+x] {
				minX, maxX = min(minX, x), max(maxX, x)
				minY, maxY = min(minY, y), max(maxY, y)
			}
		}
	}
	if maxX < 0 {
		return nil
	}
	span := maxX - minX + 1

	// Square and side by side, so the count is the width over the height.
	//
	// The strip's own height is the reference, not the height of what is drawn
	// on it: a pictogram that does not reach the top and bottom makes the
	// occupied height too small and the count comes out one too many, which
	// two of twelve collected prompts did.
	pitch := h
	count := int(math.Round(float64(span) / float64(pitch)))
	count = min(max(count, 1), 8)

	out := make([]Shape, 0, count)
	for i := range count {
		from := minX + span*i/count
		to := minX + span*(i+1)/count - 1
		var cells []int
		for y := range h {
			for x := from; x <= to && x < w; x++ {
				if mask[y*w+x] {
					cells = append(cells, y*w+x)
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
// The confidence is the point of it. Recognising these icons works some of the
// time, and a solver that clicks regardless is wrong more often than not. A
// solver that knows when it is guessing can ask for a different picture
// instead — which is what the widget's refresh button is for, and what a
// person does when they cannot make out the drawing.
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

	// Every pictogram against every candidate, scored by the trained model.
	//
	// Silhouette overlap alone was the previous rule and it is still the
	// single most useful measurement — it carries the largest weight — but it
	// is one of twelve, and what each is worth was fitted to challenges whose
	// answers are known rather than guessed at. Hand-picked weights are how
	// this got stuck: every adjustment traded one challenge for another.
	cost := make([][]float64, len(wanted))
	for i := range wanted {
		cost[i] = make([]float64, len(found))
		for j := range found {
			// Negated: the search below minimises.
			cost[i][j] = -trained.dot(Features(wanted[i], found[j], found))
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
		mean += sigmoid(-cost[i][j])
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

// mergeTouching joins candidates that are parts of one drawing.
//
// An icon is not always drawn in one piece: a pause symbol is two bars, a
// figure can have a detached head. Segmentation returns each piece separately,
// so the icon occupies several of the places available and none of the pieces
// looks like the pictogram being asked for.
//
// Only near neighbours are joined, and only when the result stays icon-sized —
// otherwise a background busy with colour would collapse into one blob, which
// is the failure mode at the other end of this.
func mergeTouching(in []Shape) []Shape {
	const gap = 9       // pixels apart, at most
	const biggest = 110 // the joined box may not exceed this

	// Union-find over the candidates.
	parent := make([]int, len(in))
	for i := range parent {
		parent[i] = i
	}
	// Not recursive: it walks to the root and flattens as it goes, so the
	// declaration does not need to be separate.
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}

	near := func(a, b Shape) bool {
		dx := max(0, max(a.MinX-(b.MinX+b.W), b.MinX-(a.MinX+a.W)))
		dy := max(0, max(a.MinY-(b.MinY+b.H), b.MinY-(a.MinY+a.H)))
		if dx > gap || dy > gap {
			return false
		}
		w := max(a.MinX+a.W, b.MinX+b.W) - min(a.MinX, b.MinX)
		h := max(a.MinY+a.H, b.MinY+b.H) - min(a.MinY, b.MinY)
		return w <= biggest && h <= biggest
	}

	for i := range in {
		for j := i + 1; j < len(in); j++ {
			if near(in[i], in[j]) {
				if ri, rj := find(i), find(j); ri != rj {
					parent[ri] = rj
				}
			}
		}
	}

	groups := map[int][]int{}
	for i := range in {
		r := find(i)
		groups[r] = append(groups[r], i)
	}

	out := in[:0:0]
	for _, members := range groups {
		if len(members) == 1 {
			out = append(out, in[members[0]])
			continue
		}
		out = append(out, weld(in, members))
	}
	return out
}

// weld builds one shape from several, on a canvas covering them all.
func weld(in []Shape, members []int) Shape {
	minX, minY, maxX, maxY := 1<<30, 1<<30, -1, -1
	for _, i := range members {
		s := in[i]
		minX, minY = min(minX, s.MinX), min(minY, s.MinY)
		maxX = max(maxX, s.MinX+s.W-1)
		maxY = max(maxY, s.MinY+s.H-1)
	}
	w, h := maxX-minX+1, maxY-minY+1
	out := Shape{Mask: make([]bool, w*h), W: w, H: h,
		MinX: minX, MinY: minY,
		CentreX: (minX + maxX) / 2, CentreY: (minY + maxY) / 2,
		sourceW: in[members[0]].sourceW}
	for _, i := range members {
		s := in[i]
		for y := range s.H {
			for x := range s.W {
				if !s.Mask[y*s.W+x] {
					continue
				}
				out.Mask[(s.MinY+y-minY)*w+(s.MinX+x-minX)] = true
				out.Pixels++
			}
		}
	}
	return out
}
