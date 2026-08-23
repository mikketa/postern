package puzzle

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand/v2"
	"os"
	"testing"
)

// Making training data out of nothing much.
//
// The vendor draws from a vocabulary of roughly two hundred pictograms — 167
// seen across 88 collected challenges, and a Chao estimate of 211 — so a model
// that learns which of K glyphs it is looking at would meet a glyph it had
// never seen on about one prompt in five. What has to be learned is instead
// whether a tracing depicts a glyph, which generalises, and that needs pairs:
// a glyph, and something drawn the way the vendor draws it.
//
// There are only 220 real pairs and there will never be many more without
// hammering somebody's demo. But the glyphs themselves are free — every prompt
// carries three — and the way the vendor turns a glyph into an icon is not
// mysterious: it traces the outline with a thick, unsteady, speckled stroke,
// then turns it. That is reproducible, and it turns 167 glyphs into as many
// pairs as the training wants.
//
// Whether the imitation is close enough to the real thing is not something to
// assume. SYNTH=<dir> writes sheets to compare against the real cut-outs.

// traceGlyph draws a glyph the way the vendor's icons are drawn: its outline,
// thickened, wobbled, speckled, and turned.
func traceGlyph(g Shape, rng *rand.Rand) Shape {
	return traceGlyphAt(g, rng, 40+rng.IntN(28))
}

// traceGlyphAt draws a tracing and then puts it at the size a real one is.
//
// side is the longer side of the finished tracing in pixels. It matters more
// than it looks: measured over the collected challenges, a real drawn icon is
// about fifty pixels across, and a tracing left at the size it is drawn at
// comes out at a hundred and four. Some of what is measured about a shape does
// not care — overlap normalises the size away, and so does the log-polar map —
// but how much of a shape is edge does care, since a stroke of a given
// thickness is a larger share of a smaller drawing. Fitted at twice the size,
// that measurement was fitted against the wrong number.
//
// It matters more again once scenery is among the distractors, because
// scenery comes out of real pictures at the real size. A model handed
// hundred-pixel drawings and fifty-pixel scenery can tell them apart by size
// alone, which is a rule that wins every synthesised group and nothing at all
// against the vendor.
func traceGlyphAt(g Shape, rng *rand.Rand, side int) Shape {
	// Worked at four times the glyph's own size, because a 24-pixel glyph has
	// no room for a stroke three pixels thick.
	s := upscale(g, 4)
	// Thick enough that the stroke survives being put back down to size.
	// Drawn at 2 to 4 and then halved, a tracing came out at 505 pixels
	// against a real icon's 1023 at the same width across; at 4 to 8 the two
	// match, which is what the measurement of how much of a shape is edge is
	// fitted against.
	s = outlineOf(s, 4+rng.IntN(5))
	s = wobble(s, rng, 1.5+rng.Float64()*2.5)
	s = speckle(s, rng, 0.80+rng.Float64()*0.18)
	if rng.IntN(2) == 0 {
		s = mirrorShape(s)
	}
	s = rotate(s, rng.Float64()*2*math.Pi)
	return resample(s, side)
}

// resample redraws a shape with its longer side at side pixels.
//
// Backward-sampled: for every pixel of the result, ask which pixel of the
// original it came from. Mapping the other way leaves a lattice of holes
// whenever the result is the larger of the two, and a shape full of holes is
// not the shape that was asked for.
func resample(s Shape, side int) Shape {
	long := max(s.W, s.H)
	if long == 0 || side <= 0 {
		return s
	}
	f := float64(long) / float64(side)
	w := max(1, int(float64(s.W)/f+0.5))
	h := max(1, int(float64(s.H)/f+0.5))
	out := Shape{Mask: make([]bool, w*h), W: w, H: h, sourceW: s.sourceW}
	for y := range h {
		for x := range w {
			sx := min(s.W-1, int((float64(x)+0.5)*f))
			sy := min(s.H-1, int((float64(y)+0.5)*f))
			if s.Mask[sy*s.W+sx] {
				out.Mask[y*w+x] = true
				out.Pixels++
			}
		}
	}
	return out
}

func upscale(s Shape, f int) Shape {
	w, h := s.W*f, s.H*f
	out := Shape{Mask: make([]bool, w*h), W: w, H: h}
	for y := range h {
		for x := range w {
			if s.Mask[(y/f)*s.W+(x/f)] {
				out.Mask[y*w+x] = true
				out.Pixels++
			}
		}
	}
	return out
}

// outlineOf keeps the border of a silhouette and thickens it, which is what
// tracing a shape leaves behind.
func outlineOf(s Shape, thick int) Shape {
	edge := make([]bool, s.W*s.H)
	for y := range s.H {
		for x := range s.W {
			if !s.Mask[y*s.W+x] {
				continue
			}
			for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := x+d[0], y+d[1]
				if nx < 0 || ny < 0 || nx >= s.W || ny >= s.H || !s.Mask[ny*s.W+nx] {
					edge[y*s.W+x] = true
					break
				}
			}
		}
	}
	edge = grow(edge, s.W, s.H, thick)
	out := Shape{Mask: edge, W: s.W, H: s.H}
	for _, on := range edge {
		if on {
			out.Pixels++
		}
	}
	return out
}

// wobble pushes the drawing around by a smooth random field, so the stroke
// wanders the way a hand does rather than following the glyph exactly.
func wobble(s Shape, rng *rand.Rand, amp float64) Shape {
	// A few low-frequency waves, which is enough to look unsteady without
	// tearing the stroke apart.
	type wave struct{ fx, fy, px, py, a float64 }
	waves := make([]wave, 3)
	for i := range waves {
		waves[i] = wave{
			fx: (0.5 + rng.Float64()*1.5) / float64(s.W),
			fy: (0.5 + rng.Float64()*1.5) / float64(s.H),
			px: rng.Float64() * 2 * math.Pi,
			py: rng.Float64() * 2 * math.Pi,
			a:  amp * (0.4 + rng.Float64()),
		}
	}
	shift := func(x, y int) (float64, float64) {
		var dx, dy float64
		for _, w := range waves {
			dx += w.a * math.Sin(2*math.Pi*w.fx*float64(x)+w.px)
			dy += w.a * math.Cos(2*math.Pi*w.fy*float64(y)+w.py)
		}
		return dx, dy
	}
	out := Shape{Mask: make([]bool, s.W*s.H), W: s.W, H: s.H}
	for y := range s.H {
		for x := range s.W {
			dx, dy := shift(x, y)
			sx := int(math.Round(float64(x) + dx))
			sy := int(math.Round(float64(y) + dy))
			if sx >= 0 && sy >= 0 && sx < s.W && sy < s.H && s.Mask[sy*s.W+sx] {
				out.Mask[y*s.W+x] = true
				out.Pixels++
			}
		}
	}
	return out
}

// speckle drops pixels at random. The vendor's strokes are full of pinholes,
// and a model trained on solid strokes would meet something else.
func speckle(s Shape, rng *rand.Rand, keep float64) Shape {
	out := Shape{Mask: make([]bool, len(s.Mask)), W: s.W, H: s.H}
	for i, on := range s.Mask {
		if on && rng.Float64() < keep {
			out.Mask[i] = true
			out.Pixels++
		}
	}
	return out
}

func mirrorShape(s Shape) Shape {
	out := Shape{Mask: make([]bool, s.W*s.H), W: s.W, H: s.H, Pixels: s.Pixels}
	for y := range s.H {
		for x := range s.W {
			out.Mask[y*s.W+(s.W-1-x)] = s.Mask[y*s.W+x]
		}
	}
	return out
}

// TestSynthesisedTracingsLookLikeTheRealThing writes sheets to look at. It is
// a judgement a person has to make, so the test only refuses the obvious
// failures — a tracing that came out empty, or solid.
func TestSynthesisedTracingsLookLikeTheRealThing(t *testing.T) {
	dir := os.Getenv("BENCH_DIR")
	if dir == "" {
		t.Skip("no BENCH_DIR")
	}
	names, err := os.ReadDir(dir)
	if err != nil {
		t.Skip(err)
	}
	rng := rand.New(rand.NewPCG(7, 11))
	out := os.Getenv("SYNTH")

	checked := 0
	for _, e := range names {
		q, err := readImage(dir + "/" + e.Name() + "/ques.png")
		if err != nil {
			continue
		}
		glyphs := FindPictograms(q, 140)
		if len(glyphs) == 0 {
			continue
		}
		var traced []Shape
		for _, g := range glyphs {
			s := traceGlyph(g, rng)
			traced = append(traced, s)
			if s.Pixels == 0 {
				t.Fatalf("%s: a tracing came out empty", e.Name())
			}
			// An outline that filled itself in is not an outline.
			if float64(s.Pixels) > 0.7*float64(s.W*s.H) {
				t.Fatalf("%s: a tracing covers %.0f%% of its own box",
					e.Name(), 100*float64(s.Pixels)/float64(s.W*s.H))
			}
		}
		checked += len(traced)
		if out != "" && checked <= 30 {
			writeStrip(t, out+"/"+e.Name()+"-synth.png", glyphs, traced)
		}
	}
	if checked == 0 {
		t.Skip("no readable prompts in BENCH_DIR")
	}
	t.Logf("%d tracages synthetises", checked)
}

// writeStrip lays glyphs above their tracings, at a size a person can judge.
func writeStrip(t *testing.T, path string, top, bottom []Shape) {
	t.Helper()
	const cell = 120
	cols := max(len(top), len(bottom))
	img := image.NewRGBA(image.Rect(0, 0, cell*cols, cell*2))
	for i := range img.Pix {
		img.Pix[i] = 20
	}
	// Sampled backwards — for each destination pixel, where it came from —
	// because forward mapping an enlargement leaves a lattice of holes and
	// makes every drawing look speckled whether it is or not.
	draw := func(s Shape, cx, cy int) {
		scale := float64(cell-12) / float64(max(s.W, s.H))
		for y := range cell - 12 {
			for x := range cell - 12 {
				sx, sy := int(float64(x)/scale), int(float64(y)/scale)
				if sx < s.W && sy < s.H && s.Mask[sy*s.W+sx] {
					img.Set(cx+6+x, cy+6+y, color.RGBA{240, 240, 240, 255})
				}
			}
		}
	}
	for i, s := range top {
		draw(s, i*cell, 0)
	}
	for i, s := range bottom {
		draw(s, i*cell, cell)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(fmt.Errorf("writing %s: %w", path, err))
	}
}

// TestAModelFittedOnSynthesisedTracingsOnly is the experiment that decides
// whether the imitation is any good, and it has to be run before anything is
// built on top of it.
//
// A model is fitted to synthesised tracings and to nothing else, then measured
// against the real labelled challenges it has never seen. If it lands near the
// model fitted on real answers, the synthesis carries what matters and there
// is as much training data as anyone wants. If it lands near chance, the
// synthesis is a picture of something else and no amount of it will help.
func TestAModelFittedOnSynthesisedTracingsOnly(t *testing.T) {
	dir := os.Getenv("BENCH_DIR")
	labelPath := os.Getenv("LABELS")
	if dir == "" || labelPath == "" {
		t.Skip("no BENCH_DIR or LABELS")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skip(err)
	}

	var names []string
	challenges := map[string]challenge{}
	var glyphs []Shape
	for _, e := range entries {
		c, ok := loadChallenge(dir, e.Name())
		if !ok {
			continue
		}
		names = append(names, e.Name())
		challenges[e.Name()] = c
		glyphs = append(glyphs, c.wanted...)
	}
	if len(glyphs) < 20 {
		t.Skip("not enough prompts to synthesise from")
	}

	// Groups shaped like the real ones: one glyph asked for, its tracing among
	// tracings of other glyphs. The distractors are traced too, so the model
	// cannot win by noticing which candidate was drawn rather than which one
	// is the right shape.
	const perGlyph, distractors = 8, 4
	junk := sceneryIfAny(t)
	t.Logf("%d glyphes, %d pieces de decor", len(glyphs), len(junk))
	rng := rand.New(rand.NewPCG(3, 5))
	var train [][]Sample
	for range perGlyph {
		for i, g := range glyphs {
			cand := []Shape{traceGlyph(g, rng)}
			for k := range distractors {
				// Half the distractors are scenery, which is roughly the mix a
				// real picture hands over: three icons among six or so
				// candidates. Fitted against traced glyphs alone the model
				// learns to tell one drawing from another and nothing about
				// telling a drawing from a kerbstone, which is the mistake
				// that actually loses challenges. See scenery_test.go.
				if len(junk) > 0 && k%2 == 1 {
					cand = append(cand, junk[rng.IntN(len(junk))])
					continue
				}
				j := rng.IntN(len(glyphs))
				for j == i {
					j = rng.IntN(len(glyphs))
				}
				cand = append(cand, traceGlyph(glyphs[j], rng))
			}
			// Not shuffled: nothing here reads a candidate's position — the
			// measurements are taken per candidate and the loss is a softmax
			// over the group — so moving the answer around would only risk
			// losing track of which one it is.
			var group []Sample
			for k, got := range cand {
				group = append(group, Sample{
					Features: Features(g, got, cand),
					Positive: k == 0,
				})
			}
			train = append(train, group)
		}
	}
	t.Logf("%d groupes synthetiques a partir de %d glyphes", len(train), len(glyphs))

	m, err := Train(train, 400, 0.05, 0.1)
	if err != nil {
		t.Fatal(err)
	}

	// This is the fit the shipped weights come from. Fitted on tracings the
	// vendor never drew, it ranks 0.695 of real pictograms right against the
	// 0.679 of a fit on the real answers themselves, and it leaves every
	// labelled challenge free to measure it rather than spent on fitting it.
	if out := os.Getenv("EMIT"); out != "" {
		emit(t, out, "synthesised tracings", m)
	}

	labels := readLabels(t, labelPath)
	real, _, _ := groupsFrom(challenges, names, labels)
	if len(real) == 0 {
		t.Skip("no real groups to measure against")
	}

	t.Logf("SUR LES VRAIS DEFIS: %.3f par pictogramme, sur %d groupes",
		Accuracy(m, real), len(real))
	t.Logf("  (modele ajuste sur les vraies reponses: 0.679, recouvrement seul: 0.664)")

	whole, ok := 0, 0
	for _, n := range names {
		c := challenges[n]
		answer, has := labels[n]
		if !has || len(answer) != len(c.wanted) {
			continue
		}
		full := true
		for _, v := range answer {
			if v < 0 || v >= len(c.found) {
				full = false
			}
		}
		if !full {
			continue
		}
		whole++
		p, err := pairWith(m, c.wanted, c.found)
		if err != nil {
			continue
		}
		right := true
		for i, j := range p.Order {
			if j != answer[i] {
				right = false
			}
		}
		if right {
			ok++
		}
	}
	if whole > 0 {
		t.Logf("DEFIS ENTIERS: %d/%d = %.3f  (sur vraies reponses: 43/59 = 0.729)",
			ok, whole, float64(ok)/float64(whole))
	}
}
