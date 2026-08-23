package puzzle

import (
	"os"
	"sort"
	"testing"
)

// The glyph vocabulary, and what it is for.
//
// Everything in this package so far compares a prompt pictogram with a drawn
// icon: a clean rendering against something traced by hand, thickened,
// speckled and turned. That comparison is between two different kinds of
// picture, and it is where the recogniser's remaining errors are — given
// candidates cut out perfectly it still ranks only 0.875 of pictograms first,
// and the ones it misses are glyphs that look alike once drawn.
//
// But the vendor's pictograms come from a finite pool. Measured over the
// collected challenges the pool is about two hundred glyphs, and every one of
// them has been drawn before. So there is a second way to ask the question:
// not "which candidate looks like this pictogram" but "which candidate looks
// like the other drawings of this pictogram". That comparison is between two
// pictures of the same kind, which is a far easier one.
//
// This file establishes the identities that makes possible: which prompt
// pictograms are the same glyph as which.

// glyphKey groups prompt pictograms that are the same glyph.
//
// The prompts are rendered, not drawn, so two sightings of one glyph are very
// nearly the same picture — the same shape at the same size, differing only
// by where the strip happened to cut it. Overlap after normalising is
// therefore near one for a match and well below it for anything else, and
// there is no need for any of the machinery the drawn icons need.
//
// The silhouette is compared as it is, not filled. Filling a shape's enclosed
// space is right when comparing a drawing with a rendering, because a traced
// outline and a solid pictogram are the same glyph — but it also makes a drawn
// ring and a solid disc identical, and here that is the difference between two
// glyphs. Filled first, this put 41 sightings in one cluster.
func glyphKey(shapes []Shape, alike float64) []int {
	id := make([]int, len(shapes))
	for i := range id {
		id[i] = -1
	}
	norm := make([][]bool, len(shapes))
	for i, s := range shapes {
		norm[i] = normalise(s)
	}
	next := 0
	for i := range shapes {
		if id[i] >= 0 {
			continue
		}
		id[i] = next
		for j := i + 1; j < len(shapes); j++ {
			if id[j] < 0 && jaccard(norm[i], norm[j]) >= alike {
				id[j] = next
			}
		}
		next++
	}
	return id
}

func TestHowBigTheGlyphVocabularyIs(t *testing.T) {
	dir := os.Getenv("BENCH_DIR")
	truthPath := os.Getenv("TRUTH")
	if dir == "" || truthPath == "" {
		t.Skip("no BENCH_DIR or TRUTH")
	}
	var glyphs []Shape
	challenges := 0
	for _, l := range readTruth(t, truthPath) {
		q, err := readImage(dir + "/" + l.name + "/ques.png")
		if err != nil {
			continue
		}
		challenges++
		glyphs = append(glyphs, FindPictograms(q, 140)...)
	}
	if len(glyphs) == 0 {
		t.Skip("no prompts")
	}

	// What the best match to another sighting actually scores, so the
	// threshold below is chosen against the distribution and not by taste.
	{
		norm := make([][]bool, len(glyphs))
		for i, s := range glyphs {
			norm[i] = normalise(s)
		}
		best := make([]float64, len(glyphs))
		for i := range glyphs {
			for j := range glyphs {
				if i != j {
					best[i] = max(best[i], jaccard(norm[i], norm[j]))
				}
			}
		}
		sorted := append([]float64(nil), best...)
		sort.Float64s(sorted)
		t.Logf("meilleur accord avec une autre observation: mediane %.3f, "+
			"1er decile %.3f, 9e decile %.3f",
			sorted[len(sorted)/2], sorted[len(sorted)/10], sorted[9*len(sorted)/10])
	}

	for _, alike := range []float64{0.90, 0.94, 0.96, 0.98} {
		id := glyphKey(glyphs, alike)
		count := map[int]int{}
		for _, v := range id {
			count[v]++
		}
		singles, most := 0, 0
		for _, n := range count {
			if n == 1 {
				singles++
			}
			most = max(most, n)
		}
		// Chao1: how many glyphs the pool has that this bench never saw twice.
		twos := 0
		for _, n := range count {
			if n == 2 {
				twos++
			}
		}
		chao := float64(len(count))
		if twos > 0 {
			chao += float64(singles*singles) / float64(2*twos)
		}
		t.Logf("seuil %.2f: %d sightings -> %d glyphes, %d vus une seule fois, "+
			"le plus vu %d fois, Chao1 ~ %.0f",
			alike, len(glyphs), len(count), singles, most, chao)
	}

	// How many drawn examples a glyph has, at the threshold used below.
	id := glyphKey(glyphs, 0.96)
	count := map[int]int{}
	for _, v := range id {
		count[v]++
	}
	var sizes []int
	for _, n := range count {
		sizes = append(sizes, n)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(sizes)))
	usable := 0
	for _, n := range sizes {
		if n >= 2 {
			usable += n
		}
	}
	t.Logf("pictogrammes ayant au moins un autre exemplaire: %d/%d", usable, len(glyphs))

	// How the coverage grows with the bench, measured by taking prefixes of it
	// rather than guessed from a distribution. What matters is not how many
	// glyphs are known but how many sightings have two other drawings of their
	// glyph to be compared with: below that the recogniser falls back to the
	// rendering and gains nothing. See exemplar_test.go.
	perChallenge := float64(len(glyphs)) / float64(challenges)
	for _, frac := range []float64{0.25, 0.5, 0.75, 1.0} {
		n := int(float64(len(glyphs)) * frac)
		if n < 3 {
			continue
		}
		sub := glyphKey(glyphs[:n], 0.96)
		c := map[int]int{}
		for _, v := range sub {
			c[v]++
		}
		two, three := 0, 0
		for _, k := range sub {
			if c[k] >= 3 {
				two++
			}
			if c[k] >= 4 {
				three++
			}
		}
		t.Logf("  %3d defis: %3d observations, %3d glyphes, "+
			"%.2f ont 2 autres dessins, %.2f en ont 3",
			int(float64(n)/perChallenge), n, len(c),
			float64(two)/float64(n), float64(three)/float64(n))
	}
}
