package browser

import (
	"strings"
	"testing"
)

// TestProxyCredentialsAreKeptFromChrome pins the half of the fix that needs no
// browser: Chrome is handed an address with no password in it. Left in, the
// whole flag is rejected on some builds and silently ignored on others, and
// either way the credentials end up in the process list for every user on the
// machine to read.
func TestProxyCredentialsAreKeptFromChrome(t *testing.T) {
	address, user, pass, err := splitProxy("http://postern:s3cret@127.0.0.1:8080")
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if user != "postern" || pass != "s3cret" {
		t.Fatalf("credentials came back as %q/%q", user, pass)
	}
	if address != "http://127.0.0.1:8080" {
		t.Fatalf("chrome would be given %q", address)
	}
	if strings.Contains(address, "s3cret") {
		t.Fatal("the password is still in the flag")
	}

	// Proxies without a password are the common case and must be untouched.
	address, user, _, err = splitProxy("socks5://127.0.0.1:9050")
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if address != "socks5://127.0.0.1:9050" || user != "" {
		t.Fatalf("a plain proxy came back as %q, user %q", address, user)
	}

	// A bare host:port is what most proxy lists hand out, and url.Parse reads
	// it as a path rather than an address unless it is given a scheme.
	address, user, pass, err = splitProxy("postern:s3cret@10.0.0.1:3128")
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if address != "10.0.0.1:3128" || user != "postern" || pass != "s3cret" {
		t.Fatalf("host:port form came back as %q, %q/%q", address, user, pass)
	}

	if _, _, _, err := splitProxy("http://%zz"); err == nil {
		t.Fatal("an unreadable proxy was accepted")
	}
}
