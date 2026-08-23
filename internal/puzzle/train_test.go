package puzzle

import (
	"bufio"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	_ "image/png"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Fitting the comparison model, offline.
//
// This is how weights.go is produced, and it is kept because a model nobody
// can retrain is a model that rots: the vendor changes its artwork, and
// whoever comes next needs to be able to collect fresh challenges and refit
// rather than reverse-engineer two dozen constants.
//
// It needs a bench of collected challenges, which is deliberately not in this
// repository — those are the vendor's pictures. Collect one with
// TestCollectIcons in internal/geetest, then:
//
//	BENCH_DIR=<dir> SHEETS=1 go test ./internal/puzzle -run TestTrainIconModel -v
//	    draws one sheet per challenge: the prompt enlarged above, the picture
//	    below with every candidate outlined in its own colour. Read the answers
//	    off into a labels file, one line per challenge: "07 2 0 1", meaning
//	    pictogram 1 is candidate 2, pictogram 2 is candidate 0, and so on. -1
//	    where segmentation missed the icon entirely.
//
// Do not read those answers off the sheets by eye if you can avoid it. It was
// done that way once and the label set was wrong often enough to make a model
// that loses to plain silhouette overlap look like one that beats it — the
// figures in features.go were derived twice, and only the second set meant
// anything. There is a better way, because the vendor reuses a small pool of
// photographs behind its challenges:
//
//	group the collected challenges by their background, which a coarse
//	thumbnail distance separates cleanly; take the per-pixel median of each
//	group, which is that photograph with every drawn icon voted away; subtract
//	it from each challenge, and what is left is its icons exactly.
//
// Cut those out and lay them beside the prompt and the answer reads itself.
// The same subtraction is the only honest measure of how well segmentation is
// doing: match each icon to the candidate whose box overlaps it and count the
// ones with no candidate at all. That is where "115 of 135" and every figure
// like it in this package comes from. Around ten challenges per background are
// needed before the median is clean.
//
//	BENCH_DIR=<dir> LABELS=<file> EMIT=weights.go go test \
//	    -count=1 ./internal/puzzle -run TestTrainIconModel -v
//	    fits the model, reports cross-validated accuracy against a baseline,
//	    and writes the weights. EMIT is relative to this directory, because a
//	    test runs in the directory of the package it tests. -count=1 matters:
//	    the labels file is not a declared dependency, so a cached result will
//	    silently ignore edits to it.
//
// Read the run's last line. weights.go declares its arrays as [FeatureCount],
// so a file left over from a shorter feature vector still compiles, with the
// features added since silently weighted zero — a stale model that looks like
// a fitted one.

var palette = []color.RGBA{
	{255, 0, 0, 255}, {0, 220, 0, 255}, {60, 120, 255, 255}, {255, 230, 0, 255},
	{255, 0, 255, 255}, {0, 240, 240, 255}, {255, 140, 0, 255}, {150, 60, 220, 255},
	{255, 255, 255, 255}, {120, 255, 160, 255}, {180, 90, 40, 255}, {90, 90, 90, 255},
}

var colourNames = []string{"rouge", "vert", "bleu", "jaune", "magenta", "cyan",
	"orange", "violet", "blanc", "menthe", "brun", "gris"}

type challenge struct {
	name   string
	wanted []Shape
	found  []Shape
}

func loadChallenge(dir, name string) (challenge, bool) {
	bg, err1 := readImage(dir + "/" + name + "/bg.png")
	q, err2 := readImage(dir + "/" + name + "/ques.png")
	if err1 != nil || err2 != nil {
		return challenge{}, false
	}
	wanted := FindPictograms(q, 140)
	found := FindIcons(bg, 200, len(wanted))
	if len(wanted) == 0 || len(found) == 0 {
		return challenge{}, false
	}
	return challenge{name: name, wanted: wanted, found: found}, true
}

func TestTrainIconModel(t *testing.T) {
	dir := os.Getenv("BENCH_DIR")
	if dir == "" {
		t.Skip("no BENCH_DIR")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	challenges := map[string]challenge{}
	for _, n := range names {
		if c, ok := loadChallenge(dir, n); ok {
			challenges[n] = c
		}
	}
	t.Logf("%d defis lisibles sur %d", len(challenges), len(names))

	if os.Getenv("SHEETS") != "" {
		for _, n := range names {
			c, ok := challenges[n]
			if !ok {
				continue
			}
			buildSheet(t, dir, c)
		}
		// A crib for annotating: how many candidates, and their colours.
		for _, n := range names {
			if c, ok := challenges[n]; ok {
				var cols []string
				for i := range c.found {
					cols = append(cols, fmt.Sprintf("%d=%s", i, colourNames[i%len(colourNames)]))
				}
				t.Logf("%s: %d pictos, %d candidats  %s",
					n, len(c.wanted), len(c.found), strings.Join(cols, " "))
			}
		}
		return
	}

	labels := readLabels(t, os.Getenv("LABELS"))
	if len(labels) == 0 {
		t.Skip("no LABELS")
	}

	// One group per pictogram: the candidates it could answer, exactly one of
	// which is right. Ranking within a group is the real task.
	var groups [][]Sample
	var groupOf []string
	missed := 0
	for _, n := range names {
		c, ok := challenges[n]
		if !ok {
			continue
		}
		answer, ok := labels[n]
		if !ok || len(answer) != len(c.wanted) {
			continue
		}
		for i, want := range c.wanted {
			// -1 marks a pictogram whose icon segmentation missed: there is
			// no right answer among the candidates, so the group teaches
			// nothing about choosing between them.
			if answer[i] < 0 || answer[i] >= len(c.found) {
				missed++
				continue
			}
			var g []Sample
			for j, got := range c.found {
				g = append(g, Sample{
					Features: Features(want, got, c.found),
					Positive: j == answer[i],
				})
			}
			groups = append(groups, g)
			groupOf = append(groupOf, n)
		}
	}
	t.Logf("%d groupes annotes, %d pictogrammes sans candidat correct (%.0f%% rates par la segmentation)",
		len(groups), missed, 100*float64(missed)/float64(len(groups)+missed))
	if len(groups) < 6 {
		t.Skipf("pas assez d'annotations (%d)", len(groups))
	}

	// Held out by challenge, not by group: three groups from one picture share
	// its candidates, so splitting inside a picture leaks.
	seen := map[string]int{}
	var order []string
	for _, n := range groupOf {
		if _, ok := seen[n]; !ok {
			seen[n] = len(order)
			order = append(order, n)
		}
	}
	folds := 4
	var accs []float64
	var wholes, basewholes [][2]int
	var wrong []string
	for fold := range folds {
		var train, test [][]Sample
		for i, g := range groups {
			if seen[groupOf[i]]%folds == fold {
				test = append(test, g)
			} else {
				train = append(train, g)
			}
		}
		if len(test) == 0 || len(train) == 0 {
			continue
		}
		m, err := Train(train, 400, 0.05, 0.1)
		if err != nil {
			t.Fatal(err)
		}
		a := Accuracy(m, test)
		accs = append(accs, a)

		// The question the solver actually asks: not whether each pictogram's
		// candidate ranks first, but whether the whole arrangement is right.
		// A challenge is passed or failed as a whole, so three pictograms at
		// two thirds each is not two thirds of a challenge.
		whole, wholeOK, baseOK := 0, 0, 0
		for _, n := range names {
			c, ok := challenges[n]
			if !ok || seen[n]%folds != fold {
				continue
			}
			answer, ok := labels[n]
			if !ok || len(answer) != len(c.wanted) {
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
			for _, cand := range []struct {
				m  Model
				at *int
			}{{m, &wholeOK}, {overlapOnly, &baseOK}} {
				pair, err := pairWith(cand.m, c.wanted, c.found)
				if err != nil {
					continue
				}
				right := true
				for i, j := range pair.Order {
					if j != answer[i] {
						right = false
					}
				}
				if right {
					*cand.at++
				} else if cand.at == &wholeOK {
					wrong = append(wrong, n)
				}
			}
		}
		wholes = append(wholes, [2]int{wholeOK, whole})
		basewholes = append(basewholes, [2]int{baseOK, whole})
		t.Logf("fold %d: %d groupes de test, precision %.2f — defis entiers %d/%d",
			fold, len(test), a, wholeOK, whole)
	}
	var mean float64
	for _, a := range accs {
		mean += a
	}
	if len(accs) > 0 {
		mean /= float64(len(accs))
	}
	t.Logf("PRECISION VALIDEE: %.3f", mean)

	var ok, tot, baseOK int
	for i, w := range wholes {
		ok += w[0]
		tot += w[1]
		baseOK += basewholes[i][0]
	}
	if tot > 0 {
		t.Logf("DEFIS ENTIERS VALIDES: %d/%d = %.3f", ok, tot, float64(ok)/float64(tot))
		t.Logf("DEFIS ENTIERS, RECOUVREMENT SEUL: %d/%d = %.3f",
			baseOK, tot, float64(baseOK)/float64(tot))
		// Named, because the next improvement comes from looking at what is
		// still wrong rather than from another sweep of the same knobs.
		sort.Strings(wrong)
		t.Logf("DEFIS RATES: %s", strings.Join(wrong, " "))
	}

	// A baseline to beat: pick whichever candidate overlaps most.
	base := 0
	for _, g := range groups {
		best, bestS := -1, -1.0
		for i, s := range g {
			if s.Features[0] > bestS {
				best, bestS = i, s.Features[0]
			}
		}
		if best >= 0 && g[best].Positive {
			base++
		}
	}
	t.Logf("BASELINE (recouvrement seul): %.3f", float64(base)/float64(len(groups)))

	// What each measurement is worth on its own, ranking by it alone. A model
	// that cannot beat its best single column is not being held back by its
	// objective or its sample size: the columns simply do not carry more than
	// one of them already says.
	for f := range FeatureCount {
		for _, sign := range []float64{1, -1} {
			hit := 0
			for _, g := range groups {
				best, bestS := -1, math.Inf(-1)
				for i, s := range g {
					if v := sign * s.Features[f]; v > bestS {
						best, bestS = i, v
					}
				}
				if best >= 0 && g[best].Positive {
					hit++
				}
			}
			dir := "+"
			if sign < 0 {
				dir = "-"
			}
			t.Logf("  seule %s%-16s %.3f", dir, FeatureNames[f], float64(hit)/float64(len(groups)))
		}
	}

	final, err := Train(groups, 600, 0.05, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	for i, w := range final.Weights {
		t.Logf("  %-18s %+.4f", FeatureNames[i], w)
	}
	if out := os.Getenv("EMIT"); out != "" {
		emit(t, out, final)
	}
}

// overlapOnly is the rule the model replaced: rank by silhouette overlap and
// nothing else. Kept as the thing to beat, on the whole arrangement and not
// only on one pictogram at a time.
var overlapOnly = func() Model {
	var m Model
	m.Weights[0] = 1
	for i := range m.Scale {
		m.Scale[i] = 1
	}
	return m
}()

func buildSheet(t *testing.T, dir string, c challenge) {
	bg, _ := readImage(dir + "/" + c.name + "/bg.png")
	q, _ := readImage(dir + "/" + c.name + "/ques.png")
	const scale = 3
	qb, bb := q.Bounds(), bg.Bounds()
	qh := qb.Dy() * 6
	w := max(qb.Dx()*6, bb.Dx()*scale)
	sheet := image.NewRGBA(image.Rect(0, 0, w, qh+bb.Dy()*scale))
	draw.Draw(sheet, sheet.Bounds(), &image.Uniform{color.RGBA{20, 20, 20, 255}}, image.Point{}, draw.Src)
	for y := range qh {
		for x := range qb.Dx() * 6 {
			sheet.Set(x, y, q.At(qb.Min.X+x/6, qb.Min.Y+y/6))
		}
	}
	for y := range bb.Dy() * scale {
		for x := range bb.Dx() * scale {
			sheet.Set(x, qh+y, bg.At(bb.Min.X+x/scale, bb.Min.Y+y/scale))
		}
	}
	for i, s := range c.found {
		col := palette[i%len(palette)]
		for th := range 3 {
			outline(sheet, s.MinX*scale-th, qh+s.MinY*scale-th,
				(s.MinX+s.W)*scale+th, qh+(s.MinY+s.H)*scale+th, col)
		}
	}
	f, err := os.Create(dir + "/" + c.name + "/sheet.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, sheet)
}

func outline(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	for x := x0; x <= x1; x++ {
		img.Set(x, y0, c)
		img.Set(x, y1, c)
	}
	for y := y0; y <= y1; y++ {
		img.Set(x0, y, c)
		img.Set(x1, y, c)
	}
}

func readLabels(t *testing.T, path string) map[string][]int {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string][]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		var idx []int
		bad := false
		for _, p := range parts[1:] {
			v, err := strconv.Atoi(p)
			if err != nil {
				bad = true
				break
			}
			idx = append(idx, v)
		}
		if !bad {
			out[parts[0]] = idx
		}
	}
	return out
}

func emit(t *testing.T, path string, m Model) {
	var b strings.Builder
	b.WriteString("package puzzle\n\n")
	b.WriteString("// Code generated from labelled challenges. DO NOT EDIT.\n\n")
	b.WriteString("// trained is the fitted comparison model.\n")
	b.WriteString("var trained = Model{\n\tWeights: [FeatureCount]float64{\n")
	for i, w := range m.Weights {
		b.WriteString(fmt.Sprintf("\t\t%+.6f, // %s\n", w, FeatureNames[i]))
	}
	b.WriteString("\t},\n\tMean: [FeatureCount]float64{\n")
	for _, v := range m.Mean {
		b.WriteString(fmt.Sprintf("\t\t%+.6f,\n", v))
	}
	b.WriteString("\t},\n\tScale: [FeatureCount]float64{\n")
	for _, v := range m.Scale {
		b.WriteString(fmt.Sprintf("\t\t%+.6f,\n", v))
	}
	b.WriteString("\t},\n}\n\n")
	// Stamped with what the weights were fitted against, so that a file left
	// over from a different feature vector is caught by a test instead of
	// compiling into a model whose columns no longer line up.
	b.WriteString("// trainedFeatures names what each weight was fitted against.\n")
	b.WriteString("var trainedFeatures = [FeatureCount]string{\n")
	for _, n := range FeatureNames {
		b.WriteString(fmt.Sprintf("\t%q,\n", n))
	}
	b.WriteString("}\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("poids ecrits dans %s", path)
}

func readImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}
