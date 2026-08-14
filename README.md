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
| **Turnstile**, dummy always-passes keys, visible and invisible | token, both |
| **Turnstile**, dummy `3x…FF` (forces an interactive challenge) | **no token**, and postern now says why: the vendor never rendered its frame |
| **reCAPTCHA v2 invisible** | token, ~4s |
| **reCAPTCHA v2 checkbox**, no challenge served | token, ~5s |
| **reCAPTCHA v2 checkbox**, image challenge served | **3/5 tokens**, ~1m10s–1m40s, with `solver-vision.py` |
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

What a solver *can* do about an address is leave from another one, which is what `-proxy`
is for, password included:

```sh
postern solve -proxy http://user:pass@host:port ...
```

Chrome cannot be given a proxy password on the command line — it drops it, gets `407` back
and raises a sign-in dialog nobody is there to answer, which surfaces as
`ERR_INVALID_AUTH_CREDENTIALS` and nothing else. Postern strips the credentials off the
flag, which also keeps them out of a world-readable `/proc`, and signs in over CDP instead.
This is plumbing, not a result: it is measured against a proxy that demands a password,
not against a claim about what any particular exit address is worth to Google.

The other half of reputation is the browser itself, and postern keeps a profile across runs
for it. Two things were wrong with that. Chrome was **killed rather than closed** — the
context was cancelled, which ends the process — so it never wrote `Default/Preferences`,
where a profile's settled state lives. Measured two runs each way: absent every time from a
killed browser, present every time from a closed one. And the profile had never been
anywhere, because nothing ever took it browsing. `postern warm` does:

```sh
postern warm -pages sites.txt          # one url per line, # comments ignored
```

It visits each page once, in a random order, scrolls it twice and stays a few seconds —
real pages fetched, real cookies set by the sites that set them, real history written. It
ships with **no built-in list**: a history that looks ordinary depends on where the solver
runs, and a list baked into the binary would be the same history for every copy of postern
in the world, which is a signal rather than the absence of one. Warming is its own command
because it belongs on a schedule, not in front of every token.

What that is worth is **not established**. Interleaved over four runs each, a warmed profile
and a fresh one were both served a picture challenge every single time — no difference at
all. That measurement was taken from an address that had already run dozens of solves that
day, where every arm is at the floor, so it says nothing about a warm profile on a clean
address. What can be said is narrower and worth saying anyway: the profile now keeps what a
profile is supposed to keep, which it demonstrably did not before.

Turnstile, by contrast, is solved reliably, including in its managed mode.

## Running it at volume

One browser answering every request is one identity, and an identity wears out.
Measured on this machine: three tokens in five early in an evening, none in five after about
twenty-five solves from the same address and profile, with no code change in between. Nothing
got worse at answering — the identity was used up.

So `serve` can work from a **fleet**. An identity is a profile and a way out, kept together
for life; the pairing matters, because a profile that browses from a different address every
time is stranger than either half alone.

```sh
cat > identities.txt <<'EOF'
# name    proxy — as your provider sells it, or as a url
alice     gate.example.com:8000:user-session-1:hunter2
bob       gate.example.com:8000:user-session-2:hunter2
carol     socks5://127.0.0.1:9050
EOF

postern serve -identities identities.txt -warm-pages sites.txt
```

Adding an identity is adding a line. Its profile is created next to the others, and it takes
itself browsing once before its first solve — nothing to set up by hand. Proxies are taken in
the `host:port:user:pass` form providers actually ship, so a supplier's list can be pasted in
as it arrives; a mistyped one is refused at startup rather than becoming an identity that
quarantines itself for no reason.

Two configurations defeat the whole exercise silently, so postern says so on startup: several
identities behind **one proxy** are one identity wearing several profiles, and several with
**no proxy at all** share this machine's address and will wear out exactly as fast as a single
identity would.

Three rules do the work:

| | |
| --- | --- |
| **Rest** | every identity waits a few minutes after a solve, spread so the fleet does not solve on one beat |
| **Quarantine** | three consecutive failures sets an identity aside for the best part of an hour, because a run of failures is almost never about the answers |
| **Memory** | how each identity has done is written to disk, so a restart does not hand a worn one a clean slate |

Throughput is therefore not how fast one solve is — it is roughly *identities ÷ rest*. Ten
identities resting four minutes each is about two solves a minute, indefinitely, and they
will still be working tomorrow. Asking for more than the fleet can rest through returns
**503 with `Retry-After`** rather than burning identities to keep up: that is the fleet
working, not failing.

A running fleet is watchable, which is what lets it be sized:

```console
$ curl -s localhost:8099/fleet | jq '.ready, .identities[] | {name, solves, failures, resting}'
```

The useful number is not the total solved but the ratio per identity. Identities failing
together is an address or a provider going bad; one failing alone is that profile burnt; and
a pool permanently at zero ready wants more identities rather than more patience. `/health`
carries the same ready-of-size count, so a monitor can tell "everything is resting" — which
is healthy — from "the server is broken". Neither endpoint returns the proxy, only whether
there is one: the identities file holds passwords.

Browsers are started per solve and closed afterwards, which costs a second and buys two
things — the profile is only written when Chrome is *closed*, and memory is bounded by how
many solves run at once rather than by how many identities exist.

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
the automation banner, reusing the same profile every run so it ages like a person's —
closed properly on the way out, and taken browsing by `postern warm`, without which
"ageing" was a word rather than a fact.

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
| `examples/solver-vision.py` | a detector first, then a trained head, then segmentation, then CLIP — whichever can answer what was asked |

Neither a detector nor CLIP is enough on its own, and the reason is the same both ways.
A detector answers "is there a bus here" because a bus was in its training labels; ask it
about a crosswalk, a staircase or a chimney — all of which reCAPTCHA asks about — and it
has no word for the question. CLIP has no list, so the category can come from the prompt
at runtime, but it scores how much a picture *looks like a sentence*, which on a grid of
street photographs is a question about the street. Measured on labelled grids of
crosswalks: a tile with no crossing in it scored 0.69 and a tile with one scored 0.48. No
threshold separates those.

So `solver-vision.py` asks whichever will actually answer, in that order:

| | Answers | Measured on grids checked by eye |
| --- | --- | --- |
| **RT-DETR on COCO** | buses, cars, bicycles, motorcycles, fire hydrants, parking meters, traffic lights — **73%** of what was served | exact on six grids of six |
| ↳ *as published, fixed at 640px* | the same | 3 short, 2 in excess over the same eight grids |
| **A trained head** | a category with no class anywhere, currently crosswalks — another **14%** | 1 crossing missed, none in excess, over a challenge it had never seen |
| **SegFormer on ADE20K** | bridges, mountains, stairs, palm trees, on a 4x4 | 1 short, 1 in excess |
| **CLIP** | anything at all, badly | roughly 4 ticks in excess per grid |

```sh
examples/install-vision.sh ~/.cache/postern-vision   # venv, models, wrapper

postern solve -kind recaptcha-v2 -url ... -sitekey ... \
    -image-solver ~/.cache/postern-vision/solve
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
over — `install-vision.sh` sets it per model, and `POSTERN_CLIP_CONFIDENCE` overrides it.
What does not change is postern: the grid is still read, clicked and submitted the same
way.

**The two layouts are different questions.** A 3x3 grid is nine separate photographs, and
each can be asked "is there a bus in this one" on its own. A 4x4 is *one* photograph cut
up, where a quarter of a bus fills four squares and none of them is a picture of a bus.
Asking each square what it is gets the middle of the object and misses its edges; asking
each square plus a margin of its neighbours picks up the empty tarmac beside it. Both are
wrong in the way that matters — ticks missing, ticks in excess, grid refused either way.

Which squares hold the bus is a question about *pixels*, so it is answered by a model that
labels pixels. SegFormer on ADE20K segments the grid, and the squares follow from the mask.
Measured over the saved 4x4 grids with blank captures excluded, squares ticked out of
sixteen:

| Method | Squares ticked (want 3-6) | On grids checked by eye |
| --- | --- | --- |
| Scoring each square | 8.7 — more than half the grid | 1 missing, 5 in excess |
| Covering squares up, greedily | 1.8 | — |
| **Segmentation** | **3.5** | **1 missing, 1 in excess** |

The bicycle and motorcycle grids come out exactly right. Covering squares up — grey a
square out, score the picture again, and the drop is how much of the answer was in there —
is a good idea that measures badly: it can only find a square whose covering changes what
the picture is *of*, and half a bus does not, so it stops early and ticks 1.8 squares.
`POSTERN_CLIP_LAYOUT=occlusion` still selects it.

The segmentation model is optional. Without `segment.onnx` beside the CLIP files the 4x4
path is not taken, and categories ADE20K does not have fall back to scoring squares, since
a mask of nothing is not an answer. B0 is what `install-vision.sh` fetches, at 15MB; B4 is
better (one tick in excess against five) but ships as PyTorch weights, and the conversion
is written out in that script.

**When nothing off the shelf knows the word.** Crosswalks were 14% of the challenges
served and no model answers them: COCO has no crosswalk, ADE20K has no crosswalk, an
open-vocabulary detector asked for "a zebra crossing" scores lane markings higher than
crossings, and CLIP ticks half the grid.

What works is not a bigger model but a hundred labelled tiles. Keep CLIP's picture
embedding, throw away its text side, and fit a logistic regression on top — the standard
linear probe, 512 numbers and a bias, seconds to train on a CPU:

```sh
postern solve ... -save-panels ~/panels     # keep every grid postern is served
# label them by eye: {"<panel name>": [0, 1, 7], ...}
python examples/train-probe.py ~/panels labels.json crosswalk \
    --models ~/.cache/postern-vision --hold <a panel series to test on>
```

**Most of those labels write themselves.** reCAPTCHA never says which square was wrong, but
it does say whether a challenge was right — a token means every answer in it was accepted.
So when a run with `-save-panels` produces a token, postern writes what it answered into
`labels.json` beside the panels, in exactly the shape above; answers from a challenge that
failed are dropped, since one wrong square fails the lot and there is no telling which. A
fleet left running therefore builds its own training set, and the categories it can already
answer pay for the ones it cannot. Labelling by eye is then for bootstrapping a category
from nothing, not for every grid.

Fitted on 21 tiles from 6 grids and measured on a three-round challenge from a later run
that shared no tile with it: **one crossing missed, nothing ticked in excess** — the miss
was a crossing half hidden behind a market stall — against roughly 4 in excess per grid for
zero-shot CLIP, which on that same grid ticked six squares where four were wanted. Three
things earn their keep here, each measured. Each head carries **the bar it should be read
at**, found by fitting without one grid at a time and keeping whichever bar costs fewest
mistakes — a number fixed in the solver cannot suit every head, and an earlier head that
missed nothing and ticked six squares in excess had that halved by its own bar. Every tile
is learned from twice, once mirrored — a crossing in a mirror is still a crossing, so the
label carries over for nothing. And **repeated tiles are thrown away**: a round only
replaces the squares you ticked, so a six-round challenge is six copies of the same
negatives around one or two new pictures, which buries the positives and lets a round be
scored on tiles it was fitted on. Measured on one challenge, dropping them took 3 ticks in
excess to 1, and it is worth labelling every round precisely because the repeats then cost
nothing.

A head is also **tied to the exact encoder it was fitted on**, which it records as a hash
of the file. Not the model name — that is not enough, and the gap is not theoretical: a
head reading 0.83 on a tile read 0.16 on the same tile under another export of the same
`patch16`, so it ticked nothing, passed on every crosswalk grid, and two live runs ended
asking for a category the solver was built to answer. Nothing looked wrong; the scores were
simply low. A head whose hash does not match the installed encoder is now ignored, loudly,
with the command to refit it. `examples/probe-crosswalk.json` is that head, fitted against
what `install-vision.sh` installs, and the script installs it. The weights only mean anything against the encoder they were fitted on, so
each head names its model and is ignored under any other.

That is the honest state of it: a category with a head is answered well, a category
without one is answered by CLIP and often wrong. The path from the second to the first is
a directory of saved panels and an evening of labelling.

**What was hard about this.** Most of these failures were silent — the challenge looked
answered and simply was not — and all of them are worth knowing about if you are building
something similar. Each was found by measuring rather than reasoning, and every one of
them was, at the time, comfortably blamed on the vision model.

*The panel stops appearing halfway, and stays there.* reCAPTCHA fades its panel in over the
page. Under a virtual display nothing composites a frame while the page sits idle, so the
fade freezes wherever it was when the last frame went out — and the screenshot catches a
grid at partial opacity with the form behind it showing through. Nothing in the document
says so: the pictures are loaded, the tiles are listed, the geometry is exact. Measured on
the demo page, the opening grid of every run came back like that, and a second screenshot
was never answered at all, because there was no new frame to answer with.

What fixes it is a change to the layout. Moving the pointer does not (no cursor is
composited), nor does scrolling a pixel, asking for a screencast, or overriding the page's
backdrop colour — all four were tried and measured. A view one pixel taller and back does,
and the panel is measured again afterwards because it does not always come back exactly
where it was. Ten grids out of ten came back clean after that, against one in six unusable
before.

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
| `-proxy` | none | Go out through this proxy; `user:pass@` is answered over CDP, not passed to Chrome |
| `-image-solver` | none | Command that answers picture grids; see [picture challenges](#picture-challenges) |
| `-save-panels` | none | Directory to keep every grid in, to calibrate a solver against later |
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

`3x00000000000000000000FF` is the exception and it does not pass — and the reason is worth
having, because it is not that the challenge was too hard. Measured: the widget is clicked
three times and **Turnstile never renders its own iframe at all**, so every click lands on
postern's container with nothing behind it. There was never anything on screen to answer.

Postern says that now rather than reporting a bare timeout:

    solver: no token after 40s — the widget was clicked 3 times but turnstile never
    rendered its own frame, so there was nothing on screen to answer.

Which is the useful message for any case where the vendor's script does not put a widget
up: a key like this one, a blocked script, a CSP that was not bypassed. The production
Turnstile sitekey in the table above is answered without ever reaching that state.

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
