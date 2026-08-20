package puzzle

import (
	"fmt"
	"math"
	"math/rand/v2"
)

// The learned part.
//
// A linear model over the comparison vector, fitted to rank the candidates of
// one pictogram against each other rather than to judge each pair on its own.
// Small enough to train in a few seconds and to ship as twelve numbers, which
// matters — a model nobody can read or retrain is a model that rots.
//
// It is trained on challenges whose answers were established by looking at
// them, and it replaces weights that were picked by hand. Hand-picking is how
// this got stuck: every adjustment traded one challenge against another,
// because a person tuning twelve interacting numbers is doing badly what
// gradient descent does well.

// Model scores a comparison.
//
// The scaling is part of the model, not a step before it. The measurements
// have wildly different ranges — an overlap is 0 to 1, a moment distance runs
// past 3 — and gradient descent on raw values crawls along the small ones
// while overshooting the large. Centring and scaling each measurement fixes
// that, and it has to travel with the weights or inference sees different
// numbers from training.
type Model struct {
	Weights [FeatureCount]float64
	Mean    [FeatureCount]float64
	Scale   [FeatureCount]float64
}

// standardise centres and scales one comparison vector.
func (m Model) standardise(f [FeatureCount]float64) [FeatureCount]float64 {
	var out [FeatureCount]float64
	for i := range f {
		s := m.Scale[i]
		if s == 0 {
			s = 1
		}
		out[i] = (f[i] - m.Mean[i]) / s
	}
	return out
}

// Score is the probability that got answers want, between 0 and 1.
func (m Model) Score(want, got Shape, context []Shape) float64 {
	return sigmoid(m.dot(Features(want, got, context)))
}

func (m Model) dot(f [FeatureCount]float64) float64 {
	z := 0.0
	sf := m.standardise(f)
	for i := range sf {
		z += m.Weights[i] * sf[i]
	}
	return z
}

// dotStandardised scores an already-scaled vector.
func (m Model) dotStandardised(sf [FeatureCount]float64) float64 {
	var z float64
	for i := range sf {
		z += m.Weights[i] * sf[i]
	}
	return z
}

func sigmoid(z float64) float64 {
	if z < -40 {
		return 0
	}
	if z > 40 {
		return 1
	}
	return 1 / (1 + math.Exp(-z))
}

// Sample is one labelled comparison.
type Sample struct {
	Features [FeatureCount]float64
	Positive bool
}

// Train fits a model by gradient descent, one pictogram at a time.
//
// The loss is a softmax over the candidates of a single pictogram rather than
// a yes/no verdict on each candidate on its own. That is the shape of the
// question: a picture holds several candidates and exactly one of them is the
// icon being asked for, so what has to come out right is the ordering within
// that set, not a calibrated probability for each pair. Scoring pairs
// independently optimises something else and then hopes the ordering follows.
//
// It also disposes of the class imbalance. Every pictogram contributes one
// positive and a handful of negatives; normalising inside the group makes that
// the definition of the problem instead of a skew to be corrected with a
// weight picked by hand.
//
// One consequence is worth knowing: a measurement that is the same for every
// candidate of a pictogram cannot move the ordering, so its gradient cancels
// and its weight stays at zero. The bias is exactly such a measurement.
func Train(groups [][]Sample, passes int, rate, decay float64) (Model, error) {
	var usable [][]Sample
	var all []Sample
	for _, g := range groups {
		pos := 0
		for _, s := range g {
			if s.Positive {
				pos++
			}
		}
		// A pictogram with no answer among the candidates, or with only one
		// candidate to choose from, has nothing to say about ranking.
		if pos != 1 || len(g) < 2 {
			continue
		}
		usable = append(usable, g)
		all = append(all, g...)
	}
	if len(usable) == 0 {
		return Model{}, fmt.Errorf("puzzle: no pictogram with one answer among two or more candidates")
	}

	var m Model
	// Fit the scaling first, on the training data only.
	for i := range FeatureCount {
		var sum float64
		for _, s := range all {
			sum += s.Features[i]
		}
		mean := sum / float64(len(all))
		var varsum float64
		for _, s := range all {
			d := s.Features[i] - mean
			varsum += d * d
		}
		sd := math.Sqrt(varsum / float64(len(all)))
		if sd < 1e-9 {
			sd = 1
		}
		m.Mean[i], m.Scale[i] = mean, sd
	}
	// The bias must not be centred away to nothing.
	m.Mean[FeatureCount-1], m.Scale[FeatureCount-1] = 0, 1

	order := make([]int, len(usable))
	for i := range order {
		order[i] = i
	}
	// Deterministic shuffling: a model that comes out different every time it
	// is trained cannot be compared against the one it replaces.
	rng := rand.New(rand.NewPCG(1, 2))

	scaled := make([][][FeatureCount]float64, len(usable))
	for i, g := range usable {
		scaled[i] = make([][FeatureCount]float64, len(g))
		for j, s := range g {
			scaled[i][j] = m.standardise(s.Features)
		}
	}

	p := make([]float64, 0, 16)
	for pass := range passes {
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		lr := rate / (1 + float64(pass)*0.02)
		for _, idx := range order {
			g, sf := usable[idx], scaled[idx]
			p = p[:0]
			top := math.Inf(-1)
			for j := range g {
				z := m.dotStandardised(sf[j])
				p = append(p, z)
				if z > top {
					top = z
				}
			}
			// Shifted before exponentiating: the scores are unbounded and a
			// confident model overflows otherwise.
			sum := 0.0
			for j := range p {
				p[j] = math.Exp(p[j] - top)
				sum += p[j]
			}
			var grad [FeatureCount]float64
			for j := range p {
				e := p[j] / sum
				if g[j].Positive {
					e--
				}
				for i := range grad {
					grad[i] += e * sf[j][i]
				}
			}
			for i := range m.Weights {
				m.Weights[i] -= lr * (grad[i] + decay*m.Weights[i])
			}
		}
	}
	return m, nil
}

// Accuracy reports how often the model ranks the right candidate first, which
// is the question actually being asked of it — not whether each comparison is
// individually well scored.
func Accuracy(m Model, groups [][]Sample) float64 {
	if len(groups) == 0 {
		return 0
	}
	right := 0
	for _, g := range groups {
		best, bestScore := -1, math.Inf(-1)
		for i, s := range g {
			if z := m.dot(s.Features); z > bestScore {
				best, bestScore = i, z
			}
		}
		if best >= 0 && g[best].Positive {
			right++
		}
	}
	return float64(right) / float64(len(groups))
}
