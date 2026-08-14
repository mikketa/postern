package pool

import (
	"path/filepath"
	"testing"
	"time"
)

func testPool(t *testing.T, names ...string) (*Pool, *time.Time) {
	t.Helper()

	var identities []*Identity
	for _, name := range names {
		identities = append(identities, &Identity{Name: name, Profile: "/tmp/" + name})
	}
	p, err := New(identities, filepath.Join(t.TempDir(), "state.json"), Settings{
		Rest: time.Minute, RestJitter: time.Second, Quarantine: time.Hour, StreakLimit: 2,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	clock := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return clock }
	return p, &clock
}

func TestRestingKeepsAnIdentityOutOfRotation(t *testing.T) {
	p, clock := testPool(t, "a")

	identity, err := p.Take()
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	p.Release(identity, true)

	// This is the whole point of the pool: the identity that just solved is not
	// available to solve again immediately, however much work is waiting.
	if _, err := p.Take(); err != ErrBusy {
		t.Fatalf("an identity that just solved was handed straight back out: %v", err)
	}

	*clock = clock.Add(2 * time.Minute)
	if _, err := p.Take(); err != nil {
		t.Fatalf("still resting after twice the rest period: %v", err)
	}
}

func TestWorkSpreadsAcrossIdentities(t *testing.T) {
	p, clock := testPool(t, "a", "b", "c")

	seen := map[string]int{}
	for range 3 {
		identity, err := p.Take()
		if err != nil {
			t.Fatalf("take: %v", err)
		}
		seen[identity.Name]++
		p.Release(identity, true)
		*clock = clock.Add(time.Second)
	}

	if len(seen) != 3 {
		t.Fatalf("three solves went to %d identities, not three: %v", len(seen), seen)
	}
}

func TestFailingRepeatedlyTakesAnIdentityOutForLonger(t *testing.T) {
	p, clock := testPool(t, "a", "b")

	// Two failures in a row on "a" — the streak limit here.
	for range 2 {
		identity, err := p.Take()
		if err != nil {
			t.Fatalf("take: %v", err)
		}
		if identity.Name != "a" {
			// Take hands out the longest-rested, so force the run onto one.
			p.Release(identity, true)
			*clock = clock.Add(time.Millisecond)
			identity, err = p.Take()
			if err != nil {
				t.Fatalf("take: %v", err)
			}
		}
		p.Release(identity, false)
		*clock = clock.Add(2 * time.Minute)
	}

	stats := p.Stats()
	var quarantined bool
	for _, identity := range stats {
		if identity.Streak >= 2 && identity.RestUntil.Sub(*clock) > 30*time.Minute {
			quarantined = true
		}
	}
	if !quarantined {
		t.Fatalf("an identity that failed twice running is still in normal rotation: %+v", stats)
	}
}

func TestSuccessClearsTheStreak(t *testing.T) {
	p, clock := testPool(t, "a")

	identity, _ := p.Take()
	p.Release(identity, false)
	*clock = clock.Add(2 * time.Minute)

	identity, err := p.Take()
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	p.Release(identity, true)

	if got := p.Stats()[0].Streak; got != 0 {
		t.Fatalf("streak is %d after a success, want 0", got)
	}
}

func TestHistorySurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	first, err := New([]*Identity{{Name: "a", Profile: "/tmp/a"}}, path, Settings{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	identity, _ := first.Take()
	first.Release(identity, false)
	first.Warmed(identity)

	// A restart that forgets is a restart that hands a worn identity straight
	// back out, which is exactly the failure the pool exists to prevent.
	second, err := New([]*Identity{{Name: "a", Profile: "/tmp/a"}}, path, Settings{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	stats := second.Stats()
	if stats[0].Failures != 1 || stats[0].Streak != 1 {
		t.Fatalf("history lost across restart: %+v", stats[0])
	}
	if !stats[0].Warmed {
		t.Fatal("a warmed profile came back needing warming again")
	}
	if _, err := second.Take(); err != ErrBusy {
		t.Fatal("a resting identity was available again after a restart")
	}
}

func TestSharedProfilesAreRefused(t *testing.T) {
	_, err := New([]*Identity{
		{Name: "a", Profile: "/tmp/same"},
		{Name: "b", Profile: "/tmp/same"},
	}, "", Settings{})
	if err == nil {
		t.Fatal("two identities were allowed to share one profile directory")
	}
}

func TestTakenIdentityIsNotHandedOutTwice(t *testing.T) {
	p, _ := testPool(t, "a")

	if _, err := p.Take(); err != nil {
		t.Fatalf("take: %v", err)
	}
	if _, err := p.Take(); err != ErrBusy {
		t.Fatalf("the same identity was handed to two callers: %v", err)
	}
}

func TestReadyCountsWhatCouldBeTaken(t *testing.T) {
	p, clock := testPool(t, "a", "b")

	if got := p.Ready(); got != 2 {
		t.Fatalf("ready is %d on a fresh pool of two", got)
	}
	identity, _ := p.Take()
	if got := p.Ready(); got != 1 {
		t.Fatalf("ready is %d with one identity taken", got)
	}
	p.Release(identity, true)
	if got := p.Ready(); got != 1 {
		t.Fatalf("ready is %d with one identity resting", got)
	}
	*clock = clock.Add(2 * time.Minute)
	if got := p.Ready(); got != 2 {
		t.Fatalf("ready is %d once rest is over", got)
	}
}
