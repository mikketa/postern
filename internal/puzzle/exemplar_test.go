package puzzle

import (
	"os"
	"testing"
)

// Matching a drawing against other drawings, rather than against a rendering.
//
// The recogniser's remaining errors are not errors of care. Given candidates
// cut out perfectly it still ranks only 0.875 of pictograms first, and what it
// is being asked to do there is compare a clean rendered pictogram with
// something traced by hand — thickened, speckled, turned, and mirrored half
// the time. Two pictures of different kinds.
//
// The vendor's pictograms come from a pool of about two hundred, and every one
// of them has been drawn before. So the same question can be asked the other
// way round: not which candidate resembles this rendering, but which candidate
// resembles the other drawings of this glyph. Both sides are then hand-drawn,
// and everything that makes a tracing hard to compare with a rendering — the
// stroke, the speckle, the wobble — is on both sides at once.
//
// This measures whether that is true, leaving one challenge out at a time so
// that a glyph's exemplars never come from the challenge being answered.
func TestDrawingsComparedWithDrawings(t *testing.T) {
	dir, plateDir := os.Getenv("BENCH_DIR"), os.Getenv("PLATE_DIR")
	truthPath := os.Getenv("TRUTH")
	if dir == "" || plateDir == "" || truthPath == "" {
		t.Skip("no BENCH_DIR, PLATE_DIR or TRUTH")
	}

	// One entry per challenge: its prompt glyphs, and the drawn icon each one
	// turned out to be.
	type sighting struct {
		name   string
		glyph  []Shape
		drawn  []Shape // same length as glyph
		cand   []Shape
		answer []int // which candidate each pictogram is
	}
	var seen []sighting
	var all []Shape // every prompt glyph, in order, for the vocabulary
	for _, l := range readTruth(t, truthPath) {
		bg, e1 := readImage(dir + "/" + l.name + "/bg.png")
		pl, e2 := readImage(plateDir + "/" + l.name + ".png")
		q, e3 := readImage(dir + "/" + l.name + "/ques.png")
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		drawn, w, h := drawnIcons(bg, pl)
		wanted := FindPictograms(q, 140)
		if len(drawn) == 0 || len(wanted) != len(l.want) {
			continue
		}
		s := sighting{name: l.name, glyph: wanted, answer: l.want}
		for _, px := range drawn {
			s.cand = append(s.cand, shapeFromPixels(px, w, h))
		}
		ok := true
		for _, ti := range l.want {
			if ti < 0 || ti >= len(s.cand) {
				ok = false
				s.drawn = append(s.drawn, Shape{})
			} else {
				s.drawn = append(s.drawn, s.cand[ti])
			}
		}
		if !ok || len(s.cand) < len(wanted) {
			continue
		}
		seen = append(seen, s)
		all = append(all, wanted...)
	}
	if len(seen) < 20 {
		t.Skip("not enough challenges")
	}

	// Which prompt sightings are the same glyph. 0.96 is flat between 0.94 and
	// 0.98; see vocab_test.go.
	id := glyphKey(all, 0.96)
	at := 0
	owner := make([][]int, len(seen)) // cluster id per pictogram
	for i, s := range seen {
		owner[i] = id[at : at+len(s.glyph)]
		at += len(s.glyph)
	}

	type example struct {
		from  int // which challenge it came from
		shape Shape
	}
	drawnOf := map[int][]example{}
	for i, s := range seen {
		for k := range s.glyph {
			if s.drawn[k].Pixels > 0 {
				drawnOf[owner[i][k]] = append(drawnOf[owner[i][k]],
					example{from: i, shape: s.drawn[k]})
			}
		}
	}

	// Three ways of scoring the same pairs.
	const (
		rendering = iota // the pictogram, as now
		drawings         // other drawings of the same glyph
		both             // whichever of the two is more sure
	)
	names := [...]string{"pictogramme (actuel)", "autres dessins", "les deux"}
	var first, asked, whole, tried [3]int
	var withExamples, without int
	// How the gain depends on how many drawings of the glyph are to hand,
	// which is what says whether a larger corpus is worth chasing.
	byCount := map[int][2]int{} // exemplars -> {right, asked}, "both" only

	for way := range 3 {
		for i, s := range seen {
			score := make([][]float64, len(s.glyph))
			for k, g := range s.glyph {
				score[k] = make([]float64, len(s.cand))
				// Exemplars of this glyph from every OTHER challenge.
				var ex []Shape
				for _, e := range drawnOf[owner[i][k]] {
					if e.from != i {
						ex = append(ex, e.shape)
					}
				}
				if way == rendering || len(ex) == 0 {
					for j, c := range s.cand {
						score[k][j] = trained.dot(Features(g, c, s.cand))
					}
					if way == drawings {
						if len(ex) == 0 {
							without++
						}
					}
				} else {
					for j, c := range s.cand {
						best := -1e18
						for _, e := range ex {
							if v := trained.dot(Features(e, c, s.cand)); v > best {
								best = v
							}
						}
						if way == both {
							if v := trained.dot(Features(g, c, s.cand)); v > best {
								best = v
							}
						}
						score[k][j] = best
					}
					if way == drawings {
						withExamples++
					}
				}
			}

			for k := range s.glyph {
				asked[way]++
				if way == both {
					n := 0
					for _, e := range drawnOf[owner[i][k]] {
						if e.from != i {
							n++
						}
					}
					if n > 4 {
						n = 5
					}
					b := byCount[n]
					b[1]++
					byCount[n] = b
				}
				best := 0
				for j := range score[k] {
					if score[k][j] > score[k][best] {
						best = j
					}
				}
				if best == s.answer[k] {
					first[way]++
					if way == both {
						n := 0
						for _, e := range drawnOf[owner[i][k]] {
							if e.from != i {
								n++
							}
						}
						if n > 4 {
							n = 5
						}
						b := byCount[n]
						b[0]++
						byCount[n] = b
					}
				}
			}
			tried[way]++
			p, err := pairScored(score)
			if err != nil {
				continue
			}
			right := true
			for k, j := range p.Order {
				if j != s.answer[k] {
					right = false
				}
			}
			if right {
				whole[way]++
			}
		}
	}

	t.Logf("%d pictogrammes ont un exemplaire ailleurs, %d n'en ont pas",
		withExamples, without)
	for n := 0; n <= 5; n++ {
		b := byCount[n]
		if b[1] == 0 {
			continue
		}
		label := "exemplaires"
		if n == 5 {
			label = "exemplaires ou plus"
		}
		t.Logf("  %d %-20s %d/%d = %.3f", n, label, b[0], b[1], ratio(b[0], b[1]))
	}
	for way := range 3 {
		t.Logf("%-22s %d/%d = %.3f par pictogramme, %d/%d defis entiers",
			names[way], first[way], asked[way],
			ratio(first[way], asked[way]), whole[way], tried[way])
	}
}
