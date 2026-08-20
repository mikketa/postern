// Package input synthesises pointer activity through the DevTools protocol.
//
// Events dispatched here travel through Chrome's real input pipeline, so the
// page receives them with isTrusted set — unlike anything dispatched from page
// JavaScript, which any challenge filters out in one line.
//
// Trusted is necessary but not sufficient: a pointer that teleports onto a
// checkbox in a single event and clicks with zero dwell time is still an
// obvious machine. Hence the curve, the jitter and the pauses below.
package input

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

// dispatchTimeout bounds the wait for Chrome to acknowledge one input event.
//
// The event is delivered when the command is sent. The reply comes back only
// once the renderer under the pointer has processed it, and a renderer busy
// with a challenge — or one drawing to a virtual display — takes its time:
// measured at 43 seconds for a single click on a page with one link on it,
// while that same page answered every other command instantly.
//
// A solve is dozens of clicks along a path of dozens of moves, so waiting on
// each acknowledgement is the difference between three seconds and three
// minutes. Nothing is gained by it: what postern needs to know about the click
// is whether the page changed, which it reads from the page afterwards.
//
// Twenty-five, and lowering it is not worth trying again. Dispatched against a
// local page an acknowledgement measures 16.7ms median over sixty events —
// suspiciously exactly a frame at 60Hz — which suggested a dozen events to a
// click were each being paid for at one frame. They are not: dropped to 8ms and
// measured over eight solves, a click cost 901-1055ms against 906-949ms before,
// which is no difference at all. Whatever that 16.7ms is, the solve does not
// pay it, and the bound here is only ever reached by a renderer in trouble.
const dispatchTimeout = 25 * time.Millisecond

// send dispatches one input event without waiting indefinitely for the reply.
func send(ctx context.Context, action chromedp.Action) error {
	bounded, cancel := context.WithTimeout(ctx, dispatchTimeout)
	defer cancel()

	err := action.Do(bounded)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		// Sent, not acknowledged. That is the busy renderer, not a failure.
		return nil
	}
	return err
}

// Point is a viewport coordinate, in CSS pixels.
type Point struct {
	X, Y float64
}

// Click walks the pointer from one point to the other, then presses and
// releases the left button where it lands.
func Click(from, to Point) chromedp.ActionFunc {
	return func(ctx context.Context) error {
		if err := move(ctx, from, to, false); err != nil {
			return err
		}

		// People do not click the instant the pointer stops.
		if err := Pause(ctx, 70, 160); err != nil {
			return err
		}

		press := input.DispatchMouseEvent(input.MousePressed, to.X, to.Y).
			WithButton(input.Left).
			WithClickCount(1)
		if err := send(ctx, press); err != nil {
			return err
		}

		// Dwell time between press and release.
		if err := Pause(ctx, 45, 110); err != nil {
			return err
		}

		release := input.DispatchMouseEvent(input.MouseReleased, to.X, to.Y).
			WithButton(input.Left).
			WithClickCount(1)
		return send(ctx, release)
	}
}

// Move walks the pointer from one point to the other without clicking.
//
// It is what a person does before deciding, and it is also the only thing that
// reliably gets a page repainted: under a virtual display nothing composites a
// new frame while the page sits idle, so an animation that was halfway through
// stays halfway through — and a screenshot taken then shows a panel still
// fading in over the page behind it.
func Move(from, to Point) chromedp.ActionFunc {
	return func(ctx context.Context) error {
		return move(ctx, from, to, false)
	}
}

// Drag walks the pointer to start, presses the left button, carries it to end,
// and releases it there.
//
// This is the shape a slider wants — hCaptcha's drag tasks, GeeTest's puzzle
// piece — and the reason it is not a click with a move in the middle is that
// the button state has to travel with every event along the way. A page
// listening for dragging watches the moves, not the endpoints: a pointer that
// presses, teleports, and releases has not dragged anything.
//
// from is where the pointer is now, so the approach to the handle is itself a
// movement rather than a jump onto it.
func Drag(from, start, end Point) chromedp.ActionFunc {
	return func(ctx context.Context) error {
		if err := move(ctx, from, start, false); err != nil {
			return err
		}

		// Settling on the handle before taking hold of it.
		if err := Pause(ctx, 70, 160); err != nil {
			return err
		}

		press := input.DispatchMouseEvent(input.MousePressed, start.X, start.Y).
			WithButton(input.Left).
			WithClickCount(1)
		if err := send(ctx, press); err != nil {
			return err
		}

		// A hand does not start moving the instant it grips.
		if err := Pause(ctx, 60, 130); err != nil {
			return err
		}

		if err := move(ctx, start, end, true); err != nil {
			return err
		}

		// And it does not let go the instant it stops, either. This pause is
		// the one a slider is most likely to be watching: released at the same
		// millisecond it arrives, the gesture has no landing.
		if err := Pause(ctx, 90, 200); err != nil {
			return err
		}

		release := input.DispatchMouseEvent(input.MouseReleased, end.X, end.Y).
			WithButton(input.Left).
			WithClickCount(1)
		return send(ctx, release)
	}
}

// move traces a curved, unevenly paced path between two points.
//
// held says the left button is down for the whole path. It has to be carried
// on every event: a mousemove that does not say which button is pressed is a
// hover, and a page watching for a drag sees the pointer arrive without ever
// having been dragged.
func move(ctx context.Context, from, to Point, held bool) error {
	distance := math.Hypot(to.X-from.X, to.Y-from.Y)

	// Roughly one event per dozen pixels, clamped so that both a nudge and a
	// cross-screen sweep stay plausible.
	steps := int(math.Round(distance / 12))
	steps = min(max(steps, 10), 40)

	control := controlPoint(from, to, distance)

	for i := 1; i <= steps; i++ {
		progress := ease(float64(i) / float64(steps))
		p := bezier(from, control, to, progress)

		// Hand tremor, except on the final event: the pointer has to end up
		// exactly where we intend to click.
		if i < steps {
			p.X += (rand.Float64() - 0.5) * 1.4
			p.Y += (rand.Float64() - 0.5) * 1.4
		}

		event := input.DispatchMouseEvent(input.MouseMoved, p.X, p.Y)
		if held {
			event = event.WithButton(input.Left).WithButtons(1)
		}
		if err := send(ctx, event); err != nil {
			return err
		}
		// A dragged pointer is slower than a free one: the hand is holding
		// something rather than travelling to it.
		if held {
			if err := Pause(ctx, 12, 34); err != nil {
				return err
			}
			continue
		}
		if err := Pause(ctx, 6, 20); err != nil {
			return err
		}
	}
	return nil
}

// controlPoint offsets the midpoint perpendicular to the path, which is what
// turns a straight line into the slight arc a hand actually makes. The side it
// bows to is random so repeated solves do not trace the same shape.
func controlPoint(from, to Point, distance float64) Point {
	midX := (from.X + to.X) / 2
	midY := (from.Y + to.Y) / 2

	if distance == 0 {
		return Point{X: midX, Y: midY}
	}

	// Unit normal of the segment.
	nx := -(to.Y - from.Y) / distance
	ny := (to.X - from.X) / distance

	bow := distance * (0.05 + rand.Float64()*0.12)
	if rand.IntN(2) == 0 {
		bow = -bow
	}

	return Point{X: midX + nx*bow, Y: midY + ny*bow}
}

// bezier evaluates a quadratic curve at t.
func bezier(p0, p1, p2 Point, t float64) Point {
	u := 1 - t
	return Point{
		X: u*u*p0.X + 2*u*t*p1.X + t*t*p2.X,
		Y: u*u*p0.Y + 2*u*t*p1.Y + t*t*p2.Y,
	}
}

// ease is a cubic ease-in-out: the pointer accelerates away, then settles onto
// the target instead of arriving at full speed.
func ease(t float64) float64 {
	if t < 0.5 {
		return 4 * t * t * t
	}
	return 1 - math.Pow(-2*t+2, 3)/2
}

// Pause sleeps for a random duration in [minMS, maxMS], honouring ctx. It is
// exported because pacing is not only a mouse concern: anything that acts on a
// page in several steps needs the same irregular gaps between them.
func Pause(ctx context.Context, minMS, maxMS int) error {
	d := time.Duration(minMS+rand.IntN(maxMS-minMS+1)) * time.Millisecond

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// PressEnter sends an Enter keystroke to whatever currently has focus.
//
// It is the way in when the pointer cannot get there: reCAPTCHA lays its panel
// out taller than the frame it is given, so the buttons underneath are never
// painted and no coordinate on screen lands on them. A focused button still
// answers the keyboard.
func PressEnter(ctx context.Context) error {
	for _, kind := range []input.KeyType{input.KeyRawDown, input.KeyChar, input.KeyUp} {
		ev := input.DispatchKeyEvent(kind).
			WithKey("Enter").
			WithCode("Enter").
			WithWindowsVirtualKeyCode(13).
			WithNativeVirtualKeyCode(13)

		if kind == input.KeyChar {
			ev = ev.WithText("\r")
		}
		if err := send(ctx, ev); err != nil {
			return err
		}
		if err := Pause(ctx, 20, 60); err != nil {
			return err
		}
	}
	return nil
}
