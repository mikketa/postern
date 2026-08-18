package api

import (
	"crypto/rand"
	"encoding/binary"
	"strconv"
	"sync"
	"time"
)

const (
	// jobTTL is how long a finished result is kept for collection. The
	// protocol is poll-based and the client decides when to come back, so a
	// result has to outlive the solve by enough for a slow or briefly broken
	// client to still get it — and not by so much that a server under load is
	// holding thousands of tokens nobody will ever ask for.
	jobTTL = 5 * time.Minute

	// maxJobs bounds the store. A client that submits and never collects would
	// otherwise grow it without limit, which is a memory leak reachable by
	// anyone who can call the endpoint. At the cap the oldest finished job is
	// dropped: it is the one whose owner is least likely to still be waiting.
	maxJobs = 10_000

	// sweepEvery is how often expiry actually walks the map. Sweeping on every
	// call is O(n) with the lock held, and this protocol is a polling one — at
	// a full store that is ten thousand iterations per poll, per client,
	// forever. Nothing depends on a job disappearing the instant it expires;
	// it only has to not accumulate.
	sweepEvery = 30 * time.Second
)

// job is one submitted captcha, in flight or finished.
type job struct {
	created  time.Time
	finished bool

	token  string
	reason Reason
	err    error
}

// jobs holds submitted captchas between the call that started one and the call
// that collects it.
//
// In memory, and deliberately: a token is worth nothing once its challenge has
// expired, which is minutes. Persisting them would mean surviving a restart
// with a store full of answers to questions nobody is asking any more.
type jobs struct {
	mu    sync.Mutex
	m     map[string]*job
	next  uint64
	swept time.Time
}

func newJobs() *jobs {
	// Ids start somewhere unpredictable rather than at 1. They are handed out
	// in sequence — the protocol's own ids are numeric — so starting from a
	// fixed point would let anyone who submits one guess the ids either side
	// of it, and collect somebody else's token.
	var seed [8]byte
	rand.Read(seed[:])
	return &jobs{
		m:     make(map[string]*job),
		next:  binary.BigEndian.Uint64(seed[:]) >> 16,
		swept: time.Now(),
	}
}

// start registers a new job and returns its id.
func (j *jobs) start() string {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.sweepLocked()
	if len(j.m) >= maxJobs {
		j.dropOldestLocked()
	}

	j.next++
	id := strconv.FormatUint(j.next, 10)
	j.m[id] = &job{created: time.Now()}
	return id
}

// finish records the outcome of a job. An id that has already been swept is
// not an error: the client stopped waiting, and there is nothing to keep.
func (j *jobs) finish(id, token string, reason Reason, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if entry, ok := j.m[id]; ok {
		entry.finished = true
		entry.token, entry.reason, entry.err = token, reason, err
	}
}

// collect returns a job's state. found is false for an id that was never
// issued, or that has expired — which the protocol cannot tell apart, and
// neither can we without keeping every id ever issued forever.
func (j *jobs) collect(id string) (entry job, found bool) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.sweepLocked()
	e, ok := j.m[id]
	if !ok {
		return job{}, false
	}
	// Copied out, so the caller reads a consistent snapshot rather than a
	// pointer into a struct another goroutine is about to finish writing.
	return *e, true
}

// sweepLocked drops everything past its time to live, at most every
// sweepEvery. See the constant for why it is not every call.
func (j *jobs) sweepLocked() {
	now := time.Now()
	if now.Sub(j.swept) < sweepEvery {
		return
	}
	j.swept = now

	cutoff := now.Add(-jobTTL)
	for id, e := range j.m {
		if e.created.Before(cutoff) {
			delete(j.m, id)
		}
	}
}

// dropOldestLocked removes the oldest finished job, or the oldest of any kind
// if none has finished. Finished first: an unfinished one still has a
// goroutine working on it and a client presumably still waiting.
func (j *jobs) dropOldestLocked() {
	var oldest string
	var at time.Time
	for id, e := range j.m {
		if !e.finished {
			continue
		}
		if oldest == "" || e.created.Before(at) {
			oldest, at = id, e.created
		}
	}
	if oldest == "" {
		for id, e := range j.m {
			if oldest == "" || e.created.Before(at) {
				oldest, at = id, e.created
			}
		}
	}
	delete(j.m, oldest)
}
