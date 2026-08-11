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
