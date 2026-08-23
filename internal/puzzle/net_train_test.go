package puzzle

import (
	"math"
	"math/rand/v2"
	"testing"
)

// A wrong gradient does not crash. It trains to something worse and leaves
// nothing behind that says why, so it is checked against the definition:
// nudge one weight either way, see how much the loss actually moved, and
// compare that with what the backward pass claimed.

func randomGroup(rng *rand.Rand, candidates int) netGroup {
	m := func() []float64 {
		v := make([]float64, polarRadii*polarAngles)
		for i := range v {
			// Sparse and positive, like a polar map of a stroke.
			if rng.Float64() < 0.3 {
				v[i] = rng.Float64()
			}
		}
		return v
	}
	g := netGroup{Want: m(), Answer: rng.IntN(candidates)}
	for range candidates {
		g.Got = append(g.Got, m())
	}
	return g
}

// TestTheGradientsAreTheOnesTheLossHas checks the backward pass against
// numerical differences, on a sample of every parameter block.
//
// The biases are moved off zero first, and that is not cosmetic. A fresh net
// starts every bias at zero, and a polar map is mostly empty, so a great many
// first-layer windows see nothing at all and land exactly on the rectifier's
// kink. Nudging such a bias up wakes them, nudging it down leaves them asleep,
// so a centred difference reports half of a jump that the derivative — right
// to call the kink flat — reports as nothing. That is a property of the
// rectifier, not a bug, and measuring it teaches nothing about the code. Away
// from the kink the two agree to every digit, which is what is demanded here.
func TestTheGradientsAreTheOnesTheLossHas(t *testing.T) {
	rng := rand.New(rand.NewPCG(21, 22))
	n := NewNet(4)
	for _, b := range [][]float64{n.C1.B, n.C2.B, n.C3.B, n.D.B} {
		for i := range b {
			b[i] = rng.NormFloat64() * 0.5
		}
	}
	group := randomGroup(rng, 4)

	g := newNetGrads(n)
	base, _ := n.lossAndGrads(group, g)
	if math.IsNaN(base) || math.IsInf(base, 0) {
		t.Fatalf("the loss came out %v before anything was perturbed", base)
	}

	loss := func() float64 {
		throwaway := newNetGrads(n)
		l, _ := n.lossAndGrads(group, throwaway)
		return l
	}

	const eps = 1e-6
	blocks := []struct {
		name string
		w, d []float64
	}{
		{"conv1.W", n.C1.W, g.C1.W},
		{"conv1.B", n.C1.B, g.C1.B},
		{"conv2.W", n.C2.W, g.C2.W},
		{"conv2.B", n.C2.B, g.C2.B},
		{"conv3.W", n.C3.W, g.C3.W},
		{"conv3.B", n.C3.B, g.C3.B},
		{"dense.W", n.D.W, g.D.W},
		{"dense.B", n.D.B, g.D.B},
	}

	checked, bad := 0, 0
	for _, b := range blocks {
		step := max(1, len(b.w)/12)
		for i := 0; i < len(b.w); i += step {
			was := b.w[i]
			b.w[i] = was + eps
			up := loss()
			b.w[i] = was - eps
			down := loss()
			b.w[i] = was

			numeric := (up - down) / (2 * eps)
			analytic := b.d[i]
			scale := math.Max(1, math.Max(math.Abs(numeric), math.Abs(analytic)))
			checked++
			if math.Abs(numeric-analytic)/scale > 1e-4 {
				bad++
				if bad <= 5 {
					t.Logf("%s[%d]: backward says %+.8f, the loss moved %+.8f",
						b.name, i, analytic, numeric)
				}
			}
		}
	}

	// The scale is one number and worth checking on its own.
	was := n.Scale
	n.Scale = was + eps
	up := loss()
	n.Scale = was - eps
	down := loss()
	n.Scale = was
	numeric := (up - down) / (2 * eps)
	if d := math.Abs(numeric - g.Scale); d/math.Max(1, math.Abs(numeric)) > 1e-4 {
		t.Errorf("scale: backward says %+.8f, the loss moved %+.8f", g.Scale, numeric)
	}

	if checked == 0 {
		t.Fatal("nothing was checked")
	}
	if bad > 0 {
		t.Fatalf("%d of %d gradients disagree with the loss", bad, checked)
	}
	t.Logf("%d gradients checked, all agree with the loss", checked)
}
