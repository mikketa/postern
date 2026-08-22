package puzzle

// Code generated from labelled challenges. DO NOT EDIT.

// trained is the fitted comparison model.
var trained = Model{
	Weights: [FeatureCount]float64{
		+1.522493, // overlap.best
		+0.153729, // overlap.upright
		+0.381213, // overlap.margin
		+1.226149, // density.diff
		+0.158379, // elongation.diff
		-0.053577, // holes.diff
		+0.419587, // stroke.wander
		+0.177004, // size.relative
		-0.166734, // hu.distance
		+0.077173, // compact.diff
		+0.968842, // fill.ratio
		-0.288648, // chamfer.best
		+0.000000, // bias
	},
	Mean: [FeatureCount]float64{
		+0.610792,
		+0.528695,
		+0.012372,
		+0.245232,
		+0.235684,
		+0.607477,
		+0.290585,
		+1.000000,
		+4.213489,
		+4.231215,
		+0.729935,
		+0.551833,
		+0.000000,
	},
	Scale: [FeatureCount]float64{
		+0.193172,
		+0.200201,
		+0.018599,
		+0.157810,
		+0.374883,
		+1.353479,
		+0.105471,
		+0.214022,
		+2.541412,
		+3.708612,
		+0.195447,
		+0.430789,
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
	"bias",
}
