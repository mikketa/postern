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
   │  3. poll window.__postern ◄──────────│   the widget callback parks the token there
   │                                      │
   ▼
 token
```

Two decisions shape everything else:

**The browser is genuine.** Not a spoofed user agent, not a patched headless build — the
real binary, launched with `--disable-blink-features=AutomationControlled` and without
the automation banner, reusing the same profile every run so it ages like a person's.

**The widget is ours.** Postern renders a fresh Turnstile widget with the site's sitekey
rather than hunting for the one on the page. Sites lay out their forms in a hundred
different ways; the widget API is identical everywhere.

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
| `-headless` | `false` | Run without a window. Headless is still distinguishable — leave it off when you can |
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

## Known limits

- **Tokens expire after 300 seconds.** Solve late, not early.
- **A strict CSP on the target page can block the widget script.** You get
  `api-script-blocked`; bypassing CSP over CDP is not wired up yet.
- **If the target URL is itself behind a full-page challenge**, navigation lands on the
  interstitial rather than the page. Point `-url` at something reachable on the origin.
- **This is one browser with a couple of tabs.** It is not built for volume and will not
  be.

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
