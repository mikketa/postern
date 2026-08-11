// Package display gets Chrome a screen to draw on without putting a window in
// front of anyone.
//
// Note the distinction this package rests on: "no window on screen" and
// "headless" are not the same thing. Chrome on a virtual display is a fully
// windowed browser that nobody can see, and that difference is worth an entire
// package because challenges can tell the two apart.
//
// This exists because of a measurement, not a preference: against a production
// Turnstile sitekey, headless Chrome was refused every single time while the
// same binary driving a windowed Chrome on a virtual display was accepted every
// time. Correcting the user agent, the GPU and the screen size was not enough —
// headless is detectable by means we cannot enumerate, so the fix is to stop
// being headless rather than to keep patching the symptoms.
//
// Xvfb gives us exactly that: a real, windowed browser on a screen nobody
// looks at.
package display

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// startupGrace is how long Xvfb gets to come up before we hand Chrome the
// display. Chrome fails outright if the X server is not listening yet.
const startupGrace = 700 * time.Millisecond

// firstScreen is where the search for a free display number starts. Low
// numbers belong to real sessions.
const firstScreen = 90

// screensToTry bounds the search so a broken Xvfb cannot spin forever.
const screensToTry = 20

// Mode selects where Chrome draws.
type Mode string

const (
	// Virtual runs Chrome on an Xvfb display of our own. This is the default:
	// it behaves the same on a workstation and on a bare server, and it never
	// puts a window in front of whoever is sitting there.
	Virtual Mode = "virtual"

	// Host borrows the session already running. Chrome is then visible, which
	// is occasionally what you want while debugging and never what you want
	// otherwise.
	Host Mode = "host"
)

// Display is a screen Chrome can render on.
type Display struct {
	// Name is the value for the DISPLAY variable, empty when the host session
	// is being used as-is.
	Name string

	cmd *exec.Cmd
}

// Ensure returns a display for Chrome to use.
func Ensure(ctx context.Context, width, height int, mode Mode) (*Display, error) {
	if mode == Host {
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return nil, errors.New("display: -display host, but no session is running here")
		}
		return &Display{}, nil
	}

	xvfb, err := exec.LookPath("Xvfb")
	if err != nil {
		return nil, errors.New("display: Xvfb is not installed — install it " +
			"(xorg-server-xvfb on Arch, xvfb on Debian), or pass -display host " +
			"to use the session already running, or -headless")
	}

	for n := firstScreen; n < firstScreen+screensToTry; n++ {
		name := fmt.Sprintf(":%d", n)
		if inUse(name) {
			continue
		}

		cmd := exec.CommandContext(ctx, xvfb, name,
			"-screen", "0", fmt.Sprintf("%dx%dx24", width, height),
			"-nolisten", "tcp")
		if err := cmd.Start(); err != nil {
			continue
		}

		// Xvfb exits immediately if the display was taken between our check
		// and the start; give it a moment and make sure it is still alive.
		time.Sleep(startupGrace)
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			continue
		}

		return &Display{Name: name, cmd: cmd}, nil
	}

	return nil, fmt.Errorf("display: no free display number in :%d-:%d",
		firstScreen, firstScreen+screensToTry-1)
}

// Env returns the environment entries Chrome needs, empty when the host
// session is used.
func (d *Display) Env() []string {
	if d == nil || d.Name == "" {
		return nil
	}
	return []string{"DISPLAY=" + d.Name}
}

// Close stops Xvfb, if we started one.
func (d *Display) Close() {
	if d == nil || d.cmd == nil || d.cmd.Process == nil {
		return
	}
	_ = d.cmd.Process.Kill()
	_, _ = d.cmd.Process.Wait()
}

// inUse reports whether an X server already holds this display number. X
// servers keep a lock file per display, which is cheaper and more reliable
// than trying to connect.
func inUse(name string) bool {
	_, err := os.Stat("/tmp/.X" + name[1:] + "-lock")
	return err == nil
}
