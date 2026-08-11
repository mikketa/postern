package challenge

import (
	"context"
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
