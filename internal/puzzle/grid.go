package puzzle

import "fmt"

// Grid challenges: the ones whose whole state is written in the page.
//
// Two of the five types this vendor serves look like they need computer vision
// and do not. The board is a table of elements, and each cell says what it
// holds — in a class name, in the file it is showing. Read that and the
// challenge stops being a picture problem and becomes a one-move puzzle, which
// is decidable rather than probable. The images are never opened.

// Cell is what a square holds. Cells compare equal when they show the same
// thing; the zero value is an empty square.
type Cell string

// Empty is a square with nothing on it.
const Empty Cell = ""

// Move is one action on a board: put what is at From onto To. For a swap board
// the two squares exchange contents; for a board with holes in it, From empties.
type Move struct {
	From, To [2]int // {row, column}
}

func (m Move) String() string {
	return fmt.Sprintf("(%d,%d)->(%d,%d)", m.From[0], m.From[1], m.To[0], m.To[1])
}

// SolveSwap finds the exchange that lines up runLen identical cells.
//
// The board is small and exactly one move is asked for, so every candidate is
// tried and the answer is checked rather than scored. Adjacent pairs are tried
// first because that is what the instruction describes; the rest are tried
// afterwards, since a board that only yields to a distant swap is worth
// solving rather than refusing.
func SolveSwap(board [][]Cell, runLen int) (Move, error) {
	if err := rectangular(board); err != nil {
		return Move{}, err
	}
	for _, adjacentOnly := range []bool{true, false} {
		for _, m := range pairs(board, adjacentOnly) {
			after := clone(board)
			a, b := m.From, m.To
			after[a[0]][a[1]], after[b[0]][b[1]] = after[b[0]][b[1]], after[a[0]][a[1]]
			if hasRun(after, runLen, false) {
				return m, nil
			}
		}
	}
	return Move{}, fmt.Errorf("puzzle: no swap on this %dx%d board lines up %d",
		len(board), len(board[0]), runLen)
}

// SolveLine finds the piece to move into a gap so that runLen cells line up.
//
// Unlike a swap, the square left behind becomes empty — which matters, because
// the piece being moved is sometimes already part of the row it is meant to
// complete, and moving it would break the row it is taken from.
func SolveLine(board [][]Cell, runLen int) (Move, error) {
	if err := rectangular(board); err != nil {
		return Move{}, err
	}
	for r := range board {
		for c := range board[r] {
			if board[r][c] == Empty {
				continue
			}
			for tr := range board {
				for tc := range board[tr] {
					if board[tr][tc] != Empty {
						continue
					}
					after := clone(board)
					after[tr][tc] = after[r][c]
					after[r][c] = Empty
					if hasRun(after, runLen, true) {
						return Move{From: [2]int{r, c}, To: [2]int{tr, tc}}, nil
					}
				}
			}
		}
	}
	return Move{}, fmt.Errorf("puzzle: no move on this %dx%d board lines up %d",
		len(board), len(board[0]), runLen)
}

// pairs lists candidate exchanges, closest first.
func pairs(board [][]Cell, adjacentOnly bool) []Move {
	var out []Move
	for r := range board {
		for c := range board[r] {
			for r2 := range board {
				for c2 := range board[r2] {
					if r2 < r || (r2 == r && c2 <= c) {
						continue // each unordered pair once
					}
					near := (r == r2 && c2-c == 1) || (c == c2 && r2-r == 1)
					if adjacentOnly != near {
						continue
					}
					out = append(out, Move{From: [2]int{r, c}, To: [2]int{r2, c2}})
				}
			}
		}
	}
	return out
}

// hasRun reports whether the board holds runLen identical non-empty cells in a
// line.
//
// Whether a diagonal counts depends on the game, and guessing wrong is a wasted
// attempt. A match-three counts rows and columns only — so a swap is chosen
// under that rule, which is also a winning line for a game that does count
// diagonals. The five-in-a-row does count them, and refusing to would leave
// solvable boards unsolved.
func hasRun(board [][]Cell, runLen int, diagonals bool) bool {
	h := len(board)
	w := len(board[0])
	dirs := [][2]int{{0, 1}, {1, 0}}
	if diagonals {
		dirs = append(dirs, [2]int{1, 1}, [2]int{1, -1})
	}
	for r := range h {
		for c := range w {
			if board[r][c] == Empty {
				continue
			}
			for _, d := range dirs {
				n := 1
				for ; n < runLen; n++ {
					rr, cc := r+d[0]*n, c+d[1]*n
					if rr < 0 || cc < 0 || rr >= h || cc >= w || board[rr][cc] != board[r][c] {
						break
					}
				}
				if n == runLen {
					return true
				}
			}
		}
	}
	return false
}

func clone(board [][]Cell) [][]Cell {
	out := make([][]Cell, len(board))
	for i, row := range board {
		out[i] = append([]Cell(nil), row...)
	}
	return out
}

func rectangular(board [][]Cell) error {
	if len(board) == 0 || len(board[0]) == 0 {
		return fmt.Errorf("puzzle: empty board")
	}
	for _, row := range board {
		if len(row) != len(board[0]) {
			return fmt.Errorf("puzzle: board is ragged: a row of %d against %d",
				len(row), len(board[0]))
		}
	}
	return nil
}
