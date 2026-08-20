package puzzle

import (
	"fmt"
	"math"
	"math/rand/v2"
)

// The learned part.
//
// A logistic model over the comparison vector: given a pictogram and a
// candidate, how likely is it that this candidate is the thing being asked
// for. Small enough to train in a few seconds and to ship as twelve numbers,
// which matters — a model nobody can read or retrain is a model that rots.
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

// Train fits a model by gradient descent on the log-loss.
//
// Positives are outnumbered — one candidate in a picture answers a given
// pictogram and the rest do not — so they are weighted up to match. Without
// that the shortest path to a low loss is to answer "no" to everything, which
// scores well and pairs nothing.
func Train(data []Sample, passes int, rate, decay float64) (Model, error) {
	if len(data) == 0 {
		return Model{}, fmt.Errorf("puzzle: nothing to train on")
	}
	pos := 0
	for _, s := range data {
		if s.Positive {
			pos++
		}
	}
	if pos == 0 || pos == len(data) {
		return Model{}, fmt.Errorf("puzzle: training data is all one class (%d of %d positive)",
			pos, len(data))
	}
	posWeight := float64(len(data)-pos) / float64(pos)

	var m Model
	// Fit the scaling first, on the training data only.
	for i := range FeatureCount {
		var sum float64
		for _, s := range data {
			sum += s.Features[i]
		}
		mean := sum / float64(len(data))
		var varsum float64
		for _, s := range data {
			d := s.Features[i] - mean
			varsum += d * d
		}
		sd := math.Sqrt(varsum / float64(len(data)))
		if sd < 1e-9 {
			sd = 1
		}
		m.Mean[i], m.Scale[i] = mean, sd
	}
	// The bias must not be centred away to nothing.
	m.Mean[FeatureCount-1], m.Scale[FeatureCount-1] = 0, 1
	order := make([]int, len(data))
	for i := range order {
		order[i] = i
	}
	// Deterministic shuffling: a model that comes out different every time it
	// is trained cannot be compared against the one it replaces.
	rng := rand.New(rand.NewPCG(1, 2))

	for pass := range passes {
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		lr := rate / (1 + float64(pass)*0.02)
		for _, idx := range order {
			s := data[idx]
			sf := m.standardise(s.Features)
			p := sigmoid(m.dotStandardised(sf))
			y, w := 0.0, 1.0
			if s.Positive {
				y, w = 1, posWeight
			}
			err := (p - y) * w
			for i := range m.Weights {
				g := err*sf[i] + decay*m.Weights[i]
				m.Weights[i] -= lr * g
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
