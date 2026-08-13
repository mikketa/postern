package browser_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/browser"
)

// The profile is the point of using a real browser at all: reCAPTCHA weighs
// what the browser has done before at least as heavily as what it does now, and
// the README says postern reuses one "so it ages like a person's".
//
// Cookies and history were never the part at risk — SQLite writes those as they
// happen, kill or no kill. Preferences is: Chrome writes it on the way out, and
// postern used to cancel the context, which kills the process instead. Measured
// two runs each way, it was absent every time from a killed browser and present
// every time from a closed one. A profile that has never been closed has no
// settled state to carry into the next run.
func TestProfileSurvivesTheBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("starts chrome")
	}

	content := listen(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:    "postern",
			Value:   "kept",
			Path:    "/",
			Expires: time.Now().Add(72 * time.Hour),
		})
		fmt.Fprint(w, "<html><body><h1>hello</h1></body></html>")
	}))

	dir := profileDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	chrome, err := browser.Launch(ctx, browser.Options{UserDataDir: dir, Headless: true})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}

	tabCtx, closeTab, err := chrome.NewTab()
	if err != nil {
		chrome.Close()
		t.Fatalf("new tab: %v", err)
	}
	if err := chromedp.Run(tabCtx,
		chromedp.Navigate("http://"+content+"/"),
		chromedp.WaitVisible("h1", chromedp.ByQuery),
	); err != nil {
		closeTab()
		chrome.Close()
		t.Fatalf("navigate: %v", err)
	}
	closeTab()

	// The whole test is what this leaves behind.
	chrome.Close()

	// Deliberately not the cookie jar: that is written either way, so checking
	// it would pass against the very bug this is here to catch.
	settled := filepath.Join(dir, "Default", "Preferences")
	if info, err := os.Stat(settled); err == nil && info.Size() > 0 {
		return
	}

	var found []string
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && len(found) < 12 {
			rel, _ := filepath.Rel(dir, path)
			found = append(found, rel)
		}
		return nil
	})
	t.Fatalf("chrome wrote no Default/Preferences, so it was killed rather than closed "+
		"and the profile carries no settled state into the next run.\n"+
		"the profile holds: %v", found)
}
