# postern

**Cloudflare Turnstile solver that drives a real Chrome instead of pretending to be one.**

No token farms, no paid captcha API, no headless browser dressed up to look human.
Postern launches the Chrome already installed on the machine, with a profile that
persists between runs, renders the widget itself, and hands back the token.

A postern is the small side door of a fortress — the one you walk through instead of
attacking the wall.

```console
$ postern solve -url https://example.com/login -sitekey 0x4AAAAAAA...
0.qF8mZ2...9dK1
```

---

## How it works

```
postern                                 Chrome — your profile, automation flags stripped
   │                                      │
   │  1. navigate to the target page ────►│   the origin Cloudflare sees is the real one
   │  2. render our own widget ──────────►│   challenges.cloudflare.com/turnstile/v0/api.js
   │  3. click, if nothing happens ──────►│   trusted pointer events, curved path
   │  4. poll window.__postern ◄──────────│   the widget callback parks the token there
   │                                      │
   ▼
 token
```

Three decisions shape everything else:

**The browser is genuine.** Not a spoofed user agent, not a patched headless build — the
real binary, launched with `--disable-blink-features=AutomationControlled` and without
the automation banner, reusing the same profile every run so it ages like a person's.

It runs headless by default, and headless gives itself away in three specific places, so
each one is corrected at the source rather than papered over in JavaScript:

| Headless out of the box | Postern |
| --- | --- |
| `HeadlessChrome/151.0.0.0` in the user agent | the same UA with the token removed, read from the browser itself so it never goes stale |
| WebGL renderer is `SwiftShader` — software rendering | GPU re-enabled, so it reports the real adapter |
| `screen` is 800x600, and the viewport is exactly as tall as it | a real screen size, with a window shorter than the screen — no browser has zero UI |

`internal/browser/fingerprint_test.go` asserts all three. They were measured, not guessed.

**The widget is ours.** Postern renders a fresh Turnstile widget with the site's sitekey
rather than hunting for the one on the page. Sites lay out their forms in a hundred
different ways; the widget API is identical everywhere. It also means the widget sits at
coordinates we chose, which is what makes the next part possible.

**The pointer is real.** Interactive challenges wait for a checkbox to be ticked, and
the checkbox lives in a cross-origin iframe nothing on the page can reach into. Postern
clicks it from the outside: pointer events dispatched over CDP, so the page sees
`isTrusted`, following a curved path with easing and jitter rather than teleporting onto
the target. The click only fires once the widget has had a few seconds to solve itself —
most challenges never need it — and is retried up to three times, eight seconds apart,
because a click that lands while Cloudflare is still thinking is a click wasted.

The click aims at Cloudflare's own iframe when it is present, and at our container
otherwise, so it follows the widget if its size or position ever changes. The token is
read from the callback and, failing that, from the hidden `cf-turnstile-response` field
next to the widget: a widget that fills the field without firing the callback would
otherwise be indistinguishable from one that solved nothing.

The pleasant consequence is that there is very little left to patch. `internal/patches/`
is nearly empty on purpose — a clumsy override is a stronger fingerprint than whatever it
was meant to hide.

## Requirements

- **Go 1.26+** — required by `chromedp`, not by Postern itself
- **Chrome or Chromium**, any recent version

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
```

### As a local service

```sh
postern serve -addr 127.0.0.1:8099
```

The server binds to localhost and has **no authentication**. Keep it that way, or put
something in front of it.

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
| `action` | string | no | Required whenever the site sets one |
| `cdata` | string | no | Required whenever the site sets one |
| `timeout_ms` | int | no | Overrides the server default for this request |

```json
{ "token": "0.qF8mZ2...9dK1", "elapsed_ms": 3140 }
```

Errors come back as `{"error": "..."}` with a `4xx`/`5xx` status. A token obtained
without the `action` and `cdata` the site actually uses will be refused at validation
time, so pass them when they are there.

#### `GET /health`

```json
{ "status": "ok" }
```

### Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `-profile` | `~/.config/postern/profile` | Chrome profile directory, reused across runs |
| `-headless` | `true` | Run without a window. `-headless=false` for a windowed browser |
| `-screen` | `1920x1080` | Virtual screen size, `WxH`. The window is sized from it |
| `-chrome` | autodetect | Path to the Chrome binary |
| `-proxy` | none | Passed through to `--proxy-server` |
| `-timeout` | `60s` | Give up on a challenge after this long |
| `-concurrency` | `2` | *(serve)* solves running at the same time |
| `-addr` | `127.0.0.1:8099` | *(serve)* listen address |

## Testing without a target site

Cloudflare publishes dummy keys that work from any domain, including localhost. Use them
to check the pipeline end to end before pointing Postern at anything real.

| Sitekey | Behaviour |
| --- | --- |
| `1x00000000000000000000AA` | always passes, visible |
| `2x00000000000000000000AB` | always fails, visible |
| `1x00000000000000000000BB` | always passes, invisible |
| `2x00000000000000000000BB` | always fails, invisible |
| `3x00000000000000000000FF` | forces an interactive challenge, visible |

A dummy token is only accepted by a matching dummy secret — `1x0000000000000000000000000000000AA`
always validates, `2x0000000000000000000000000000000AA` never does. Production secrets
reject dummy tokens outright.

```sh
token=$(postern solve -url https://example.com -sitekey 1x00000000000000000000AA)

curl -s https://challenges.cloudflare.com/turnstile/v0/siteverify \
  -d secret=1x0000000000000000000000000000000AA \
  -d response="$token"
```

A green `"success": true` here means the whole chain works — browser, widget, callback,
and a token Cloudflare's own endpoint accepts.

Be aware of what the dummy keys do **not** exercise: they return a fixed
`XXXX.DUMMY.TOKEN.XXXX` with no risk analysis behind it — no fingerprint scoring, no
behavioural checks — and they render no iframe, only the container and the hidden field.
They prove the plumbing works. They say nothing about a production sitekey.

There are also integration tests that launch a browser and check what a page can see.
They need Chrome, so `go test -short ./...` skips them:

```sh
go test ./internal/... -v
```

Rough timings against the dummy keys, headless, warm profile:

| Sitekey | Outcome |
| --- | --- |
| `1x…AA` | token in ~2s |
| `3x…FF` | token in ~4s — 3s of that is the deliberate wait before clicking |
| `2x…AB` | fails fast with Turnstile error `600010`, no waiting for the timeout |

## Known limits

- **Tokens expire after 300 seconds.** Solve late, not early.
- **A strict CSP on the target page can block the widget script.** You get
  `api-script-blocked`; bypassing CSP over CDP is not wired up yet.
- **If the target URL is itself behind a full-page challenge**, navigation lands on the
  interstitial rather than the page. Point `-url` at something reachable on the origin.
- **This is one browser with a couple of tabs.** It is not built for volume and will not
  be.

## Running on a server

Postern runs fine on a headless Linux box — Chrome, roughly 500MB of RAM, nothing else.
Running as root works without extra flags, since `--no-sandbox` is added automatically in
that case, though a dedicated user is the better idea.

Be aware of what a server takes back, though:

- **No GPU means SwiftShader again.** Virtualised graphics adapters offer no 3D
  acceleration, so WebGL reports software rendering — one of the three things the
  headless setup above exists to avoid. Faking the WebGL strings is not a fix: supported
  extensions, shader precision and raw rendering speed keep giving it away, so the
  override ends up more inconsistent than the thing it hid.
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

Bug reports travel much better with the target URL, the sitekey, and the Chrome version.

## License

[MIT](LICENSE)
