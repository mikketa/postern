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
	if mustStart(a) == mustStart(b) {
		t.Error("two servers issued the same first id — the sequence starts " +
			"somewhere predictable, so submitting one job reveals the others")
	}
}

func TestAJobThatIsNeverCollectedIsNotKeptForever(t *testing.T) {
	// Submit-and-never-collect is a memory leak reachable by anyone who can
	// call the endpoint.
	j := newJobs()
	id := mustStart(j)
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
		id := mustStart(j)
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
	inFlight := mustStart(j)

	for range maxJobs + 10 {
		id := mustStart(j)
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
	id := mustStart(j)

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

// mustStart takes an id with the queue bound out of the way, for tests that
// are about something else.
func mustStart(j *jobs) string {
	id, ok := j.start(maxJobs + 1)
	if !ok {
		panic("the store refused a job with no bound in the way")
	}
	return id
}

func TestASubmissionIsRefusedOnceTheQueueIsDeep(t *testing.T) {
	// Accepting work there is no prospect of doing is a worse failure
	// deferred: each waiting job holds a goroutine, a context and a timer, and
	// every one of them times out having never reached a browser.
	const depth = 5
	j := newJobs()

	for i := range depth {
		if _, ok := j.start(depth); !ok {
			t.Fatalf("refused job %d of %d, before the queue was full", i+1, depth)
		}
	}
	if _, ok := j.start(depth); ok {
		t.Error("accepted a job past the queue depth — nothing is stopping a " +
			"client from queueing more work than the fleet can ever reach")
	}
}

func TestFinishingAJobMakesRoomForAnother(t *testing.T) {
	// The bound is on what is waiting, not on what has ever been submitted.
	const depth = 2
	j := newJobs()

	first, _ := j.start(depth)
	j.start(depth)
	if _, ok := j.start(depth); ok {
		t.Fatal("the queue did not fill")
	}

	j.finish(first, "token", ReasonOK, nil)
	if _, ok := j.start(depth); !ok {
		t.Error("a finished job did not free its place — the queue only ever " +
			"closes, and the server stops accepting work for good")
	}
}
