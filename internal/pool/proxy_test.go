package pool

import (
	"strings"
	"testing"
)

func TestProviderLinesBecomeURLs(t *testing.T) {
	cases := []struct{ in, want string }{
		// The form providers actually sell.
		{"gate.example.com:8000:user-42:hunter2", "http://user-42:hunter2@gate.example.com:8000"},
		{"1.2.3.4:8080", "http://1.2.3.4:8080"},
		// Already a URL: left exactly as it is, including a socks scheme the
		// four-field form cannot express.
		{"http://u:p@host:3128", "http://u:p@host:3128"},
		{"socks5://127.0.0.1:9050", "socks5://127.0.0.1:9050"},
		{"", ""},
	}
	for _, c := range cases {
		got, err := ParseProxy(c.in)
		if err != nil {
			t.Fatalf("ParseProxy(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("ParseProxy(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMistypedProxiesAreRefused(t *testing.T) {
	// Each of these would otherwise become a browser that cannot reach anything
	// and an identity that quarantines itself for no reason.
	for _, bad := range []string{
		"gate.example.com",
		"gate.example.com:notaport",
		"gate.example.com:8000:user",
		"gate.example.com:8000:user:pass:extra",
		"gate.example.com:99999:user:pass",
		":8000:user:pass",
	} {
		if got, err := ParseProxy(bad); err == nil {
			t.Fatalf("ParseProxy(%q) was accepted as %q", bad, got)
		}
	}
}

func TestFleetProblemsAreReported(t *testing.T) {
	problems := CheckFleet([]*Identity{
		{Name: "a", Proxy: "http://one:1@gate:8000"},
		{Name: "b", Proxy: "http://one:1@gate:8000"},
		{Name: "c"},
		{Name: "d"},
	})
	if len(problems) != 2 {
		t.Fatalf("expected the shared proxy and the bare identities to be reported, got %v", problems)
	}

	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "a, b") && !strings.Contains(joined, "b, a") {
		t.Fatalf("the two identities sharing an exit were not named: %v", problems)
	}
	if !strings.Contains(joined, "2 identities have no proxy") {
		t.Fatalf("the identities with no proxy were not reported: %v", problems)
	}
}

func TestAWellFormedFleetHasNoProblems(t *testing.T) {
	problems := CheckFleet([]*Identity{
		{Name: "a", Proxy: "http://one:1@gate:8000"},
		{Name: "b", Proxy: "http://two:2@gate:8000"},
		// One identity on the machine's own address is a reasonable thing to
		// want, and must not be nagged about.
		{Name: "c"},
	})
	if len(problems) != 0 {
		t.Fatalf("a sound fleet was reported as %v", problems)
	}
}
