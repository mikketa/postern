package puzzle

import "testing"

// board is shorthand: one string per row, one rune per cell, '.' for empty.
func board(rows ...string) [][]Cell {
	out := make([][]Cell, len(rows))
	for r, row := range rows {
		out[r] = make([]Cell, len(row))
		for c, ch := range row {
			if ch != '.' {
				out[r][c] = Cell(string(ch))
			}
		}
	}
	return out
}

// apply performs a move the way the challenge does, so a test asserts on the
// resulting board rather than on the coordinates the solver happened to pick.
// Several different moves can be correct, and pinning one of them would make
// the test fail on a better answer.
func apply(b [][]Cell, m Move, swap bool) [][]Cell {
	out := clone(b)
	a, z := m.From, m.To
	if swap {
		out[a[0]][a[1]], out[z[0]][z[1]] = out[z[0]][z[1]], out[a[0]][a[1]]
		return out
	}
	out[z[0]][z[1]] = out[a[0]][a[1]]
	out[a[0]][a[1]] = Empty
	return out
}

// TestTheSwapBoardFromTheLiveChallenge is the 3x3 board this vendor actually
// served, read out of the page: every cell names what it holds in its own class
// name, so the challenge is decided rather than guessed.
func TestTheSwapBoardFromTheLiveChallenge(t *testing.T) {
	b := board(
		"aba",
		"bcd",
		"ddc",
	)
	m, err := SolveSwap(b, 3)
	if err != nil {
		t.Fatalf("SolveSwap: %v", err)
	}
	if !hasRun(apply(b, m, true), 3, false) {
		t.Errorf("%s does not line up three", m)
	}
}

// TestTheLineBoardFromTheLiveChallenge is the 5x5 board served alongside it:
// four pieces along the top row with a gap in it, and one piece stranded
// elsewhere to fill the gap with.
func TestTheLineBoardFromTheLiveChallenge(t *testing.T) {
	b := board(
		"xx.xx",
		".....",
		"...x.",
		".....",
		".....",
	)
	m, err := SolveLine(b, 5)
	if err != nil {
		t.Fatalf("SolveLine: %v", err)
	}
	if !hasRun(apply(b, m, false), 5, true) {
		t.Errorf("%s does not line up five", m)
	}
}

// TestAPieceIsNotTakenFromTheRowItCompletes is the trap in moving rather than
// swapping: the square left behind empties. A solver that only checks the
// destination will happily take a piece out of the very row it is filling and
// report a win on a board that is one short.
func TestAPieceIsNotTakenFromTheRowItCompletes(t *testing.T) {
	// The top row needs one more piece, and the only other pieces on the board
	// are in that same row. No move can finish it.
	b := board(
		"xxxx.",
		".....",
		".....",
		".....",
		".....",
	)
	if m, err := SolveLine(b, 5); err == nil {
		after := apply(b, m, false)
		t.Errorf("claimed %s solves a board that cannot be solved; result has "+
			"a run: %v", m, hasRun(after, 5, true))
	}
}

func TestADiagonalCounts(t *testing.T) {
	b := board(
		"x....",
		".x...",
		"..x..",
		"...x.",
		"....x",
	)
	if !hasRun(b, 5, true) {
		t.Error("five on the leading diagonal were not counted")
	}
	anti := board(
		"....x",
		"...x.",
		"..x..",
		".x...",
		"x....",
	)
	if !hasRun(anti, 5, true) {
		t.Error("five on the anti-diagonal were not counted")
	}
}

func TestEmptySquaresNeverFormARun(t *testing.T) {
	if hasRun(board("...", "...", "..."), 3, true) {
		t.Error("a board of empty squares reported three in a row")
	}
}

func TestAnUnsolvableSwapBoardIsRefused(t *testing.T) {
	// Every cell distinct: with no symbol appearing even twice, no exchange
	// can produce three alike. The first attempt at this fixture was a Latin
	// square, which already had three alike down its anti-diagonal.
	if m, err := SolveSwap(board("abc", "def", "ghi"), 3); err == nil {
		t.Errorf("claimed %s solves a board with no solution", m)
	}
}

func TestARaggedBoardIsRefused(t *testing.T) {
	ragged := [][]Cell{{"a", "b"}, {"a"}}
	if _, err := SolveSwap(ragged, 3); err == nil {
		t.Error("a ragged board was accepted")
	}
	if _, err := SolveLine(ragged, 3); err == nil {
		t.Error("a ragged board was accepted by SolveLine")
	}
}

// TestAnAdjacentSwapIsPreferred covers the ordering: the instruction says to
// swap neighbours, so when both work the neighbourly one is the one to play.
func TestAnAdjacentSwapIsPreferred(t *testing.T) {
	// Swapping (0,0) with its neighbour (1,0) completes the top row; so does
	// the distant swap of (0,0) with (2,2).
	b := board(
		"caa",
		"a..",
		"..a",
	)
	m, err := SolveSwap(b, 3)
	if err != nil {
		t.Fatalf("SolveSwap: %v", err)
	}
	dr := m.To[0] - m.From[0]
	dc := m.To[1] - m.From[1]
	if dr*dr+dc*dc != 1 {
		t.Errorf("played %s, which is not between neighbours", m)
	}
}
