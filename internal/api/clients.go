package api

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client is one caller: who it is, the key it proves that with, and how much
// it may spend.
//
// There is always exactly one client behind a request, even when nobody
// configured any: a lone POSTERN_TOKEN is loaded as a single client named
// "default". Having one shape for both cases is what keeps ownership and
// accounting from being special-cased into uselessness on the common path.
type Client struct {
	// Name identifies the client in logs, metrics and job ownership. It is
	// not a secret and is never compared for authentication.
	Name string

	// key is the secret. Unexported: nothing outside this file should be able
	// to read it back out of a loaded set.
	key string

	// quota bounds how many solves this client may start. Nil means no limit,
	// which is what a single operator running their own server wants.
	quota *bucket
}

// DefaultClientName is the client a bare POSTERN_TOKEN is loaded as.
const DefaultClientName = "default"

// minKeyLength is the shortest key that will load. Short keys are the failure
// mode of hand-written config files, and a four-character key on an endpoint
// that answers over the network is not authentication, it is a formality.
const minKeyLength = 16

// Clients is a set of callers, looked up by the key they present.
type Clients struct {
	byKey map[string]*Client

	// names is the load order, for a stable listing in logs and metrics.
	names []*Client
}

// add registers a client under its own key.
//
// Every construction goes through here so that the key a client is filed
// under and the key it carries cannot drift apart. They are compared against
// each other on every lookup, so a set built by hand with one of them missing
// authenticates nobody, and does it silently.
func (c *Clients) add(cl *Client) {
	if c.byKey == nil {
		c.byKey = make(map[string]*Client)
	}
	c.byKey[cl.key] = cl
	c.names = append(c.names, cl)
}

// SingleClient builds the one-client set that a bare POSTERN_TOKEN means. It
// returns nil for an empty token, which is the open-on-loopback case.
func SingleClient(token string) *Clients {
	if token == "" {
		return nil
	}
	set := &Clients{}
	set.add(&Client{Name: DefaultClientName, key: token})
	return set
}

// LoadClients reads a client file: one "name key [per-minute]" per line, with
// # comments and blank lines ignored.
//
// The per-minute column is optional and is the sustained rate of solves that
// client may start; a client with no column is unlimited. Bursting up to one
// minute's worth is allowed, because a caller submitting a batch and then
// going quiet is normal use, not abuse.
func LoadClients(path string) (*Clients, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("api: clients: %w", err)
	}
	defer f.Close()

	// The file is a list of passwords. One that any user on the machine can
	// read is not one, and refusing to start is the only way to say so that
	// does not get scrolled past — the same reasoning that keeps the token out
	// of the command line.
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("api: clients: %w", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return nil, fmt.Errorf("api: clients: %s is readable by other users "+
			"(mode %04o) and holds every client's key. chmod 600 it", path, mode)
	}

	set := &Clients{byKey: make(map[string]*Client)}
	seenName := make(map[string]int)

	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		fields := strings.Fields(text)
		if len(fields) < 2 || len(fields) > 3 {
			return nil, fmt.Errorf("api: clients: %s:%d: want \"name key\" or "+
				"\"name key per-minute\", got %d fields", path, line, len(fields))
		}
		name, key := fields[0], fields[1]

		if len(key) < minKeyLength {
			return nil, fmt.Errorf("api: clients: %s:%d: the key for %q is %d "+
				"characters, want at least %d", path, line, name, len(key), minKeyLength)
		}
		if at, dup := seenName[name]; dup {
			return nil, fmt.Errorf("api: clients: %s:%d: %q is already defined on line %d",
				path, line, name, at)
		}
		// Two clients sharing a key are one client with two names, and every
		// quota, log line and job owner downstream would be attributed to
		// whichever the map happened to hold.
		if other, dup := set.byKey[key]; dup {
			return nil, fmt.Errorf("api: clients: %s:%d: %q has the same key as %q",
				path, line, name, other.Name)
		}
		seenName[name] = line

		c := &Client{Name: name, key: key}
		if len(fields) == 3 {
			perMinute, err := strconv.ParseFloat(fields[2], 64)
			if err != nil || perMinute <= 0 {
				return nil, fmt.Errorf("api: clients: %s:%d: %q is not a "+
					"solves-per-minute rate above zero", path, line, fields[2])
			}
			c.quota = newBucket(perMinute)
		}

		set.add(c)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("api: clients: %s: %w", path, err)
	}
	if len(set.byKey) == 0 {
		return nil, fmt.Errorf("api: clients: %s defines no clients", path)
	}
	return set, nil
}

// Len is how many clients are configured.
func (c *Clients) Len() int {
	if c == nil {
		return 0
	}
	return len(c.byKey)
}

// Names lists the clients in the order they were loaded.
func (c *Clients) Names() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.names))
	for _, cl := range c.names {
		out = append(out, cl.Name)
	}
	return out
}

// lookup finds the client presenting a key, or nil.
//
// The map does the finding and a constant-time compare does the confirming.
// The map alone would be enough to be correct, but its hit is not constant
// time, and confirming afterwards costs nothing and keeps the property the
// rest of this package is careful about.
func (c *Clients) lookup(key string) *Client {
	if c == nil || key == "" {
		return nil
	}
	found, ok := c.byKey[key]
	if !ok || !subtleEqual(key, found.key) {
		return nil
	}
	return found
}

// allow reports whether this client may start another solve now.
func (c *Client) allow() bool {
	if c == nil || c.quota == nil {
		return true
	}
	return c.quota.take()
}

// retryAfter is roughly how long until this client may try again.
func (c *Client) retryAfter() time.Duration {
	if c == nil || c.quota == nil {
		return 0
	}
	return c.quota.wait()
}

// bucket is a token bucket: a sustained rate, and a minute's worth of burst.
type bucket struct {
	mu sync.Mutex

	tokens float64
	max    float64
	perSec float64
	last   time.Time
}

func newBucket(perMinute float64) *bucket {
	return &bucket{
		tokens: perMinute,
		max:    perMinute,
		perSec: perMinute / 60,
		last:   time.Now(),
	}
}

// take removes one token if there is one.
func (b *bucket) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refillLocked(time.Now())
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// wait is how long until at least one token is back.
func (b *bucket) wait() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refillLocked(time.Now())
	if b.tokens >= 1 {
		return 0
	}
	// Rounded up: a Retry-After that lands a moment early only produces a
	// second refusal, which is the one outcome this header exists to avoid.
	seconds := (1 - b.tokens) / b.perSec
	return time.Duration(seconds*float64(time.Second)) + time.Second
}

func (b *bucket) refillLocked(now time.Time) {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed <= 0 {
		return
	}
	b.last = now
	b.tokens = min(b.max, b.tokens+elapsed*b.perSec)
}
