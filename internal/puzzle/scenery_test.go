package puzzle

import (
	"os"
	"testing"
)

// Scenery: the candidates that are not icons.
//
// Every synthesised group until now was one traced glyph among other traced
// glyphs, which teaches a model to tell one drawing from another and nothing
// whatever about telling a drawing from a piece of a photograph. That is the
// wrong lesson for the job: what the segmenter actually hands over is three
// icons and a handful of window frames, kerbstones and bits of lettering, and
// a model that has never been shown one has no reason to rank it last.
//
// Measured with one half of the collected challenges held back from the
// fitting entirely, on the other half: 22 of 46 challenges solved end to end
// without scenery in the groups, 24 of 46 with it. The segmentation is
// identical in both — the same code cutting up the same pictures — so all of
// the difference is in the choosing, which is where it was predicted.
//
// It does not buy what it was meant to buy. The point of teaching a model to
// refuse scenery was to let the segmenter be generous: it loses 24 true icons
// of 261 by welding drawings to their neighbours, and offering the pieces as
// well finds 35 challenges' worth of icons instead of 33. Measured the same
// way, that is still 22 of 46 — the extra candidates cost the choosing more
// than the extra icons are worth, with scenery in the fitting exactly as
// without it. Twice now, loosening the segmenter has measured better on icons
// found and no better on challenges solved.
//
// There is a free supply of the stuff, correctly labelled and already hard:
// run the segmenter over the collected challenges, match its candidates
// against the reconstructed truth, and everything left over is scenery it
// really does produce. No eye, no guessing, and no real answer used.
func scenery(t *testing.T, dir, plateDir, truthPath string) []Shape {
	t.Helper()
	var out []Shape
	for _, l := range readTruth(t, truthPath) {
		if !inTrainingHalf(l.name) {
			continue
		}
		bg, e1 := readImage(dir + "/" + l.name + "/bg.png")
		pl, e2 := readImage(plateDir + "/" + l.name + ".png")
		if e1 != nil || e2 != nil {
			continue
		}
		drawn, w, _ := drawnIcons(bg, pl)
		if len(drawn) == 0 {
			continue
		}
		cand := FindIcons(bg, 200, len(l.want))
		for j, c := range cand {
			icon := false
			for _, px := range drawn {
				if boxOverlap(spanOf(px, w), spanOfShape(c)) >= 0.4 {
					icon = true
					break
				}
			}
			if !icon {
				out = append(out, cand[j])
			}
		}
	}
	return out
}

// inTrainingHalf splits the collected challenges in two.
//
// The scenery is cut out of the same challenges the whole chain is measured
// against, and there are only a hundred or so pieces of it against hundreds of
// thousands of draws over a fit. A network can simply learn those hundred
// shapes, and would then measure as if it could reject scenery it had never
// seen. So with HOLDOUT set the fitting gets one half and the measuring gets
// the other, and neither ever sees the other's pictures. Unset, both get
// everything, which is what to ship and not what to believe.
func inTrainingHalf(name string) bool {
	if os.Getenv("HOLDOUT") == "" {
		return true
	}
	return halfOf(name) == 0
}

// halfOf puts a challenge in one half or the other by its name, so that the
// split is the same in every run and in every test that consults it.
func halfOf(name string) int {
	h := 0
	for _, c := range name {
		h = h*31 + int(c)
	}
	return ((h % 2) + 2) % 2
}

// sceneryIfAny collects scenery when the plates are to hand, and returns
// nothing when they are not: a fit without it is worse but not wrong, and a
// test that skipped for want of an environment variable would hide that.
func sceneryIfAny(t *testing.T) []Shape {
	t.Helper()
	dir, plateDir := os.Getenv("BENCH_DIR"), os.Getenv("PLATE_DIR")
	truthPath := os.Getenv("TRUTH")
	// NO_SCENERY is how the control is run: everything else the same, and the
	// only difference the thing being measured.
	if dir == "" || plateDir == "" || truthPath == "" || os.Getenv("NO_SCENERY") != "" {
		return nil
	}
	return scenery(t, dir, plateDir, truthPath)
}

func TestHowMuchSceneryTheBenchHolds(t *testing.T) {
	dir, plateDir := os.Getenv("BENCH_DIR"), os.Getenv("PLATE_DIR")
	truthPath := os.Getenv("TRUTH")
	if dir == "" || plateDir == "" || truthPath == "" {
		t.Skip("no BENCH_DIR, PLATE_DIR or TRUTH")
	}
	s := scenery(t, dir, plateDir, truthPath)
	t.Logf("%d candidats de decor collectes", len(s))
	if len(s) < 50 {
		t.Fatalf("only %d: not enough to train against", len(s))
	}
}
