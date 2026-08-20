package solver

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestTheConsoleWatcherActuallyCatchesThings is a witness for the instrument
// itself. Concluding "the browser said nothing" from a listener that was never
// working is the same mistake as reading an empty log from a command that did
// not run.
func TestTheConsoleWatcherActuallyCatchesThings(t *testing.T) {
	ctx := pageShowing(t, `<script>
	  console.error("ERROR for site owner: Invalid domain for site key");
	  fetch("https://127.0.0.1:9/blocked-on-purpose").catch(() => {});
	</script>`)

	w := watchConsole(ctx)
	// The listener is installed after the page loaded, so make it speak again.
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`console.error("recaptcha: second line after the listener")`, nil)); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	time.Sleep(2 * time.Second)

	got := w.telling()
	if len(got) == 0 {
		t.Fatal("the console watcher caught nothing at all — it cannot be used " +
			"to conclude that a page said nothing")
	}
	t.Logf("caught %d line(s): %v", len(got), got)
}
