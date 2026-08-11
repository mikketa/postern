package challenge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// frameMarker identifies the challenge document among the page's frames.
// reCAPTCHA renders several: an anchor frame per widget, and a bframe that is
// empty until a challenge is actually served.
const frameMarker = "/recaptcha/api2/bframe"

// minPanelHeight is the height below which the frame is not showing a
// challenge. reCAPTCHA does not remove the frame when the panel closes, it
// collapses it — and a frame that still lists its tiles while occupying 150
// pixels somewhere off to one side is a challenge that is over. Photographing
// it anyway gets a picture of whatever the page has there instead, which is
// what a solver would then be asked to find buses in.
const minPanelHeight = 250

// attachTimeout bounds reaching a frame in another process. Attaching is
// normally instant; when it is not, the solve loop must not be held up by it.
const attachTimeout = 8 * time.Second

// readScript asks the challenge document to describe itself. Reading rather
// than acting: nothing here clicks, focuses or dispatches anything, so the
// widget sees an ordinary page it was never touched by script. Every click
// still travels the long way round, through the real pointer.
//
// Measuring from the inside is the difference between knowing the layout and
// guessing it. From the outside the panel is an opaque rectangle whose grid
// could be 3x3 or 4x4, whose prompt has to be read back out of a screenshot by
// OCR, and whose buttons can only be aimed at by assuming where they sit.
const readScript = `(() => {
  const rect = el => {
    if (!el) return null;
    const r = el.getBoundingClientRect();
    return { x: r.x, y: r.y, w: r.width, h: r.height };
  };

  const desc = document.querySelector(
    '.rc-imageselect-desc-no-canonical, .rc-imageselect-desc, .rc-imageselect-desc-wrapper');

  // The panel says why it refused, when it refuses: "please try again",
  // "select all matching images", "keep clicking until none are left". Only
  // one is ever on screen, and only when it applies.
  const notice = ['.rc-imageselect-incorrect-response',
                  '.rc-imageselect-error-select-more',
                  '.rc-imageselect-error-dynamic-more',
                  '.rc-imageselect-error-select-something']
    .map(sel => document.querySelector(sel))
    .filter(el => el && getComputedStyle(el).display !== 'none')
    .map(el => el.innerText.replace(/\s+/g, ' ').trim())[0] || '';
  const verify = document.querySelector('#recaptcha-verify-button');
  const reload = document.querySelector('#recaptcha-reload-button');

  return JSON.stringify({
    url: location.href,
    docHeight: document.body.scrollHeight,
    prompt: desc ? desc.innerText.replace(/\s+/g, ' ').trim() : '',
    tiles: [...document.querySelectorAll('.rc-imageselect-tile')].map(el => {
      const img = el.querySelector('img');
      return {
        ...rect(el),
        selected: !!el.querySelector('.rc-imageselect-tileselected') ||
                  el.classList.contains('rc-imageselect-tileselected'),
        src: img ? img.src : '',
      };
    }),
    button: verify ? {
      ...rect(verify),
      label: verify.innerText.trim(),
      // Whether a click would actually land on it. Geometry alone says yes for
      // a button reCAPTCHA has laid out below a container that clips it: the
      // rectangle is real, the pixels are not, and the click goes to whatever
      // is painted there instead. Asking the document what is at that point is
      // the difference between where the button is and where it can be hit.
      hittable: (() => {
        const r = verify.getBoundingClientRect();
        if (r.width <= 0 || r.height <= 0) return false;

        const at = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
        // The button itself, or something drawn inside it. An *ancestor* means
        // the click would land on the container instead — which is exactly the
        // clipped case, and the one worth catching.
        return !!at && (at === verify || verify.contains(at));
      })(),
    } : null,
    notice,
    reload: rect(reload),
    settling: !!document.querySelector('.rc-imageselect-dynamic-selected'),
    loading: [...document.querySelectorAll('.rc-imageselect-tile img')]
      .some(img => !img.complete || img.naturalWidth === 0),
    width: window.innerWidth,
    height: window.innerHeight,
  });
})()`

// openPanelScript reports whether any challenge frame in the host page is
// actually deployed. reCAPTCHA keeps a collapsed one around for every widget —
// 300x150, parked out of the way — and grows it only when a challenge is
// served.
//
// It exists to keep the expensive route cheap. Reaching a cross-origin frame
// means attaching to its target, and the solve loop asks for the panel five
// times a second: attaching, reading and detaching at that rate is both slow
// and a good way to end up waiting on a target that is already attached. This
// question can be answered from the host page for nothing, and answers "no"
// almost every time.
const openPanelScript = `(() => {
  for (const frame of document.querySelectorAll('iframe')) {
    if (!(frame.src || '').includes('%s')) continue;

    const r = frame.getBoundingClientRect();
    if (r.width > 100 && r.height > %d) return true;
  }
  return false;
})()`

// hostFrameScript finds where a frame's element sits in the page it belongs to.
// Asked of the host document because the frame itself cannot know: a document
// has no way of seeing where it was embedded.
const hostFrameScript = `(url => {
  const frames = [...document.querySelectorAll('iframe')];
  const frame = frames.find(f => f.src === url) ||
                frames.find(f => (f.src || '').includes('%s'));
  if (!frame) return null;

  const r = frame.getBoundingClientRect();
  return { x: r.x, y: r.y };
})(%q)`

// Box is a rectangle in the challenge document's own coordinates, which are
// also the coordinates of the panel screenshot.
type Box struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`

	// Selected reports whether a tile is already ticked, and Label carries the
	// button's wording — it reads "Vérifier", "Ignorer" or "Suivant" depending
	// on the round, which is how the challenge says what it expects next.
	Selected bool   `json:"selected"`
	Label    string `json:"label"`

	// Src is the tile's picture. A dynamic challenge answers a correct click by
	// swapping in a different one, so comparing these across a click is how
	// postern knows the round is not over.
	Src string `json:"src"`

	// Hittable reports that a click at this box's centre would reach it.
	Hittable bool `json:"hittable"`
}

// Center is the middle of the box.
func (b Box) Center() (float64, float64) {
	return b.X + b.W/2, b.Y + b.H/2
}

// View is what the challenge document reports about itself.
type View struct {
	// URL is the frame's own address, which is how the element holding it is
	// picked out of the host page.
	URL string `json:"url"`

	// DocHeight is what the document inside needs. It is not always what the
	// frame gives it: reCAPTCHA sizes the panel to the room around the widget
	// and lets the rest overflow, which is how the verify button ends up laid
	// out where nothing paints it. Widening the frame does not recover it —
	// the clipping is done by a container inside the document, not by the
	// frame — so this is kept for the log rather than acted on.
	DocHeight float64 `json:"docHeight"`

	Prompt string  `json:"prompt"`
	Tiles  []Box   `json:"tiles"`
	Button *Box    `json:"button"`
	Reload *Box    `json:"reload"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`

	// Notice is the panel's own complaint about the last answer, when it has
	// one. It is the only feedback reCAPTCHA gives, and it distinguishes a
	// wrong answer from an incomplete one from a fresh round.
	Notice string `json:"notice"`

	// Settling is true while a tile is fading out to be replaced, and Loading
	// while a fresh picture is still on its way. Photographing the grid then
	// gets a half-dissolved picture nobody can classify, or a blank one — which
	// is exactly what a solver was once asked to find cars in.
	Settling bool `json:"settling"`
	Loading  bool `json:"loading"`
}

// Columns is the width of the tile grid. reCAPTCHA serves a 3x3 grid of
// separate photographs and a 4x4 grid laid over a single picture; both are
// square, so the count settles it without having to read the wording.
func (v View) Columns() int {
	switch len(v.Tiles) {
	case 9:
		return 3
	case 16:
		return 4
	default:
		return 0
	}
}

// Pictures lists the tiles' images, for telling one round from the next.
func (v View) Pictures() []string {
	srcs := make([]string, len(v.Tiles))
	for i, t := range v.Tiles {
		srcs[i] = t.Src
	}
	return srcs
}

// Frame is a challenge panel, addressed from the inside.
type Frame struct {
	// runCtx is where evaluation happens. For a frame that shares a process
	// with the page that is the page's own context; for one that does not it is
	// a context attached to the frame's own target.
	runCtx context.Context
	world  runtime.ExecutionContextID

	// locate re-measures where the frame sits, because it moves: reCAPTCHA
	// slides the panel in and out and a position taken one round ago is not
	// where the tiles are now.
	locate func(context.Context) (float64, float64, error)

	// OriginX, OriginY place the frame's top-left corner in the viewport, so
	// that a box read from inside can be aimed at from outside.
	OriginX, OriginY float64

	// View is the reading taken when the frame was last measured.
	View View
}

// Viewport is the frame's box in viewport coordinates: what to screenshot.
func (f *Frame) Viewport() (x, y, w, h float64) {
	return f.OriginX, f.OriginY, f.View.Width, f.View.Height
}

// Point maps a position inside the challenge document onto the viewport.
func (f *Frame) Point(x, y float64) (float64, float64) {
	return f.OriginX + x, f.OriginY + y
}

// Finder locates challenge panels in one tab, and remembers what it had to
// attach to in order to do it.
//
// The remembering is not an optimisation. Reaching a frame in another process
// means attaching to its target, and chromedp tears an attachment down by
// closing the target — which for a frame means closing the page that holds it.
// Attaching and detaching on each poll would therefore shut the tab, five times
// a second, on any site but Google's own. So an attachment is made once and
// kept for the life of the tab, and released only when the tab itself goes: the
// contexts descend from it, and closing the tab takes them with it.
type Finder struct {
	attached map[target.ID]*attachment
}

// attachment is a session inside a frame that lives in its own process.
type attachment struct {
	ctx   context.Context
	world runtime.ExecutionContextID
}

// NewFinder returns a finder for one tab. It must not outlive that tab.
func NewFinder() *Finder {
	return &Finder{attached: make(map[target.ID]*attachment)}
}

// Find returns the challenge panel, or nil when no challenge is up. A bframe
// with no tiles in it is not a challenge: reCAPTCHA keeps one around for every
// widget and flashes it open during ordinary verifications too.
//
// The panel is looked for twice over, because where it lives depends on whose
// site postern was pointed at. Chrome runs a cross-site iframe in a process of
// its own, and such a frame is invisible to the page's own session — it shows
// up as an empty about:blank in the frame tree and as a separate target in the
// browser. On Google's own demo the frame is same-site and only the first route
// finds it; on everybody else's site only the second one does. Postern has to
// work on everybody else's site.
func (f *Finder) Find(ctx context.Context) (*Frame, error) {
	frame, err := findLocal(ctx)
	if err != nil {
		return nil, fmt.Errorf("challenge: find panel: %w", err)
	}
	if frame != nil {
		return frame, nil
	}

	// Nothing in this session's own frames. Before going the long way round,
	// ask the page whether a panel is deployed at all — a question it answers
	// for nothing, and answers "no" almost every time.
	open, err := panelIsOpen(ctx)
	if err != nil || !open {
		return nil, err
	}

	frame, err = f.findAttached(ctx)
	if err != nil {
		// An attachment that times out costs one poll; treating it as a failure
		// would cost the run.
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("challenge: find panel: %w", err)
	}
	return frame, nil
}

// panelIsOpen reports whether the host page is showing a deployed challenge
// frame.
func panelIsOpen(ctx context.Context) (bool, error) {
	var open bool

	script := fmt.Sprintf(openPanelScript, frameMarker, int(minPanelHeight))
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &open)); err != nil {
		// Mid-navigation, or no document yet. Not a panel, and not a failure.
		return false, nil
	}
	return open, nil
}

// findLocal looks through the frames the page's own session can see.
func findLocal(outer context.Context) (*Frame, error) {
	var found *Frame

	err := chromedp.Run(outer, chromedp.ActionFunc(func(ctx context.Context) error {
		tree, err := page.GetFrameTree().Do(ctx)
		if err != nil {
			return err
		}

		for _, id := range challengeFrames(tree) {
			world, err := page.CreateIsolatedWorld(id).WithWorldName("postern").Do(ctx)
			if err != nil {
				continue
			}

			view, err := readWorld(ctx, world)
			if err != nil || len(view.Tiles) == 0 || view.Height < minPanelHeight {
				continue
			}

			locate := func(ctx context.Context) (x, y float64, err error) {
				err = chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
					x, y, err = ownerPosition(ctx, id)
					return err
				}))
				return x, y, err
			}

			x, y, err := ownerPosition(ctx, id)
			if err != nil {
				continue
			}
			found = &Frame{runCtx: outer, world: world, locate: locate,
				OriginX: x, OriginY: y, View: view}
			return nil
		}
		return nil
	}))
	return found, err
}

// findAttached looks through the browser's targets, which is where a frame in
// a process of its own turns up.
func (f *Finder) findAttached(ctx context.Context) (*Frame, error) {
	var candidates []*target.Info

	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		list, err := target.GetTargets().Do(ctx)
		if err != nil {
			return err
		}
		for _, info := range list {
			if info.Type == "iframe" && strings.Contains(info.URL, frameMarker) {
				candidates = append(candidates, info)
			}
		}
		return nil
	}))
	if err != nil {
		return nil, err
	}

	for _, info := range candidates {
		session, err := f.attach(ctx, info.TargetID)
		if err != nil {
			return nil, err
		}

		view, err := readIn(ctx, session)
		if err != nil {
			// The world goes stale when the frame navigates, which it does
			// between challenges. Drop it and let the next poll make another.
			delete(f.attached, info.TargetID)
			continue
		}
		if len(view.Tiles) == 0 || view.Height < minPanelHeight {
			continue
		}

		url := info.URL
		locate := func(ctx context.Context) (float64, float64, error) {
			return hostPosition(ctx, url)
		}

		x, y, err := locate(ctx)
		if err != nil {
			continue
		}
		return &Frame{runCtx: session.ctx, world: session.world, locate: locate,
			OriginX: x, OriginY: y, View: view}, nil
	}
	return nil, nil
}

// attach opens a session inside a frame's own target, or returns the one it
// already has. The context it builds is never cancelled here — see Finder.
func (f *Finder) attach(ctx context.Context, id target.ID) (*attachment, error) {
	if session, ok := f.attached[id]; ok {
		return session, nil
	}

	// chromedp.NewContext hands back a cancel that closes the target, which for
	// a frame closes the page. It is deliberately dropped: this context is
	// released by the tab context it descends from.
	frameCtx, _ := chromedp.NewContext(ctx, chromedp.WithTargetID(id))

	// Bounded, because an attachment that hangs must not hold up the solve. The
	// deadline applies to getting attached, not to the session afterwards.
	deadline, stop := context.WithTimeout(frameCtx, attachTimeout)
	defer stop()

	var world runtime.ExecutionContextID
	err := chromedp.Run(deadline, chromedp.ActionFunc(func(fctx context.Context) error {
		tree, err := page.GetFrameTree().Do(fctx)
		if err != nil {
			return err
		}

		world, err = page.CreateIsolatedWorld(tree.Frame.ID).WithWorldName("postern").Do(fctx)
		return err
	}))
	if err != nil {
		return nil, err
	}

	session := &attachment{ctx: frameCtx, world: world}
	f.attached[id] = session
	return session, nil
}

// readIn takes a measurement through an attached session.
func readIn(ctx context.Context, session *attachment) (View, error) {
	var view View

	err := chromedp.Run(session.ctx, chromedp.ActionFunc(func(fctx context.Context) error {
		var err error
		view, err = readWorld(fctx, session.world)
		return err
	}))
	return view, err
}

// Open reports whether the panel is still showing a challenge.
func (f *Frame) Open() bool {
	return len(f.View.Tiles) > 0 && f.View.Height >= minPanelHeight
}

// focusScript puts the keyboard on an element inside the challenge.
const focusScript = `(() => {
  const el = document.querySelector(%q);
  if (!el) return false;
  el.focus();
  return document.activeElement === el;
})()`

// Focus puts the keyboard on the panel's button. Focusing is not clicking: it
// moves the caret, it does not fire the activation, so what follows is still a
// real keystroke from the browser rather than an event made up by script.
func (f *Frame) Focus(selector string) (bool, error) {
	var focused bool

	err := chromedp.Run(f.runCtx, chromedp.ActionFunc(func(fctx context.Context) error {
		result, exception, err := runtime.Evaluate(fmt.Sprintf(focusScript, selector)).
			WithContextID(f.world).
			WithReturnByValue(true).
			Do(fctx)
		if err != nil {
			return err
		}
		if exception != nil {
			return fmt.Errorf("%s", exception.Text)
		}
		return json.Unmarshal(result.Value, &focused)
	}))
	if err != nil {
		return false, fmt.Errorf("challenge: focus %s: %w", selector, err)
	}
	return focused, nil
}

// Reread takes a fresh measurement of the same frame, for after a click has
// changed what it shows or moved it.
func (f *Frame) Reread(ctx context.Context) error {
	var view View

	err := chromedp.Run(f.runCtx, chromedp.ActionFunc(func(fctx context.Context) error {
		var err error
		view, err = readWorld(fctx, f.world)
		return err
	}))
	if err != nil {
		return fmt.Errorf("challenge: reread panel: %w", err)
	}
	f.View = view

	if !f.Open() {
		return nil
	}

	x, y, err := f.locate(ctx)
	if err != nil {
		return fmt.Errorf("challenge: relocate panel: %w", err)
	}
	f.OriginX, f.OriginY = x, y
	return nil
}

// challengeFrames returns the candidate challenge frames in a tree.
func challengeFrames(node *page.FrameTree) []cdp.FrameID {
	var ids []cdp.FrameID
	if strings.Contains(node.Frame.URL, frameMarker) {
		ids = append(ids, node.Frame.ID)
	}
	for _, child := range node.ChildFrames {
		ids = append(ids, challengeFrames(child)...)
	}
	return ids
}

// readWorld takes one measurement inside an isolated world.
func readWorld(ctx context.Context, world runtime.ExecutionContextID) (View, error) {
	var view View

	result, exception, err := runtime.Evaluate(readScript).
		WithContextID(world).
		WithReturnByValue(true).
		Do(ctx)
	if err != nil {
		return view, err
	}
	if exception != nil {
		return view, fmt.Errorf("read panel: %s", exception.Text)
	}

	var encoded string
	if err := json.Unmarshal(result.Value, &encoded); err != nil {
		return view, err
	}
	return view, json.Unmarshal([]byte(encoded), &view)
}

// ownerPosition asks the DOM where a frame's element sits. Only usable for a
// frame the page's session owns; a frame in another process has no node here.
func ownerPosition(ctx context.Context, id cdp.FrameID) (float64, float64, error) {
	backendID, _, err := dom.GetFrameOwner(id).Do(ctx)
	if err != nil {
		return 0, 0, err
	}

	model, err := dom.GetBoxModel().WithBackendNodeID(backendID).Do(ctx)
	if err != nil {
		return 0, 0, err
	}
	if len(model.Content) < 2 {
		return 0, 0, fmt.Errorf("frame owner has no box")
	}
	return model.Content[0], model.Content[1], nil
}

// hostPosition finds the frame's element in the host page by its address.
func hostPosition(ctx context.Context, url string) (float64, float64, error) {
	var box *Box

	script := fmt.Sprintf(hostFrameScript, frameMarker, url)
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &box)); err != nil {
		return 0, 0, err
	}
	if box == nil {
		return 0, 0, fmt.Errorf("no element in the page holds %s", frameMarker)
	}
	return box.X, box.Y, nil
}
