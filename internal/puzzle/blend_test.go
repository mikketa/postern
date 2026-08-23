package puzzle

import (
	"math"
	"math/rand/v2"
	"os"
	"testing"
)

// The network and the fitted vector together.
//
// Apart they measure 0.732 and 0.695 per pictogram, and 37 and 39 whole
// arrangements out of 59: the network ranks a pictogram's candidates better
// and yet settles fewer whole challenges, which can only mean the two are
// wrong about different pictures. The vector leans on silhouette overlap,
// which fails a challenge wholesale when the segmentation was poor; the
// network's mistakes are spread one here and one there. That is the shape of
// two measurements worth adding, so this adds them: the network's score
// becomes one more number in the vector, and the whole thing is fitted again.
//
// It is fitted on synthesised tracings, so every real challenge below is one
// nothing here has seen.

// blendFit fits a softmax over each group's candidates, exactly the loss the
// vector already uses, over plain slices so that a trial measurement can be
// added without disturbing the shape the rest of the package agrees on.
//
// The columns are centred and scaled first, as Model does. One step size and
// one penalty are shared by every weight, so a measurement that happens to be
// counted in hundreds would otherwise swamp one counted in tenths, and the
// penalty would fall on the small one hardest. Fitted without that, the
// vector's own measurements score 0.373 per pictogram against the 0.695 they
// are worth — the arithmetic is the same, the numbers are simply not
// comparable until they are put on one scale.
type blend struct{ W, Mean, Scale []float64 }

func blendFit(groups [][][]float64, answer []int, passes int, rate, decay float64) blend {
	if len(groups) == 0 {
		return blend{}
	}
	n := len(groups[0][0])
	b := blend{W: make([]float64, n), Mean: make([]float64, n), Scale: make([]float64, n)}

	count := 0.0
	for _, g := range groups {
		for _, f := range g {
			for k := range f {
				b.Mean[k] += f[k]
			}
			count++
		}
	}
	for k := range b.Mean {
		b.Mean[k] /= count
	}
	for _, g := range groups {
		for _, f := range g {
			for k := range f {
				d := f[k] - b.Mean[k]
				b.Scale[k] += d * d
			}
		}
	}
	for k := range b.Scale {
		b.Scale[k] = math.Sqrt(b.Scale[k] / count)
		// A measurement that never varies carries nothing; left at one it
		// simply contributes a constant the fit can absorb.
		if b.Scale[k] < 1e-9 {
			b.Scale[k] = 1
		}
	}

	std := make([][][]float64, len(groups))
	for gi, g := range groups {
		std[gi] = make([][]float64, len(g))
		for j, f := range g {
			row := make([]float64, n)
			for k := range f {
				row[k] = (f[k] - b.Mean[k]) / b.Scale[k]
			}
			std[gi][j] = row
		}
	}

	grad := make([]float64, n)
	for range passes {
		for i := range grad {
			grad[i] = 0
		}
		for gi, g := range std {
			z := make([]float64, len(g))
			top := math.Inf(-1)
			for j, f := range g {
				for k := range f {
					z[j] += b.W[k] * f[k]
				}
				top = math.Max(top, z[j])
			}
			sum := 0.0
			for j := range z {
				z[j] = math.Exp(z[j] - top)
				sum += z[j]
			}
			for j := range z {
				z[j] /= sum
			}
			for j, f := range g {
				e := z[j]
				if j == answer[gi] {
					e--
				}
				for k := range f {
					grad[k] += e * f[k]
				}
			}
		}
		scale := 1 / float64(len(std))
		for k := range b.W {
			b.W[k] -= rate * (grad[k]*scale + decay*b.W[k])
		}
	}
	return b
}

func (b blend) score(f []float64) float64 {
	var s float64
	for k := range b.W {
		s += b.W[k] * (f[k] - b.Mean[k]) / b.Scale[k]
	}
	return s
}

func TestTheNetworkAndTheVectorTogether(t *testing.T) {
	dir := os.Getenv("BENCH_DIR")
	labelPath := os.Getenv("LABELS")
	if dir == "" || labelPath == "" {
		t.Skip("no BENCH_DIR or LABELS")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skip(err)
	}

	var names []string
	challenges := map[string]challenge{}
	var folds []netFold
	var glyphs []Shape
	for _, e := range entries {
		c, ok := loadChallenge(dir, e.Name())
		if !ok {
			continue
		}
		names = append(names, e.Name())
		challenges[e.Name()] = c
		for _, g := range c.wanted {
			folds = append(folds, netFold{glyph: g, want: polarMap(g)})
			glyphs = append(glyphs, g)
		}
	}
	if len(folds) < 20 {
		t.Skip("not enough prompts to synthesise from")
	}
	labels := readLabels(t, labelPath)

	junkShapes := sceneryIfAny(t)
	var junk [][]float64
	for _, s := range junkShapes {
		junk = append(junk, polarMap(s))
	}
	n := fitNet(t, folds, junk, envInt("NET_EPOCHS", 200),
		envFloat("NET_RATE", 0.05), envFloat("NET_DECAY", 1e-4))

	// Synthetic groups again, this time measured both ways.
	const perGlyph, distractors = 8, 4
	rng := rand.New(rand.NewPCG(23, 29))
	var train [][][]float64
	var answers []int
	for range perGlyph {
		for i, g := range glyphs {
			cand := []Shape{traceGlyph(g, rng)}
			for k := range distractors {
				if len(junkShapes) > 0 && k%2 == 1 {
					cand = append(cand, junkShapes[rng.IntN(len(junkShapes))])
					continue
				}
				j := rng.IntN(len(glyphs))
				for j == i {
					j = rng.IntN(len(glyphs))
				}
				cand = append(cand, traceGlyph(glyphs[j], rng))
			}
			we := n.Embed(g)
			var group [][]float64
			for _, got := range cand {
				f := Features(g, got, cand)
				row := make([]float64, 0, FeatureCount+1)
				row = append(row, f[:]...)
				row = append(row, cosine(we, n.Embed(got)))
				group = append(group, row)
			}
			train = append(train, group)
			answers = append(answers, 0)
		}
	}
	t.Logf("%d groupes synthetiques, %d mesures par candidat", len(train), FeatureCount+1)

	w := blendFit(train, answers, 400, 0.05, 0.1)
	t.Logf("poids du reseau dans le melange: %+.3f", w.W[FeatureCount])

	// Only the vector's own measurements, fitted the same way on the same
	// groups, so that the comparison is between what is added and nothing else.
	vecOnly := make([][][]float64, len(train))
	for i, g := range train {
		vecOnly[i] = make([][]float64, len(g))
		for j, f := range g {
			vecOnly[i][j] = f[:FeatureCount]
		}
	}
	wv := blendFit(vecOnly, answers, 400, 0.05, 0.1)

	for _, run := range []struct {
		name string
		w    blend
	}{{"vecteur seul", wv}, {"vecteur + reseau", w}} {
		groups, first, whole, ok := 0, 0, 0, 0
		for _, name := range names {
			c := challenges[name]
			answer, has := labels[name]
			if !has || len(answer) != len(c.wanted) || len(c.found) < len(c.wanted) {
				continue
			}
			sc := make([][]float64, len(c.wanted))
			for i, want := range c.wanted {
				we := n.Embed(want)
				sc[i] = make([]float64, len(c.found))
				for j, got := range c.found {
					f := Features(want, got, c.found)
					row := append(append([]float64{}, f[:]...), cosine(we, n.Embed(got)))
					sc[i][j] = run.w.score(row[:len(run.w.W)])
				}
			}
			full := true
			for i, a := range answer {
				if a < 0 || a >= len(c.found) {
					full = false
					continue
				}
				groups++
				best := 0
				for j := range sc[i] {
					if sc[i][j] > sc[i][best] {
						best = j
					}
				}
				if best == a {
					first++
				}
			}
			if !full {
				continue
			}
			whole++
			p, err := pairScored(sc)
			if err != nil {
				continue
			}
			hit := true
			for i, j := range p.Order {
				if j != answer[i] {
					hit = false
				}
			}
			if hit {
				ok++
			}
		}
		if groups == 0 {
			t.Skip("no real groups to measure against")
		}
		got := float64(first) / float64(groups)
		t.Logf("%-18s %.3f par pictogramme sur %d groupes, defis entiers %d/%d",
			run.name, got, groups, ok, whole)
		// The vector on its own is the witness. Fitted elsewhere on the same
		// kind of synthesised groups it is worth 0.695, so if this fitter
		// cannot reproduce that, it is the fitter that is being measured and
		// nothing said about the network below would mean anything.
		if run.name == "vecteur seul" && got < 0.64 {
			t.Fatalf("le vecteur seul tombe a %.3f alors qu'il vaut 0.695: "+
				"c'est l'ajustement qui est en cause, pas le melange", got)
		}
	}
	t.Logf("pour memoire: reseau seul 0.732 et 37/59, vecteur sur vraies reponses 0.679 et 43/59, recouvrement seul 0.664 et 36/59")
}

func cosine(a, b []float64) float64 {
	var d float64
	for i := range a {
		d += a[i] * b[i]
	}
	return d
}
