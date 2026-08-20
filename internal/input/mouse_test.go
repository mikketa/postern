package input

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// recorder is a page that writes down every pointer event it receives, which
// is the only way to check a gesture from the outside: what matters is not
// where the pointer ended up but what the page was told along the way.
const recorder = `<!doctype html><title>r</title>
<body style="margin:0;width:100vw;height:100vh">
<script>
  window.events = [];
  for (const name of ['mousedown', 'mousemove', 'mouseup']) {
    document.addEventListener(name, (e) => {
      window.events.push({ type: name, x: e.clientX, y: e.clientY, buttons: e.buttons });
    });
  }
</script>
</body>`

type event struct {
	Type    string  `json:"type"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Buttons int     `json:"buttons"`
}

// TestADragIsCarriedAndNotTeleported is the whole point of Drag.
//
// A page watching for a drag watches the moves. A pointer that presses at one
// end, jumps, and releases at the other has pressed and released — it has not
// dragged, and a slider written to follow the gesture never sees one.
func TestADragIsCarriedAndNotTeleported(t *testing.T) {
	ctx := recordingPage(t)

	from := Point{X: 40, Y: 300}
	start := Point{X: 100, Y: 400}
	end := Point{X: 420, Y: 400}

	if err := chromedp.Run(ctx, Drag(from, start, end)); err != nil {
		t.Fatalf("Drag: %v", err)
	}

	events := collect(t, ctx)
	if len(events) == 0 {
		t.Fatal("the page received no pointer events at all")
	}

	var down, up *event
	var heldMoves []event
	for i := range events {
		switch events[i].Type {
		case "mousedown":
			if down == nil {
				down = &events[i]
			}
		case "mouseup":
			up = &events[i]
		case "mousemove":
			// Only the moves between the press and the release count: the
			// approach to the handle is an ordinary movement.
			if down != nil && up == nil && events[i].Buttons == 1 {
				heldMoves = append(heldMoves, events[i])
			}
		}
	}

	if down == nil || up == nil {
		t.Fatalf("no press/release pair: got %d events", len(events))
	}
	if !near(down.X, start.X) || !near(down.Y, start.Y) {
		t.Errorf("pressed at %.0f,%.0f, want the handle at %.0f,%.0f",
			down.X, down.Y, start.X, start.Y)
	}
	if !near(up.X, end.X) || !near(up.Y, end.Y) {
		t.Errorf("released at %.0f,%.0f, want %.0f,%.0f", up.X, up.Y, end.X, end.Y)
	}

	// The number is deliberately low: what is being ruled out is a teleport,
	// not a particular pacing.
	if len(heldMoves) < 5 {
		t.Fatalf("only %d move(s) arrived with the button held — a slider "+
			"following the gesture would never see it move", len(heldMoves))
	}

	// And they have to actually cross the distance rather than sit still.
	spread := math.Abs(heldMoves[len(heldMoves)-1].X - heldMoves[0].X)
	if want := math.Abs(end.X-start.X) * 0.5; spread < want {
		t.Errorf("the held moves covered %.0fpx of the %.0fpx drag", spread, end.X-start.X)
	}
}

// TestAPlainMoveDoesNotClaimAButton guards the other direction: Move and the
// approach inside Drag must not report a pressed button, or every page would
// see the pointer dragging everywhere it goes.
func TestAPlainMoveDoesNotClaimAButton(t *testing.T) {
	ctx := recordingPage(t)

	if err := chromedp.Run(ctx, Move(Point{X: 20, Y: 20}, Point{X: 300, Y: 250})); err != nil {
		t.Fatalf("Move: %v", err)
	}

	for _, e := range collect(t, ctx) {
		if e.Type == "mousemove" && e.Buttons != 0 {
			t.Fatalf("a plain move reported buttons=%d", e.Buttons)
		}
	}
}

func near(got, want float64) bool { return math.Abs(got-want) <= 2 }

func collect(t *testing.T, ctx context.Context) []event {
	t.Helper()
	var raw json.RawMessage
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.events`, &raw)); err != nil {
		t.Fatalf("reading the events back: %v", err)
	}
	var events []event
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatalf("decoding events: %v", err)
	}
	return events
}

func recordingPage(t *testing.T) context.Context {
	t.Helper()

	if testing.Short() {
		t.Skip("launches a browser")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, recorder)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.Flag("headless", true))

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	t.Cleanup(cancelAlloc)
	tabCtx, cancelTab := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelTab)

	if err := chromedp.Run(tabCtx, chromedp.Navigate(server.URL)); err != nil {
		t.Skipf("no browser to test against: %v", err)
	}
	return tabCtx
}
