package puzzle

import "math"

// Training the network.
//
// Kept beside it rather than in a test file because a backward pass is part of
// a model, not part of checking one, and because it is the half where a
// mistake is silent: a wrong gradient does not crash, it just trains to
// something worse and leaves no trace of why. The linker drops all of this
// from a build that only ever runs Embed.
//
// The loss is the same one the linear model uses — a softmax over the
// candidates of one pictogram, exactly one of which is right — so the two are
// measured on the same question and their numbers mean the same thing.

// netGrads accumulates the derivative of the loss with respect to every
// parameter.
type netGrads struct {
	C1, C2, C3 convGrads
	D          denseGrads
	Scale      float64
}

type convGrads struct{ W, B []float64 }
type denseGrads struct{ W, B []float64 }

func newNetGrads(n *Net) *netGrads {
	return &netGrads{
		C1: convGrads{W: make([]float64, len(n.C1.W)), B: make([]float64, len(n.C1.B))},
		C2: convGrads{W: make([]float64, len(n.C2.W)), B: make([]float64, len(n.C2.B))},
		C3: convGrads{W: make([]float64, len(n.C3.W)), B: make([]float64, len(n.C3.B))},
		D:  denseGrads{W: make([]float64, len(n.D.W)), B: make([]float64, len(n.D.B))},
	}
}

func (g *netGrads) zero() {
	for _, s := range [][]float64{g.C1.W, g.C1.B, g.C2.W, g.C2.B, g.C3.W, g.C3.B, g.D.W, g.D.B} {
		for i := range s {
			s[i] = 0
		}
	}
	g.Scale = 0
}

// backConv sends a gradient back through a convolution and its rectifier,
// adding what it owes to the weights on the way.
func backConv(c conv, g *convGrads, in, act, dAct []float64, radii int) []float64 {
	const T = polarAngles
	dIn := make([]float64, c.In*radii*T)
	for oc := range c.Out {
		for r := range radii {
			for t := range T {
				o := (oc*radii+r)*T + t
				// Past a rectifier nothing flows where nothing came through.
				if act[o] <= 0 {
					continue
				}
				d := dAct[o]
				if d == 0 {
					continue
				}
				g.B[oc] += d
				for ic := range c.In {
					for kr := -1; kr <= 1; kr++ {
						rr := r + kr
						if rr < 0 || rr >= radii {
							continue
						}
						for kt := -1; kt <= 1; kt++ {
							tt := (t + kt + T) % T
							wi := ((oc*c.In+ic)*3+(kr+1))*3 + (kt + 1)
							ii := (ic*radii+rr)*T + tt
							g.W[wi] += d * in[ii]
							dIn[ii] += d * c.W[wi]
						}
					}
				}
			}
		}
	}
	return dIn
}

func backPoolRadii(dOut []float64, from []int, ch, radii int) []float64 {
	const T = polarAngles
	half := radii / 2
	dIn := make([]float64, ch*radii*T)
	for c := range ch {
		for r := range half {
			for t := range T {
				o := (c*half+r)*T + t
				dIn[(c*radii+from[o])*T+t] += dOut[o]
			}
		}
	}
	return dIn
}

func backMaxAngle(dOut []float64, at []int, ch, radii int) []float64 {
	const T = polarAngles
	dIn := make([]float64, ch*radii*T)
	for c := range ch {
		for r := range radii {
			o := c*radii + r
			dIn[(c*radii+r)*T+at[o]] += dOut[o]
		}
	}
	return dIn
}

func backDense(d dense, g *denseGrads, in, dOut []float64) []float64 {
	dIn := make([]float64, d.In)
	for j := range d.Out {
		o := dOut[j]
		if o == 0 {
			continue
		}
		g.B[j] += o
		for i := range d.In {
			g.W[j*d.In+i] += o * in[i]
			dIn[i] += o * d.W[j*d.In+i]
		}
	}
	return dIn
}

// backward sends a gradient on the embedding back through one forward pass.
func (n *Net) backward(p *pass, g *netGrads, dEmbed []float64) {
	// Through the unit-length step: only the part of the gradient across the
	// embedding matters, since moving along it changes nothing after scaling.
	var along float64
	for i := range dEmbed {
		along += dEmbed[i] * p.embed[i]
	}
	dRaw := make([]float64, len(p.raw))
	for i := range dRaw {
		dRaw[i] = (dEmbed[i] - p.embed[i]*along) / p.norm
	}

	dPooled := backDense(n.D, &g.D, p.pooled, dRaw)
	dA3 := backMaxAngle(dPooled, p.imax, netC3, netR3)
	dP2 := backConv(n.C3, &g.C3, p.p2, p.a3, dA3, netR3)
	dA2 := backPoolRadii(dP2, p.i2, netC2, netR2)
	dP1 := backConv(n.C2, &g.C2, p.p1, p.a2, dA2, netR2)
	dA1 := backPoolRadii(dP1, p.i1, netC1, netR1)
	backConv(n.C1, &g.C1, p.in, p.a1, dA1, netR1)
}

// netGroup is one pictogram's worth of training: the glyph asked for, the
// candidates, and which of them is right.
type netGroup struct {
	Want   []float64   // polar map of the glyph
	Got    [][]float64 // polar maps of the candidates
	Answer int
}

// lossAndGrads runs one group forward and back, returning the loss and whether
// the right candidate came first.
func (n *Net) lossAndGrads(gr netGroup, g *netGrads) (float64, bool) {
	pw := n.run(gr.Want)
	ps := make([]*pass, len(gr.Got))
	score := make([]float64, len(gr.Got))
	dot := make([]float64, len(gr.Got))
	top := math.Inf(-1)
	best := 0
	for j, m := range gr.Got {
		ps[j] = n.run(m)
		var d float64
		for i := range pw.embed {
			d += pw.embed[i] * ps[j].embed[i]
		}
		dot[j] = d
		score[j] = n.Scale * d
		if score[j] > score[best] {
			best = j
		}
		top = math.Max(top, score[j])
	}

	sum := 0.0
	prob := make([]float64, len(score))
	for j := range score {
		prob[j] = math.Exp(score[j] - top)
		sum += prob[j]
	}
	for j := range prob {
		prob[j] /= sum
	}
	loss := -math.Log(math.Max(prob[gr.Answer], 1e-12))

	dWant := make([]float64, netEmb)
	for j := range gr.Got {
		e := prob[j]
		if j == gr.Answer {
			e--
		}
		if e == 0 {
			continue
		}
		g.Scale += e * dot[j]
		dGot := make([]float64, netEmb)
		for i := range dGot {
			dGot[i] = e * n.Scale * pw.embed[i]
			dWant[i] += e * n.Scale * ps[j].embed[i]
		}
		n.backward(ps[j], g, dGot)
	}
	n.backward(pw, g, dWant)
	return loss, best == gr.Answer
}

// step moves every parameter against its gradient, with momentum.
type netMomentum struct{ g *netGrads }

func (n *Net) step(g *netGrads, m *netMomentum, rate, decay float64) {
	apply := func(w, grad, vel []float64) {
		for i := range w {
			v := 0.9*vel[i] - rate*(grad[i]+decay*w[i])
			vel[i] = v
			w[i] += v
		}
	}
	apply(n.C1.W, g.C1.W, m.g.C1.W)
	apply(n.C1.B, g.C1.B, m.g.C1.B)
	apply(n.C2.W, g.C2.W, m.g.C2.W)
	apply(n.C2.B, g.C2.B, m.g.C2.B)
	apply(n.C3.W, g.C3.W, m.g.C3.W)
	apply(n.C3.B, g.C3.B, m.g.C3.B)
	apply(n.D.W, g.D.W, m.g.D.W)
	apply(n.D.B, g.D.B, m.g.D.B)

	v := 0.9*m.g.Scale - rate*g.Scale
	m.g.Scale = v
	n.Scale += v
	// A scale that can go negative or run away turns the softmax inside out.
	n.Scale = math.Min(math.Max(n.Scale, 1), 40)
}
