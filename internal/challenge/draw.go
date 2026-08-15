package challenge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// drawScript builds the panel picture inside the page instead of photographing
// it, by drawing the tiles' own images onto a canvas.
//
// A screenshot is a picture of what the compositor last put on screen, and
// under a virtual display that is a thing postern does not control. It has cost
// three separate classes of failure: a fade frozen halfway, so the grid comes
// back translucent with the page showing through; a photograph taken mid-fade,
// which no single frame reveals as such; and a panel that never painted at all,
// which arrives as one flat white rectangle while the document lists every tile
// with exact geometry and every picture in it loaded and decoded.
//
// A canvas is drawn by the renderer. It does not care what has been composited,
// so none of the three can happen: the pictures are in the document, and the
// document is what gets read.
//
// The canvas is the size of the challenge document's own viewport, and each
// tile is drawn at the rectangle the document gives for it — the same
// coordinates the screenshot path produces, because the panel clip is exactly
// this viewport. So everything downstream, the tile boxes handed to the solver
// and the coordinates it answers with, means the same thing either way.
//
// Each image is drawn clipped to its tile. On a 4x4 the sixteen squares are one
// photograph, shown by sixteen elements pointing at the same picture and offset
// so each shows its own quarter of it; clipping is what keeps a tile to its own
// quarter rather than painting the whole photograph sixteen times.
//
// It returns "" rather than throwing when it cannot: a cross-origin picture
// taints the canvas and makes toDataURL a security error, and the caller
// photographs the screen instead.
const drawScript = `(() => {
  const tiles = [...document.querySelectorAll('.rc-imageselect-tile')];
  if (!tiles.length) return '';

  const canvas = document.createElement('canvas');
  canvas.width = window.innerWidth;
  canvas.height = window.innerHeight;
  const ctx = canvas.getContext('2d');
  if (!ctx) return '';

  // The panel's own background, so the area around the grid is not transparent
  // black. Only the grid is ever looked at, but a solver handed a black margin
  // is a solver being told something untrue about the picture.
  ctx.fillStyle = getComputedStyle(document.body).backgroundColor || '#fff';
  ctx.fillRect(0, 0, canvas.width, canvas.height);

  let drawn = 0;
  for (const tile of tiles) {
    const box = tile.getBoundingClientRect();
    if (box.width <= 0 || box.height <= 0) continue;

    const img = tile.querySelector('img');
    if (!img || !img.complete || !img.naturalWidth) continue;

    const at = img.getBoundingClientRect();
    ctx.save();
    ctx.beginPath();
    ctx.rect(box.x, box.y, box.width, box.height);
    ctx.clip();
    try {
      ctx.drawImage(img, at.x, at.y, at.width, at.height);
      drawn++;
    } catch (e) {
      // A picture that will not draw is one tile short, not a reason to throw
      // away the other fifteen. If enough of them fail the count below rejects
      // the whole thing anyway.
    }
    ctx.restore();
  }
  if (!drawn || drawn * 2 < tiles.length) return '';

  try {
    return canvas.toDataURL('image/png');
  } catch (e) {
    // Tainted: the pictures came from somewhere this document may not read
    // back. Nothing to be done from here.
    return '';
  }
})()`

// draw asks the page to build the panel picture and returns it as PNG bytes.
//
// An empty result is not an error. It means this panel cannot be drawn — no
// tiles yet, pictures still arriving, or a canvas the document is not allowed
// to read back — and the caller falls back to photographing the screen.
func draw(ctx context.Context, frame *Frame) ([]byte, error) {
	var encoded string

	err := chromedp.Run(frame.runCtx, chromedp.ActionFunc(func(fctx context.Context) error {
		result, exception, err := runtime.Evaluate(drawScript).
			WithContextID(frame.world).
			WithReturnByValue(true).
			Do(fctx)
		if err != nil {
			return err
		}
		if exception != nil {
			return fmt.Errorf("%s", exception.Text)
		}
		return json.Unmarshal(result.Value, &encoded)
	}))
	if stale(err) {
		// The panel went away underneath us, which is not this function's
		// problem to report: the caller rereads and finds it closed.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("challenge: draw panel: %w", err)
	}

	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(encoded, prefix) {
		return nil, nil
	}
	shot, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, prefix))
	if err != nil {
		return nil, fmt.Errorf("challenge: decode drawn panel: %w", err)
	}
	return shot, nil
}
