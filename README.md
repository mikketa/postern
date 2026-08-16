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
| **reCAPTCHA v2 checkbox**, image challenge served | **10/10 tokens**, 21s median, with `solver-vision.py` at 48/48 on the bench |
| Same, before the parked-panel fix | 4/10, 72–85s each |
| Same, an earlier build of the same solver | 3/5, then 2/5 with `solver-yolos.py` an hour later |

The last three rows deserve the detail rather than a footnote. When Google decides you are
worth challenging, it puts up a grid of photographs. Postern drives that grid — finds it,
measures it, answers it in as many rounds as it takes, and submits — but the looking is
delegated to a command you nominate. With no solver configured at all, it reports the
challenge in about nine seconds rather than burning the timeout.

Read those rows together, because the story they tell is not the one they were first written
to tell. Token rates fell from 3/5 to 0/5 to 4/10 over an evening of testing from one home
connection, and every explanation offered here for a long time was external: a worn address,
a shallow profile, a reputation ceiling.

It was a bug. Between rounds reCAPTCHA parks its panel off screen, the panel's own document
does not change when it happens, and postern was clicking anyway — at `y=-9448`, on nothing.
The grids were answered correctly and the answers were thrown out of the window; the endless
challenge that looked like an untrusted address was postern never submitting. Fixing it took
the same address, the same evening, from 4/10 to **10/10**, six of them waved through after a
single grid. The full trail is in "the control that should have been run first", below,
because how the wrong conclusion survived so long is the more useful part.

Two earlier failures were genuine vision mistakes, and both are the same shape — an object at
the edge of a square. One was a grid of cars noised almost to static, served four times
over, where the solver ticked nothing: it does hold cars, a row of them parked along the
bottom tenth of one tile, which took a median filter and a seven-fold enlargement before I
could see them myself. The other was a 4x4 of a coach filling the middle of the frame, where
the solver ticked two squares the bus only grazes at the bumper. Missing a tenth of a tile
and claiming a tenth of a tile are the two ends of one threshold, they are what the bench
measures, and moving it is a decision to make on 48 grids rather than on the one that just
failed.

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
from a run that took some other number of rounds.

That was read here, for a while, as a decision taken before the pictures — the grid as
theatre. A later run says otherwise, and says it with the panels saved. A thirteen-round run
that produced nothing was served three crosswalk grids; on all three the head scored the
plainest crossing in the grid — full-width white bars across the road — at 0.30 to 0.34
against its bar of 0.40, and missed it every time. On a dynamic grid a miss is not a partial
answer, it is a failed one: the challenge comes back, and it comes back again. Thirteen
rounds is what a solver that misses one square looks like from the outside.

That crossing is now scored 0.58. What changed was not the corpus — see below for the
measurement that ruled it out — but which export of the encoder the head reads against.

So the round count is a symptom, not a verdict, and it does not separate the causes. A third
one turned out to be underneath most of it: a submitted answer that never arrived, because
the click was aimed at a panel reCAPTCHA had parked off screen. That is why runs piled up
rounds without ever being told they were wrong — nothing had been said. With it fixed, ten
consecutive runs took a median of one round and every one produced a token.

What survives of this table is narrower than it looked: a run that answers everything *and
manages to submit it* finishes in one or two rounds. Everything else arrives as more rounds,
and "more rounds" was never evidence about the address.

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

**Warming is the expensive part, by two orders of magnitude.** Measured through a counting
proxy — the total both ways through the tunnel, which is what a metered provider bills:

| | traffic |
|---|---|
| Chrome itself, fresh profile, one trivial page | 0.1 MB |
| One full solve attempt, run to a 2m30 timeout | **1.1 MB** |
| `warm` over four google.com pages | 42.9 MB |
| `warm` over youtube.com | 6.4 MB |
| `warm` over an 18-page list | **99.8 MB** |

So a browser is not what costs bandwidth here, and neither is solving: an entire attempt
that ground through grid after grid until it timed out cost about a hundredth of warming
the profile that made it. Chrome's own startup is negligible — the whole bill is the pages
you choose. Search pages are the heaviest thing on that list at roughly 10 MB each, and
also the ones that matter most for a Google challenge, so the trade is real rather than
free: warming on two Google pages instead of eighteen mixed ones costs a fifth as much and
leaves the cookies that count. Pick the list with the meter in mind if there is one.

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

**Two of those models have to be built rather than downloaded**, and it is worth ten grids
of the bench below — 38 of 48 against 48 of 48, same solver, same everything else. What the
hub has is a detector that refuses any input but 640x640 (see below) and a segmenter one
size down from the one this wants. `examples/export-models.py` builds both from the PyTorch
weights, and the install script runs it when asked:

```sh
examples/install-vision.sh -export ~/.cache/postern-vision
```

That costs 430MB of models and a 1.4GB virtualenv of CPU-only PyTorch to produce them,
which nothing needs afterwards — so it is a flag rather than the default, and the solver
works without it. Re-running is free: each model is checked for what it must be able to do
before anything is built.

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

**The 3x3 grids get the same question, one tile at a time.** A tile is a photograph of its
own, so the mask is asked of each separately and any share of the class at all counts —
these are the categories CLIP is worst at, and a bridge two hundred metres off is a handful
of pixels. Over 54 tiles of bridges and hills it found the class in nine squares that hold
one and in none that do not, which is what makes a floor that low safe.

The segmentation model is optional. Without `segment.onnx` beside the CLIP files neither
path is taken, and categories ADE20K does not have fall back to scoring squares, since a
mask of nothing is not an answer. B0 is what `install-vision.sh` fetches without `-export`,
at 15MB; B4 is better and is what `-export` builds — one tick in excess against five, and
over those bridge and hill grids B0 found every hill and never once predicted the bridge
class, which is in its vocabulary. On the bench that is one grid, 47 against 48.

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

**Half of those labels write themselves.** When a run with `-save-panels` produces a token,
postern writes the squares it ticked into `answers.json` beside the panels, and
`train-probe.py --answers` folds them in.

Half, and not more, because of what a token actually vouches for. It grades the squares that
were ticked — not the ones that were not. Measured on a live run: a bus challenge handed over
a token with a school bus sitting unticked in square 7 of two consecutive rounds. Recording
that grid as "no bus here" would teach a head that a school bus is not a bus, which is worse
than having no data at all. So the file holds positives only and makes no claim about the
rest, and a round where nothing was ticked is not recorded — it means the solver saw nothing,
not that there was nothing.

A fleet left running therefore accumulates confirmed examples of what things look like, and
the categories it can already answer pay for the ones it cannot. Negatives still have to come
from a person looking at a grid, which is what the by-eye labels above are for.

Fitted on 32 tiles from 12 grids. Held out against a three-round challenge from a later run,
the version fitted on six grids missed one crossing and ticked three squares in excess — and
what it missed was not a hard case but the plainest crossing in the grid, full-width white
bars across the road, scored 0.30 to 0.34 against its bar of 0.40. That is worth stating
because of what it costs: on a dynamic grid a missed square is a wrong answer, the challenge
comes back, and a run that should have taken six rounds took thirteen and produced nothing.
Those three grids are now in the corpus and are answered exactly, which is learning the case
rather than generalising to it; the honest number is the calibration, four mistakes across
twelve grids each left out in turn. Against roughly 4 in excess per grid for
zero-shot CLIP, which on one such grid ticked six squares where four were wanted. Three
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

**How any of this gets judged.** A live run measures the solver, the address, Google's
opinion of that address and the time of night all at once, and reports one bit at the end.
Six runs in one evening, with nothing changed between them, produced 0, 1 and 2 tokens out
of three — a spread that would swallow any change worth making. Two separate conclusions
were nearly drawn from three-run samples before that was noticed.

So `examples/bench.py` scores a solver against saved grids whose answers were written down
by eye:

```sh
python examples/bench.py ~/panels labels.json --solver ~/.cache/postern-vision/solve
```

Deterministic, no network, seconds rather than minutes, and it reports **grids answered
exactly** — because that is what reCAPTCHA grades. A grid with one square missed is not 89%
right; it is wrong, and on a dynamic grid it brings the challenge back. Missed and excess
squares are reported alongside, since they say which way a solver is wrong and are what a
confidence threshold trades between.

Built from 48 real grids spanning what reCAPTCHA actually serves — bicycles, motorcycles,
cars, buses, hydrants, traffic lights, crosswalks, bridges, hills — in both 3x3 and 4x4, and
deduplicated on the pixels of the grid, since reCAPTCHA serves the same challenge over and
over and a bench that counts one grid four times is reporting its own repetition.

The first thing it reported was that misses outnumbered excess four to one — 33 squares
missed against 8 over. That is not a solver confused about what a bicycle is; it is one
failing to see them, and it pointed at how the pictures were being handed over rather than at
the models. The detector only accepts 640x640, so a 96px tile asked on its own was stretched
nearly seven times first. Measured on one bicycle in a dark porch, same model, same weights:

| what the model was shown | scored |
| --- | --- |
| the tile stretched to 640 | 0.026 |
| the tile laid on a 640 field at its own size | 0.234 |

Laying the tile down instead of stretching it took the example solver from **21 of 48 grids
exact to 29**, and the misses from 33 to 19. No new model, no download — the model was never
blind, it was being handed a seven-fold enlargement of a thumbnail.

Re-exporting the detector so it accepts the size it is being given takes it to **33**, and
counting a square the detector's box merely clips — 5% of it rather than 15% — to **35 of
48**, with misses down to 5. The four together, each measured on its own:

| | grids exact | missed | in excess |
| --- | --- | --- | --- |
| published detector, tile stretched to fill 640 | 21/48 | 33 | 8 |
| tile laid on the field instead | 29/48 | 19 | 7 |
| detector re-exported to take 224 | 33/48 | 23 | 6 |
| a clipped square counts | **35/48** | **5** | 12 |

None of that is a better model. It is the same weights throughout, fed properly.

Two things that looked promising and were not, both measured here rather than argued about:
detecting on the whole 3x3 mosaic in one pass, which scores that same bicycle at 0.768 but
finds nothing else on the grid and cost 10 grids; and moving the confidence threshold, whose
curve is flat from 0.25 to 0.45 — the one grid between them is noise, so 0.35 stays.

**A bench is only as honest as its labels.** Fifteen of these were inherited from an older
corpus rather than written for this, and three of them were wrong: a grid holding three buses
was recorded as holding none. They accounted for more than half the excess squares, and they
made the solver look wrong where it had been right. Every label here has now been read off
the picture with the square numbers drawn onto it — misreading which square is which is the
one mistake that poisons a bench silently, and it had already happened once.

Seven more of them were wrong, found by re-reading every grid the solver was scored wrong
on before touching the solver. Two of those were the solver being right: a bike *rack*
labelled as a bicycle, and a car read as a truck because noise at 96px made a hubcap look
like a steel wheel. Three came from reading a tile at too small a magnification and saying
so with more confidence than the pixels allowed — one of them read a van's rear bumper as a
motorcycle's exhaust. Denoise before judging, and name the features that separate the two
answers rather than the ones you expect to see.

**48 of 48.** Everything below is against the labels as they now stand, with the models
`-export` builds, so the rows differ by code alone. The first row is the same solver as the
35/48 above, re-measured: the corrected labels and the two exported models are the whole
difference between those two numbers, and neither is a change to the solver.

| | grids exact | missed | in excess |
| --- | --- | --- | --- |
| where the four fixes above left it | 43/48 | 4 | 2 |
| tile *and* grid laid on a 288px field | 45/48 | 3 | 0 |
| segmentation asked tile by tile as well | 46/48 | 2 | 0 |
| each 4x4 square read with its neighbours around it | 47/48 | 0 | 1 |
| boxes merged across frames before reading squares | **48/48** | **0** | **0** |

**288 rather than 224, and the curve is not smooth.** The backbone strides by 32, and a
tile is laid in the *centre* of the field — so a field that is an odd multiple of 32 has a
feature cell centred on it and an even multiple does not. 224, 288 and 352 win; 256, 320
and 384 lose. 288 and 352 fail on exactly the same grids, so the choice does not rest on a
borderline one.

**Reading each 4x4 square with room around it** is what took misses to zero. A 4x4 is one
photograph, and a bicycle half-hidden in a corner square is a fragment of a bicycle: shown
that square plus half a square of its neighbours, with the whole field to itself, the
detector finds it. That is seventeen passes per grid rather than one, and it brought back
the same motorcycle under a dozen boxes — so they are merged the way a detector merges its
own, greedily by overlap, before the squares are read off them. Without that the union is
the *worst* localisation of each thing rather than the best, which put a sliver of tyre in
a square no threshold could remove without taking a real square with it.

**The head's own bar is worth the same last grid, independently.** Refit by the procedure
in `train-probe.py` over 21 labelled crosswalk grids, it comes out at 0.36 in 18 folds of
21; at the 0.32 it had before, the final solver ticks one square in excess and scores 47.
An earlier attempt at exactly this number was rejected for good reason — it was being
picked by hand against the bench, which is fitting the bench, not calibrating a head. What
changed is who chooses the number, not the number.

**Leave-one-grid-out is what separates an improvement from a memorisation.** Every change
worth one grid was refit with the grid it fixes held out. Merging boxes across frames
survives that (45/48 in cross-validation); a relative confidence bar that also scored one
extra grid does not (44/48) — its whole case rested on the grid it repaired. Three knobs
each worth exactly one grid were refused on that test: a hand-picked crosswalk bar, an
overlap threshold of 0.03 sitting as an isolated point between two neighbours at 43, and a
segmentation share of 0.08 with a window of 0.005 around it. The tie-break matters as much
as the test: taking the smallest tied value systematically lands on the edge of a plateau,
where the next grid's noise pushes it off. Take the middle.

Measured and rejected, each because the number said so rather than because it sounded
wrong: RT-DETR r101 in place of r50 (37/48 at every input size), a mirrored second pass
(identical failures, twice the CPU), the full-precision encoder on the zero-shot path
(41/48), dropping "truck" from what counts as a car (41/48), slicing 4x4 grids into
overlapping tiles (43/48 at the middle of its plateau — the 44 and 45 readings were noise),
rejecting boxes truncated at the field edge (no effect), refitting the crosswalk head on 12
fresh grids and then on 21 (42/48, and it ticks the same disputed square — two heads fitted
on disjoint data making the same mistake is not a data problem), OWLv2 for crosswalks (5 of
9 grids against the head's 8), a stripe-periodicity feature, cropping to the road before
scoring, arbitrating squares by segmentation pixels, and voting a square in only when
several frames agree.

**More tiles was the wrong answer, and the corpus said so.** The obvious way to fix a head
that misses a plain crossing is to label more grids. Measured before doing it: fit on 4, 6,
8, 10 and 12 grids, score each by leaving one grid out, and the mistakes per grid go 1.19,
0.67, 0.47, 0.34, 0.33. Flat from nine. The last three grids collected and labelled bought
nothing, and thirty more from the same stream would be thirty more of the same urban street
corners.

Two other things were tried and are written down because they failed. Reading each tile
with its neighbours — the nine tiles are one photograph cut up, so a crossing is a
whole-panel object and the stripes carry on into the next square — scored 22 mistakes on
its own and 5 alongside the tile, against 4 for the tile alone. Blending the fitted head
back towards CLIP's text direction for "a photo of a crosswalk", which is the standard fix
for an over-specialised probe, did work: 3 mistakes across a broad basin of mixes from 0.3
to 0.7, and it is a basin rather than a spike, so it is real.

What it is not is additive. The **full-precision export of the same encoder** gets the same
3 mistakes and the same 10 grids of 12 answered exactly, with no blend, no extra knob and
nothing to calibrate — and the tile it stops missing is the one that has no excuse, a
crossing painted across a whole carriageway, 0.30 before and 0.58 after. Stacking the blend
on top of it makes it worse again. So the head reads against `clip-vision-fp32.onnx` and
nothing else does.

That last part is deliberate. The full-precision file is 345MB against 84MB, and half a
second a grid against a fifth of one — 161ms to load and 347ms for nine tiles quantised,
475ms and 550ms full-precision, which is 2.6s across a five-round challenge and not worth
arguing about. What would be worth arguing about is swapping it in everywhere: the
zero-shot path's thresholds were calibrated against the quantised export, and moving the
encoder under them changes every one of those numbers silently. So a head now names its
encoder *file* as well as its hash, `install-vision.sh` fetches what the installed heads
ask for, and a grid pays for the larger encoder only when it reaches a head at all.

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

That fixed the fade and did not fix the whole problem, which is worth saying plainly because
the ten-out-of-ten above reads as though it did. A grid can also come back *entirely* flat —
not a frozen fade but nothing at all, one white rectangle where the panel is, while the
document lists nine tiles with exact geometry and every picture in them loaded and decoded.
It arrives in runs, right after a round whose tiles were replaced, and the resize does not
shift it: measured, three full rounds of waking the page — pointer travel included — left it
white. Counted over four runs on the demo page, unpainted grids came out 3, 7, 0 and 6.

What that used to cost was invisible. A blank photograph is one the solver can answer: it
finds nothing, which is a legal answer to a dynamic grid, so postern ticked nothing and
pressed verify — submitting an empty answer to a grid nobody had looked at. reCAPTCHA
refuses it and serves another, just as unpainted. Two runs measured before this was found
spent 8 rounds of 11 and 5 of 13 exactly that way, and both ended with no token and no
indication of why.

A grid that never painted is now its own outcome rather than an empty answer. It is not
photographed to the solver, not submitted, and not charged against the six rounds a
challenge gets — postern goes back and looks again, and after three of those asks for a
different challenge and says which of the two reasons drove it there. That last part is not
cosmetic: the reload path used to report every reload as "the solver has nothing for this",
which cost this author an hour of looking at a vision model over a run whose panels were
blank.

The standard flags for a window Chrome thinks nobody is watching —
`--disable-backgrounding-occluded-windows`, `--disable-renderer-backgrounding`,
`--disable-background-timer-throttling` — were tried and measured over eight runs alternating
with and without, so that Google's mood drifting could not be read as an effect: 3, 7, 0, 6
unpainted grids with them and 7, 7, 7, 0 without. The spread between runs is larger than the
difference between the arms. They are not carried, and the negative result is written into
`browser.go` so the next person does not spend the evening rediscovering it.

**What did fix it was giving up on the screen.** Every failure above is a failure of the
same thing: postern was asking the compositor what the panel looked like, and under a
virtual display the compositor is not postern's to command. But the pictures are in the
document. Each tile is an `<img>` with a source and a rectangle, both of which are already
read every round for the geometry — so the panel can be *drawn* rather than photographed:
a canvas the size of the challenge document, each tile's image drawn at its own rectangle
and clipped to it, read back as a PNG.

A canvas is drawn by the renderer, so there is no compositor in the path and none of the
three failures can occur. The clip is the challenge document's own viewport, which is
exactly what the screenshot clipped to, so the tile boxes handed to the solver and the
coordinates it answers with mean the same thing as before and nothing downstream changed.

The one thing that could have sunk it is canvas tainting — a cross-origin picture makes
`toDataURL` a security error. reCAPTCHA serves its payloads from the origin its own frame
runs on, so it does not arise; the code returns an empty string rather than throwing if it
ever does, and the screenshot path is still there underneath.

Measured over five runs after the change: **zero unpainted grids and zero falls back to the
screenshot**, against 3, 7, 0 and 6 unpainted before it. Drawing a panel costs 24ms. The
machinery it replaces — waking the page, photographing it, photographing it again to find
out whether the first one had finished, up to six times — was the largest phase of a round
that did not involve the solver.

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

*Postern ran itself out of screens.* Every run gets an Xvfb of its own, and every run that
reached its timeout killed it with SIGKILL — the default for a command bound to a cancelled
context. An X server killed that way cannot remove its lock file, and postern read any lock
file as a display in use, so each timed-out run cost a display number permanently. After an
afternoon of measuring, sixty-four dead locks and `no display free between :99 and :162` on
a machine with nothing running on any of them. Both halves are fixed: the server is asked
before it is killed, so it clears up after itself, and a lock whose process is gone is
removed rather than believed. The lesson generalises past X — a lock file is a claim about
a process, and it is worth checking that the process is still there.

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

**The outcome is settled before the first picture.** Counted over seventeen runs across two
campaigns, grids served in a run against whether it ended in a token:

| grids served in the run | outcome |
| --- | --- |
| **6** | 7 runs, **7 tokens** |
| 12 | 2 runs, 1 token |
| 8, 10, 11, 13, 14 | 8 runs, **0 tokens** |

A run either gets a finite challenge — six grids, occasionally two of them — and a token at
the end of it, or it gets a treadmill that never terminates however well the grids are
answered. Nothing in between happens. Which one you get is decided at the checkbox, and the
vision cannot influence it: the same solver, at 48 of 48 on the bench, is on both rows.

That is worth knowing before optimising anything. It means a token rate measures the
address and the profile, not the answers, and it means "answer the pictures better" has a
ceiling that was reached some time ago. It also gives a cheaper instrument than the token:
**how often a challenge is served at all**. A browser Google trusts ticks the checkbox and
is waved through in about five seconds. Measured here, ten runs with no solver configured:
ten challenges, no waves through — before and after a warm-up that took the profile from 6
cookies to 77 and from 5 URLs of history to 33, which moved it not at all.

What postern does on its own:

- **The profile is kept.** `-profile` defaults to a stable directory precisely so cookies,
  history and the vendor's own `_GRECAPTCHA` accumulate. A fresh profile per solve throws
  that away every time, and it is the single easiest way to make postern look worse than
  it is — which is exactly the mistake the measurements above were first made with.
- **It stops when it is no longer being graded.** Past twelve picture grids in one solve,
  postern gives up and says the address is what is being refused, rather than spending the
  rest of the timeout and reporting "no token" as though the answers were wrong.

**Profiles are not the lever either.** The fleet was measured against the single profile it
replaces, on the same connection, ten solves each: six identities that rotate, rest and warm
themselves came back with **3 tokens**, against **4** for one profile hammered — the same
number, given how noisy ten runs are.

### The control that should have been run first

Everything above measures postern against postern, which cannot tell a hard problem from a
self-inflicted one. The missing control is a browser that is *not* postern, on the same
machine, the same address, the same evening: a stock Chrome on a two-minute-old profile,
clicked through the X server with XTEST, and read by a human.

| browser | tokens | grids per token |
| --- | --- | --- |
| stock Chrome, fresh profile, XTEST clicks | **4/4** | **1** |
| ...plus every one of postern's 26 Chrome flags | **1/1** | 1 |
| ...plus `--remote-debugging-port` and clicks dispatched over CDP | **1/1** | 1 |
| postern, same address, same evening | 4/10 | 6 or more |

Six for six, always one grid, answered slowly and by hand. So the ceiling is not the
address — the same address waves a stock Chrome through on the first grid. It is not profile
age either: those profiles were minutes old, with no history and no cookies, which is *less*
than the warmed profile postern was using. And it is none of the things that get blamed for
this by default, because they were added one at a time and none of them cost a token: not
the flag set, not the open debugging port, not synthesising the pointer through CDP.

Those were then added to the control one at a time, and none of them costs a token either:
script injection on every document, `Page.setBypassCSP`, focus emulation, postern's own
`grecaptcha.render` widget in its `position:fixed; transform:translateY(-50%); z-index:999999`
host, and the second browser window chromedp opens. The control kept getting tokens through
all of it — including a round where a deliberately wrong answer came back "please try again"
and the corrected one was accepted, which is what being graded normally looks like.

### What postern actually does wrong

With every external explanation eliminated, its own logs and saved panels say it plainly.
One run, six challenges:

- five pointer clicks on the verify button, **four of them swallowed** — dispatched, and the
  panel does not move;
- `#recaptcha-verify-button` **refused focus four times out of four**, so the keyboard
  fallback had nothing to type into either;
- three saved panels in a row **byte-identical**: the same grid photographed, answered,
  photographed again. `-save-panels` makes this visible in one `md5sum`.

So the endless challenge is not reCAPTCHA refusing the address. It is postern answering
correctly and then failing to submit, re-reading the unchanged panel, and answering it again.
The comment above `Verify` blames a button clipped below the panel; postern's own log
contradicts it — `buttonY=530 panelHeight=580`, the button is inside.

### Where the swallowed clicks were going

Logging the viewport coordinate of each verify click, and asking the page what it hit-tests
there, answered it in one run:

```
verify by pointer  origin=112,150    viewport=454,700    under="bframe 112,150 400x580"
verify by pointer  origin=112,150    viewport=454,700    under="bframe 112,150 400x580"
verify by pointer  origin=112,150    viewport=454,700    under="bframe 112,150 400x580"
verify by pointer  origin=112,-9999  viewport=454,-9448  under=rien
```

A click is aimed by arithmetic: the frame's origin, plus an offset measured inside the frame.
The origin comes from finding the frame's element in the host page **by its address** — and
reCAPTCHA gives every widget on the page a bframe whose `src` is the same string, sitekey
included, parking the closed ones at `y=-9999` under `visibility:hidden`. On a page that
carries its own widget beside the one postern renders, `find(f => f.src === url)` therefore
returned a parked frame roughly a quarter of the time, and that round's clicks were dispatched
nine thousand pixels above the window.

Preferring a candidate that is on screen fixed the frame *choice* and moved the number very
little, because the deeper mistake was next to it: the panel's own document is **identical
whether or not it is parked**. It reports its tiles, its button and a 580-pixel `innerHeight`
from the inside either way — none of that changes when reCAPTCHA moves the iframe holding it
to `y=-9999` between rounds. `Open()` asked only the inside, so postern went on clicking a
panel that had been put away.

So the two questions are now separated. `hasPanel` is what the document says about itself and
decides whether the frame is worth measuring again; `Open` is that **and** an outside
measurement saying the element is on screen. Nothing is clicked unless both agree.

This is also why the panel never photographed: the screenshot is clipped to the same origin,
so it came back blank and got reconstructed from the DOM instead.

**Measured, ten solves back to back on one fresh profile, same address and same evening as
the 4/10 above:**

| | before | after |
| --- | --- | --- |
| tokens | 4/10 | **10/10** |
| clicks dispatched off screen | 6 in one run | **0 in ten** |
| challenges per token | 6 or more | **1** (median; 5 at worst) |
| time to a token | 72–85s | **21s** (median) |

Six of the ten were waved through after a single grid, which is what the stock-Chrome control
gets. The endless challenge was never Google refusing this address: it was postern answering
correctly and throwing the answer nine thousand pixels above the window.

Two smaller defects fell out of the same session: `-display host` starts Chrome without
`--ozone-platform=x11`, so it tries Wayland and dies where there is no compositor; and
chromedp's second window leaves an orphan `about:blank` window open for the whole run.

The earlier claim here, that the address was the ceiling and a commercial solver's only real
edge was its pool of residential IPs, was wrong. It came from comparing postern against
itself and never against a browser that works.

What it cannot do for you, and there is no clever way around either:

- **Where the requests come from.** A residential address is worth more than any
  fingerprint work. `-proxy` takes one, and `-identities` takes one per line.
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
