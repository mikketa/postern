package puzzle

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"runtime"
	"sync"
	"testing"
)

// Fitting the network, and measuring it on challenges it never saw.
//
// The tracings are synthesised fresh every epoch rather than drawn once and
// reused, so there is no fixed training set to memorise: the network sees a
// glyph wobbled, speckled, mirrored and turned differently every time it meets
// it. That is the whole reason a network is worth trying here at all — the
// real challenges number in the hundreds, which is far too few, but the
// vendor's own prompts are clean pictograms and traceGlyph already reproduces
// what a hand-drawn one looks like closely enough that a linear model fitted
// on synthesised tracings alone beats one fitted on the real answers.

// netFold gathers what training needs from one glyph: its map, computed once
// because the prompt never changes, and the glyph itself for tracing.
type netFold struct {
	glyph Shape
	want  []float64
}

// synthGroup traces one glyph and a handful of others into a group shaped like
// a real one: one pictogram, its tracing among tracings of other glyphs. The
// distractors are traced too, so the network cannot win by noticing which
// candidate was drawn by hand rather than which one is the right shape.
func synthGroup(folds []netFold, i int, distractors int, rng *rand.Rand) netGroup {
	g := netGroup{Want: folds[i].want, Answer: 0}
	g.Got = append(g.Got, polarMap(traceGlyph(folds[i].glyph, rng)))
	for range distractors {
		j := rng.IntN(len(folds))
		for j == i {
			j = rng.IntN(len(folds))
		}
		g.Got = append(g.Got, polarMap(traceGlyph(folds[j].glyph, rng)))
	}
	return g
}

// batchGrads runs a batch of groups over as many threads as the machine has,
// each with its own gradients, and sums them. Nothing is shared but the
// network, which is only read.
func batchGrads(n *Net, batch []netGroup, into *netGrads) (loss float64, right int) {
	workers := min(runtime.NumCPU(), len(batch))
	if workers < 1 {
		workers = 1
	}
	parts := make([]*netGrads, workers)
	losses := make([]float64, workers)
	rights := make([]int, workers)
	var wg sync.WaitGroup
	for w := range workers {
		parts[w] = newNetGrads(n)
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for k := w; k < len(batch); k += workers {
				l, ok := n.lossAndGrads(batch[k], parts[w])
				losses[w] += l
				if ok {
					rights[w]++
				}
			}
		}(w)
	}
	wg.Wait()

	into.zero()
	add := func(dst, src []float64) {
		for i := range dst {
			dst[i] += src[i]
		}
	}
	for w := range workers {
		add(into.C1.W, parts[w].C1.W)
		add(into.C1.B, parts[w].C1.B)
		add(into.C2.W, parts[w].C2.W)
		add(into.C2.B, parts[w].C2.B)
		add(into.C3.W, parts[w].C3.W)
		add(into.C3.B, parts[w].C3.B)
		add(into.D.W, parts[w].D.W)
		add(into.D.B, parts[w].D.B)
		into.Scale += parts[w].Scale
		loss += losses[w]
		right += rights[w]
	}
	scale := 1 / float64(len(batch))
	for _, s := range [][]float64{into.C1.W, into.C1.B, into.C2.W, into.C2.B,
		into.C3.W, into.C3.B, into.D.W, into.D.B} {
		for i := range s {
			s[i] *= scale
		}
	}
	into.Scale *= scale
	return loss, right
}

// netScores scores every pictogram against every candidate, embedding each
// shape once rather than once per pair.
func netScores(n *Net, wanted, found []Shape) [][]float64 {
	we := make([][]float64, len(wanted))
	for i, s := range wanted {
		we[i] = n.Embed(s)
	}
	fe := make([][]float64, len(found))
	for j, s := range found {
		fe[j] = n.Embed(s)
	}
	out := make([][]float64, len(wanted))
	for i := range wanted {
		out[i] = make([]float64, len(found))
		for j := range found {
			var d float64
			for k := range we[i] {
				d += we[i][k] * fe[j][k]
			}
			out[i][j] = n.Scale * d
		}
	}
	return out
}

// fitNet trains a network on tracings synthesised fresh every epoch.
func fitNet(t *testing.T, folds []netFold, epochs int, rate, decay float64) *Net {
	const batch, distractors = 32, 4

	n := NewNet(7)
	grads := newNetGrads(n)
	mom := &netMomentum{g: newNetGrads(n)}
	rng := rand.New(rand.NewPCG(11, 13))

	order := make([]int, len(folds))
	for i := range order {
		order[i] = i
	}
	for e := range epochs {
		rng.Shuffle(len(order), func(a, b int) { order[a], order[b] = order[b], order[a] })
		var loss float64
		var right, seen int
		for s := 0; s < len(order); s += batch {
			end := min(s+batch, len(order))
			b := make([]netGroup, 0, end-s)
			for _, i := range order[s:end] {
				b = append(b, synthGroup(folds, i, distractors, rng))
			}
			l, r := batchGrads(n, b, grads)
			loss += l
			right += r
			seen += len(b)
			// The rate is eased down over the run: large steps to find the
			// shape of the thing, small ones to settle into it.
			warm := 1.0
			if e < 2 {
				warm = 0.2
			}
			cos := 0.5 * (1 + math.Cos(math.Pi*float64(e)/float64(epochs)))
			n.step(grads, mom, rate*warm*(0.1+0.9*cos), decay)
		}
		if e%20 == 0 || e == epochs-1 {
			t.Logf("epoque %3d: perte %.4f, tracés bien classés %.3f, echelle %.1f",
				e, loss/float64(seen), float64(right)/float64(seen), n.Scale)
		}
	}
	return n
}

func TestANetworkFittedOnSynthesisedTracingsOnly(t *testing.T) {
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
	for _, e := range entries {
		c, ok := loadChallenge(dir, e.Name())
		if !ok {
			continue
		}
		names = append(names, e.Name())
		challenges[e.Name()] = c
		for _, g := range c.wanted {
			folds = append(folds, netFold{glyph: g, want: polarMap(g)})
		}
	}
	if len(folds) < 20 {
		t.Skip("not enough prompts to synthesise from")
	}
	labels := readLabels(t, labelPath)

	n := fitNet(t, folds, envInt("NET_EPOCHS", 40),
		envFloat("NET_RATE", 0.05), envFloat("NET_DECAY", 1e-4))

	// Every real group the network never saw.
	//
	// The arrangement is scored at several temperatures as well as the one the
	// network learnt. Which candidate ranks first does not depend on the
	// temperature at all, so the per-pictogram figure is the same for every
	// one of them; the arrangement is a trade-off between pictograms, and a
	// score too sharp to gradate leaves it nothing to trade off with.
	temps := []float64{1, 2, 4, 8, 16, 32}
	wholeAt := make([]int, len(temps))
	groups, first, whole, ok := 0, 0, 0, 0
	for _, name := range names {
		c := challenges[name]
		answer, has := labels[name]
		if !has || len(answer) != len(c.wanted) {
			continue
		}
		if len(c.found) < len(c.wanted) {
			continue
		}
		sc := netScores(n, c.wanted, c.found)
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

		for ti, temp := range temps {
			cooled := make([][]float64, len(sc))
			for i := range sc {
				cooled[i] = make([]float64, len(sc[i]))
				for j := range sc[i] {
					cooled[i][j] = sc[i][j] / temp
				}
			}
			q, err := pairScored(cooled)
			if err != nil {
				continue
			}
			right := true
			for i, j := range q.Order {
				if j != answer[i] {
					right = false
				}
			}
			if right {
				wholeAt[ti]++
			}
		}
	}
	if out := os.Getenv("EMITNET"); out != "" {
		emitNet(t, out, n)
	}

	if groups == 0 {
		t.Skip("no real groups to measure against")
	}
	t.Logf("SUR LES VRAIS DEFIS: %.3f par pictogramme, sur %d groupes",
		float64(first)/float64(groups), groups)
	t.Logf("  (vecteur ajusté sur les vraies réponses: 0.679, sur des tracés synthétiques: 0.695, recouvrement seul: 0.664)")
	if whole > 0 {
		t.Logf("DEFIS ENTIERS: %d/%d = %.3f  (vecteur sur vraies réponses: 43/59 = 0.729)",
			ok, whole, float64(ok)/float64(whole))
		for ti, temp := range temps {
			t.Logf("  a l'echelle %.1f (temperature %.0f): %d/%d",
				n.Scale/temp, temp, wholeAt[ti], whole)
		}
	}
}

func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func envFloat(name string, def float64) float64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	var f float64
	if _, err := fmt.Sscanf(v, "%g", &f); err != nil {
		return def
	}
	return f
}
