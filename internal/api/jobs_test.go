package api

import (
	"testing"
	"time"
)

func TestIdsAreNotGuessableFromOneAnother(t *testing.T) {
	// Ids are handed out in sequence because the protocol's own are numeric.
	// Starting from a fixed point would let anyone who submits one job collect
	// the tokens either side of it by asking for id±1.
	a, b := newJobs(), newJobs()
	if a.start() == b.start() {
		t.Error("two servers issued the same first id — the sequence starts " +
			"somewhere predictable, so submitting one job reveals the others")
	}
}

func TestAJobThatIsNeverCollectedIsNotKeptForever(t *testing.T) {
	// Submit-and-never-collect is a memory leak reachable by anyone who can
	// call the endpoint.
	j := newJobs()
	id := j.start()
	j.finish(id, "token", ReasonOK, nil)

	j.mu.Lock()
	j.m[id].created = time.Now().Add(-jobTTL - time.Minute)
	// The sweep is throttled, so move its own clock back too — otherwise this
	// tests the throttle rather than the expiry.
	j.swept = time.Now().Add(-sweepEvery - time.Second)
	j.mu.Unlock()

	if _, found := j.collect(id); found {
		t.Error("a job past its time to live is still held")
	}
}

func TestTheStoreIsBounded(t *testing.T) {
	j := newJobs()
	for range maxJobs + 100 {
		id := j.start()
		j.finish(id, "t", ReasonOK, nil)
	}

	j.mu.Lock()
	held := len(j.m)
	j.mu.Unlock()

	if held > maxJobs {
		t.Errorf("holding %d jobs, cap is %d — a client that submits and never "+
			"collects grows this without limit", held, maxJobs)
	}
}

func TestAnInFlightJobIsNotDroppedForAFinishedOne(t *testing.T) {
	// At the cap the oldest *finished* job goes first: it is the one whose
	// owner is least likely to still be waiting, and an unfinished one still
	// has a goroutine working on it.
	j := newJobs()
	inFlight := j.start()

	for range maxJobs + 10 {
		id := j.start()
		j.finish(id, "t", ReasonOK, nil)
	}

	if _, found := j.collect(inFlight); !found {
		t.Error("an in-flight job was evicted while finished ones were held — " +
			"its client is still waiting and will never get an answer")
	}
}

func TestCollectingReadsAConsistentSnapshot(t *testing.T) {
	// collect returns a copy: a pointer into the map would be read while
	// another goroutine is writing the outcome into it.
	j := newJobs()
	id := j.start()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			j.finish(id, "token", ReasonOK, nil)
		}
	}()
	for range 1000 {
		if e, found := j.collect(id); found && e.finished && e.token == "" {
			t.Error("read a job marked finished with no token — a torn read")
			break
		}
	}
	<-done
}
