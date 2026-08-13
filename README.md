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
| **Turnstile**, dummy interactive key, after the widget moved | token, 3.9s |
| **reCAPTCHA v2 invisible** | token, ~4s |
| **reCAPTCHA v2 checkbox**, no challenge served | token, ~5s |
| **reCAPTCHA v2 checkbox**, image challenge served | **3/5 tokens**, ~1m10s–1m40s, with `solver-clip.py` |
| Same, with `solver-yolos.py` | 2/5, measured in the following hour |
| Same, after ~25 solves from one address | **0/5** — see reputation, below |

The last three rows deserve the detail rather than a footnote. When Google decides you are
worth challenging, it puts up a grid of photographs. Postern drives that grid — finds it,
measures it, answers it in as many rounds as it takes, and submits — but the looking is
delegated to a command you nominate. With no solver configured at all, it reports the
challenge in about nine seconds rather than burning the timeout.

Read those three rows together, because they are the honest shape of this. They fell from
3/5 to 0/5 over an evening of testing from one home connection — and the builds in between
differed only by fixes that should have helped, every one of them verified separately.
Success here is mostly not about the solver.

The usual escape hatch, the audio challenge, is not one here: Google refuses to serve it
at all, answering *"Your computer or network may be sending automated queries"*. So the
other lever for reCAPTCHA v2 is **reputation** — a profile with history behind it, and an
IP that is not a datacenter — which is not something a solver can manufacture.

How much it dominates is visible in the runs. Counting the rounds each one took — a round
being one grid read, answered and submitted, six of which is one full pass of the solve
loop — twenty runs separate perfectly:

| Rounds in the run | Token |
| --- | --- |
| 6 | **5 out of 5** |
| 12 | **2 out of 2** |
| 2, 3, 4, 5, 8, 11, 13, 15, 17, 19, 20 | none, 0 out of 13 |

Every token came from a run that finished in exactly one or two full passes. Not one came
from a run that took some other number of rounds, which is not what "the model was right
more often" looks like — it is what a decision made before the pictures looked like.

This is also why campaigns run back to back are not comparable. After an hour of solving
from one address, the same build is served more rounds than it was at the start. Comparing
two solvers means interleaving their runs, not running one campaign after the other.

Which is worth stating plainly about the table above: the 3/5 and the 2/5 were measured in
consecutive windows, not interleaved, so they do not establish that one solver beats the
other. An interleaved A/B was started and abandoned — by then both arms were being cut off
at three rounds, which measures the address rather than either solver. The per-solver
comparisons that this file does make are the offline ones, on saved grids whose answers
were checked by eye.

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

## Picture challenges

Postern ships no vision model. Which one to use is not a decision a captcha solver should
make for you, and embedding one would drag a large dependency into a binary whose whole
appeal is that it has none. So the panel is captured, handed to a command you nominate,
and whatever that command says gets clicked:

```sh
postern solve -kind recaptcha-v2 -url ... -sitekey ... \
    -image-solver "python3 examples/solver-template.py"
```

The protocol is deliberately dumb — a solver is a twenty-line script:

| | |
| --- | --- |
| **argv** | the path to a PNG of the challenge panel, prompt included |
| **stdout** | one `x,y` per line, in pixels within that image; nothing means nothing to click |
| **exit 0** | answered |
| **exit 2** | cannot answer this one — postern asks for a different challenge |
| **other** | a failure, which ends the solve |

The difference between exit 0 with no coordinates and exit 2 is the difference between
"none of these are buses" and "I do not know what a crosswalk looks like". The first is
an answer worth submitting; the second is a guess that will be marked wrong. Saying so
gets you another grid instead, which is how a model that knows ten kinds of thing still
gets through a challenge that asks about twenty.

Postern passes what it already knows through the environment, so a solver does not have
to work it out from the picture:

| | |
| --- | --- |
| `POSTERN_PROMPT` | the instruction, as text |
| `POSTERN_COLUMNS` | 3 or 4, the width of the grid |
| `POSTERN_TILES` | `x,y,w,h;...` one per tile, in image pixels |

These are read out of the challenge document itself over CDP, not inferred from the
screenshot. It is the difference between OCR-ing a prompt and being told it, and between
counting tiles and being handed their rectangles. A solver may ignore all three and read
the picture alone — postern still clicks whatever comes back.

Three examples ship with it:

| | |
| --- | --- |
| `examples/solver-template.py` | the twenty lines, to build your own on |
| `examples/solver-yolos.py` | YOLOS-tiny, a detector: knows the eighty things COCO has words for, passes on the rest |
| `examples/solver-clip.py` | CLIP, zero-shot: the category comes from the prompt at runtime, so there is no list to fall off |

The difference matters more than model size. A detector answers "is there a bus here"
because a bus was in its training labels; ask it about a crosswalk, a staircase, a chimney
or a bridge — all of which reCAPTCHA asks about — and it has no word for the question.
CLIP scores a picture against a sentence, and postern already read the sentence out of the
challenge document, so the category is whatever was asked for this round.

```sh
examples/install-clip.sh ~/.cache/postern-clip     # venv, models, wrapper

postern solve -kind recaptcha-v2 -url ... -sitekey ... \
    -image-solver ~/.cache/postern-clip/solve
```

That script exists because "supply your own vision model" should be a line to copy, not an
afternoon. It is still an example: a larger CLIP, a fine-tune on captcha tiles or a hosted
model all plug into the same protocol, which is a PNG in and coordinates out.

**Changing the model is the biggest lever there is, and it is one line.** reCAPTCHA
degrades its photographs on purpose, and ViT-B/32 hits a wall on the worst of them: on a
grid of cars, no tile scored above **0.20** — nothing to click, on a grid full of cars.
Pointing the same script at ViT-B/16 instead, by fetching that model into the directory it
reads:

| | ViT-B/32 | ViT-B/16 |
| --- | --- | --- |
| That grid of cars | nothing above 0.20 | seven tiles over the threshold |
| A bus, on the tile holding it | 0.54 | **0.94** |

A sharper model is also more confident about everything, so the threshold does not carry
over — set `POSTERN_CLIP_CONFIDENCE` when you change models. What does not change is
postern: the grid is still read, clicked and submitted the same way.

Things that were tried and did not work, so you can skip them: **median-filtering the
noise out** before scoring lifts the best score on a bad grid from 0.20 to 0.25 and costs
more than it gains — across the saved panels it took grids with something to click from 33
down to 30, because it removes as much signal as noise. And **swapping the negatives** —
suspecting "a photo of an empty road" of absorbing a grid of parked cars — moves the same
number from 0.20 to 0.25. Neither is the problem. The model is.

**What was hard about this.** Most of these failures were silent — the challenge looked
answered and simply was not — and all of them are worth knowing about if you are building
something similar. Each was found by measuring rather than reasoning, and every one of
them was, at the time, comfortably blamed on the vision model.

*The verify button is often not where the browser says it is.* reCAPTCHA lays its panel
out taller than the space it gives it, and the buttons end up below a container that clips
them: `getBoundingClientRect` returns a perfectly plausible rectangle, nothing is painted
there, and the click lands on the page behind. A challenge answered correctly then sits
untouched, ticks and all — indistinguishable from a wrong answer. Postern asks the document
what is *actually* at that point, and falls back to focusing the button and pressing Enter.
Widening the frame does not help; the clipping is inside the document.
`TestVerifyReachesTheButton` fails if a submission stops registering.

*The challenge frame lives in another process.* Everywhere except Google's own demo, the
panel is a cross-site iframe, which Chrome runs on its own and which is invisible to the
page's session — it shows up as an empty `about:blank` in the frame tree. It has to be
reached as a separate target instead. Worse, chromedp tears such an attachment down by
closing the target, and closing a frame's target closes the page holding it: postern was
shutting its own tab, on every site but the demo. The attachment is now made once and kept
for the life of the tab.

*A tile is a toggle, and postern was clicking its own answers off.* Naming a tile that is
already ticked unticks it. On a dynamic grid the solver names the same still-correct tile
every round, so postern alternated between selecting and deselecting it until the rounds
ran out — one run spent all six on tile 6 of a bridge challenge, the score alternating
between two values because the same two pictures kept coming back. Tiles already ticked
are now left alone. The signature is worth remembering: runs that finish take six grids,
runs stuck in this take eight to nineteen.

*Chrome does not acknowledge an input event until it has drawn.* `Input.dispatchMouseEvent`
replies only once the renderer under the pointer has processed it — measured at **43
seconds for a single click** on a page with one link on it, while that same page answered
every other command instantly. The event is delivered when the command is sent, so postern
does not wait for the reply. A click went from 43s to 740ms, and a picture challenge from
two or three grids in three minutes to six or eight in two.

*The reload button does not promise a different grid.* Pressing it asks; reCAPTCHA is free
to hand back what it just showed you. Postern pressed and carried on, so a grid the solver
had nothing for came straight back — seventeen identical rounds, the same score to two
decimal places, until the budget ran out. Two refusals in a row now end the solve with the
reason. Failing in twenty seconds beats failing in two minutes.

*An expired challenge is not a refusal.* reCAPTCHA gives a challenge a couple of minutes,
and a grid answered over several rounds can outlast it. The page's own remedy is the
widget's "please try again", so that is what postern does now rather than reporting a
failure the browser never hit.

*What is left really is the model.* Postern answers the challenge; something has to
recognise a bicycle in a deliberately degraded 100-pixel photograph. Run with `-v` to see
which prompt came up, what your solver made of it, and what the panel objected to.

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
| `-image-solver` | none | Command that answers picture grids; see [picture challenges](#picture-challenges) |
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

## Reputation, which decides more than the solver does

Everything above is about making one solve work. This is about making the hundredth work,
and it is where reCAPTCHA v2 is actually won or lost — the evening that produced the
numbers at the top of this file ended at 0/5 with a better solver than it started at 3/5
with.

What postern does on its own:

- **The profile is kept.** `-profile` defaults to a stable directory precisely so cookies,
  history and the vendor's own `_GRECAPTCHA` accumulate. A fresh profile per solve throws
  that away every time, and it is the single easiest way to make postern look worse than
  it is — which is exactly the mistake the measurements above were first made with.
- **It stops when it is no longer being graded.** Past twelve picture grids in one solve,
  postern gives up and says the address is what is being refused, rather than spending the
  rest of the timeout and reporting "no token" as though the answers were wrong.

What it cannot do for you, and there is no clever way around either:

- **Where the requests come from.** A residential address is worth more than any
  fingerprint work. `-proxy` takes one.
- **How fast you ask.** Twenty-five solves in an evening from one address was enough to go
  from 3/5 to nothing, on a home connection. Spread the work, or spread the addresses.

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

CI runs `gofmt`, `go vet`, `go build` and the short tests under `-race` on every push, and
the browser-driving tests separately — those talk to the vendors' live demos, so they
report rather than block: a run that is waved through without a challenge has not tested
anything.

## License

[MIT](LICENSE)
