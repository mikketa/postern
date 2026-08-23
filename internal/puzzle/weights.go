package puzzle

// Code generated from synthesised tracings. DO NOT EDIT.

// trained is the fitted comparison model.
var trained = Model{
	Weights: [FeatureCount]float64{
		+0.154936, // overlap.best
		-0.097883, // overlap.upright
		+0.152200, // overlap.margin
		+0.062672, // density.diff
		-0.429821, // elongation.diff
		+0.018686, // holes.diff
		+0.105475, // stroke.wander
		-0.032174, // size.relative
		-0.163731, // hu.distance
		-0.055551, // compact.diff
		+0.159376, // fill.ratio
		-0.235523, // chamfer.best
		-0.175764, // fit.excess
		-0.023002, // fit.missing
		-0.044796, // ring.0
		-0.078638, // ring.1
		-0.095533, // ring.2
		-0.079851, // ring.3
		-0.055736, // ring.4
		-0.014721, // ring.5
		+0.101399, // ring.6
		+0.600079, // ring.7
		+1.515806, // net.alike
		+0.000000, // bias
	},
	Mean: [FeatureCount]float64{
		+0.645956,
		+0.538232,
		+0.007011,
		+0.423982,
		+0.153000,
		+4.860794,
		+0.544813,
		+1.000000,
		+4.325644,
		+42.356534,
		+0.493312,
		+0.459622,
		+0.140155,
		+0.213889,
		+0.867492,
		+0.853659,
		+0.826904,
		+0.795709,
		+0.756420,
		+0.694550,
		+0.618494,
		+0.400006,
		+2.809036,
		+0.000000,
	},
	Scale: [FeatureCount]float64{
		+0.178612,
		+0.183749,
		+0.010908,
		+0.176096,
		+0.208226,
		+8.084965,
		+0.119169,
		+0.120228,
		+2.435963,
		+19.136776,
		+0.164743,
		+0.365799,
		+0.132753,
		+0.188685,
		+0.310035,
		+0.287766,
		+0.261321,
		+0.250809,
		+0.239372,
		+0.224651,
		+0.215424,
		+0.167907,
		+4.867012,
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
