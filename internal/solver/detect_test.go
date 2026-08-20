package solver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestTheWidgetIsReadOffThePage covers the ways a sitekey actually appears in
// the wild. A caller should not have to go and find a value that is written in
// the markup of the page they already named.
func TestTheWidgetIsReadOffThePage(t *testing.T) {
	for _, c := range []struct {
		name     string
		body     string
		wantKind Kind
		wantKey  string
	}{
		{
			"a turnstile div",
			`<div class="cf-turnstile" data-sitekey="0x4AAAAAAABkMYinukE8nzY"></div>`,
			Turnstile, "0x4AAAAAAABkMYinukE8nzY",
		},
		{
			"a recaptcha div",
			`<div class="g-recaptcha" data-sitekey="6LeIxAcTAAAAAJcZVRqyHh71UMIEGNQ_MXjiZKhI"></div>`,
			RecaptchaV2, "6LeIxAcTAAAAAJcZVRqyHh71UMIEGNQ_MXjiZKhI",
		},
		{
			"an invisible recaptcha says so itself",
			`<div class="g-recaptcha" data-size="invisible"
			      data-sitekey="6LeIxAcTAAAAAJcZVRqyHh71UMIEGNQ_MXjiZKhI"></div>`,
			RecaptchaInvis, "6LeIxAcTAAAAAJcZVRqyHh71UMIEGNQ_MXjiZKhI",
		},
		{
			// Nothing around the key names a vendor, so the shape of the key
			// is all there is to go on.
			"a bare data-sitekey",
			`<div id="widget" data-sitekey="0x4AAAAAAABkMYinukE8nzY"></div>`,
			Turnstile, "0x4AAAAAAABkMYinukE8nzY",
		},
		{
			// The generated case: no data-sitekey anywhere, only the frame the
			// vendor's own script loaded. This is what world-novel.fr looked
			// like, and what a markup-only reader would miss.
			"only a recaptcha anchor frame",
			`<iframe src="https://www.google.com/recaptcha/api2/anchor?ar=1&k=6Ld_hFEqAAAAAN-SouW8rfW-KW_KwzOg1pJnHWfc&co=x&hl=fr"></iframe>`,
			RecaptchaV2, "6Ld_hFEqAAAAAN-SouW8rfW-KW_KwzOg1pJnHWfc",
		},
		{
			"only a turnstile frame",
			`<iframe src="https://challenges.cloudflare.com/cdn-cgi/challenge-platform/h/b/turnstile/if/ov2/av0/rcv0/0/abcde/0x4AAAAAAABkMYinukE8nzY/light/normal"></iframe>`,
			Turnstile, "0x4AAAAAAABkMYinukE8nzY",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := pageShowing(t, c.body)

			f, err := detect(ctx, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatalf("detect: %v", err)
			}
			if f.Key != c.wantKey {
				t.Errorf("read sitekey %q, want %q", f.Key, c.wantKey)
			}
			if f.Kind != c.wantKind {
				t.Errorf("read kind %q, want %q — the wrong provider renders the "+
					"wrong widget and the vendor rejects it", f.Kind, c.wantKind)
			}
		})
	}
}

// TestAPageWithNoWidgetSaysSo checks the refusal, which has to name what the
// caller can do: a page with no widget is the ordinary result of a wrong url.
func TestAPageWithNoWidgetSaysSo(t *testing.T) {
	ctx := pageShowing(t, `<p>nothing to solve here</p>`)

	// Waiting the full detectWait would make this test as slow as the timeout,
	// and what is being checked is the refusal, not the patience.
	short, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if _, err := detect(short, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("a page with no widget reported one")
	}
}

// pageShowing serves one body and returns a tab looking at it.
func pageShowing(t *testing.T, body string) context.Context {
	t.Helper()

	if testing.Short() {
		t.Skip("launches a browser")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "<!doctype html><title>t</title><body>%s</body>", body)
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
