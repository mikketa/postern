package puzzle

import (
	"image"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The whole chain, end to end, against reconstructed ground truth.
//
// Every other measurement in this package starts from a labels file that says
// which FindIcons candidate each pictogram means — and that file is keyed by
// candidate index, so it is only true of the segmentation that produced it.
// Change how the picture is cut up and the labels quietly stop meaning
// anything, which makes the one number worth having, whether the whole
// arrangement is right, impossible to compare across exactly the changes most
// worth making.
//
// So this starts a step earlier. The vendor reuses a small pool of
// photographs; the per-pixel median over challenges sharing one reconstructs
// it with every drawn icon voted away, and subtracting gives the icons
// themselves, wherever the segmenter happens to put its boxes. Match those to
// whatever the segmenter returned, and the labels are derived rather than
// stored. See train_test.go for how the plates are built.
//
//	BENCH_DIR=<dir> PLATE_DIR=<dir> TRUTH=<file> go test -count=1 \
//	    ./internal/puzzle -run TestTheWholeChainAgainstTruth -v
//
// The number to read is the last one: challenges solved out of challenges
// tried, counting one lost to segmentation as lost. That is what an attempt
// against the live widget is, and nothing else here predicts it.

// truthLine is one challenge's answer: which drawn icon each pictogram means,
// as an index into the difference against the reconstructed photograph.
type truthLine struct {
	name string
	want []int
}

func readTruth(t *testing.T, path string) []truthLine {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skip(err)
	}
	var out []truthLine
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		l := truthLine{name: f[0]}
		for _, s := range f[1:] {
			n, err := strconv.Atoi(s)
			if err != nil {
				n = -1
			}
			l.want = append(l.want, n)
		}
		out = append(out, l)
	}
	return out
}

// drawnIcons is the difference between a challenge and the photograph it was
// drawn on: the icons, exactly, and nothing of the scenery.
//
// The threshold, the two-pixel dilation and the ninety-pixel floor are the
// ones the plates were read with; changing them here would renumber the
// answers in truth.txt, which are indices into this list.
func drawnIcons(bg, plate image.Image) (icons [][]int, w, h int) {
	b := bg.Bounds()
	w, h = b.Dx(), b.Dy()
	if plate.Bounds().Dx() != w || plate.Bounds().Dy() != h {
		return nil, w, h
	}
	mask := make([]bool, w*h)
	for y := range h {
		for x := range w {
			r1, g1, b1, _ := bg.At(b.Min.X+x, b.Min.Y+y).RGBA()
			r2, g2, b2, _ := plate.At(plate.Bounds().Min.X+x, plate.Bounds().Min.Y+y).RGBA()
			if apart8(r1, r2) > 46 || apart8(g1, g2) > 46 || apart8(b1, b2) > 46 {
				mask[y*w+x] = true
			}
		}
	}
	mask = spread(mask, w, h, 2)

	seen := make([]bool, w*h)
	for s := range mask {
		if !mask[s] || seen[s] {
			continue
		}
		stack := []int{s}
		seen[s] = true
		var cur []int
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			cur = append(cur, i)
			x, y := i%w, i/w
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					nx, ny := x+dx, y+dy
					if nx < 0 || ny < 0 || nx >= w || ny >= h {
						continue
					}
					if j := ny*w + nx; mask[j] && !seen[j] {
						seen[j] = true
						stack = append(stack, j)
					}
				}
			}
		}
		if len(cur) >= 90 {
			icons = append(icons, cur)
		}
	}
	// Largest first, which is the order truth.txt counts in.
	sort.Slice(icons, func(i, j int) bool { return len(icons[i]) > len(icons[j]) })
	return icons, w, h
}

func apart8(a, b uint32) int {
	d := int(a>>8) - int(b>>8)
	if d < 0 {
		return -d
	}
	return d
}

func spread(m []bool, w, h, r int) []bool {
	for range r {
		n := make([]bool, len(m))
		copy(n, m)
		for i, on := range m {
			if !on {
				continue
			}
			x, y := i%w, i/w
			if x > 0 {
				n[i-1] = true
			}
			if x < w-1 {
				n[i+1] = true
			}
			if y > 0 {
				n[i-w] = true
			}
			if y < h-1 {
				n[i+w] = true
			}
		}
		m = n
	}
	return m
}

type bounds struct{ x0, y0, x1, y1 int }

func spanOf(px []int, w int) bounds {
	b := bounds{1 << 30, 1 << 30, -1, -1}
	for _, i := range px {
		x, y := i%w, i/w
		b.x0, b.y0 = min(b.x0, x), min(b.y0, y)
		b.x1, b.y1 = max(b.x1, x), max(b.y1, y)
	}
	return b
}

func spanOfShape(s Shape) bounds {
	return bounds{s.MinX, s.MinY, s.MinX + s.W - 1, s.MinY + s.H - 1}
}

// boxOverlap is intersection over union of two boxes.
//
// Boxes, not strokes: an outline drawing fills barely a third of its own
// bounding box, so asking how many of the icon's own pixels a candidate covers
// would reject even an exact segmentation.
func boxOverlap(a, b bounds) float64 {
	ox := max(0, min(a.x1, b.x1)-max(a.x0, b.x0)+1)
	oy := max(0, min(a.y1, b.y1)-max(a.y0, b.y0)+1)
	in := ox * oy
	un := (a.x1-a.x0+1)*(a.y1-a.y0+1) + (b.x1-b.x0+1)*(b.y1-b.y0+1) - in
	if un <= 0 {
		return 0
	}
	return float64(in) / float64(un)
}

// answerFor matches each drawn icon to the candidate whose box covers it best,
// returning -1 for a pictogram whose icon no candidate found.
func answerFor(icons [][]int, w int, want []int, cand []Shape) []int {
	out := make([]int, len(want))
	for k, ti := range want {
		out[k] = -1
		if ti < 0 || ti >= len(icons) {
			continue
		}
		ib := spanOf(icons[ti], w)
		best, score := -1, 0.4
		for j, c := range cand {
			if v := boxOverlap(ib, spanOfShape(c)); v > score {
				best, score = j, v
			}
		}
		out[k] = best
	}
	return out
}

func TestTheWholeChainAgainstTruth(t *testing.T) {
	dir, plateDir := os.Getenv("BENCH_DIR"), os.Getenv("PLATE_DIR")
	truthPath := os.Getenv("TRUTH")
	if dir == "" || plateDir == "" || truthPath == "" {
		t.Skip("no BENCH_DIR, PLATE_DIR or TRUTH")
	}

	var tried, whole, complete, icons, found int
	for _, l := range readTruth(t, truthPath) {
		// With HOLDOUT set this is the half the scenery did not come from.
		if os.Getenv("HOLDOUT") != "" && halfOf(l.name) == 0 {
			continue
		}
		// TUNE is the other half, and the only one a threshold may be chosen
		// against: a number picked on the pictures it is then reported on is
		// not a measurement of anything.
		if os.Getenv("TUNE") != "" && halfOf(l.name) == 1 {
			continue
		}
		bg, e1 := readImage(dir + "/" + l.name + "/bg.png")
		pl, e2 := readImage(plateDir + "/" + l.name + ".png")
		q, e3 := readImage(dir + "/" + l.name + "/ques.png")
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		drawn, w, _ := drawnIcons(bg, pl)
		if len(drawn) == 0 {
			continue
		}
		wanted := FindPictograms(q, 140)
		if len(wanted) != len(l.want) {
			continue
		}
		tried++

		cand := FindIcons(bg, 200, len(wanted))
		answer := answerFor(drawn, w, l.want, cand)
		all := true
		for _, a := range answer {
			icons++
			if a < 0 {
				all = false
			} else {
				found++
			}
		}
		if !all {
			continue
		}
		complete++

		p, err := PairIcons(wanted, cand)
		if err != nil {
			continue
		}
		right := true
		for i, j := range p.Order {
			if j != answer[i] {
				right = false
			}
		}
		if right {
			whole++
		}
	}
	if tried == 0 {
		t.Skip("no challenge could be read")
	}
	t.Logf("icones retrouvees        %d/%d = %.3f", found, icons, ratio(found, icons))
	t.Logf("defis entierement decoupes %d/%d = %.3f", complete, tried, ratio(complete, tried))
	t.Logf("  dont l'arrangement est juste %d/%d = %.3f", whole, complete, ratio(whole, complete))
	t.Logf("DEFIS RESOLUS            %d/%d = %.3f", whole, tried, ratio(whole, tried))
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}
