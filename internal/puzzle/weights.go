package puzzle

// Code generated from labelled challenges. DO NOT EDIT.

// trained is the fitted comparison model.
var trained = Model{
	Weights: [FeatureCount]float64{
		+1.242588, // overlap.best
		-0.534816, // overlap.upright
		+0.027614, // overlap.margin
		-0.189349, // density.diff
		+0.233803, // elongation.diff
		+0.000000, // holes.diff
		+0.605840, // drawn.candidate
		+0.351611, // size.relative
		-0.227135, // hu.distance
		+0.174809, // compact.diff
		+0.088188, // fill.candidate
		-0.089708, // bias
	},
	Mean: [FeatureCount]float64{
		+0.561413,
		+0.475979,
		+0.010182,
		+0.232635,
		+0.207679,
		+0.000000,
		+0.345522,
		+1.000000,
		+4.068709,
		+4.799844,
		+0.424043,
		+0.000000,
	},
	Scale: [FeatureCount]float64{
		+0.225880,
		+0.230458,
		+0.014513,
		+0.174827,
		+0.287995,
		+1.000000,
		+0.196013,
		+0.153684,
		+2.480070,
		+3.634661,
		+0.135747,
		+1.000000,
	},
}
