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
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// startupTimeout bounds the wait for Xvfb to report the display it took.
const startupTimeout = 5 * time.Second

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
	if mode != Virtual && mode != "" {
		return nil, fmt.Errorf("display: unknown mode %q, want %q or %q", mode, Virtual, Host)
	}

	xvfb, err := exec.LookPath("Xvfb")
	if err != nil {
		return nil, errors.New("display: Xvfb is not installed — install it " +
			"(xorg-server-xvfb on Arch, xvfb on Debian), or pass -display host " +
			"to use the session already running, or -headless")
	}

	// -displayfd hands the choice of display number to Xvfb, which writes the
	// one it took to the given descriptor. Picking a number ourselves means
	// racing every other X server on the machine and tripping over the stale
	// lock files a killed one leaves behind.
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("display: pipe: %w", err)
	}
	defer readEnd.Close()

	cmd := exec.CommandContext(ctx, xvfb,
		"-displayfd", "3",
		"-screen", "0", fmt.Sprintf("%dx%dx24", width, height),
		"-nolisten", "tcp")
	cmd.ExtraFiles = []*os.File{writeEnd}

	if err := cmd.Start(); err != nil {
		writeEnd.Close()
		return nil, fmt.Errorf("display: start Xvfb: %w", err)
	}
	// The child holds its own copy; ours has to go or the read never ends.
	writeEnd.Close()

	number, err := readDisplayNumber(readEnd)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, err
	}

	return &Display{Name: ":" + number, cmd: cmd}, nil
}

// readDisplayNumber waits for Xvfb to announce the display it claimed.
func readDisplayNumber(r *os.File) (string, error) {
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)

	go func() {
		line, err := bufio.NewReader(r).ReadString('\n')
		done <- result{strings.TrimSpace(line), err}
	}()

	select {
	case res := <-done:
		if res.line == "" {
			return "", fmt.Errorf("display: Xvfb reported no display number: %w", res.err)
		}
		if _, err := strconv.Atoi(res.line); err != nil {
			return "", fmt.Errorf("display: Xvfb reported %q, which is not a display number", res.line)
		}
		return res.line, nil

	case <-time.After(startupTimeout):
		return "", errors.New("display: Xvfb did not come up within " + startupTimeout.String())
	}
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
