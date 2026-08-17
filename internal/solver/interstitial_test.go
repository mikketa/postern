package solver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// challengePage is a stand-in for Cloudflare's interstitial, built the way the
// real one is: the widget in a closed shadow root, the host wrapped in two
// divs sharing its box, and every id randomised. Nothing here can be found by
// name, which is the whole difficulty.
const challengePage = `<!doctype html>
<title>Un instant…</title>
<body style="margin:0">
<div style="width:896px;margin:0 auto">
  <h2 id="a7Kd2">Vérification de sécurité en cours</h2>
  <p id="q1Xz9">Ce site utilise un service de sécurité.</p>
  <div id="w0Pl4"><div><div id="host" style="width:896px;height:68px"></div></div></div>
</div>
<script src="/cdn-cgi/challenge-platform/h/g/orchestrate/chl_page/v1"></script>
<script>
  document.getElementById('host').attachShadow({ mode: 'closed' })
    .innerHTML = '<div style="width:300px;height:65px">case</div>';
</script>
</body>`

// sitePage is any ordinary page. It must not be mistaken for a challenge, which
// is the case that matters most: every request to every site goes through this.
const sitePage = `<!doctype html>
<title>Accueil</title>
<body style="margin:0">
<div style="width:896px;height:68px"></div>
<p>bienvenue</p>
</body>`

func TestOrdinaryPageIsNotTakenForAChallenge(t *testing.T) {
	ctx := challengeTab(t, sitePage)

	page, err := look(ctx)
	if err != nil {
		t.Fatalf("look: %v", err)
	}
	if page.Challenge {
		t.Error("an ordinary page was read as a challenge — every solve would " +
			"stop to cross something that is not there")
	}
}

// TestChallengeCheckboxIsFoundThroughAClosedShadowRoot is the rule this whole
// feature rests on. The widget cannot be seen: no iframe, no readable content,
// ids regenerated per request. What gives the host away is that it fills the
// space of a widget while holding no text at all.
func TestChallengeCheckboxIsFoundThroughAClosedShadowRoot(t *testing.T) {
	ctx := challengeTab(t, challengePage)

	page, err := look(ctx)
	if err != nil {
		t.Fatalf("look: %v", err)
	}
	if !page.Challenge {
		t.Fatal("the challenge platform script was on the page and went unnoticed")
	}
	if page.W == 0 {
		t.Fatal("no checkbox found: the host of a closed shadow root has to be " +
			"recognised by its emptiness, since nothing can look inside it")
	}
	if page.W != 896 || page.H != 68 {
		t.Errorf("host measured %.0fx%.0f, want 896x68 — a wrapper or the widget "+
			"itself was picked instead of the host", page.W, page.H)
	}
}

// TestCrossingCannotOutspendTheSolve locks the rule that a crossing takes its
// budget from what the solve has left rather than from a constant. It used to
// take the constant, so a site behind a managed challenge could spend a whole
// default -timeout before the widget was even on the page — and then report a
// widget that would not install.
func TestCrossingCannotOutspendTheSolve(t *testing.T) {
	for _, c := range []struct {
		name string
		have time.Duration // what the solve has left, 0 for no deadline at all
		want time.Duration
	}{
		{"no deadline leaves the crossing its own budget", 0, interstitialBudget},
		{"a roomy deadline leaves it too", 5 * time.Minute, interstitialBudget},
		{"a tight deadline cuts it down", 10 * time.Second, 10 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			if c.have > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, c.have)
				defer cancel()
			}

			// Deadline arithmetic loses a moment to the clock; a second of
			// slack keeps this about the rule and not about scheduling.
			if got := left(ctx); got > c.want || got < c.want-time.Second {
				t.Errorf("left() = %s, want about %s — a crossing that outspends "+
					"the solve leaves nothing for the widget", got, c.want)
			}
		})
	}
}

// TestASecondChallengeIsNotStartedWithoutTimeForIt guards the other half: with
// less on the clock than one challenge takes, asking for another one can only
// end in the timeout it was already heading for, having spent the budget the
// widget still needs.
func TestASecondChallengeIsNotStartedWithoutTimeForIt(t *testing.T) {
	if minimumInstance < settleBeforeClick+ignoredWait+verdictWait {
		t.Fatal("minimumInstance is under what one challenge actually costs, so a " +
			"second one gets started with no room to be answered")
	}
	if minimumInstance >= interstitialBudget {
		t.Error("minimumInstance is at or over the whole crossing budget, so a second " +
			"challenge would never be started even when there is room for it")
	}
}

// challengeTab serves one page and returns a tab looking at it.
func challengeTab(t *testing.T, body string) context.Context {
	t.Helper()

	if testing.Short() {
		t.Skip("launches a browser")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/cdn-cgi/challenge-platform/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "// le vrai script, dont seule la présence compte ici")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	})
	server := httptest.NewServer(mux)
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
