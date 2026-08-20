// Package geetest drives the grid challenges this vendor serves.
//
// Two of its five challenge types are boards: a five-by-five to line five up,
// and a three-by-three to swap three into a row. Both look like they need to
// be looked at, and neither does — the board is a table of elements and every
// square names what it holds. Reading the page turns them into puzzles that
// are decided rather than guessed, and no image is ever opened.
package geetest

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/puzzle"
)

// Square is one cell of the board: what it holds and where it is on screen.
type Square struct {
	Row int     `json:"r"`
	Col int     `json:"c"`
	ID  string  `json:"id"`
	X   float64 `json:"x"`
	Y   float64 `json:"y"`
}

// readGrid reports every square.
//
// What a square holds is written twice over — as a class on the icon it shows,
// and as the file that icon loads — so both are read and whichever is present
// wins. The -bg twins are skipped: every square has a backing element with a
// near-identical class, and taking both would double the board.
const readGrid = `(() => {
  const out = [];
  for (const e of document.querySelectorAll('[class*=geetest_item-]')) {
    const cls = e.getAttribute('class') || '';
    const m = cls.match(/geetest_item-(\d+)-(\d+)(?![\w-])/);
    if (!m || /geetest_item-\d+-\d+-bg/.test(cls)) continue;

    let id = '';
    for (const n of [e, ...e.querySelectorAll('*')]) {
      const c = n.getAttribute('class') || '';
      const k = c.match(/geetest_item_(\w+)/);
      if (k) { id = 'k' + k[1]; break; }
      const src = n.tagName === 'IMG' ? n.src : '';
      const bg = src || (getComputedStyle(n).backgroundImage || '');
      const u = bg.match(/([\w.-]+)\.(png|jpg|jpeg|webp)/);
      if (u) { id = u[1]; break; }
    }
    const r = e.getBoundingClientRect();
    out.push({ r: +m[1], c: +m[2], id, x: r.x + r.width/2, y: r.y + r.height/2 });
  }
  return out;
})()`

// Board is a board read off the page, with each square's position kept so a
// move can be played back.
type Board struct {
	Cells [][]puzzle.Cell
	at    map[[2]int]Square
}

// At returns where a square is on screen.
func (b Board) At(rc [2]int) (Square, bool) {
	s, ok := b.at[rc]
	return s, ok
}

// ReadBoard reads the challenge currently on the page.
func ReadBoard(ctx context.Context) (Board, error) {
	var raw json.RawMessage
	if err := chromedp.Run(ctx, chromedp.Evaluate(readGrid, &raw)); err != nil {
		return Board{}, fmt.Errorf("geetest: reading the board: %w", err)
	}
	var squares []Square
	if err := json.Unmarshal(raw, &squares); err != nil {
		return Board{}, fmt.Errorf("geetest: decoding the board: %w", err)
	}
	if len(squares) == 0 {
		return Board{}, fmt.Errorf("geetest: no board on the page")
	}

	maxR, maxC := 0, 0
	for _, s := range squares {
		maxR, maxC = max(maxR, s.Row), max(maxC, s.Col)
	}
	cells := make([][]puzzle.Cell, maxR+1)
	for i := range cells {
		cells[i] = make([]puzzle.Cell, maxC+1)
	}
	at := map[[2]int]Square{}
	for _, s := range squares {
		cells[s.Row][s.Col] = puzzle.Cell(s.ID)
		at[[2]int{s.Row, s.Col}] = s
	}
	return Board{Cells: cells, at: at}, nil
}

// Play performs a move: the square being taken from, then the square it goes
// to, with a pause between them.
func Play(ctx context.Context, b Board, m puzzle.Move) error {
	from, ok := b.At(m.From)
	if !ok {
		return fmt.Errorf("geetest: no square at %v", m.From)
	}
	to, ok := b.At(m.To)
	if !ok {
		return fmt.Errorf("geetest: no square at %v", m.To)
	}
	if err := Click(ctx, from.X, from.Y); err != nil {
		return err
	}
	time.Sleep(time.Duration(280+rand.IntN(320)) * time.Millisecond)
	return Click(ctx, to.X, to.Y)
}

// Click moves to a point and presses it.
//
// The approach matters: these widgets watch the pointer, and a click that
// arrives with no movement behind it is a click nothing with a hand made.
func Click(ctx context.Context, x, y float64) error {
	steps := 8 + rand.IntN(6)
	fx, fy := x-60+rand.Float64()*30, y-45+rand.Float64()*25
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		e := 1 - (1-t)*(1-t)
		if err := chromedp.Run(ctx, chromedp.MouseEvent("mouseMoved",
			fx+(x-fx)*e, fy+(y-fy)*e)); err != nil {
			return err
		}
		time.Sleep(time.Duration(10+rand.IntN(16)) * time.Millisecond)
	}
	time.Sleep(time.Duration(70+rand.IntN(120)) * time.Millisecond)
	return chromedp.Run(ctx, chromedp.MouseClickXY(x, y))
}
