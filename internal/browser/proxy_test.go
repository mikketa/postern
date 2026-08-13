package browser_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mikketa/postern/internal/browser"
)

// A solver that cannot leave the machine it runs on is a solver that answers to
// one IP reputation, and reputation is the single largest factor in whether
// reCAPTCHA hands over a token — measured on this machine, the same code went
// from three tokens in five to none in five over an evening, purely from use.
// Going out through somebody else's address is the only lever that does not
// require asking the operator to touch their network, and every proxy worth
// pointing at is authenticated.
//
// Chrome will not do that on its own. Credentials in --proxy-server are
// dropped, and the 407 that comes back is answered by a dialog that no one is
// there to fill in, so the page simply never loads.

// proxyServer is an HTTP proxy that demands a password, and counts how many
// requests got through. Plain HTTP only: enough to prove Chrome authenticated,
// without a certificate authority to stand up for CONNECT.
type proxyServer struct {
	user, pass string
	// upstream is where everything is relayed, whatever host was asked for.
	// The test asks for a name that does not resolve, on purpose: Chrome sends
	// loopback straight out and would never touch the proxy at all.
	upstream   string
	served     atomic.Int64
	challenged atomic.Int64
}

func (p *proxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(p.user+":"+p.pass))
	if r.Header.Get("Proxy-Authorization") != want {
		p.challenged.Add(1)
		w.Header().Set("Proxy-Authenticate", `Basic realm="postern"`)
		w.WriteHeader(http.StatusProxyAuthRequired)
		return
	}
	p.served.Add(1)

	r.RequestURI = ""
	r.Header.Del("Proxy-Authorization")
	r.URL.Scheme = "http"
	r.URL.Host = p.upstream
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// listen starts a server on a loopback port and returns its address.
func listen(t *testing.T, handler http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: handler}
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })
	return ln.Addr().String()
}

func TestProxyWithCredentials(t *testing.T) {
	if testing.Short() {
		t.Skip("starts chrome")
	}

	const marker = "postern went out through the proxy"
	content := listen(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "<html><body><h1>%s</h1></body></html>", marker)
	}))

	proxy := &proxyServer{user: "postern", pass: "s3cret", upstream: content}
	proxyAddr := listen(t, proxy)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// profileDir, not t.TempDir: Chrome is still writing to the profile as it
	// shuts down, and t.TempDir fails the test when it cannot empty it.
	dir := profileDir(t)
	chrome, err := browser.Launch(ctx, browser.Options{
		UserDataDir: dir,
		Headless:    true,
		Proxy:       fmt.Sprintf("http://%s:%s@%s", proxy.user, proxy.pass, proxyAddr),
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer chrome.Close()

	tabCtx, closeTab, err := chrome.NewTab()
	if err != nil {
		t.Fatalf("new tab: %v", err)
	}
	defer closeTab()

	// A hostname rather than the loopback address: Chrome bypasses the proxy
	// for localhost, so pointing straight at 127.0.0.1 would pass whether the
	// credentials worked or not.
	_, port, _ := net.SplitHostPort(content)
	target := "http://content.invalid:" + port + "/"

	var body string
	err = chromedp.Run(tabCtx,
		chromedp.Navigate(target),
		chromedp.WaitVisible("h1", chromedp.ByQuery),
		chromedp.Text("h1", &body, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatalf("navigating through the proxy failed: %v\n"+
			"the proxy challenged %d requests and served %d",
			err, proxy.challenged.Load(), proxy.served.Load())
	}

	if !strings.Contains(body, marker) {
		t.Fatalf("page came from somewhere else: %q", body)
	}
	if proxy.served.Load() == 0 {
		t.Fatal("the page loaded without ever passing the proxy")
	}
}
