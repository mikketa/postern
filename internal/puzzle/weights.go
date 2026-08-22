package puzzle

// Code generated from labelled challenges. DO NOT EDIT.

// trained is the fitted comparison model.
var trained = Model{
	Weights: [FeatureCount]float64{
		+0.216167, // overlap.best
		+0.104321, // overlap.upright
		+0.120546, // overlap.margin
		+0.183642, // density.diff
		+0.035225, // elongation.diff
		-0.043203, // holes.diff
		+0.039865, // stroke.wander
		-0.042216, // size.relative
		-0.109894, // hu.distance
		+0.054465, // compact.diff
		+0.037169, // fill.ratio
		-0.179054, // chamfer.best
		-0.167836, // fit.excess
		-0.098814, // fit.missing
		-0.136764, // ring.0
		-0.139413, // ring.1
		-0.146750, // ring.2
		+0.088236, // ring.3
		+0.256009, // ring.4
		+0.430575, // ring.5
		+0.288526, // ring.6
		+0.100189, // ring.7
		+0.000000, // bias
	},
	Mean: [FeatureCount]float64{
		+0.615411,
		+0.528695,
		+0.008202,
		+0.245232,
		+0.235684,
		+0.607477,
		+0.290585,
		+1.000000,
		+4.213489,
		+4.231215,
		+0.729935,
		+0.540180,
		+0.132283,
		+0.252306,
		+0.829812,
		+0.816656,
		+0.765422,
		+0.692088,
		+0.592963,
		+0.413587,
		+0.178886,
		+0.039370,
		+0.000000,
	},
	Scale: [FeatureCount]float64{
		+0.192510,
		+0.200201,
		+0.012495,
		+0.157810,
		+0.374883,
		+1.353479,
		+0.105471,
		+0.214022,
		+2.541412,
		+3.708612,
		+0.195447,
		+0.424290,
		+0.125208,
		+0.208470,
		+0.329258,
		+0.278059,
		+0.249747,
		+0.241750,
		+0.230659,
		+0.193342,
		+0.182696,
		+0.113213,
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
	"bias",
}
