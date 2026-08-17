# Reputation, and the bug that was mistaken for it

← [back to the README](../README.md)

The README covers making one solve work. This covers making the hundredth work — and, more
usefully, how a whole day of careful measurement produced a confident conclusion that was
wrong.

It is kept in the order it happened, wrong turns included, because the wrong turns are the
part worth reading. One evening ended at 0/5 with a better solver than it started at 3/5
with, and every explanation reached for was external.

## Two failures that really were the vision

Two earlier failures were genuine vision mistakes, and both are the same shape — an object at
the edge of a square. One was a grid of cars noised almost to static, served four times
over, where the solver ticked nothing: it does hold cars, a row of them parked along the
bottom tenth of one tile, which took a median filter and a seven-fold enlargement before I
could see them myself. The other was a 4x4 of a coach filling the middle of the frame, where
the solver ticked two squares the bus only grazes at the bumper. Missing a tenth of a tile
and claiming a tenth of a tile are the two ends of one threshold, they are what the bench
measures, and moving it is a decision to make on 48 grids rather than on the one that just
failed.

## The audio challenge is not a way out

The usual escape hatch, the audio challenge, is not one here: Google refuses to serve it
at all, answering *"Your computer or network may be sending automated queries"*. So the
other lever for reCAPTCHA v2 is **reputation** — a profile with history behind it, and an
IP that is not a datacenter — which is not something a solver can manufacture.

## What the round counts said, and what survives of it

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

## Leaving from another address

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

## Ageing a profile, and what it costs in bandwidth

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

## The outcome is settled before the first picture

Counted over seventeen runs across two campaigns, grids served in a run against whether it
ended in a token:

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
verify by pointer  origin=112,-9999  viewport=454,-9448  under=nothing
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

