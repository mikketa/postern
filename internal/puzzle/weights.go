package puzzle

// Code generated from labelled challenges. DO NOT EDIT.

// trained is the fitted comparison model.
var trained = Model{
	Weights: [FeatureCount]float64{
		+0.222439, // overlap.best
		+0.101020, // overlap.upright
		+0.150831, // overlap.margin
		+0.184568, // density.diff
		+0.043511, // elongation.diff
		-0.060141, // holes.diff
		+0.043502, // stroke.wander
		-0.055637, // size.relative
		-0.115396, // hu.distance
		+0.055740, // compact.diff
		+0.037940, // fill.ratio
		-0.191122, // chamfer.best
		-0.152641, // fit.excess
		-0.113733, // fit.missing
		-0.110034, // ring.0
		-0.105342, // ring.1
		-0.127591, // ring.2
		-0.118140, // ring.3
		+0.011526, // ring.4
		+0.142157, // ring.5
		+0.229346, // ring.6
		+0.532775, // ring.7
		-0.000000, // bias
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
		+0.833723,
		+0.827470,
		+0.801875,
		+0.763100,
		+0.717641,
		+0.652967,
		+0.582775,
		+0.380754,
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
		+0.340794,
		+0.301595,
		+0.264786,
		+0.252516,
		+0.242289,
		+0.241555,
		+0.232327,
		+0.179686,
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
