package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeClients puts a client file on disk with the permissions LoadClients
// insists on, unless a test asks for others.
func writeClients(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clients")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile is subject to umask, so the mode has to be set outright.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAClientFileReadableByOthersIsRefused(t *testing.T) {
	// The file is a list of passwords. Loading it anyway and logging a warning
	// would mean the one deployment that needed to hear it is the one that
	// scrolled past it.
	path := writeClients(t, "alice averylongkey0123456789\n", 0o644)

	_, err := LoadClients(path)
	if err == nil {
		t.Fatal("a world-readable file of client keys loaded without complaint")
	}
	if !strings.Contains(err.Error(), "readable by other users") {
		t.Errorf("error was %q, which does not say the permissions are the problem", err)
	}
}

func TestAClientFileIsReadWithItsQuotas(t *testing.T) {
	path := writeClients(t, `
# name  key  solves per minute
alice   alice-key-0123456789   30
bob     bob-key-01234567890
`, 0o600)

	set, err := LoadClients(path)
	if err != nil {
		t.Fatalf("LoadClients: %v", err)
	}
	if got := set.Len(); got != 2 {
		t.Fatalf("loaded %d clients, want 2", got)
	}

	alice := set.lookup("alice-key-0123456789")
	if alice == nil || alice.Name != "alice" {
		t.Fatalf("alice did not come back for her key: %v", alice)
	}
	if alice.quota == nil {
		t.Error("alice has a rate column and no quota was built from it")
	}
	if bob := set.lookup("bob-key-01234567890"); bob == nil || bob.quota != nil {
		t.Error("bob has no rate column, so he should have loaded with no quota")
	}
	if set.lookup("not-a-key-at-all-x") != nil {
		t.Error("an unknown key resolved to a client")
	}
}

func TestAClientFileRefusesWhatWouldBeAmbiguous(t *testing.T) {
	for _, c := range []struct {
		name, body, want string
	}{
		{
			"a short key",
			"alice short\n",
			"at least",
		},
		{
			"two clients sharing a key",
			"alice sharedkey0123456789\nbob sharedkey0123456789\n",
			"same key",
		},
		{
			"the same name twice",
			"alice key-one-0123456789\nalice key-two-0123456789\n",
			"already defined",
		},
		{
			"a rate that is not a number",
			"alice alice-key-0123456789 soon\n",
			"rate above zero",
		},
		{
			"a rate of zero",
			"alice alice-key-0123456789 0\n",
			"rate above zero",
		},
		{
			"no clients at all",
			"# nothing but a comment\n",
			"defines no clients",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadClients(writeClients(t, c.body, 0o600))
			if err == nil {
				t.Fatalf("%s loaded without complaint", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error was %q, want something about %q", err, c.want)
			}
		})
	}
}

// TestAJobBelongsToTheClientThatSubmittedIt is the reason ownership exists.
// Ids are handed out in sequence, so without this any authenticated client
// could walk the id space and collect tokens somebody else paid for.
func TestAJobBelongsToTheClientThatSubmittedIt(t *testing.T) {
	j := newJobs()
	id, ok := j.start("alice", maxJobs)
	if !ok {
		t.Fatal("the store refused a job")
	}
	j.finish(id, "0.token", ReasonOK, nil)

	if _, found := j.collect(id, "bob"); found {
		t.Error("bob collected alice's job — sequential ids make this a walk, " +
			"not a guess")
	}
	entry, found := j.collect(id, "alice")
	if !found || entry.token != "0.token" {
		t.Errorf("alice could not collect her own job: found=%v entry=%+v", found, entry)
	}
}

// TestAnotherClientsJobLooksExactlyLikeAnExpiredOne locks the shape of the
// refusal, not just that there is one. Answering differently would confirm
// that the id exists, which is half of what walking the id space is for.
func TestAnotherClientsJobLooksExactlyLikeAnExpiredOne(t *testing.T) {
	s := serverFor(&stubFleet{ready: 1})
	s.clients = SingleClient("alice-key-0123456789")

	id := mustStart(s.jobs) // owned by nobody, so not by alice
	s.jobs.finish(id, "0.token", ReasonOK, nil)

	handler := s.Handler()
	theirs := get(t, handler, "/res.php?key=alice-key-0123456789&action=get&id="+id)
	nonexistent := get(t, handler, "/res.php?key=alice-key-0123456789&action=get&id=999999999")

	if theirs != nonexistent {
		t.Errorf("somebody else's job answered %q and an unknown id answered %q — "+
			"the difference confirms the id exists", theirs, nonexistent)
	}
	if theirs != errNoSuchID {
		t.Errorf("got %q, want %q", theirs, errNoSuchID)
	}
}

func TestAQuotaRefusesPastItsRateAndSaysWhenToComeBack(t *testing.T) {
	// Two per minute: two go through on the burst, the third does not.
	c := &Client{Name: "alice", quota: newBucket(2)}

	// Written out rather than folded into one condition: each call spends a
	// token, so short-circuiting would silently make this test spend fewer
	// than it reads as spending.
	for i := range 2 {
		if !c.allow() {
			t.Fatalf("the burst did not cover a minute's worth of solves: "+
				"refused at %d of 2", i+1)
		}
	}
	if c.allow() {
		t.Error("a third solve went through on a two-per-minute quota")
	}

	wait := c.retryAfter()
	if wait <= 0 || wait > 90*time.Second {
		t.Errorf("Retry-After is %s, which is not a sane wait for a "+
			"two-per-minute quota", wait)
	}
}

func TestAClientWithNoQuotaIsNotLimited(t *testing.T) {
	c := &Client{Name: "alice"}
	for i := range 1000 {
		if !c.allow() {
			t.Fatalf("a client with no rate column was refused at solve %d", i)
		}
	}
	// The no-authentication case goes through the same calls.
	var none *Client
	if !none.allow() {
		t.Error("a server with no authentication refused its own caller")
	}
	if got := clientName(none); got != "anonymous" {
		t.Errorf("an absent client is named %q", got)
	}
}

func TestGoingOverQuotaIs429AndNot503(t *testing.T) {
	// 503 means the server is loaded and everyone should back off; 429 means
	// this caller has spent its allowance. A client that cannot tell them
	// apart cannot behave correctly for either.
	s := serverFor(&stubFleet{ready: 1})
	key := "alice-key-0123456789"
	s.clients = &Clients{}
	s.clients.add(&Client{Name: "alice", key: key, quota: newBucket(1)})
	handler := s.Handler()

	body := `{"url":"https://e.com","sitekey":"0x4A"}`
	var last *httptest.ResponseRecorder
	for range 3 {
		req := httptest.NewRequest(http.MethodPost, "/solve", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		last = httptest.NewRecorder()
		handler.ServeHTTP(last, req)
	}

	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("over quota answered %d, want %d", last.Code, http.StatusTooManyRequests)
	}
	if last.Header().Get("Retry-After") == "" {
		t.Error("a 429 with no Retry-After leaves the client guessing")
	}
	if !strings.Contains(last.Body.String(), string(ReasonQuota)) {
		t.Errorf("body %q does not carry the %q code", last.Body.String(), ReasonQuota)
	}
}

// TestEachClientIsCountedSeparately is what makes billing and a
// noisy-neighbour question answerable. The counter deliberately carries only
// the caller and the outcome: crossing it with the vendor and the reason would
// multiply the series instead of adding to them.
func TestEachClientIsCountedSeparately(t *testing.T) {
	m := NewMetrics()
	m.Observe("alice", "turnstile", time.Second, true, ReasonOK)
	m.Observe("alice", "turnstile", time.Second, true, ReasonOK)
	m.Observe("bob", "recaptcha", time.Second, false, ReasonTimeout)

	var buf strings.Builder
	m.Write(&buf, nil)
	out := buf.String()

	for _, want := range []string{
		`postern_client_solves_total{client="alice",outcome="token"} 2`,
		`postern_client_solves_total{client="bob",outcome="failed"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics do not carry %s", want)
		}
	}
}

func TestAnUnnamedCallerStillLandsOnTheCounter(t *testing.T) {
	// A server with no authentication has no client, and a solve that is not
	// counted at all is worse than one counted as anonymous.
	m := NewMetrics()
	m.Observe("", "turnstile", time.Second, true, ReasonOK)

	var buf strings.Builder
	m.Write(&buf, nil)
	if !strings.Contains(buf.String(), `client="anonymous"`) {
		t.Errorf("an unauthenticated solve fell off the per-client counter:\n%s", buf.String())
	}
}

// TestTheNativeRoutesAnswerUnderV1AndBare locks both halves: new callers get a
// versioned path to point at, and every existing integration keeps working.
func TestTheNativeRoutesAnswerUnderV1AndBare(t *testing.T) {
	s := serverFor(&stubFleet{ready: 1})
	handler := s.Handler()

	for _, path := range []string{"/health", "/v1/health", "/metrics", "/v1/metrics"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s answered %d, want 200", path, rec.Code)
		}
	}

	// /solve is asserted on routing rather than on outcome: what the stub
	// fleet does with the request is another test's business, and the two
	// paths must simply reach the same handler. A 404 on either is the failure
	// this is here to catch.
	codes := map[string]int{}
	for _, path := range []string{"/solve", "/v1/solve"} {
		req := httptest.NewRequest(http.MethodPost, path,
			strings.NewReader(`{"url":"https://e.com","sitekey":"0x4A"}`))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		codes[path] = rec.Code
		if rec.Code == http.StatusNotFound {
			t.Errorf("POST %s is not routed anywhere", path)
		}
	}
	if codes["/solve"] != codes["/v1/solve"] {
		t.Errorf("/solve answered %d and /v1/solve answered %d — they are meant "+
			"to be the same handler", codes["/solve"], codes["/v1/solve"])
	}
}

// TestTheVersionedHealthCheckIsStillOpen guards a pairing that is easy to
// miss: /health is exempt from the bearer check so a load balancer can probe
// it, and a versioned copy that is not exempt would fail every probe that
// moved to it.
func TestTheVersionedHealthCheckIsStillOpen(t *testing.T) {
	s := serverFor(&stubFleet{ready: 1})
	s.clients = SingleClient("alice-key-0123456789")
	handler := s.Handler()

	for _, path := range []string{"/health", "/v1/health"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s with no token answered %d, want 200", path, rec.Code)
		}
	}
	// And the versioned copy of a closed route is still closed.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/fleet", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/fleet with no token answered %d, want 401 — the "+
			"versioned path must not be a way around the bearer check", rec.Code)
	}
}
