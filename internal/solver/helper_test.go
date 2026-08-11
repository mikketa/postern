package solver

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/mikketa/postern/internal/browser"
	"github.com/mikketa/postern/internal/display"
)

// newVirtualTab opens a tab on a windowed Chrome running on a virtual display,
// which is how postern actually runs.
func newVirtualTab(t *testing.T) (context.Context, func()) {
	t.Helper()

	dir, err := os.MkdirTemp("", "postern-test-profile-")
	if err != nil {
		t.Fatalf("temp profile: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)

	screen, err := display.Ensure(ctx, 1920, 1080, display.Virtual)
	if err != nil {
		cancel()
		t.Skipf("no virtual display available: %v", err)
	}

	b, err := browser.Launch(ctx, browser.Options{
		UserDataDir:  dir,
		ScreenWidth:  1920,
		ScreenHeight: 1080,
		Env:          screen.Env(),
	})
	if err != nil {
		screen.Close()
		cancel()
		t.Fatalf("launch: %v", err)
	}

	tabCtx, closeTab, err := b.NewTab()
	if err != nil {
		b.Close()
		screen.Close()
		cancel()
		t.Fatalf("new tab: %v", err)
	}

	return tabCtx, func() {
		closeTab()
		b.Close()
		screen.Close()
		cancel()
		_ = os.RemoveAll(dir)
	}
}
