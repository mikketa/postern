package puzzle

import (
	"math"
	"math/rand/v2"
)

// A small convolutional network over the polar description.
//
// The measurements in features.go carry one thing between them — how well two
// silhouettes agree — and fitting weights over them cannot invent a second.
// What is missing is not a better fit but a description that separates glyphs
// sharing a silhouette: a balloon, a magnifier and a map pin are all a round
// thing on a stem, and the overlap between a hand-traced one and the glyph it
// depicts is about the same for all three.
//
// So the description is learned instead of chosen. Each shape is read into an
// embedding, and how alike two shapes are is how aligned their embeddings are.
// Nothing about the architecture is clever; what makes it work here is that
// the input is the polar map, where the vendor's turning of its icons is a
// shift along one axis. Convolutions that wrap around that axis and a maximum
// taken along it at the end are unmoved by such a shift, so the network never
// spends capacity learning that a turned icon is the same icon — and inference
// needs one pass per shape rather than one per alignment per pair.
//
// It is trained on tracings synthesised from the prompt glyphs, which is what
// makes a network possible at all: there are only a couple of hundred real
// answers, and there are as many synthetic ones as anybody wants. See
// synth_test.go for the measurement that says the imitation carries what
// matters.

const (
	netC1  = 8  // channels after the first convolution
	netC2  = 16 // after the second
	netC3  = 16 // after the third
	netEmb = 32 // length of the embedding

	// The radius axis is halved twice; the angle axis is never reduced until
	// the maximum at the end, because reducing it would blur the very axis the
	// invariance lives on.
	netR1 = polarRadii     // 16
	netR2 = polarRadii / 2 // 8
	netR3 = polarRadii / 4 // 4
)

// conv is a 3x3 convolution that wraps around the angle axis and pads the
// radius axis with zeros. Wrapping is the point: the first and last angle are
// neighbours on the shape, and treating them as edges would put a seam in
// every description at whatever angle the sampling happened to start.
type conv struct {
	In, Out int
	W       []float64 // Out x In x 3 x 3
	B       []float64 // Out
}

func newConv(in, out int, rng *rand.Rand) conv {
	c := conv{In: in, Out: out, W: make([]float64, out*in*9), B: make([]float64, out)}
	// Scaled to the number of inputs each output sees, so that the signal
	// neither dies nor explodes on the way through.
	sd := math.Sqrt(2 / float64(in*9))
	for i := range c.W {
		c.W[i] = rng.NormFloat64() * sd
	}
	return c
}

// forward applies the convolution and a rectifier, over radii rows of angles
// columns.
func (c conv) forward(in []float64, radii int) []float64 {
	const T = polarAngles
	out := make([]float64, c.Out*radii*T)
	for oc := range c.Out {
		for r := range radii {
			for t := range T {
				sum := c.B[oc]
				for ic := range c.In {
					for kr := -1; kr <= 1; kr++ {
						rr := r + kr
						if rr < 0 || rr >= radii {
							continue
						}
						for kt := -1; kt <= 1; kt++ {
							tt := (t + kt + T) % T
							w := c.W[((oc*c.In+ic)*3+(kr+1))*3+(kt+1)]
							sum += w * in[(ic*radii+rr)*T+tt]
						}
					}
				}
				if sum < 0 {
					sum = 0
				}
				out[(oc*radii+r)*T+t] = sum
			}
		}
	}
	return out
}

// dense is the last layer, mapping the pooled description to an embedding.
type dense struct {
	In, Out int
	W       []float64 // Out x In
	B       []float64 // Out
}

func newDense(in, out int, rng *rand.Rand) dense {
	d := dense{In: in, Out: out, W: make([]float64, out*in), B: make([]float64, out)}
	sd := math.Sqrt(1 / float64(in))
	for i := range d.W {
		d.W[i] = rng.NormFloat64() * sd
	}
	return d
}

func (d dense) forward(in []float64) []float64 {
	out := make([]float64, d.Out)
	for j := range d.Out {
		s := d.B[j]
		for i := range d.In {
			s += d.W[j*d.In+i] * in[i]
		}
		out[j] = s
	}
	return out
}

// Net reads a shape into an embedding.
type Net struct {
	C1, C2, C3 conv
	D          dense

	// Scale turns the alignment of two embeddings, which lives between -1 and
	// 1, into a score the softmax can separate with. Learned rather than
	// picked: how sharply the model should commit is not something to guess.
	Scale float64
}

// NewNet returns an untrained network.
func NewNet(seed uint64) *Net {
	rng := rand.New(rand.NewPCG(seed, seed*2+1))
	return &Net{
		C1:    newConv(1, netC1, rng),
		C2:    newConv(netC1, netC2, rng),
		C3:    newConv(netC2, netC3, rng),
		D:     newDense(netC3*netR3, netEmb, rng),
		Scale: 8,
	}
}

// pass holds everything the forward run computed, because the backward run
// needs all of it.
type pass struct {
	in         []float64
	a1, a2, a3 []float64
	p1, p2     []float64
	i1, i2     []int // which of the two rows each pooled value came from
	pooled     []float64
	imax       []int // which angle each pooled value came from
	raw, embed []float64
	norm       float64
}

// poolRadii halves the radius axis by keeping the larger of each pair, and
// records which one it kept.
func poolRadii(in []float64, ch, radii int) ([]float64, []int) {
	const T = polarAngles
	half := radii / 2
	out := make([]float64, ch*half*T)
	from := make([]int, ch*half*T)
	for c := range ch {
		for r := range half {
			for t := range T {
				a := in[(c*radii+2*r)*T+t]
				b := in[(c*radii+2*r+1)*T+t]
				k := 2 * r
				if b > a {
					a, k = b, 2*r+1
				}
				out[(c*half+r)*T+t] = a
				from[(c*half+r)*T+t] = k
			}
		}
	}
	return out, from
}

// maxAngle collapses the angle axis by keeping its largest value, which is
// what makes the whole description unmoved by turning the shape.
func maxAngle(in []float64, ch, radii int) ([]float64, []int) {
	const T = polarAngles
	out := make([]float64, ch*radii)
	at := make([]int, ch*radii)
	for c := range ch {
		for r := range radii {
			best, bi := math.Inf(-1), 0
			for t := range T {
				if v := in[(c*radii+r)*T+t]; v > best {
					best, bi = v, t
				}
			}
			out[c*radii+r] = best
			at[c*radii+r] = bi
		}
	}
	return out, at
}

// Embed reads a shape into a unit-length embedding.
func (n *Net) Embed(s Shape) []float64 { return n.run(polarMap(s)).embed }

func (n *Net) run(in []float64) *pass {
	p := &pass{in: in}
	p.a1 = n.C1.forward(in, netR1)
	p.p1, p.i1 = poolRadii(p.a1, netC1, netR1)
	p.a2 = n.C2.forward(p.p1, netR2)
	p.p2, p.i2 = poolRadii(p.a2, netC2, netR2)
	p.a3 = n.C3.forward(p.p2, netR3)
	p.pooled, p.imax = maxAngle(p.a3, netC3, netR3)
	p.raw = n.D.forward(p.pooled)

	var sum float64
	for _, v := range p.raw {
		sum += v * v
	}
	p.norm = math.Sqrt(sum)
	if p.norm < 1e-9 {
		p.norm = 1e-9
	}
	p.embed = make([]float64, len(p.raw))
	for i, v := range p.raw {
		p.embed[i] = v / p.norm
	}
	return p
}

// Alike is how much two shapes look like the same thing, as a score without an
// upper bound rather than a probability: it is only ever compared against the
// other candidates for the same pictogram.
func (n *Net) Alike(a, b Shape) float64 {
	ea, eb := n.Embed(a), n.Embed(b)
	var d float64
	for i := range ea {
		d += ea[i] * eb[i]
	}
	return n.Scale * d
}
