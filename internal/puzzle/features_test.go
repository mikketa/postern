package puzzle

import (
	"math"
	"testing"
)

// disc returns a filled circle of the given radius on the comparison grid.
func disc(r float64) []bool {
	m := make([]bool, normalSize*normalSize)
	c := float64(normalSize-1) / 2
	for y := range normalSize {
		for x := range normalSize {
			if math.Hypot(float64(x)-c, float64(y)-c) <= r {
				m[y*normalSize+x] = true
			}
		}
	}
	return m
}

// annulus returns a ring: the disc with its middle taken out, which is what an
// icon drawn in outline looks like when the fill has failed.
func annulus(outer, inner float64) []bool {
	m := disc(outer)
	for i, on := range disc(inner) {
		if on {
			m[i] = false
		}
	}
	return m
}

// TestARingAndADiscAgreeOnlyAtTheEdge is the claim the ring decomposition is
// built on: a single overlap figure cannot tell a drawn outline from a solid
// blob that shares its silhouette, because the two agree perfectly at the edge
// and not at all in the middle. Sliced by radius, they say so.
func TestARingAndADiscAgreeOnlyAtTheEdge(t *testing.T) {
	solid := disc(14)
	ring := annulus(14, 8)

	got := ringAgreement(solid, ring)
	if got[rings-1] <= got[0] {
		t.Fatalf("agreement at the rim %.2f is not above the middle %.2f — the "+
			"slicing is not measuring what it claims", got[rings-1], got[0])
	}
	if got[0] > 0.2 {
		t.Errorf("the middle agrees %.2f, where one shape is solid and the "+
			"other is empty", got[0])
	}
	if got[rings-1] < 0.8 {
		t.Errorf("the rim agrees only %.2f, where the two coincide", got[rings-1])
	}
}

// TestAgreementWithItselfIsTotal is the degenerate case, and it catches an
// off-by-one in the radius that would leave the outermost ring empty.
func TestAgreementWithItselfIsTotal(t *testing.T) {
	d := disc(15)
	for k, v := range ringAgreement(d, d) {
		if v != 1 {
			t.Errorf("ring %d agrees %.2f with itself", k, v)
		}
	}
	if e, m := fitError(d, d); e != 0 || m != 0 {
		t.Errorf("a shape against itself reports %.2f excess and %.2f missing", e, m)
	}
}

// TestFallingShortAndSpillingOverAreToldApart is what the split is for: one
// overlap figure scores both failures the same, and they are not the same.
func TestFallingShortAndSpillingOverAreToldApart(t *testing.T) {
	want := disc(12)

	if e, m := fitError(want, disc(6)); m <= e {
		t.Errorf("a candidate inside the pictogram reports %.2f excess and "+
			"%.2f missing — it misses, it does not spill", e, m)
	}
	if e, m := fitError(want, disc(15)); e <= m {
		t.Errorf("a candidate around the pictogram reports %.2f excess and "+
			"%.2f missing — it spills, it does not fall short", e, m)
	}
}
