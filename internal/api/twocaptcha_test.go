package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mikketa/postern/internal/browser"
	"github.com/mikketa/postern/internal/solver"
)

// This layer exists to be indistinguishable from the service clients already
// use, so the tests are about the wire: exact parameter names, exact error
// strings, both response formats. A field renamed here is a client broken
// somewhere else, and nothing in our own code would notice.

func TestParametersTranslateToTheRightChallenge(t *testing.T) {
	for _, c := range []struct {
		name     string
		query    string
		wantKind string
		wantErr  string
	}{
		{"recaptcha v2",
			"method=userrecaptcha&googlekey=6Lc&pageurl=https://e.com",
			string(solver.RecaptchaV2), ""},
		{"recaptcha v2 invisible",
			"method=userrecaptcha&googlekey=6Lc&pageurl=https://e.com&invisible=1",
			string(solver.RecaptchaInvis), ""},
		{"recaptcha v3",
			"method=userrecaptcha&googlekey=6Lc&pageurl=https://e.com&version=v3&action=login",
			string(solver.RecaptchaV3), ""},
		{"turnstile",
			"method=turnstile&sitekey=0x4A&pageurl=https://e.com",
			string(solver.Turnstile), ""},
		{"turnstile with the sitekey in googlekey anyway",
			"method=turnstile&googlekey=0x4A&pageurl=https://e.com",
			string(solver.Turnstile), ""},
		{"pageurl spelled url, as several clients send it",
			"method=turnstile&sitekey=0x4A&url=https://e.com",
			string(solver.Turnstile), ""},

		{"no page url", "method=turnstile&sitekey=0x4A", "", errPageURL},
		{"no sitekey", "method=turnstile&pageurl=https://e.com", "", errBadSitekey},
		{"no method", "googlekey=6Lc&pageurl=https://e.com", "", errBadParams},
		{"a vendor we do not solve",
			"method=hcaptcha&sitekey=x&pageurl=https://e.com", "", errNoMethod},
	} {
		t.Run(c.name, func(t *testing.T) {
			q, err := url.ParseQuery(c.query)
			if err != nil {
				t.Fatal(err)
			}
			req, code := translate(q)
			if code != c.wantErr {
				t.Fatalf("error %q, want %q", code, c.wantErr)
			}
			if c.wantErr == "" && req.Kind != c.wantKind {
				t.Errorf("kind %q, want %q", req.Kind, c.wantKind)
			}
		})
	}
}

func TestAV3ActionIsKeptAndAV2OneIsNot(t *testing.T) {
	// action means different things on either side. On v3 it is the vendor's
	// own parameter and the token depends on it. On v2 it is 2Captcha's field
	// for its own bookkeeping, and passing it to the widget would ask for a
	// different token than the site expects.
	v3, _ := translate(mustQuery(t,
		"method=userrecaptcha&version=v3&googlekey=6Lc&pageurl=https://e.com&action=login"))
	if v3.Action != "login" {
		t.Errorf("v3 action = %q, want login", v3.Action)
	}

	v2, _ := translate(mustQuery(t,
		"method=userrecaptcha&googlekey=6Lc&pageurl=https://e.com&action=login"))
	if v2.Action != "" {
		t.Errorf("v2 action = %q, want it dropped", v2.Action)
	}

	// And a v3 request that names none gets the service's own default rather
	// than an empty one, which the vendor would reject.
	noAction, _ := translate(mustQuery(t,
		"method=userrecaptcha&version=v3&googlekey=6Lc&pageurl=https://e.com"))
	if noAction.Action != "verify" {
		t.Errorf("v3 default action = %q, want verify", noAction.Action)
	}
}

// blockingFleet never lends a browser, so a submitted job stays in flight for
// as long as the test needs it to. A stub that returns a nil browser would
// have the solve dereference it instead.
type blockingFleet struct{ stubFleet }

func (f *blockingFleet) Borrow(ctx context.Context) (*browser.Browser, func(bool), error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

func blockingServer() *Server {
	return serverFor(&blockingFleet{stubFleet{ready: 1}})
}

func TestSubmittingAnswersWithAnIdInBothFormats(t *testing.T) {
	handler := blockingServer().Handler()

	t.Run("plain", func(t *testing.T) {
		body := post(t, handler, "/in.php",
			"method=turnstile&sitekey=0x4A&pageurl=https://e.com")
		if !strings.HasPrefix(body, "OK|") {
			t.Fatalf("body %q, want OK|<id>", body)
		}
	})

	t.Run("json", func(t *testing.T) {
		body := post(t, handler, "/in.php",
			"method=turnstile&sitekey=0x4A&pageurl=https://e.com&json=1")
		var got struct {
			Status  int    `json:"status"`
			Request string `json:"request"`
		}
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("not json: %q", body)
		}
		if got.Status != 1 || got.Request == "" {
			t.Errorf("got %+v, want status 1 and an id", got)
		}
	})
}

func TestAnUnfinishedJobIsNotReadyAndAnUnknownOneIsNot(t *testing.T) {
	handler := blockingServer().Handler()

	id := strings.TrimPrefix(post(t, handler, "/in.php",
		"method=turnstile&sitekey=0x4A&pageurl=https://e.com"), "OK|")

	if body := get(t, handler, "/res.php?action=get&id="+id); body != notReady {
		// The misspelling is the service's, and clients match on it exactly.
		t.Errorf("pending job answered %q, want %q", body, notReady)
	}
	if body := get(t, handler, "/res.php?action=get&id=999999999"); body != errNoSuchID {
		t.Errorf("unknown id answered %q, want %q", body, errNoSuchID)
	}
}

func TestAFinishedJobHandsOverItsToken(t *testing.T) {
	s := serverFor(&stubFleet{ready: 1})
	id := mustStart(s.jobs)
	s.jobs.finish(id, "0.qF8mZ2", ReasonOK, nil)

	if body := get(t, s.Handler(), "/res.php?action=get&id="+id); body != "OK|0.qF8mZ2" {
		t.Errorf("got %q, want OK|0.qF8mZ2", body)
	}
	body := get(t, s.Handler(), "/res.php?action=get&json=1&id="+id)
	if !strings.Contains(body, `"status":1`) || !strings.Contains(body, "0.qF8mZ2") {
		t.Errorf("json result %q", body)
	}
}

func TestAFailedJobMapsToTheProtocolsOwnErrors(t *testing.T) {
	for _, c := range []struct {
		reason Reason
		want   string
	}{
		{ReasonBusy, errNoSlot},
		{ReasonCrossing, errUnsolvable},
		{ReasonTimeout, errUnsolvable},
		{ReasonInvalid, errBadParams},
	} {
		s := serverFor(&stubFleet{ready: 1})
		id := mustStart(s.jobs)
		s.jobs.finish(id, "", c.reason, errFailed)

		if body := get(t, s.Handler(), "/res.php?action=get&id="+id); body != c.want {
			t.Errorf("%q answered %q, want %q", c.reason, body, c.want)
		}
	}
}

func TestTheWrongKeyIsRefusedOnBothEndpoints(t *testing.T) {
	s := blockingServer()
	s.token = "s3cret"
	handler := s.Handler()

	for _, path := range []string{
		"/in.php?key=wrong&method=turnstile&sitekey=0x4A&pageurl=https://e.com",
		"/res.php?key=wrong&action=get&id=1",
	} {
		if body := get(t, handler, path); body != errWrongKey {
			t.Errorf("%s answered %q, want %q", path, body, errWrongKey)
		}
	}
}

func TestTheCompatEndpointsDoNotWantABearerToken(t *testing.T) {
	// They carry their own credential, in the parameter the protocol names.
	// Demanding a bearer as well would mean no existing client could reach
	// them, which is the whole point of speaking this protocol.
	handler := guardedServer("s3cret")
	body := get(t, handler,
		"/res.php?key=s3cret&action=get&id=1")

	if strings.Contains(body, "bearer") {
		t.Errorf("the bearer check ran on a compat endpoint: %q", body)
	}
	if body != errNoSuchID {
		t.Errorf("got %q, want the protocol's own %q", body, errNoSuchID)
	}
}

func TestThisProtocolReportsFailureWithAStatusOfTwoHundred(t *testing.T) {
	// Easy to get wrong, and it breaks every client at once: the outcome is in
	// the body, and a client that sees anything but 200 usually treats it as
	// the service being down rather than as an answer.
	rec := httptest.NewRecorder()
	guardedServer("s3cret").ServeHTTP(rec,
		httptest.NewRequest("GET", "/res.php?key=wrong&action=get&id=1", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("an error answered %d; this protocol puts failures in the body "+
			"with a 200, and clients read it that way", rec.Code)
	}
}

// helpers

var errFailed = &failure{}

type failure struct{}

func (*failure) Error() string { return "failed" }

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	q, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func post(t *testing.T, h http.Handler, path, body string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	return strings.TrimSpace(rec.Body.String())
}

func get(t *testing.T, h http.Handler, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return strings.TrimSpace(rec.Body.String())
}
