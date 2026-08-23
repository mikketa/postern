package puzzle

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// Labelling a challenge without a person looking at it.
//
// truth.txt was read by eye off generated sheets, which is the one step in
// this project that does not scale: a bench four times the size needs four
// times the reading, and reading is where the first label set's errors came
// from. But the plate subtraction hands over the drawn icons exactly, and on
// candidates that exact the recogniser already ranks 0.875 of pictograms first
// and settles 0.847 of whole arrangements — which is not good enough to trust
// blindly, and quite good enough to trust when it is sure.
//
// So this proposes a labelling and reports how sure it is, and the measurement
// below is the only thing that matters about it: of the challenges it claims,
// how many does it get right, against the labels a person actually read. A
// labeller that is right 87% of the time is worse than useless — it would seed
// the exemplar library with wrong drawings, and every later match against them
// would inherit the error.
//
// The lever is the margin. pairScored already reports how far the best
// arrangement beats the runner-up, in log probability summed over the
// pictograms of the challenge, and a challenge whose second-best arrangement
// is far behind is one where nothing was close.

// proposal is one challenge labelled by machine.
type proposal struct {
	name   string
	answer []int
	margin float64
	mean   float64
}

// autoLabel proposes which drawn icon each pictogram is, from candidates cut
// out by the plate subtraction.
func autoLabel(name string, wanted, cand []Shape) (proposal, bool) {
	if len(cand) < len(wanted) || len(wanted) == 0 {
		return proposal{}, false
	}
	scores := make([][]float64, len(wanted))
	for i, w := range wanted {
		scores[i] = make([]float64, len(cand))
		for j, c := range cand {
			scores[i][j] = trained.dot(Features(w, c, cand))
		}
	}
	p, err := pairScored(scores)
	if err != nil {
		return proposal{}, false
	}
	return proposal{name: name, answer: p.Order, margin: p.Margin, mean: p.Mean}, true
}

func TestLabellingWithoutAPersonLooking(t *testing.T) {
	dir, plateDir := os.Getenv("BENCH_DIR"), os.Getenv("PLATE_DIR")
	truthPath := os.Getenv("TRUTH")
	if dir == "" || plateDir == "" || truthPath == "" {
		t.Skip("no BENCH_DIR, PLATE_DIR or TRUTH")
	}

	type outcome struct {
		p     proposal
		right bool
	}
	var got []outcome
	for _, l := range readTruth(t, truthPath) {
		bg, e1 := readImage(dir + "/" + l.name + "/bg.png")
		pl, e2 := readImage(plateDir + "/" + l.name + ".png")
		q, e3 := readImage(dir + "/" + l.name + "/ques.png")
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		drawn, w, h := drawnIcons(bg, pl)
		wanted := FindPictograms(q, 140)
		if len(drawn) == 0 || len(wanted) != len(l.want) {
			continue
		}
		var cand []Shape
		for _, px := range drawn {
			cand = append(cand, shapeFromPixels(px, w, h))
		}
		usable := true
		for _, ti := range l.want {
			if ti < 0 || ti >= len(cand) {
				usable = false
			}
		}
		if !usable {
			continue
		}
		p, ok := autoLabel(l.name, wanted, cand)
		if !ok {
			continue
		}
		right := true
		for i, j := range p.answer {
			if j != l.want[i] {
				right = false
			}
		}
		got = append(got, outcome{p: p, right: right})
	}
	if len(got) < 20 {
		t.Skip("not enough labelled challenges to check against")
	}

	// What accepting only the surest proposals buys and costs.
	sort.Slice(got, func(i, j int) bool { return got[i].p.margin > got[j].p.margin })
	t.Logf("%d defis proposes", len(got))
	for _, keep := range []float64{1.0, 0.9, 0.75, 0.5, 0.25} {
		n := int(float64(len(got)) * keep)
		if n == 0 {
			continue
		}
		ok := 0
		for _, o := range got[:n] {
			if o.right {
				ok++
			}
		}
		t.Logf("  en gardant les %3.0f%% plus surs (marge >= %6.2f): %d/%d justes = %.3f",
			keep*100, got[n-1].p.margin, ok, n, ratio(ok, n))
	}

	// And what a margin threshold does, which is what a collector would use.
	for _, cut := range []float64{0, 2, 4, 6, 8, 12} {
		ok, n := 0, 0
		for _, o := range got {
			if o.p.margin >= cut {
				n++
				if o.right {
					ok++
				}
			}
		}
		if n == 0 {
			continue
		}
		t.Logf("  marge >= %4.1f: %3d defis retenus, %d justes = %.3f",
			cut, n, ok, ratio(ok, n))
	}

	// A threshold chosen on the challenges it is then reported on measures
	// nothing, so it is chosen on one half and reported on the other. The
	// halves are the same ones scenery_test.go splits by.
	var tune, check []outcome
	for _, o := range got {
		if halfOf(o.p.name) == 0 {
			tune = append(tune, o)
		} else {
			check = append(check, o)
		}
	}
	if len(tune) > 4 && len(check) > 4 {
		// The lowest margin on the tuning half at which nothing is wrong.
		best := 0.0
		for _, o := range tune {
			if !o.right && o.p.margin > best {
				best = o.p.margin
			}
		}
		cut := best + 0.01
		ok, n := 0, 0
		for _, o := range check {
			if o.p.margin >= cut {
				n++
				if o.right {
					ok++
				}
			}
		}
		t.Logf("SEUIL CHOISI SUR UNE MOITIE: marge >= %.2f", cut)
		t.Logf("  verifie sur l'autre: %d/%d retenus, %d justes = %.3f",
			n, len(check), ok, ratio(ok, n))
	}

	if out := os.Getenv("EMIT_TRUTH"); out != "" {
		var b strings.Builder
		b.WriteString("# Proposed by machine, not read by eye. See autolabel_test.go.\n")
		for _, o := range got {
			var f []string
			for _, v := range o.p.answer {
				f = append(f, fmt.Sprint(v))
			}
			b.WriteString(fmt.Sprintf("%s %s\n", o.p.name, strings.Join(f, " ")))
		}
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("etiquettes ecrites dans %s", out)
	}
}
