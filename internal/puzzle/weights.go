package puzzle

// Code generated from labelled challenges. DO NOT EDIT.

// trained is the fitted comparison model.
var trained = Model{
	Weights: [FeatureCount]float64{
		+0.335938, // overlap.best
		+0.129604, // overlap.upright
		+0.184510, // overlap.margin
		+0.266174, // density.diff
		-0.090611, // elongation.diff
		-0.074799, // holes.diff
		-0.053121, // stroke.wander
		-0.005983, // size.relative
		-0.217333, // hu.distance
		-0.086371, // compact.diff
		+0.072988, // fill.ratio
		-0.297487, // chamfer.best
		+0.000000, // bias
	},
	Mean: [FeatureCount]float64{
		+0.582447,
		+0.495921,
		+0.011381,
		+0.252128,
		+0.289311,
		+0.726937,
		+0.329422,
		+1.000000,
		+3.802580,
		+6.226024,
		+0.758313,
		+0.616886,
		+0.000000,
	},
	Scale: [FeatureCount]float64{
		+0.203757,
		+0.215527,
		+0.014486,
		+0.169513,
		+0.386945,
		+1.702823,
		+0.159754,
		+0.223926,
		+2.492287,
		+5.939076,
		+0.166372,
		+0.460534,
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
