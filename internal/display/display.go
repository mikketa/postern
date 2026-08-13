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
package display

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"
)

// startupTimeout bounds the wait for Xvfb to start answering on its display.
const startupTimeout = 5 * time.Second

// firstDisplay is where the search for a free display number starts. The low
// numbers are where real sessions live, and an X server handed a number that is
// already taken does not reliably step aside: finding a socket it cannot bind,
// it may decide the thing is stale, unlink it and bind its own. The session that
// was there first keeps running with a socket nobody can reach any more, so
// every X client started afterwards — Steam, Wine, anything on XWayland — gets
// "unable to open a connection". Starting at 99, as xvfb-run does, keeps us out
// of that neighbourhood entirely.
const firstDisplay = 99

// displaysToTry bounds the search, so a machine already crowded with virtual
// displays fails with a clear message instead of scanning forever.
const displaysToTry = 64

// errDisplayTaken means the server we started gave up on the number, which
// someone else claimed between our check and its bind. The next one is worth a
// try.
var errDisplayTaken = errors.New("display: number taken")

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
	// exited closes once Xvfb has been reaped, so Close can wait for it
	// without racing the goroutine that watches the process.
	exited chan struct{}
}

// Ensure returns a display for Chrome to use.
func Ensure(ctx context.Context, width, height int, mode Mode) (*Display, error) {
	if mode == Host {
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return nil, errors.New("display: -display host, but no session is running here")
		}
		return &Display{}, nil
	}
	if mode != Virtual && mode != "" {
		return nil, fmt.Errorf("display: unknown mode %q, want %q or %q", mode, Virtual, Host)
	}

	xvfb, err := exec.LookPath("Xvfb")
	if err != nil {
		return nil, errors.New("display: Xvfb is not installed — install it " +
			"(xorg-server-xvfb on Arch, xvfb on Debian), or pass -display host " +
			"to use the session already running, or -headless")
	}

	// The number is ours to pick, high enough to be out of reach of the session
	// running on this machine. Letting Xvfb choose with -displayfd looks tidier
	// and is not: its search starts at :0, right on top of the desktop.
	for number := firstDisplay; number < firstDisplay+displaysToTry; number++ {
		if displayInUse(number) {
			continue
		}

		cmd := exec.CommandContext(ctx, xvfb, fmt.Sprintf(":%d", number),
			"-screen", "0", fmt.Sprintf("%dx%dx24", width, height),
			"-nolisten", "tcp")
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("display: start Xvfb: %w", err)
		}
		exited := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(exited)
		}()

		err := waitUntilAnswering(number, exited)
		if err == nil {
			return &Display{Name: fmt.Sprintf(":%d", number), cmd: cmd, exited: exited}, nil
		}
		_ = cmd.Process.Kill()
		<-exited
		if errors.Is(err, errDisplayTaken) {
			continue
		}
		return nil, err
	}

	return nil, fmt.Errorf("display: no display free between :%d and :%d",
		firstDisplay, firstDisplay+displaysToTry-1)
}

// displayInUse reports whether a display number already belongs to someone —
// either an X server holding its lock file, or a socket that still answers.
func displayInUse(number int) bool {
	if _, err := os.Stat(fmt.Sprintf("/tmp/.X%d-lock", number)); err == nil {
		return true
	}
	return displayAnswers(number)
}

// displayAnswers reports whether a server is listening on the display's socket.
// A socket file left behind by a dead server refuses the connection, so it
// counts as free.
func displayAnswers(number int) bool {
	conn, err := net.DialTimeout("unix", fmt.Sprintf("/tmp/.X11-unix/X%d", number), 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// waitUntilAnswering blocks until the server we just started takes connections.
// A server that exits by itself lost the race for the number to someone who
// bound it first, which is errDisplayTaken and worth retrying one number up.
func waitUntilAnswering(number int, exited <-chan struct{}) error {
	deadline := time.Now().Add(startupTimeout)
	for {
		if displayAnswers(number) {
			return nil
		}
		select {
		case <-exited:
			return errDisplayTaken
		default:
		}
		if time.Now().After(deadline) {
			return errors.New("display: Xvfb did not come up within " + startupTimeout.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Env returns the environment entries Chrome needs, empty when the host
// session is used.
func (d *Display) Env() []string {
	if d == nil || d.Name == "" {
		return nil
	}
	// WAYLAND_DISPLAY is emptied, not left alone. Chrome picks its backend
	// before it looks at DISPLAY: on a Wayland session — which on this machine
	// is every session — it finds WAYLAND_DISPLAY inherited from the caller,
	// connects to the compositor and opens a real window on the user's desktop,
	// with the Xvfb we just started sitting unused. The whole point of the
	// virtual display is that nothing appears on screen and nothing takes the
	// pointer, so the variable has to go for the choice to be ours.
	return []string{"DISPLAY=" + d.Name, "WAYLAND_DISPLAY="}
}

// Close stops Xvfb, if we started one.
func (d *Display) Close() {
	if d == nil || d.cmd == nil || d.cmd.Process == nil {
		return
	}
	_ = d.cmd.Process.Kill()
	<-d.exited
}
