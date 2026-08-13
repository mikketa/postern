package challenge

import (
	"log/slog"
	"sort"
	"time"
)

// timings adds up where a picture challenge spent its time.
//
// A round of a grid challenge was measured at twelve to sixteen seconds, of
// which the vision model is about three tenths of one — so whatever the rest of
// it is, it is not the looking. Every part of a round is a wait for something:
// for pictures to arrive, for the compositor to paint, for a screenshot to come
// back, for a replaced tile to fade in, for the panel to make up its mind. Some
// of those waits are the page's and cannot be hurried; the others are constants
// in this file, chosen to look unhurried rather than measured.
//
// Which is which is not something to guess at, and shaving a pause that was not
// costing anything trades a token for nothing. So this counts them, and the
// numbers go in the log at the end of every challenge.
//
// No lock: a solve runs in one goroutine from end to end, and the one thing
// that does not — the screenshot bounded by its own context — is still awaited
// before anything else happens.
type timings struct {
	spent map[string]time.Duration
	calls map[string]int
	// inner holds measurements taken *inside* a tracked phase — the mouse
	// travel within a repaint, one turn of a polling loop. They are kept apart
	// because adding them to the phases they sit in would count the same
	// milliseconds twice and take the shares past a hundred.
	inner  map[string]time.Duration
	within map[string]int
	begun  time.Time
}

func newTimings() *timings {
	return &timings{
		spent:  map[string]time.Duration{},
		calls:  map[string]int{},
		inner:  map[string]time.Duration{},
		within: map[string]int{},
		begun:  time.Now(),
	}
}

// track starts the clock on a phase and returns the function that stops it,
// which is meant to be deferred.
func (t *timings) track(phase string) func() {
	if t == nil {
		return func() {}
	}
	started := time.Now()
	return func() {
		t.spent[phase] += time.Since(started)
		t.calls[phase]++
	}
}

// detail is track for something that happens inside a phase. Its call count is
// often the answer on its own: a polling loop that always turns the maximum
// number of times is a fixed cost pretending to be a wait.
func (t *timings) detail(phase string) func() {
	if t == nil {
		return func() {}
	}
	started := time.Now()
	return func() {
		t.inner[phase] += time.Since(started)
		t.within[phase]++
	}
}

// mark counts something that has no duration worth measuring — a branch taken,
// a wait given up on. It shares the inner table so it prints alongside the
// phase it explains.
func (t *timings) mark(name string) {
	if t == nil {
		return
	}
	t.within[name]++
}

// report writes what each phase cost, longest first.
//
// The unattributed line is the point of the exercise as much as the phases are:
// it is everything not inside a tracked call — re-reading the panel, comparing
// two photographs, the round loop itself — and a large one means the phases
// below it are not where to look.
func (t *timings) report(log *slog.Logger, rounds int) {
	if t == nil || len(t.spent) == 0 {
		return
	}

	total := time.Since(t.begun)
	phases := make([]string, 0, len(t.spent))
	var tracked time.Duration
	for phase, spent := range t.spent {
		phases = append(phases, phase)
		tracked += spent
	}
	sort.Slice(phases, func(i, j int) bool {
		return t.spent[phases[i]] > t.spent[phases[j]]
	})

	share := func(d time.Duration) int {
		if total <= 0 {
			return 0
		}
		return int(d * 100 / total)
	}

	log.Info("where the time went", "total", total.Round(time.Millisecond),
		"rounds", rounds, "per round", (total / time.Duration(max(rounds, 1))).Round(time.Millisecond))
	for _, phase := range phases {
		spent := t.spent[phase]
		log.Info("time", "phase", phase,
			"took", spent.Round(time.Millisecond),
			"share", share(spent),
			"calls", t.calls[phase],
			"each", (spent / time.Duration(max(t.calls[phase], 1))).Round(time.Millisecond))
	}
	if rest := total - tracked; rest > 0 {
		log.Info("time", "phase", "unattributed", "took", rest.Round(time.Millisecond),
			"share", share(rest))
	}

	within := make([]string, 0, len(t.within))
	for phase := range t.within {
		within = append(within, phase)
	}
	sort.Slice(within, func(i, j int) bool {
		return t.inner[within[i]] > t.inner[within[j]]
	})
	for _, phase := range within {
		spent := t.inner[phase]
		log.Info("time", "within", phase,
			"took", spent.Round(time.Millisecond),
			"share", share(spent),
			"times", t.within[phase],
			"each", (spent / time.Duration(max(t.within[phase], 1))).Round(time.Millisecond))
	}
}
