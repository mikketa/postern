package puzzle

import (
	"fmt"
	"go/format"
	"os"
	"strings"
	"testing"
)

// emitNet writes a fitted network out as Go source.
//
// Stamped with the shape it was fitted at. A network read back into an
// architecture whose layers are a different size does not fail to compile — it
// fails to mean anything — so the guard is a test that compares the stamp with
// the constants, not a comment asking the reader to be careful.
func emitNet(t *testing.T, path string, n *Net) {
	var b strings.Builder
	b.WriteString("package puzzle\n\n")
	b.WriteString("// Code generated from synthesised tracings. DO NOT EDIT.\n\n")

	b.WriteString("// trainedNetShape is what the weights below were fitted at.\n")
	b.WriteString(fmt.Sprintf("var trainedNetShape = [...]int{%d, %d, %d, %d, %d, %d}\n\n",
		polarRadii, polarAngles, netC1, netC2, netC3, netEmb))

	b.WriteString("// trainedNet compares two shapes by the description it learnt.\n")
	b.WriteString("var trainedNet = &Net{\n")
	block := func(name string, c conv) {
		b.WriteString(fmt.Sprintf("\t%s: conv{In: %d, Out: %d,\n", name, c.In, c.Out))
		b.WriteString("\t\tW: []float64{")
		writeFloats(&b, c.W)
		b.WriteString("},\n\t\tB: []float64{")
		writeFloats(&b, c.B)
		b.WriteString("},\n\t},\n")
	}
	block("C1", n.C1)
	block("C2", n.C2)
	block("C3", n.C3)
	b.WriteString(fmt.Sprintf("\tD: dense{In: %d, Out: %d,\n", n.D.In, n.D.Out))
	b.WriteString("\t\tW: []float64{")
	writeFloats(&b, n.D.W)
	b.WriteString("},\n\t\tB: []float64{")
	writeFloats(&b, n.D.B)
	b.WriteString("},\n\t},\n")
	b.WriteString(fmt.Sprintf("\tScale: %+.6f,\n", n.Scale))
	b.WriteString("}\n")

	src, err := format.Source([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("reseau ecrit dans %s", path)
}

// writeFloats lays the numbers out several to a line: a few thousand weights
// one per line is a file nothing can read and every diff rewrites whole.
func writeFloats(b *strings.Builder, v []float64) {
	for i, f := range v {
		if i%8 == 0 {
			b.WriteString("\n\t\t")
		}
		b.WriteString(fmt.Sprintf("%+.6f, ", f))
	}
	b.WriteString("\n\t")
}
