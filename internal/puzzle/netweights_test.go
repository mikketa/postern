package puzzle

import "testing"

// TestTheNetworkFileMatchesTheNetworkItIsRead into. A generated file left over
// from a different architecture does not fail to compile — the layers are
// sized at run time from the slices it carries — it simply describes shapes by
// a rule nobody fitted. The stamp is what catches that.
func TestTheNetworkFileMatchesTheNetwork(t *testing.T) {
	want := [...]int{polarRadii, polarAngles, netC1, netC2, netC3, netEmb}
	if trainedNetShape != want {
		t.Fatalf("netweights.go was fitted at %v and the network is %v now — "+
			"refit it: EMITNET=netweights.go", trainedNetShape, want)
	}
	for _, c := range []struct {
		name    string
		conv    conv
		in, out int
	}{
		{"C1", trainedNet.C1, 1, netC1},
		{"C2", trainedNet.C2, netC1, netC2},
		{"C3", trainedNet.C3, netC2, netC3},
	} {
		if c.conv.In != c.in || c.conv.Out != c.out {
			t.Errorf("%s carries %dx%d and the network wants %dx%d",
				c.name, c.conv.In, c.conv.Out, c.in, c.out)
		}
		if len(c.conv.W) != c.in*c.out*9 || len(c.conv.B) != c.out {
			t.Errorf("%s carries %d weights and %d biases, not %d and %d",
				c.name, len(c.conv.W), len(c.conv.B), c.in*c.out*9, c.out)
		}
	}
	if trainedNet.D.In != netC3*netR3 || trainedNet.D.Out != netEmb {
		t.Errorf("the last layer maps %d to %d, not %d to %d",
			trainedNet.D.In, trainedNet.D.Out, netC3*netR3, netEmb)
	}
	// A network read back with every weight at zero compiles and describes
	// every shape identically, which is the failure this cannot see from the
	// sizes alone.
	var sum float64
	for _, w := range trainedNet.D.W {
		sum += w * w
	}
	if sum == 0 {
		t.Fatal("the last layer is all zeros: netweights.go holds no network")
	}
}
