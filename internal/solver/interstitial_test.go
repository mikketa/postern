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
