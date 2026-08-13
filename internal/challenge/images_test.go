package challenge

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// solverScript writes an executable standing in for a vision model.
func solverScript(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "solver.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatalf("write solver: %v", err)
	}
	return path
}

func TestAskParsesCoordinates(t *testing.T) {
	solver := solverScript(t, `printf '10,20\n30.5, 40.5\n\n'`)

	points, err := ask(context.Background(), solver, "/tmp/whatever.png", View{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}

	want := []point{{X: 10, Y: 20}, {X: 30.5, Y: 40.5}}
	if len(points) != len(want) {
		t.Fatalf("got %d points, want %d: %v", len(points), len(want), points)
	}
	for i := range want {
		if points[i] != want[i] {
			t.Errorf("point %d is %v, want %v", i, points[i], want[i])
		}
	}
}

func TestAskAcceptsNoSelection(t *testing.T) {
	// A solver that finds nothing to click is answering, not failing — the
	// caller decides what an empty answer means.
	solver := solverScript(t, `exit 0`)

	points, err := ask(context.Background(), solver, "/tmp/whatever.png", View{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if len(points) != 0 {
		t.Errorf("got %v, want no points", points)
	}
}

func TestAskRejectsGarbage(t *testing.T) {
	solver := solverScript(t, `printf 'left a bit\n'`)

	if _, err := ask(context.Background(), solver, "/tmp/whatever.png", View{}); err == nil {
		t.Fatal("expected an error for unparseable output")
	} else if !strings.Contains(err.Error(), "want") {
		t.Errorf("error %q should say what was expected instead", err)
	}
}

func TestAskReportsSolverFailure(t *testing.T) {
	solver := solverScript(t, `exit 3`)

	if _, err := ask(context.Background(), solver, "/tmp/whatever.png", View{}); err == nil {
		t.Fatal("expected an error when the solver exits non-zero")
	}
}

func TestAskPassesTheImagePathLast(t *testing.T) {
	// Flags given inline must survive, with the image path appended after them,
	// so that "-image-solver 'python solve.py --model foo'" works.
	solver := solverScript(t, `printf '%s\n' "$*" | tr ' ' '\n' | grep -c . > /dev/null; echo "1,$#"`)

	points, err := ask(context.Background(), solver+" --model foo", "/tmp/whatever.png", View{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if len(points) != 1 || points[0].Y != 3 {
		t.Errorf("solver saw %v args, want 3 (--model, foo, image path)", points)
	}
}

func TestAskPassesWhatIsKnownThroughTheEnvironment(t *testing.T) {
	// A solver should not have to OCR a prompt postern can read, nor guess a
	// grid postern has measured.
	solver := solverScript(t, `echo "$POSTERN_COLUMNS,${#POSTERN_PROMPT}"; echo "$POSTERN_TILES" >&2`)

	view := View{
		Prompt: "select all images with buses",
		Tiles:  make([]Box, 16),
	}
	view.Tiles[0] = Box{X: 6, Y: 126, W: 72, H: 72}

	points, err := ask(context.Background(), solver, "/tmp/whatever.png", view)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("got %v, want one line", points)
	}
	if points[0].X != 4 {
		t.Errorf("solver saw %v columns, want 4 for a sixteen-tile grid", points[0].X)
	}
	if points[0].Y != float64(len(view.Prompt)) {
		t.Errorf("solver saw a %v-character prompt, want %d", points[0].Y, len(view.Prompt))
	}
}

func TestEncodeTiles(t *testing.T) {
	got := encodeTiles([]Box{{X: 6, Y: 126, W: 72, H: 72}, {X: 78, Y: 126, W: 72, H: 72}})
	want := "6,126,72,72;78,126,72,72"
	if got != want {
		t.Errorf("encodeTiles = %q, want %q", got, want)
	}
}

func TestColumns(t *testing.T) {
	for _, tc := range []struct {
		tiles int
		want  int
	}{{9, 3}, {16, 4}, {0, 0}, {7, 0}} {
		if got := (View{Tiles: make([]Box, tc.tiles)}).Columns(); got != tc.want {
			t.Errorf("%d tiles: Columns() = %d, want %d", tc.tiles, got, tc.want)
		}
	}
}

func TestAskRejectsABlankCommand(t *testing.T) {
	// "-image-solver $SOLVER" with the variable unset used to panic on
	// fields[1:] rather than say what was wrong.
	if _, err := ask(context.Background(), "   ", "/tmp/whatever.png", View{}); err == nil {
		t.Fatal("expected an error for a blank command")
	}
}

func TestAskQuotesWhatTheSolverSaid(t *testing.T) {
	// A non-zero exit ends the solve, so its author gets one chance to be told
	// why — and it is whatever the solver printed.
	solver := solverScript(t, `echo "no model at /opt/model.onnx" >&2; exit 1`)

	_, err := ask(context.Background(), solver, "/tmp/whatever.png", View{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "/opt/model.onnx") {
		t.Errorf("error %q drops what the solver said", err)
	}
}

func TestLastLines(t *testing.T) {
	got := lastLines([]byte("one\ntwo\nthree\nfour\n"), 2)
	if got != "three; four" {
		t.Errorf("lastLines = %q, want %q", got, "three; four")
	}
}

// TestTileAtFindsWhatIsUnderAPoint covers the lookup that keeps postern from
// unticking its own answers. A tile is a toggle: clicking one that is already
// ticked clears it, and a solver that names the same correct tile every round
// would otherwise have postern alternate between selecting and deselecting it
// until the rounds ran out.
func TestTileAtFindsWhatIsUnderAPoint(t *testing.T) {
	// Two tiles side by side, 100x100, the second one ticked.
	view := View{
		Width:  200,
		Height: 100,
		Tiles: []Box{
			{X: 0, Y: 0, W: 100, H: 100},
			{X: 100, Y: 0, W: 100, H: 100, Selected: true},
		},
	}

	cases := []struct {
		name     string
		x, y     float64
		wantTile int // index, or -1 for none
	}{
		{"middle of the first", 50, 50, 0},
		{"middle of the second", 150, 50, 1},
		{"top left corner belongs to its tile", 0, 0, 0},
		{"the boundary belongs to the tile it starts", 100, 0, 1},
		{"past the last tile", 200, 50, -1},
		{"below the grid", 50, 150, -1},
		{"negative", -1, 50, -1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := view.TileAt(c.x, c.y)
			if c.wantTile < 0 {
				if got != nil {
					t.Errorf("point %.0f,%.0f matched a tile at %.0f,%.0f", c.x, c.y, got.X, got.Y)
				}
				return
			}
			if got == nil {
				t.Fatalf("point %.0f,%.0f matched no tile", c.x, c.y)
			}
			if want := &view.Tiles[c.wantTile]; got != want {
				t.Errorf("point %.0f,%.0f matched the tile at %.0f, wanted the one at %.0f",
					c.x, c.y, got.X, want.X)
			}
		})
	}

	// The whole point of the lookup.
	if tile := view.TileAt(150, 50); tile == nil || !tile.Selected {
		t.Error("a point on a ticked tile has to come back ticked, or postern clicks it off")
	}
}

// TestBlankSpotsAnUnpaintedGrid covers the check that keeps a solver from being
// handed a photograph of nothing.
//
// reCAPTCHA fades its pictures in, and the document reports them as loaded
// before they have been painted. Measured over 88 panels captured from live
// runs, fourteen were entirely flat squares — one round in six spent asking a
// vision model to find buses in a blank grid.
func TestBlankSpotsAnUnpaintedGrid(t *testing.T) {
	tiles := []Box{
		{X: 0, Y: 0, W: 60, H: 60}, {X: 60, Y: 0, W: 60, H: 60},
		{X: 0, Y: 60, W: 60, H: 60}, {X: 60, Y: 60, W: 60, H: 60},
	}

	cases := []struct {
		name string
		draw func(*image.RGBA)
		want bool
	}{
		{
			name: "a grid that has not arrived",
			draw: func(img *image.RGBA) { fill(img, 0, 0, 120, 120, 255, 255, 255) },
			want: true,
		},
		{
			name: "the grey reCAPTCHA parks there",
			draw: func(img *image.RGBA) { fill(img, 0, 0, 120, 120, 238, 238, 238) },
			want: true,
		},
		{
			name: "photographs",
			draw: noise,
			want: false,
		},
		{
			name: "one tile of plain sky among photographs",
			draw: func(img *image.RGBA) {
				noise(img)
				fill(img, 0, 0, 60, 60, 150, 190, 230)
			},
			want: false,
		},
		{
			name: "three tiles still arriving",
			draw: func(img *image.RGBA) {
				noise(img)
				fill(img, 0, 0, 120, 60, 255, 255, 255)
				fill(img, 0, 60, 60, 120, 255, 255, 255)
			},
			want: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			img := image.NewRGBA(image.Rect(0, 0, 120, 120))
			c.draw(img)

			var buf bytes.Buffer
			if err := png.Encode(&buf, img); err != nil {
				t.Fatalf("encode: %v", err)
			}

			if got := blank(buf.Bytes(), tiles); got != c.want {
				t.Errorf("blank = %v, want %v", got, c.want)
			}
		})
	}

	// A screenshot that will not decode is not evidence of anything.
	if blank([]byte("not a png"), tiles) {
		t.Error("an unreadable screenshot was called blank")
	}
	// Neither is a panel with no tiles read out of it.
	if blank(nil, nil) {
		t.Error("a panel with no tiles was called blank")
	}
}

// TestSettledSpotsAGridStillFadingIn covers the half-painted case, which is
// the dangerous one: the pictures are there, so nothing about the photograph
// says it was taken too early, and the answers come back wrong.
func TestSettledSpotsAGridStillFadingIn(t *testing.T) {
	tiles := []Box{
		{X: 0, Y: 0, W: 60, H: 60}, {X: 60, Y: 0, W: 60, H: 60},
		{X: 0, Y: 60, W: 60, H: 60}, {X: 60, Y: 60, W: 60, H: 60},
	}

	// The same photographs, one at half opacity over white and one finished.
	fading := image.NewRGBA(image.Rect(0, 0, 120, 120))
	noise(fading)
	washOut(fading)

	finished := image.NewRGBA(image.Rect(0, 0, 120, 120))
	noise(finished)

	// And the same picture again, with a cursor's worth of pixels moved.
	nudged := image.NewRGBA(image.Rect(0, 0, 120, 120))
	noise(nudged)
	fill(nudged, 20, 20, 32, 32, 0, 0, 0)

	cases := []struct {
		name          string
		before, after *image.RGBA
		want          bool
	}{
		{"a grid that has finished arriving", finished, finished, true},
		{"a grid still fading in", fading, finished, false},
		{"the same grid with the cursor over it", finished, nudged, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := settled(encode(t, c.before), encode(t, c.after), tiles); got != c.want {
				t.Errorf("settled = %v, want %v", got, c.want)
			}
		})
	}

	// Nothing to compare is not evidence that anything has settled.
	if settled(nil, encode(t, finished), tiles) {
		t.Error("a missing photograph was called settled")
	}
	if settled(encode(t, finished), encode(t, finished), nil) {
		t.Error("a panel with no tiles was called settled")
	}
	if settled([]byte("not a png"), encode(t, finished), tiles) {
		t.Error("an unreadable screenshot was called settled")
	}
}

// TestKeepWritesAPanelAndItsGeometry covers the corpus format, which several
// scripts outside the repository read: the JSON has to say what the panel asked
// and where its tiles were, or a saved grid cannot be replayed.
func TestKeepWritesAPanelAndItsGeometry(t *testing.T) {
	dir := t.TempDir()
	view := View{
		Prompt: "Sélectionnez toutes les images montrant des bus",
		Tiles: []Box{
			{X: 5, Y: 125, W: 96, H: 96}, {X: 101, Y: 125, W: 96, H: 96},
			{X: 197, Y: 125, W: 96, H: 96}, {X: 5, Y: 221, W: 96, H: 96},
			{X: 101, Y: 221, W: 96, H: 96}, {X: 197, Y: 221, W: 96, H: 96},
			{X: 5, Y: 317, W: 96, H: 96}, {X: 101, Y: 317, W: 96, H: 96},
			{X: 197, Y: 317, W: 96, H: 96},
		},
	}

	if err := keep(dir, []byte("a png, as far as this is concerned"), view); err != nil {
		t.Fatalf("keep: %v", err)
	}

	written, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(written) != 2 {
		t.Fatalf("wrote %d files, want a png and a json", len(written))
	}

	var meta struct {
		Prompt  string `json:"prompt"`
		Columns string `json:"columns"`
		Tiles   string `json:"tiles"`
	}
	for _, entry := range written {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if err := json.Unmarshal(body, &meta); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
	}

	if meta.Prompt != view.Prompt {
		t.Errorf("prompt = %q, want %q", meta.Prompt, view.Prompt)
	}
	if meta.Columns != "3" {
		t.Errorf("columns = %q, want 3", meta.Columns)
	}
	if want := "5,125,96,96;"; !strings.HasPrefix(meta.Tiles, want) {
		t.Errorf("tiles = %q, want it to start %q", meta.Tiles, want)
	}
	if got := strings.Count(meta.Tiles, ";"); got != len(view.Tiles)-1 {
		t.Errorf("tiles has %d separators, want %d", got, len(view.Tiles)-1)
	}
}

func encode(t *testing.T, img *image.RGBA) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

// washOut is a picture halfway through reCAPTCHA's fade: still recognisable,
// still wrong to answer.
func washOut(img *image.RGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.RGBAAt(x, y)
			img.SetRGBA(x, y, color.RGBA{
				R: uint8((int(c.R) + 255) / 2),
				G: uint8((int(c.G) + 255) / 2),
				B: uint8((int(c.B) + 255) / 2),
				A: 255,
			})
		}
	}
}

func fill(img *image.RGBA, x0, y0, x1, y1 int, r, g, b uint8) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
}

// noise is a stand-in for photographs: varied enough that no tile is flat.
func noise(img *image.RGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			v := uint8((x*7 + y*13) % 256)
			img.SetRGBA(x, y, color.RGBA{R: v, G: 255 - v, B: uint8((x * y) % 256), A: 255})
		}
	}
}
