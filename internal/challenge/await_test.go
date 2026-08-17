package challenge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// A one-pixel gif, twice over, so a tile can be given a different picture
// without fetching anything.
const (
	firstPicture  = "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"
	secondPicture = "data:image/gif;base64,R0lGODlhAQABAIAAAP///wAAACH5BAEAAAAALAAAAAABAAEAAAIBRAA7"
)

// panelPage embeds a stand-in for the challenge document, laid out like the
// real one: the classes below are the ones postern reads the panel through.
const panelPage = `<!doctype html>
<body style="margin:0">
<iframe src="%s" style="position:absolute;left:112px;top:20px"
        width="400" height="580"></iframe>
</body>`

// staticPanel is a grid that will never change: two pictures, no fade, nothing
// on its way. reCAPTCHA serves this and postern used to wait four and a half
// seconds at it before submitting.
const staticPanel = `<!doctype html>
<body style="margin:0">
<div class="rc-imageselect-desc">Sélectionnez toutes les images montrant des ponts</div>
<table class="rc-imageselect-table-33">
  <tr><td class="rc-imageselect-tile"><img src="%[1]s"></td>
      <td class="rc-imageselect-tile"><img src="%[1]s"></td></tr>
</table>
<button id="recaptcha-verify-button">Valider</button>
</body>`

// dynamicPanel is the other kind: a tile is marked for replacement shortly
// after the click, and the fresh picture arrives well after the grace period
// has run out. Getting this one wrong submits a half-answer.
const dynamicPanel = `<!doctype html>
<body style="margin:0">
<div class="rc-imageselect-desc">Sélectionnez toutes les images montrant des bus</div>
<table class="rc-imageselect-table-33">
  <tr><td class="rc-imageselect-tile" id="first"><img src="%[1]s"></td>
      <td class="rc-imageselect-tile"><img src="%[1]s"></td></tr>
</table>
<button id="recaptcha-verify-button">Valider</button>
<script>
  // The class reCAPTCHA fades a doomed tile out with, and the only sign on
  // screen that a replacement is coming.
  setTimeout(() => document.getElementById('first')
    .classList.add('rc-imageselect-dynamic-selected'), 500);
  setTimeout(() => {
    document.getElementById('first').classList.remove('rc-imageselect-dynamic-selected');
    document.querySelector('#first img').src = %[2]q;
  }, 2500);
</script>
</body>`

// TestAwaitGivesUpOnAGridThatIsNotChanging is the measurement that motivated
// the grace: every wait for a replacement in eight consecutive solves ran the
// whole budget out, and not one of them was ever going to produce anything.
func TestAwaitGivesUpOnAGridThatIsNotChanging(t *testing.T) {
	frame, ctx := panelFrame(t, staticPanel)

	before := frame.View.Pictures()
	start := time.Now()
	replaced, err := await(ctx, frame, before, replaceBudget, replaceGrace, newTimings())
	took := time.Since(start)

	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if replaced {
		t.Error("replaced = true on a grid where nothing changed")
	}
	// The grace, plus room for the polls that notice it has passed — the
	// budget is 4.5s, so there is no reading of this that passes by accident.
	if limit := replaceGrace + 5*pollWait; took > limit {
		t.Errorf("waited %s at a grid doing nothing, want under %s", took.Round(time.Millisecond), limit)
	}
}

// TestAwaitWaitsOutAGridThatHasStarted is the other half, and the one that
// keeps the grace honest: a tile marked for replacement is a change under way,
// and calling the wait off at the grace would submit an answer to a challenge
// still asking.
func TestAwaitWaitsOutAGridThatHasStarted(t *testing.T) {
	frame, ctx := panelFrame(t, dynamicPanel)

	before := frame.View.Pictures()
	start := time.Now()
	replaced, err := await(ctx, frame, before, replaceBudget, replaceGrace, newTimings())
	took := time.Since(start)

	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if !replaced {
		t.Fatal("replaced = false, but the tile was swapped inside the budget")
	}
	if took <= replaceGrace {
		t.Errorf("returned after %s, at or before the grace: the fresh picture only "+
			"arrives at 2.5s, so this cannot have seen it", took.Round(time.Millisecond))
	}
}

// panelFrame serves one of the panels above inside a page and hands back the
// frame postern would have found, taken through the same code path.
func panelFrame(t *testing.T, panel string) (*Frame, context.Context) {
	t.Helper()

	if testing.Short() {
		t.Skip("launches a browser")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	// Postern picks the challenge document out of the frame tree by its path,
	// so the stand-in has to answer on one.
	mux.HandleFunc(frameMarker, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, panel, firstPicture, secondPicture)
	})
	mux.HandleFunc("/host", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, panelPage, server.URL+frameMarker+"?k=test")
	})

	tabCtx, closeTab := headlessTab(ctx, t)
	t.Cleanup(closeTab)

	if err := chromedp.Run(tabCtx, chromedp.Navigate(server.URL+"/host")); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	frame, err := findLocal(tabCtx)
	if err != nil {
		t.Fatalf("find panel: %v", err)
	}
	if frame == nil {
		t.Fatal("no panel found in a page that is showing one")
	}
	return frame, tabCtx
}
