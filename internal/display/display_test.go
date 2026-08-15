package display

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A display number is only free if the process named in its lock file is gone,
// so the two halves of that question are worth pinning down: what the file says,
// and whether that process is still there. Neither is tested through
// displayInUse itself, which reads /tmp and would be answering about whatever
// this machine happens to be running.

func TestLockHolder(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		name    string
		content string
		want    int
	}{
		// What Xvfb actually writes: the pid in ten columns, then a newline.
		{"as Xvfb writes it", "      1234\n", 1234},
		{"no padding", "1234", 1234},
		{"empty", "", 0},
		{"not a number", "held by nobody\n", 0},
		{"negative", "-5\n", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, c.name)
			if err := os.WriteFile(path, []byte(c.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := lockHolder(path); got != c.want {
				t.Errorf("lockHolder(%q) = %d, want %d", c.content, got, c.want)
			}
		})
	}

	if got := lockHolder(filepath.Join(dir, "nothing here")); got != 0 {
		t.Errorf("lockHolder of a missing file = %d, want 0", got)
	}
}

func TestRunning(t *testing.T) {
	if !running(os.Getpid()) {
		t.Error("running() says this test is not running")
	}

	// A process that has exited and been reaped: its pid answers nothing, which
	// is the case that makes a lock file rubbish.
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a throwaway process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if running(pid) {
		t.Errorf("running(%d) is true for a process that has exited", pid)
	}
}

func TestDisplayInUseClearsADeadLock(t *testing.T) {
	// Outside the range Ensure searches, so a postern running alongside this
	// test cannot be handed the number while it is being used as a fixture.
	const number = 250
	lock := lockPath(number)
	if _, err := os.Stat(lock); err == nil {
		t.Skipf("%s exists, so this machine is using :%d", lock, number)
	}

	// The pid of a process that has exited: what an X server killed rather than
	// asked to stop leaves behind.
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a throwaway process: %v", err)
	}
	dead := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(lock, []byte(fmt.Sprintf("%10d\n", dead)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(lock) })

	if displayInUse(number) {
		t.Errorf("displayInUse(%d) is true for a lock held by a dead process", number)
	}
	if _, err := os.Stat(lock); err == nil {
		t.Error("the dead lock is still there; Xvfb will refuse to start on that number")
	}
}

func TestDisplayInUseKeepsALiveLock(t *testing.T) {
	const number = 251
	lock := lockPath(number)
	if _, err := os.Stat(lock); err == nil {
		t.Skipf("%s exists, so this machine is using :%d", lock, number)
	}

	if err := os.WriteFile(lock, []byte(fmt.Sprintf("%10d\n", os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(lock) })

	if !displayInUse(number) {
		t.Errorf("displayInUse(%d) is false for a lock held by a running process", number)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Error("a live display's lock file was removed")
	}
}

func TestLockPath(t *testing.T) {
	// The name is X's, not ours: Xvfb looks for exactly this and refuses to
	// start when it is there.
	if got, want := lockPath(99), fmt.Sprintf("/tmp/.X%d-lock", 99); got != want {
		t.Errorf("lockPath(99) = %q, want %q", got, want)
	}
}
