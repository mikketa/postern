# postern

**A captcha solver that drives a real Chrome instead of pretending to be one.**

No token farms, no paid captcha API, no headless browser dressed up to look human.
Postern launches the Chrome already installed on the machine, gives it a screen nobody is
looking at, renders the widget itself, and hands back the token.

A postern is the small side door of a fortress — the one you walk through instead of
attacking the wall.

```console
$ postern solve -url https://example.com/login -sitekey 0x4AAAAAAA...
0.qF8mZ2...9dK1
```

---

## What actually works

Measured, not asserted. Every row was run against the live service.

| Challenge | Result |
| --- | --- |
| **Turnstile**, production sitekey, managed mode | **5/5 tokens, ~3s each** |
| Same, through `serve`, 10 requests at concurrency 3 | **10/10 tokens, 13.7s total**, median 4s |
| **reCAPTCHA v3** | token, ~4s |
| **reCAPTCHA v2 invisible** | token, ~4s |
| **reCAPTCHA v2 checkbox**, no challenge served | token, ~5s |
| **reCAPTCHA v2 checkbox**, image challenge served | **fails, by design** — see below |

The one that does not work deserves the detail rather than a footnote. When Google
decides you are worth challenging, it puts up a grid of photographs, and postern has no
answer for that — it reports it in about nine seconds instead of burning the timeout. The
usual escape hatch is the audio challenge; on this setup Google refuses to serve it at
all, answering *"Your computer or network may be sending automated queries"*. So the
lever for reCAPTCHA v2 is not better automation, it is **reputation**: a profile with
history behind it, and an IP that is not a datacenter.

Turnstile, by contrast, is solved reliably, including in its managed mode.

## How it works

```
postern                                  Chrome — real binary, real window, virtual screen
   │                                       │
   │  1. navigate to the target page ─────►│   the origin the vendor sees is the real one
   │  2. add our own widget to it ────────►│   the vendor's api.js, our sitekey
   │  3. click, if nothing happens ───────►│   trusted pointer events, curved path
   │  4. poll window.__postern ◄───────────│   the widget callback parks the token there
   │                                       │
   ▼
 token
```

Four decisions carry the whole thing.

**The browser is genuine.** Not a spoofed user agent, not a patched headless build — the
real binary, launched with `--disable-blink-features=AutomationControlled` and without
the automation banner, reusing the same profile every run so it ages like a person's.

**It is windowed, not headless.** This is the one that mattered most, and it came from a
measurement. Against a production Turnstile sitekey, headless Chrome was refused **six
times out of six** — even with its user agent corrected, its GPU re-enabled and its
screen size fixed. The same binary driving a windowed Chrome on a virtual display was
accepted **seven times out of seven**. Headless is detectable by means that cannot be
enumerated, so postern stops being headless instead of patching symptoms one at a time:
it starts its own Xvfb, which shows nothing on screen either. `-headless` is still
there, and the fingerprint corrections still apply to it, but it is not the default and
the numbers say it should not be.

**The widget is ours.** Postern renders a fresh widget with the site's key rather than
hunting for the one on the page. Sites lay out their forms in a hundred different ways;
the widget APIs are identical everywhere. The container is *added* to the page rather
than replacing it — an earlier version wiped the document, which worked for Turnstile
and silently broke reCAPTCHA v3, whose `execute()` needs the elements the API quietly
created for itself.

**The pointer is real.** Interactive challenges wait for a checkbox to be ticked, and the
checkbox lives in a cross-origin iframe nothing on the page can reach into. Postern
clicks it from the outside: pointer events dispatched over CDP, so the page sees
`isTrusted`, following a curved path with easing and jitter rather than teleporting onto
the target. The click fires only once the widget has had a few seconds to solve itself,
and is retried up to three times, eight seconds apart.

## Requirements

- **Go 1.26+** to build — required by `chromedp`, not by Postern itself
- **Chrome or Chromium**
- **Xvfb**, unless you pass `-display host` or `-headless`
  (`xorg-server-xvfb` on Arch, `xvfb` on Debian)

## Install

```sh
go install github.com/mikketa/postern/cmd/postern@latest
```

From a clone:

```sh
go build -o postern ./cmd/postern
```

## Usage

### One shot

Token on stdout, nothing else — pipe it straight into whatever needs it.

```sh
postern solve -url https://example.com/login -sitekey 0x4AAAAAAA...
postern solve -kind recaptcha-v3 -url https://example.com -sitekey 6Lc... -action login
```

`-kind` takes `turnstile` (the default), `recaptcha-v2`, `recaptcha-v2-invisible` or
`recaptcha-v3`.

### As a local service

```sh
postern serve -addr 127.0.0.1:8099
```

The server binds to localhost and has **no authentication**. Keep it that way, or put
something in front of it.

One Chrome is shared across requests, and each solve gets its own window inside it —
which is both faster than starting a browser per solve (a warm browser solves in about
1.5s) and necessary: tabs sharing a window are backgrounded, and a page that is not
painted never runs its widget.

#### `POST /solve`

```sh
curl -s localhost:8099/solve -d '{
  "url": "https://example.com/login",
  "sitekey": "0x4AAAAAAA..."
}'
```

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `url` | string | yes | The page the widget belongs to — it decides the origin |
| `sitekey` | string | yes | Found in the target page markup |
| `kind` | string | no | Challenge kind, as above. Defaults to `turnstile` |
| `action` | string | no | Turnstile and reCAPTCHA v3; must match what the site uses |
| `cdata` | string | no | Turnstile only |
| `timeout_ms` | int | no | Overrides the server default for this request |

```json
{ "token": "0.qF8mZ2...9dK1", "elapsed_ms": 3140 }
```

Errors come back as `{"error": "..."}` with a `4xx`/`5xx` status.

#### `GET /health`

```json
{ "status": "ok" }
```

### Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `-profile` | `~/.config/postern/profile` | Chrome profile directory, reused across runs |
| `-display` | `virtual` | `virtual` starts an Xvfb of our own; `host` uses your session and is visible |
| `-headless` | `false` | Headless mode. Measurably more detectable — see above |
| `-screen` | `1920x1080` | Virtual screen size, `WxH`. The window is sized from it |
| `-chrome` | autodetect | Path to the Chrome binary |
| `-proxy` | none | Passed through to `--proxy-server` |
| `-timeout` | `60s` | Give up on a challenge after this long |
| `-concurrency` | `2` | *(serve)* solves running at the same time |
| `-addr` | `127.0.0.1:8099` | *(serve)* listen address |

## Testing

Cloudflare publishes dummy keys that work from any domain, including localhost.

| Sitekey | Behaviour |
| --- | --- |
| `1x00000000000000000000AA` | always passes, visible |
| `2x00000000000000000000AB` | always fails, visible |
| `1x00000000000000000000BB` | always passes, invisible |
| `2x00000000000000000000BB` | always fails, invisible |
| `3x00000000000000000000FF` | forces an interactive challenge, visible |

Be aware of what they do **not** exercise: they return a fixed `XXXX.DUMMY.TOKEN.XXXX`
with no risk analysis behind it — no fingerprint scoring, no behavioural checks — and
they render no iframe, only the container and the hidden field. They prove the plumbing
works. They say nothing about a production sitekey, which is why the table at the top of
this file was measured against one.

Validate a dummy token with the matching dummy secret:

```sh
token=$(postern solve -url https://example.com -sitekey 1x00000000000000000000AA)

curl -s https://challenges.cloudflare.com/turnstile/v0/siteverify \
  -d secret=1x0000000000000000000000000000000AA \
  -d response="$token"
```

Integration tests launch a browser and check what a page can see, including a fingerprint
test that pins down the headless corrections. They need Chrome, so `go test -short ./...`
skips them:

```sh
go test ./internal/... -v
```

## Running on a server

Postern runs fine on a headless Linux box — Chrome, Xvfb, roughly 500MB of RAM. Running
as root works without extra flags, since `--no-sandbox` is added automatically in that
case, though a dedicated user is the better idea.

Be aware of what a server takes back:

- **No GPU means software rendering.** Virtualised graphics adapters offer no 3D
  acceleration, so WebGL reports SwiftShader, which no desktop does. Faking the WebGL
  strings is not a fix: supported extensions, shader precision and raw rendering speed
  keep giving it away, so the override ends up more inconsistent than the thing it hid.
- **Datacenter IPs carry their own reputation**, and it weighs more than anything the
  browser does. Expect challenges to be served more often and to be harder from a hosting
  range than from a residential connection. `-proxy` exists for this reason.

## Scope

Postern exists for automating things you are allowed to automate: your own sites, your
own staging environments, end-to-end tests a challenge would otherwise block, and
research into how these challenges behave.

Don't point it at services whose terms you have not read, and don't use it to hammer
someone else's infrastructure.

## Contributing

The evasion layer is the part that will need the most maintenance, and it is plain
JavaScript in `internal/patches/` for exactly that reason. Files are embedded into the
binary in filename order and evaluated before any page script — you can add or fix one
without touching a line of Go.

Two rules for patches:

1. **Only fix what a real Chrome under automation actually gets wrong.** Verify it first.
2. **Make the override conditional.** Redefining a property that was already correct
   leaves a descriptor that does not match a stock browser, which is its own tell.

Adding a vendor means adding an entry to the registry in `internal/solver/provider.go`
and a bootstrap function that renders the widget and parks the result on
`window.__postern`. The solve loop does not change.

And the house rule for this repository: **claims come with measurements**. If you improve
the success rate, say against what, how many runs, and what it was before.

## License

[MIT](LICENSE)
