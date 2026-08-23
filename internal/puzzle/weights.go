package puzzle

// Code generated from synthesised tracings. DO NOT EDIT.

// trained is the fitted comparison model.
var trained = Model{
	Weights: [FeatureCount]float64{
		+0.187718, // overlap.best
		-0.128084, // overlap.upright
		+0.053458, // overlap.margin
		+0.120638, // density.diff
		-0.253818, // elongation.diff
		-0.024015, // holes.diff
		+0.196936, // stroke.wander
		-0.015059, // size.relative
		-0.183926, // hu.distance
		+0.018973, // compact.diff
		+0.107687, // fill.ratio
		-0.258416, // chamfer.best
		+0.006722, // fit.excess
		-0.168716, // fit.missing
		-0.074375, // ring.0
		-0.083683, // ring.1
		-0.086417, // ring.2
		-0.052655, // ring.3
		+0.006060, // ring.4
		+0.075586, // ring.5
		+0.194078, // ring.6
		+0.577867, // ring.7
		+1.405769, // net.alike
		+0.000000, // bias
	},
	Mean: [FeatureCount]float64{
		+0.612098,
		+0.509097,
		+0.006677,
		+0.297098,
		+0.260000,
		+1.320952,
		+0.447144,
		+1.000000,
		+4.309920,
		+12.381197,
		+0.713890,
		+0.551292,
		+0.132735,
		+0.255167,
		+0.849823,
		+0.841904,
		+0.811568,
		+0.769431,
		+0.715472,
		+0.645992,
		+0.573302,
		+0.370275,
		+2.482228,
		+0.000000,
	},
	Scale: [FeatureCount]float64{
		+0.187446,
		+0.190211,
		+0.009074,
		+0.175159,
		+0.414067,
		+3.528636,
		+0.183184,
		+0.301385,
		+2.457916,
		+10.476383,
		+0.185542,
		+0.430645,
		+0.127070,
		+0.213621,
		+0.312624,
		+0.267293,
		+0.234409,
		+0.239377,
		+0.247153,
		+0.243774,
		+0.232245,
		+0.173444,
		+5.120251,
		+1.000000,
	},
}

// trainedFeatures names what each weight was fitted against.
var trainedFeatures = [FeatureCount]string{
	"overlap.best",
	"overlap.upright",
	"overlap.margin",
	"density.diff",
	"elongation.diff",
	"holes.diff",
	"stroke.wander",
	"size.relative",
	"hu.distance",
	"compact.diff",
	"fill.ratio",
	"chamfer.best",
	"fit.excess",
	"fit.missing",
	"ring.0",
	"ring.1",
	"ring.2",
	"ring.3",
	"ring.4",
	"ring.5",
	"ring.6",
	"ring.7",
	"net.alike",
	"bias",
}
