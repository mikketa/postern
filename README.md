# postern

A Cloudflare Turnstile solver that drives a real Chrome instead of pretending to be one.

A postern is the small side door of a fortress — the one you walk through instead of
attacking the wall. That is roughly the approach here: no token farms, no paid captcha
API, no headless browser dressed up to look human. Postern launches the Chrome you
already have, with a profile that persists between runs, renders the widget itself, and
hands you the token.

## How it works

1. Chrome starts with a persistent profile and the automation flags stripped.
2. A tab navigates to the target page, so the origin Cloudflare sees is the real one.
3. Postern renders its own Turnstile widget with the site's sitekey, rather than trying
   to find and drive the page's widget. Sites lay out their forms in a hundred different
   ways; the widget API is the same everywhere.
4. The callback token is parked on the page and polled until it appears.

The interesting consequence: because the browser is genuine and the profile ages
normally, there is very little to patch. `internal/patches/` is nearly empty on purpose —
a clumsy override is a stronger fingerprint than the thing it was hiding.

## Requirements

- Go 1.24+ to build
- Chrome or Chromium installed

## Install

```sh
go install github.com/mikketa/postern/cmd/postern@latest
```

Or from a clone:

```sh
go build -o postern ./cmd/postern
```

## Usage

One shot, token on stdout:

```sh
postern solve -url https://example.com/login -sitekey 0x4AAAAAAA...
```

As a local service:

```sh
postern serve -addr 127.0.0.1:8099
```

```sh
curl -s localhost:8099/solve -d '{
  "url": "https://example.com/login",
  "sitekey": "0x4AAAAAAA..."
}'
```

```json
{ "token": "0.xxxxx...", "elapsed_ms": 3140 }
```

`action` and `cdata` are accepted too, and are required whenever the target site sets
them — a token obtained without them will be refused on validation.

### Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `-profile` | `~/.config/postern/profile` | Chrome profile directory, reused across runs |
| `-headless` | `false` | Run without a window. Headless is still distinguishable; leave it off when you can |
| `-chrome` | autodetect | Path to the Chrome binary |
| `-proxy` | none | Passed to `--proxy-server` |
| `-timeout` | `60s` | Give up on a challenge after this long |
| `-concurrency` | `2` (serve) | Solves running at once |

The server binds to localhost by default and has no authentication. Keep it that way, or
put something in front of it.

## Scope

This exists for automating things you are allowed to automate: your own sites, your own
staging environments, end-to-end tests that a challenge would otherwise block, and
research on how these challenges behave. It is deliberately a single browser with a
couple of concurrent tabs — it is not built for volume, and it will not be.

Don't point it at services whose terms you have not read, and don't use it to hammer
someone else's infrastructure.

## Contributing

The part that will need the most maintenance is the evasion layer, and it is plain
JavaScript in `internal/patches/` for exactly that reason — files are embedded in
filename order and evaluated before any page script, so you can add or fix one without
touching Go.

Two rules for patches:

- Only fix what a real Chrome under automation actually gets wrong. Check first.
- Make the override conditional. Redefining a property that was already correct leaves a
  descriptor that does not match a stock browser, which is its own tell.

Bug reports are more useful with the target URL, the sitekey, and the Chrome version.

## License

MIT
