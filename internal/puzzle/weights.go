package puzzle

// Code generated from labelled challenges. DO NOT EDIT.

// trained is the fitted comparison model.
var trained = Model{
	Weights: [FeatureCount]float64{
		+0.330309, // overlap.best
		+0.124185, // overlap.upright
		+0.099572, // overlap.margin
		+0.327996, // density.diff
		+0.052597, // elongation.diff
		-0.137632, // holes.diff
		+0.092491, // stroke.wander
		+0.035260, // size.relative
		-0.163881, // hu.distance
		+0.144912, // compact.diff
		+0.180372, // fill.ratio
		-0.237775, // chamfer.best
		-0.341952, // fit.excess
		-0.097861, // fit.missing
		-0.018951, // ring.0
		-0.054852, // ring.1
		-0.187437, // ring.2
		-0.257133, // ring.3
		-0.035838, // ring.4
		+0.106664, // ring.5
		+0.210242, // ring.6
		+0.728027, // ring.7
		+0.000000, // bias
	},
	Mean: [FeatureCount]float64{
		+0.607389,
		+0.519162,
		+0.007310,
		+0.253905,
		+0.242332,
		+0.548267,
		+0.292853,
		+1.000000,
		+4.331325,
		+4.104074,
		+0.734926,
		+0.554918,
		+0.125288,
		+0.267324,
		+0.825501,
		+0.815896,
		+0.789427,
		+0.753927,
		+0.709728,
		+0.646074,
		+0.577850,
		+0.373401,
		+0.000000,
	},
	Scale: [FeatureCount]float64{
		+0.196856,
		+0.199459,
		+0.011142,
		+0.166395,
		+0.380037,
		+1.157262,
		+0.104583,
		+0.209269,
		+2.519104,
		+3.609641,
		+0.186064,
		+0.434588,
		+0.127524,
		+0.218842,
		+0.342392,
		+0.301531,
		+0.267750,
		+0.258530,
		+0.252620,
		+0.247162,
		+0.236484,
		+0.182354,
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
