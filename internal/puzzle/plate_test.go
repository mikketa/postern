package puzzle

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"sort"
	"testing"
)

// Reconstructing the photograph behind a challenge.
//
// The vendor draws its icons over a small pool of reused photographs.
// Challenges sharing a photograph differ only where the icons are, so the
// per-pixel median over enough of them is that photograph with every drawn
// icon voted away — and subtracting it from a challenge gives the icons
// exactly. That is the witness this project would otherwise not have: without
// it, "the segmenter missed one" is an opinion formed by looking at a
// screenshot, and the first label set's worst errors came from exactly that.
//
// Nothing here ships. It needs a pool of challenges sharing a photograph,
// which a solver facing one challenge does not have. It is a labelling and
// measuring aid, and it lives in the repository rather than in a scratch
// directory because everything downstream — the truth, the scenery, the
// exemplars — is derived from it, and a pipeline whose first step is a script
// somebody once had in /tmp is a pipeline that cannot be rerun.
//
//	BENCH_DIR=<dir> PLATE_DIR=<dir> go test -count=1 \
//	    ./internal/puzzle -run TestReconstructThePlates -v

// near is the coarse distance below which two challenges are taken to share a
// photograph, and least is how many neighbours a median needs to be trusted.
// A challenge with fewer simply gets no plate, which costs a challenge from
// the bench and never costs a wrong answer.
const (
	near  = 12.0
	least = 5
	stack = 11
)

func TestReconstructThePlates(t *testing.T) {
	dir, out := os.Getenv("BENCH_DIR"), os.Getenv("PLATE_DIR")
	if dir == "" || out == "" {
		t.Skip("no BENCH_DIR or PLATE_DIR")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skip(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	type shot struct {
		name   string
		w, h   int
		px     []byte // RGB, row-major
		coarse []byte
	}
	var shots []shot
	for _, e := range entries {
		img, err := readImage(dir + "/" + e.Name() + "/bg.png")
		if err != nil {
			continue
		}
		b := img.Bounds()
		s := shot{name: e.Name(), w: b.Dx(), h: b.Dy()}
		s.px = make([]byte, 3*s.w*s.h)
		for y := range s.h {
			for x := range s.w {
				r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				i := 3 * (y*s.w + x)
				s.px[i], s.px[i+1], s.px[i+2] = byte(r>>8), byte(g>>8), byte(bl>>8)
			}
		}
		s.coarse = thumb(s.px, s.w, s.h, 24, 18)
		shots = append(shots, s)
	}
	if len(shots) < least+1 {
		t.Skipf("only %d challenges", len(shots))
	}

	made, alone := 0, 0
	for i, s := range shots {
		var kin [][]byte
		for j, o := range shots {
			if i == j || o.w != s.w || o.h != s.h {
				continue
			}
			// Above zero as well as below near: a challenge identical to
			// another is the same picture served twice, and stacking a
			// duplicate of the thing being cleaned up puts the icons back.
			if d := apartBy(s.coarse, o.coarse); d > 0.5 && d < near {
				kin = append(kin, o.px)
			}
		}
		if len(kin) < least {
			alone++
			continue
		}
		if len(kin) > stack {
			kin = kin[:stack]
		}
		plate := median(kin, len(s.px))
		if err := writePNG(out+"/"+s.name+".png", plate, s.w, s.h); err != nil {
			t.Fatal(err)
		}
		made++
	}
	t.Logf("%d plaques reconstruites, %d defis sans voisins suffisants", made, alone)
	if made == 0 {
		t.Fatal("no plate could be built: the pool has no repeats in it")
	}
}

// thumb shrinks a picture by averaging, which is enough to tell one
// photograph from another and blind to the icons drawn on either.
func thumb(px []byte, w, h, tw, th int) []byte {
	out := make([]byte, 3*tw*th)
	for ty := range th {
		for tx := range tw {
			x0, x1 := tx*w/tw, max(tx*w/tw+1, (tx+1)*w/tw)
			y0, y1 := ty*h/th, max(ty*h/th+1, (ty+1)*h/th)
			var sum [3]int
			n := 0
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					i := 3 * (y*w + x)
					sum[0] += int(px[i])
					sum[1] += int(px[i+1])
					sum[2] += int(px[i+2])
					n++
				}
			}
			o := 3 * (ty*tw + tx)
			for c := range 3 {
				out[o+c] = byte(sum[c] / max(1, n))
			}
		}
	}
	return out
}

func apartBy(a, b []byte) float64 {
	var sum int
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return float64(sum) / float64(len(a))
}

// median votes each channel of each pixel separately. The icons are drawn in
// different places in each challenge, so at any one pixel most of the stack
// shows the photograph and the vote goes to it.
func median(stack [][]byte, n int) []byte {
	out := make([]byte, n)
	col := make([]int, len(stack))
	for i := range n {
		for k, s := range stack {
			col[k] = int(s[i])
		}
		sort.Ints(col)
		out[i] = byte(col[len(col)/2])
	}
	return out
}

func writePNG(path string, px []byte, w, h int) error {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			i := 3 * (y*w + x)
			img.Set(x, y, color.RGBA{px[i], px[i+1], px[i+2], 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
