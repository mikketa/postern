<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/logo-dark.svg">
    <img src="docs/logo-light.svg" alt="postern" width="112" height="112">
  </picture>
</p>

<h1 align="center">postern</h1>

<p align="center">
  <strong>A captcha solver that drives a real Chrome instead of pretending to be one.</strong>
</p>

<p align="center">
  <a href="https://github.com/mikketa/postern/actions/workflows/ci.yml"><img src="https://github.com/mikketa/postern/actions/workflows/ci.yml/badge.svg" alt="ci"></a>
  <img src="https://img.shields.io/badge/go-1.26%2B-00ADD8" alt="go 1.26+">
  <img src="https://img.shields.io/badge/license-MIT-blue" alt="MIT">
</p>

```console
$ postern solve -url https://example.com/login -sitekey 0x4AAAAAAA...
0.qF8mZ2...9dK1
```

No token farm. No paid captcha API. No headless browser dressed up to look human.
Postern launches the Chrome already on the machine, gives it a screen nobody is looking
at, renders the widget itself, and hands back the token.

> [!NOTE]
> A postern is the small side door of a fortress — the one you walk through instead of
> attacking the wall.

## What works

Every row was run against the live service. Nothing here is an estimate.

| Challenge | Result |
| --- | --- |
| **Turnstile**, production sitekey, managed mode | **5/5 tokens**, ~3s each |
| Same, through `serve`, 10 requests at concurrency 3 | **10/10 tokens**, 13.7s total, median 4s |
| **reCAPTCHA v2 checkbox**, image challenge served | **8/8 tokens**, 16s median |
| **reCAPTCHA v2 checkbox**, no challenge served | token, ~5s |
| **reCAPTCHA v2 invisible** | token, ~4s |
| **reCAPTCHA v3** | token, ~4s |

The image-challenge row needs a vision model, which postern does not ship. That number is
with `examples/solver-vision.py`.

## Install

```sh
go install github.com/mikketa/postern/cmd/postern@latest
```

You also need **Chrome or Chromium**, and **Xvfb** unless you pass `-display host` or
`-headless` (`xorg-server-xvfb` on Arch, `xvfb` on Debian).

## Use it

### One shot

The token goes to stdout and nothing else does, so it pipes straight into whatever needs it.

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

```sh
curl -s localhost:8099/solve -d '{
  "url": "https://example.com/login",
  "sitekey": "0x4AAAAAAA..."
}'
```

```json
{ "token": "0.qF8mZ2...9dK1", "elapsed_ms": 3140 }
```

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `url` | string | yes | The page the widget belongs to — it decides the origin |
| `sitekey` | string | yes | Found in the target page markup |
| `kind` | string | no | As above. Defaults to `turnstile` |
| `action` | string | no | Turnstile and reCAPTCHA v3; must match what the site uses |
| `cdata` | string | no | Turnstile only |
| `timeout_ms` | int | no | Overrides the server default for this request |

Errors come back as `{"error": "..."}` with a `4xx`/`5xx` status. `GET /health` returns
`{"status": "ok"}`.

> [!WARNING]
> `serve` binds to localhost and has **no authentication**. Keep it on localhost, or put
> something in front of it.

## Picture challenges

Sooner or later reCAPTCHA puts up a grid of photographs. Postern drives that grid — finds
it, measures it, answers it in as many rounds as it takes, submits it — but the *looking*
is delegated to a command you nominate. Which model to use is not a decision a captcha
solver should make for you.

```sh
postern solve -kind recaptcha-v2 -url ... -sitekey ... \
    -image-solver "python3 examples/solver-template.py"
```

The protocol is deliberately dumb. A solver is a twenty-line script:

| | |
| --- | --- |
| **argv** | path to a PNG of the panel, prompt included |
| **stdout** | one `x,y` per line, in pixels within that image; nothing means nothing to click |
| **exit 0** | answered |
| **exit 2** | cannot answer this one — postern asks for a different challenge |
| **other** | a failure, which ends the solve |

Postern also passes `POSTERN_PROMPT`, `POSTERN_COLUMNS` and `POSTERN_TILES` through the
environment, read out of the challenge document itself rather than guessed from the
screenshot. A solver may ignore all three.

Three examples ship with it: `solver-template.py` to build on, `solver-yolos.py` (a plain
detector), and `solver-vision.py`, which scores **48 of 48** grids on the bench.

One process per grid is the right protocol for a twenty-line script and the wrong one for
600MB of models, so `solver-vision.py` keeps a copy of itself resident and answers over a
socket. **2.49s a grid becomes 1.30s.** Postern is not involved: it still runs a command
that takes a PNG and prints coordinates.

> [!TIP]
> `examples/install-vision.sh -export` sets up `solver-vision.py` and its models in one
> command. Without `-export` you get a working install that scores 38 of 48 — the two
> models that close the gap have to be exported locally.

**[Read the long version →](docs/picture-challenges.md)** — why a detector alone is not
enough, why CLIP alone is worse, how the trained head was fitted, and every idea that was
measured and thrown away.

## Running it at volume

One browser answering every request is one identity, and an identity wears out. So `serve`
can work from a fleet. An identity is a profile and a way out, kept together for life.

```sh
cat > identities.txt <<'EOF'
# name    proxy — as your provider sells it, or as a url
alice     gate.example.com:8000:user-session-1:hunter2
bob       gate.example.com:8000:user-session-2:hunter2
carol     socks5://127.0.0.1:9050
EOF

postern serve -identities identities.txt -warm-pages sites.txt
```

Adding an identity is adding a line. Its profile is created next to the others, and it
takes itself browsing once before its first solve. Three rules do the work:

| | |
| --- | --- |
| **Rest** | every identity waits a few minutes after a solve, spread so the fleet does not solve on one beat |
| **Quarantine** | three failures in a row sets an identity aside for the best part of an hour |
| **Memory** | how each identity has done is written to disk, so a restart does not hand a worn one a clean slate |

> [!TIP]
> Throughput is not how fast one solve is. It is roughly **identities ÷ rest** — ten
> identities resting four minutes each is about two solves a minute, indefinitely. Ask for
> more than the fleet can rest through and you get **503 with `Retry-After`**, which is the
> fleet working rather than failing.

`GET /fleet` shows what each identity has done. The useful number is the ratio per
identity: several failing together is an address going bad, one failing alone is that
profile burnt.

> [!IMPORTANT]
> Several identities behind one proxy are one identity wearing several profiles, and
> several with no proxy at all share this machine's address. Postern says so at startup,
> because both configurations quietly defeat the whole exercise.

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

**The browser is genuine.** The real binary, launched without the automation banner,
reusing the same profile every run so it ages like a person's — and closed properly on the
way out, without which the profile never actually aged.

**It is windowed, not headless.** This one came from a measurement. Against a production
Turnstile sitekey, headless Chrome was refused **6 times out of 6**, even with its user
agent corrected, its GPU re-enabled and its screen size fixed. The same code driving a
windowed Chrome on a virtual display was accepted **7 times out of 7**. So postern stops
being headless instead of patching symptoms one at a time. It starts its own Xvfb, which
shows nothing on screen either.

**The widget is ours.** Postern renders a fresh widget with the site's key rather than
hunting for the one on the page. Sites lay out their forms a hundred ways; the widget APIs
are identical everywhere.

**The pointer is real.** The checkbox lives in a cross-origin iframe nothing on the page
can reach into, so postern clicks it from the outside — pointer events over CDP, so the
page sees `isTrusted`, on a curved path with easing and jitter rather than teleporting onto
the target.

## Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `-profile` | `~/.config/postern/profile` | Chrome profile directory, reused across runs |
| `-display` | `virtual` | `virtual` starts an Xvfb of our own; `host` uses your session and is visible |
| `-headless` | `false` | Headless mode. Measurably more detectable — see above |
| `-screen` | `1920x1080` | Virtual screen size, `WxH`. The window is sized from it |
| `-chrome` | autodetect | Path to the Chrome binary |
| `-proxy` | none | Go out through this proxy; `user:pass@` is answered over CDP, not passed to Chrome |
| `-image-solver` | none | Command that answers picture grids |
| `-save-panels` | none | Directory to keep every grid in, to calibrate a solver against later |
| `-timeout` | `60s` | Give up on a challenge after this long |
| `-concurrency` | `2` | *(serve)* solves running at the same time |
| `-addr` | `127.0.0.1:8099` | *(serve)* listen address |

## Testing

Cloudflare publishes dummy keys that work from any domain, including localhost:
`1x00000000000000000000AA` always passes and `2x00000000000000000000AB` always fails.

> [!NOTE]
> Dummy keys return a fixed `XXXX.DUMMY.TOKEN.XXXX` with no risk analysis behind it. They
> prove the plumbing works and nothing else, which is why the table at the top was measured
> against a production sitekey.

```sh
go test -short ./...   # no browser
go test ./internal/...  # launches Chrome
```

## On a server

Postern runs fine on a headless Linux box: Chrome, Xvfb, roughly 500MB of RAM. A dedicated
user is a better idea than root. Two things a server takes back:

- **No GPU means software rendering.** WebGL reports SwiftShader, which no desktop does.
  Faking the strings is not a fix — extensions, shader precision and raw speed keep giving
  it away.
- **Datacenter IPs carry their own reputation.** Expect challenges more often, and harder,
  than from a residential connection. `-proxy` exists for this.

## Reputation

One solve is a browser problem. The hundredth is a reputation problem: the same code that
gets a token in the evening gets none at midnight from the same address. Postern has three
answers to that, and they are what makes it hold up over a run rather than over a demo.

**A profile that ages.** Kept across runs and closed properly on the way out, so it keeps
what a profile is supposed to keep. `postern warm -pages sites.txt` takes it browsing, on a
schedule rather than in front of every token.

**A way out that isn't yours.** `-proxy` takes one, password included. Chrome drops proxy
credentials given on the command line and raises a sign-in dialog nobody is there to
answer, so postern strips them off the flag — which also keeps them out of a world-readable
`/proc` — and signs in over CDP instead.

**A fleet instead of an identity.** Rest, quarantine and memory, as above.

> [!IMPORTANT]
> Before blaming reputation, check that your own clicks are landing. One evening went from
> 4 tokens in 10 to **10 in 10** on the same address the moment a misaimed click was found:
> the grids had been answered correctly all along, and the answers were being dispatched off
> screen.

**[The whole trail →](docs/reputation.md)** — what the fleet measured, what a stock-Chrome
control proved, and how a wrong conclusion survived a day of careful measurement.

## Scope

Postern exists for automating things you are allowed to automate: your own sites, your own
staging environments, end-to-end tests a challenge would otherwise block, and research into
how these challenges behave.

> [!CAUTION]
> Don't point it at services whose terms you have not read, and don't use it to hammer
> someone else's infrastructure.

## Contributing

The evasion layer is the part that needs the most maintenance, and it is plain JavaScript
in `internal/patches/` for exactly that reason. Files are embedded in filename order and
run before any page script, so you can add or fix one without touching a line of Go.

Two rules for patches:

1. **Only fix what a real Chrome under automation actually gets wrong.** Verify it first.
2. **Make the override conditional.** Redefining a property that was already correct leaves
   a descriptor that does not match a stock browser, which is its own tell.

Adding a vendor means an entry in the registry in `internal/solver/provider.go` and a
bootstrap function that renders the widget. The solve loop does not change.

And the house rule: **claims come with measurements**. If you improve the success rate, say
against what, how many runs, and what it was before.

## License

[MIT](LICENSE)
