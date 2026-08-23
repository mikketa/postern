package puzzle

import (
	"os"
	"testing"
)

// What the chain would be worth if the picture were cut up perfectly.
//
// The plate subtraction is used as a measuring instrument here and nowhere
// else: it tells us what the icons actually were, so the candidates handed to
// the matcher are exactly right and segmentation cannot be blamed for
// anything. Nothing about it is shippable — it only works for the handful of
// photographs this demo happens to be using — but it answers the one question
// worth answering before rebuilding the segmenter: whether the recogniser is
// capable of the number being asked of it even when perfectly fed.
func TestTheCeilingIfSegmentationWerePerfect(t *testing.T) {
	dir, plateDir := os.Getenv("BENCH_DIR"), os.Getenv("PLATE_DIR")
	truthPath := os.Getenv("TRUTH")
	if dir == "" || plateDir == "" || truthPath == "" {
		t.Skip("no bench")
	}
	var tried, whole, usable, picto, first int
	for _, l := range readTruth(t, truthPath) {
		bg, e1 := readImage(dir + "/" + l.name + "/bg.png")
		pl, e2 := readImage(plateDir + "/" + l.name + ".png")
		q, e3 := readImage(dir + "/" + l.name + "/ques.png")
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		drawn, w, h := drawnIcons(bg, pl)
		if len(drawn) == 0 {
			continue
		}
		wanted := FindPictograms(q, 140)
		if len(wanted) != len(l.want) {
			continue
		}
		tried++

		var cand []Shape
		for _, px := range drawn {
			cand = append(cand, shapeFromPixels(px, w, h))
		}
		ok := true
		for _, ti := range l.want {
			if ti < 0 || ti >= len(cand) {
				ok = false
			}
		}
		if !ok || len(cand) < len(wanted) {
			continue
		}
		usable++
		// Per pictogram as well: a challenge needs all three, and the two
		// numbers say different things about where the remainder goes.
		for i, want := range wanted {
			picto++
			best, top := 0, -1e18
			for j, got := range cand {
				if v := trained.dot(Features(want, got, cand)); v > top {
					top, best = v, j
				}
			}
			if best == l.want[i] {
				first++
			}
		}
		p, err := PairIcons(wanted, cand)
		if err != nil {
			continue
		}
		right := true
		for i, j := range p.Order {
			if j != l.want[i] {
				right = false
			}
		}
		if right {
			whole++
		}
	}
	t.Logf("defis utilisables %d/%d", usable, tried)
	t.Logf("PLAFOND avec decoupe parfaite: %d/%d = %.3f", whole, usable, ratio(whole, usable))
	t.Logf("  par pictogramme: %d/%d = %.3f", first, picto, ratio(first, picto))
	t.Logf("  (decoupage actuel: 47/90 = 0.522 ; sur defis complets 47/61 = 0.770)")
	// 72 of 85 whole, 223 of 255 pictograms, against 47 of 90 and 0.768 as
	// the picture is really cut up. Two things follow and both are worth
	// having in writing.
	//
	// Perfect segmentation would be worth 0.522 to 0.847, which is the largest
	// gain left anywhere in this package — and no further. Anyone asking this
	// chain for 0.95 on one picture is asking the recogniser for something it
	// cannot do however well it is fed, because 0.875 of pictograms first is
	// what it manages on candidates that are exactly right.
	//
	// And the recogniser is not far off its own data. It is fitted on tracings
	// synthesised from the prompts, and 0.875 against candidates cut out
	// perfectly says the remaining errors are glyphs that genuinely look alike
	// once traced by hand — a balloon, a magnifier and a map pin are all a
	// round thing on a stem — rather than anything a larger network would
	// obviously fix.
}

func shapeFromPixels(px []int, w, h int) Shape {
	b := spanOf(px, w)
	sw, sh := b.x1-b.x0+1, b.y1-b.y0+1
	s := Shape{Mask: make([]bool, sw*sh), W: sw, H: sh,
		MinX: b.x0, MinY: b.y0, Pixels: len(px), sourceW: w}
	var sx, sy int
	for _, i := range px {
		x, y := i%w, i/w
		s.Mask[(y-b.y0)*sw+(x-b.x0)] = true
		sx += x
		sy += y
	}
	s.CentreX, s.CentreY = sx/len(px), sy/len(px)
	return s
}
