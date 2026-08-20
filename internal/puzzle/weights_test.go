package puzzle

import "testing"

// TestTheShippedWeightsMatchTheFeatures catches a stale weights.go.
//
// The generated file declares its arrays as [FeatureCount], so one written
// against a shorter feature vector still compiles: the features added since
// are simply weighted zero, and the solver runs a model that was never fitted
// to what it is being handed. That has happened once and cost a live run.
func TestTheShippedWeightsMatchTheFeatures(t *testing.T) {
	if trainedFeatures != FeatureNames {
		t.Fatalf("weights.go was fitted against %v, the code now measures %v — "+
			"refit it (see train_test.go)", trainedFeatures, FeatureNames)
	}
}
