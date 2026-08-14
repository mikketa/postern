// Package pool shares solving out across many identities instead of one.
//
// A captcha solver that runs at any volume has a problem the solving itself
// does not touch. Measured on one machine over one evening: the same build went
// from three tokens in five to none in five after about twenty-five solves from
// the same address and the same profile, with no code change in between. Nothing
// got worse at answering. The identity wore out.
//
// So throughput is not "how fast is one solve" — that is a minute either way —
// it is how many identities are rested enough to be worth using. Ten identities
// resting four minutes each will out-solve one identity going flat out, and they
// will still be working tomorrow.
//
// An identity here is a profile and a way out, kept together for life. That
// pairing is the point: a profile that browses from a different address every
// time is a stranger arriving in a new city every morning, which is worse than
// either half alone. Everything else in this file is bookkeeping around the two
// rules that matter — rest between uses, and stop using what has stopped
// working.
package pool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

var (
	// ErrBusy is every identity being either in use or resting. It is a normal
	// condition under load, not a fault: the caller should wait or shed.
	ErrBusy = errors.New("pool: every identity is busy or resting")

	// ErrEmpty is a pool with nothing in it.
	ErrEmpty = errors.New("pool: no identities")
)

// Identity is one profile and the way out it always uses.
type Identity struct {
	// Name is how this identity is referred to in logs and on disk.
	Name string `json:"name"`

	// Profile is the Chrome user data directory. It is never shared: two
	// browsers on one profile is a locked profile and a browser that exits.
	Profile string `json:"profile"`

	// Proxy is what this identity goes out through, in the form the browser
	// package takes. Empty means the machine's own address, which is fine for
	// one identity and a bad idea for ten.
	Proxy string `json:"proxy,omitempty"`

	// Solves and Failures are what this identity has done. They persist, so a
	// restart does not hand a burnt identity a clean slate.
	Solves   int `json:"solves"`
	Failures int `json:"failures"`

	// Streak is consecutive failures. Reset by any success.
	Streak int `json:"streak"`

	// LastUsed is when it last finished a solve.
	LastUsed time.Time `json:"last_used,omitzero"`

	// RestUntil is when it may next be picked up. Set after every solve, and
	// set much further out after a run of failures.
	RestUntil time.Time `json:"rest_until,omitzero"`

	// Warmed records that this profile has been taken browsing at least once,
	// so the caller knows to do it before the first solve rather than every
	// time. See browser.Warm.
	Warmed bool `json:"warmed"`

	// inUse is held between Take and Release. Not persisted: a process that
	// died holding an identity should not leave it stuck forever.
	inUse bool
}

// Pool hands out identities and remembers how they did.
type Pool struct {
	mu    sync.Mutex
	items []*Identity
	path  string

	// rest is how long an identity waits after a solve, and restJitter how much
	// that varies. A fixed interval across a pool makes every identity solve on
	// the same beat, which is a pattern in itself.
	rest       time.Duration
	restJitter time.Duration

	// quarantine is how long an identity is set aside after failing
	// consecutively, and streakLimit how many failures that takes.
	quarantine  time.Duration
	streakLimit int

	// now is time.Now, replaced in tests.
	now func() time.Time
}

// Settings configures a pool. Zero values take the defaults, which were chosen
// from the one measurement available: tokens stopped coming after roughly
// twenty-five solves in an evening from a single identity, so a few minutes of
// rest per solve is the order of magnitude that keeps an identity below that
// rate indefinitely.
type Settings struct {
	Rest        time.Duration
	RestJitter  time.Duration
	Quarantine  time.Duration
	StreakLimit int
}

// New builds a pool over the given identities, remembering their state in path.
// Existing state in that file is merged in by name, so restarting does not
// forget which identities are worn out.
func New(identities []*Identity, path string, settings Settings) (*Pool, error) {
	if len(identities) == 0 {
		return nil, ErrEmpty
	}

	seen := map[string]bool{}
	profiles := map[string]bool{}
	for _, identity := range identities {
		if identity.Name == "" || identity.Profile == "" {
			return nil, fmt.Errorf("pool: identity needs a name and a profile")
		}
		if seen[identity.Name] {
			return nil, fmt.Errorf("pool: two identities named %q", identity.Name)
		}
		// Two identities sharing a profile directory would be one Chrome
		// finding the profile locked and handing over to the other, which
		// presents as a browser that died on startup.
		if profiles[identity.Profile] {
			return nil, fmt.Errorf("pool: two identities share the profile %q", identity.Profile)
		}
		seen[identity.Name], profiles[identity.Profile] = true, true
	}

	p := &Pool{
		items:       identities,
		path:        path,
		rest:        settings.Rest,
		restJitter:  settings.RestJitter,
		quarantine:  settings.Quarantine,
		streakLimit: settings.StreakLimit,
		now:         time.Now,
	}
	if p.rest <= 0 {
		p.rest = 4 * time.Minute
	}
	if p.restJitter <= 0 {
		p.restJitter = 90 * time.Second
	}
	if p.quarantine <= 0 {
		p.quarantine = 45 * time.Minute
	}
	if p.streakLimit <= 0 {
		p.streakLimit = 3
	}

	if err := p.load(); err != nil {
		return nil, err
	}
	return p, nil
}

// Take hands out the identity that has rested longest, or ErrBusy.
//
// Longest-rested rather than best-performing on purpose. Picking winners
// concentrates the work on whichever identity is currently lucky, which is the
// fastest way to wear it out — and the pool's whole reason for existing is that
// use is what wears identities out.
func (p *Pool) Take() (*Identity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	var pick *Identity
	for _, identity := range p.items {
		if identity.inUse || now.Before(identity.RestUntil) {
			continue
		}
		if pick == nil || identity.LastUsed.Before(pick.LastUsed) {
			pick = identity
		}
	}
	if pick == nil {
		return nil, ErrBusy
	}

	pick.inUse = true
	return pick, nil
}

// Release gives an identity back, recording how it went and sending it to rest.
func (p *Pool) Release(identity *Identity, solved bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	identity.inUse = false
	identity.LastUsed = now

	if solved {
		identity.Solves++
		identity.Streak = 0
	} else {
		identity.Failures++
		identity.Streak++
	}

	// The jitter is deterministic rather than random: derived from the counters
	// it already keeps, so a pool restores to exactly the state it saved and a
	// test can predict it. What it has to avoid is every identity in a pool
	// coming back at the same instant, and any spread does that.
	spread := time.Duration(identity.Solves+identity.Failures) % (p.restJitter + 1)
	identity.RestUntil = now.Add(p.rest + spread)

	if identity.Streak >= p.streakLimit {
		// Consecutive failures are rarely about the answers. Measured, a burnt
		// address fails every arm of an A/B equally — so the useful response is
		// to stop asking with this identity for a while, not to try harder.
		identity.RestUntil = now.Add(p.quarantine)
	}

	if err := p.save(); err != nil {
		// The pool still works from memory; the cost is that a restart forgets.
		_ = err
	}
}

// Warmed records that a profile has been taken browsing.
func (p *Pool) Warmed(identity *Identity) {
	p.mu.Lock()
	defer p.mu.Unlock()
	identity.Warmed = true
	_ = p.save()
}

// Ready is how many identities could be taken right now, and Size how many
// there are. The difference is what a caller should watch: a pool that is
// permanently at zero ready is a pool that needs more identities, not more
// patience.
func (p *Pool) Ready() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	ready := 0
	for _, identity := range p.items {
		if !identity.inUse && !now.Before(identity.RestUntil) {
			ready++
		}
	}
	return ready
}

// Size is how many identities the pool holds.
func (p *Pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.items)
}

// Stats returns a copy of every identity, for reporting.
func (p *Pool) Stats() []Identity {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]Identity, 0, len(p.items))
	for _, identity := range p.items {
		out = append(out, *identity)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// load merges saved state into the identities, matched by name.
func (p *Pool) load() error {
	if p.path == "" {
		return nil
	}

	body, err := os.ReadFile(p.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pool: read %s: %w", p.path, err)
	}

	var saved []Identity
	if err := json.Unmarshal(body, &saved); err != nil {
		return fmt.Errorf("pool: %s is not readable: %w", p.path, err)
	}

	by := map[string]Identity{}
	for _, identity := range saved {
		by[identity.Name] = identity
	}
	for _, identity := range p.items {
		was, ok := by[identity.Name]
		if !ok {
			continue
		}
		// Only the history is restored. The profile and proxy come from the
		// configuration, so editing them takes effect without having to clear
		// the state file.
		identity.Solves, identity.Failures, identity.Streak = was.Solves, was.Failures, was.Streak
		identity.LastUsed, identity.RestUntil, identity.Warmed = was.LastUsed, was.RestUntil, was.Warmed
	}
	return nil
}

// save writes the state, atomically enough that a crash cannot leave half a
// file where the history was.
func (p *Pool) save() error {
	if p.path == "" {
		return nil
	}

	body, err := json.MarshalIndent(p.items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return err
	}

	temp := p.path + ".tmp"
	if err := os.WriteFile(temp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, p.path)
}
