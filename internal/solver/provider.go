package solver

import (
	"fmt"
	"sort"
	"strings"
)

// Kind names a challenge vendor and mode. The wire strings are part of the CLI
// and HTTP API, so they are lowercase and hyphenated.
type Kind string

const (
	Turnstile        Kind = "turnstile"
	RecaptchaV2      Kind = "recaptcha-v2"
	RecaptchaInvis   Kind = "recaptcha-v2-invisible"
	RecaptchaV3      Kind = "recaptcha-v3"
	defaultKindValue      = Turnstile
)

// provider is everything that differs between vendors. The solve loop itself —
// navigate, poll, click, give up — is shared, because it is the same job every
// time: put a widget on the page and wait for it to hand over a token.
type provider struct {
	// tokenField is the hidden input the vendor writes the token into, polled
	// as a fallback for a callback that never fires.
	tokenField string

	// frameHost identifies the vendor's iframe among the ones on the page, so
	// clicks land on the real widget rather than on our container.
	frameHost string

	// clickable is false for flows with nothing to click: a v3 execute, or an
	// invisible widget driven entirely from JavaScript.
	clickable bool

	// bootstrap builds the in-page script that renders the widget and parks
	// the result on window.__postern.
	bootstrap func(Request) (string, error)
}

// providers is the registry. Adding a vendor means adding an entry and a
// bootstrap function — the loop does not change.
var providers = map[Kind]provider{
	Turnstile: {
		tokenField: "cf-turnstile-response",
		frameHost:  "challenges.cloudflare.com",
		clickable:  true,
		bootstrap:  turnstileBootstrap,
	},
	RecaptchaV2: {
		tokenField: "g-recaptcha-response",
		frameHost:  "google.com/recaptcha",
		clickable:  true,
		bootstrap:  recaptchaV2Bootstrap,
	},
	RecaptchaInvis: {
		tokenField: "g-recaptcha-response",
		frameHost:  "google.com/recaptcha",
		clickable:  false,
		bootstrap:  recaptchaInvisibleBootstrap,
	},
	RecaptchaV3: {
		tokenField: "",
		frameHost:  "",
		clickable:  false,
		bootstrap:  recaptchaV3Bootstrap,
	},
}

// lookup resolves a Kind, defaulting to Turnstile when none was given.
func lookup(k Kind) (provider, error) {
	if k == "" {
		k = defaultKindValue
	}
	p, ok := providers[k]
	if !ok {
		return provider{}, fmt.Errorf("solver: unknown challenge kind %q, want one of %s",
			k, strings.Join(Kinds(), ", "))
	}
	return p, nil
}

// Kinds lists the supported challenge kinds, for help text and errors.
func Kinds() []string {
	names := make([]string, 0, len(providers))
	for k := range providers {
		names = append(names, string(k))
	}
	sort.Strings(names)
	return names
}
