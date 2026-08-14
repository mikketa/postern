#!/usr/bin/env python3
"""Fits a head for one category, from grids you have labelled by eye.

Some of what reCAPTCHA asks has no class in any model you can download.
Crosswalks are the clearest case: COCO has no crosswalk, ADE20K has no
crosswalk, and CLIP asked "is this a crosswalk" answers a question about the
street rather than about the paint — on a road, every tile looks a bit like the
answer, so it ticks half the grid.

The fix is not a bigger model. It is a hundred labelled tiles and a logistic
regression on top of CLIP's picture embedding, which is the standard linear
probe and takes seconds on a CPU. Measured over seven labelled grids, trained
on one series of grids and tested on another so that no tile appeared in both:
five ticks short and one in excess, against roughly four in excess per grid for
zero-shot CLIP.

Each head also carries the bar it should be read at, found by fitting without
one grid at a time and keeping whichever bar costs fewest mistakes. That matters
more than it sounds: a bar fixed in the solver was measurably wrong for this
head, which missed nothing and ticked six squares in excess on grids it had
never seen. Its own bar halved that, to three, and dropping the tiles reCAPTCHA
had already served took it to one over a whole six-round challenge.

    # 1. save some grids. postern writes one per round with -save-panels.
    postern solve -kind recaptcha-v2 -url ... -sitekey ... \\
        -image-solver ... -save-panels ~/panels
    # 2. label them: which tiles hold the thing, by eye.
    # 3. fit
    python examples/train-probe.py ~/panels labels.json crosswalk \\
        --models ~/.cache/postern-vision

labels.json is yours to write, one entry per panel:

    {"1786495864627503775": [0, 1, 7],
     "1786495877711282412": [2, 3, 7]}

The key is the panel filename without its extension; the value is the tiles
that hold the thing. Only panels listed there are used, so a half-labelled
directory is fine.

Two things matter more than the amount of data:

  * Label what the grid actually shows, not what you think it wants. A tile
    with a crossing in the far corner is a tile with a crossing.
  * Label every round of a challenge, reloads included. A round only replaces
    the squares you ticked, so a six-round challenge is six copies of the same
    negatives around one or two new pictures; both the fitting and the bar are
    thrown off by the repeats, and a round scored against the round before it
    is scored on tiles it was fitted on. This throws the repeats away for you,
    across --hold as well, so labelling them costs nothing and the few new
    pictures are kept.

The head lands next to the models as probe-<category>.json and is picked up
automatically. It is tied to the exact encoder it was fitted on, which the file
records as a hash: another export of the same model name multiplies out to
numbers that mean nothing, so the solver ignores a head it cannot match and says
so rather than answering badly.
"""

import argparse
import hashlib
import json
import os
import sys

import numpy as np
import onnxruntime as ort
from PIL import Image

IMAGE_SIZE = 224
MEAN = np.array([0.48145466, 0.4578275, 0.40821073], dtype=np.float32)
STD = np.array([0.26862954, 0.26130258, 0.27577711], dtype=np.float32)

# Fitting. Deliberately plain: a few hundred tiles do not need anything
# cleverer, and every knob is one more thing calibrated on data too small to
# calibrate it with. The regularisation is the one exception — it is what
# stands between a hundred tiles and a head that has memorised them — and 0.003
# was measured, over the labelled grids, to generalise best across series.
STEPS = 3000
RATE = 1.0
REGULARISATION = 0.003


def fingerprint(path):
    """The encoder this head is being fitted against, as a hash of the file.

    A head is 512 numbers read against one particular set of embeddings. Point
    it at another export of nominally the same model — quantised instead of
    not, exported by another tool, a different revision on the hub — and the
    arithmetic still works and the answers are noise. Measured: a head reading
    0.83 on a tile read 0.16 on the same tile under a different export of the
    same model name, which looks exactly like a head that has learned nothing.
    So the head names the file, and the solver refuses to read it against
    anything else.
    """
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for block in iter(lambda: handle.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def encode(vision, images):
    batch = np.stack([
        ((np.asarray(image.convert("RGB").resize((IMAGE_SIZE, IMAGE_SIZE),
                                                 Image.Resampling.BICUBIC),
                     dtype=np.float32) / 255.0) - MEAN) / STD
        for image in images
    ]).transpose(0, 3, 1, 2)
    embeds = vision.run(None, {"pixel_values": batch})[0]
    return embeds / np.linalg.norm(embeds, axis=-1, keepdims=True)


def read(panels, labels, vision, augment=True, seen=None):
    """Every labelled tile, as an embedding and a yes or no.

    Tiles already in `seen` are skipped, and the ones kept are added to it, so
    the same picture is never read twice. reCAPTCHA replaces only the squares
    you tick and serves the rest again, so six rounds of one challenge are six
    copies of the same six negatives and one or two new pictures. Learning
    those six times does not learn them better, it just buries the positives;
    and a round scored against the round before it is scored on tiles it was
    fitted on. Passing the same set through training and then through the
    held-out grids removes both at once.
    """
    embeds, wanted, source = [], [], []
    if seen is None:
        seen = set()
    for stem, tiles in labels.items():
        shot = os.path.join(panels, stem + ".png")
        meta_path = os.path.join(panels, stem + ".json")
        if not os.path.exists(shot) or not os.path.exists(meta_path):
            print(f"skipping {stem}: no panel saved for it", file=sys.stderr)
            continue

        with open(meta_path) as handle:
            meta = json.load(handle)
        boxes = [tuple(int(v) for v in part.split(",")) for part in meta["tiles"].split(";")]

        panel = Image.open(shot).convert("RGB")
        crops, marks = [], []
        for i, (x, y, w, h) in enumerate(boxes):
            crop = panel.crop((x, y, x + w, y + h))
            already = hashlib.sha1(crop.tobytes()).hexdigest()
            if already in seen:
                continue
            seen.add(already)
            crops.append(crop)
            marks.append(1.0 if i in tiles else 0.0)
        if not crops:
            continue

        # Each tile twice, the second one mirrored. A crossing seen in a mirror
        # is still a crossing, so the label carries over for free and the head
        # gets twice the tiles to learn the shape from — measured on grids it
        # had never seen, three ticks in excess became two.
        #
        # Both copies answer to the same grid, which matters: the bar below is
        # calibrated by leaving one grid out, and a mirrored copy left in while
        # its original is taken out is the same leak as validating a grid on its
        # own reloads. Kept apart, it chose a bar that was worse, not better.
        versions = [crops]
        if augment:
            versions.append([c.transpose(Image.FLIP_LEFT_RIGHT) for c in crops])
        for images in versions:
            embeds.append(encode(vision, images))
            wanted += marks
            source += [stem] * len(marks)

    if not embeds:
        raise SystemExit("nothing to train on")
    return np.concatenate(embeds), np.array(wanted), np.array(source)


def read_positives(panels, positives, vision, seen=None):
    """The ticked-and-accepted squares only, with no negatives invented.

    Same reading as read(), minus every tile the answer did not name. A head
    fitted on positives alone would be useless — it needs negatives to have
    anything to separate — so this is meant to be added to hand-labelled grids,
    not used on its own.
    """
    embeds, wanted, source = [], [], []
    if seen is None:
        seen = set()
    for stem, tiles in positives.items():
        shot = os.path.join(panels, stem + ".png")
        meta_path = os.path.join(panels, stem + ".json")
        if not os.path.exists(shot) or not os.path.exists(meta_path):
            continue

        with open(meta_path) as handle:
            meta = json.load(handle)
        boxes = [tuple(int(v) for v in part.split(",")) for part in meta["tiles"].split(";")]

        panel = Image.open(shot).convert("RGB")
        crops = []
        for i in sorted(tiles):
            if i >= len(boxes):
                continue
            x, y, w, h = boxes[i]
            crop = panel.crop((x, y, x + w, y + h))
            already = hashlib.sha1(crop.tobytes()).hexdigest()
            if already in seen:
                continue
            seen.add(already)
            crops.append(crop)
        if not crops:
            continue

        for images in (crops, [c.transpose(Image.FLIP_LEFT_RIGHT) for c in crops]):
            embeds.append(encode(vision, images))
            wanted += [1.0] * len(images)
            source += [stem] * len(images)

    if not embeds:
        return None
    return np.concatenate(embeds), np.array(wanted), np.array(source)


def fit(embeds, wanted):
    weights = np.zeros(embeds.shape[1])
    bias = 0.0
    for _ in range(STEPS):
        predicted = 1.0 / (1.0 + np.exp(-(embeds @ weights + bias)))
        weights -= RATE * (embeds.T @ (predicted - wanted) / len(wanted)
                           + REGULARISATION * weights)
        bias -= RATE * (predicted - wanted).mean()
    return weights, bias


def score(weights, bias, embeds, wanted, source, bar=None, share=0.70, floor=0.50):
    """What the head gets right and wrong, grid by grid."""
    predicted = 1.0 / (1.0 + np.exp(-(embeds @ weights + bias)))
    missing = excess = 0
    for stem in sorted(set(source)):
        here = source == stem
        scores = predicted[here]
        cut = bar if bar is not None else max(floor, share * float(scores.max()))
        picked = {i for i, v in enumerate(scores) if v >= cut}
        truth = {i for i, v in enumerate(wanted[here]) if v}
        missing += len(truth - picked)
        excess += len(picked - truth)
        # Positions among the tiles kept for this grid, not square numbers:
        # anything served twice was dropped before it got here.
        mark = "exact" if picked == truth else f"picked {sorted(picked)} want {sorted(truth)}"
        print(f"  {stem}: {mark}")
    return missing, excess


def calibrate(embeds, wanted, source):
    """The bar this head should be read at, found by leaving one grid out.

    How sure a head is depends on what it was fitted on, so a bar chosen once
    and written into the solver is wrong for every head but the one it was
    chosen for — and it was measurably wrong for this one, which missed nothing
    and ticked six squares in excess on grids it had not seen.

    So each head carries its own, picked the only honest way: fit without a
    grid, score that grid, and keep whichever bar costs fewest mistakes across
    all of them. A miss and a false tick are weighed the same because reCAPTCHA
    weighs them the same — either one fails the grid.
    """
    grids = sorted(set(source))
    if len(grids) < 3:
        return None

    predicted = np.zeros(len(wanted))
    for grid in grids:
        held = source == grid
        weights, bias = fit(embeds[~held], wanted[~held])
        predicted[held] = 1.0 / (1.0 + np.exp(-(embeds[held] @ weights + bias)))

    best, cost = None, None
    for bar in np.arange(0.30, 0.90, 0.01):
        mistakes = 0
        for grid in grids:
            here = source == grid
            picked = {i for i, v in enumerate(predicted[here]) if v >= bar}
            truth = {i for i, v in enumerate(wanted[here]) if v}
            mistakes += len(truth - picked) + len(picked - truth)
        if cost is None or mistakes < cost:
            best, cost = float(bar), mistakes

    print(f"bar {best:.2f}, costing {cost} mistakes over {len(grids)} grids left out one at a time")
    return best


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("panels", help="directory of saved panels")
    parser.add_argument("labels", help="json of panel name to tile numbers")
    parser.add_argument("category", help="what the head is for, e.g. crosswalk")
    parser.add_argument("--models", default=".", help="where the CLIP encoder lives")
    parser.add_argument("--model", default="patch16", help="which encoder that is")
    parser.add_argument("--hold", nargs="*", default=[],
                        help="panels to keep out of training and test on")
    parser.add_argument("--answers", default="",
                        help="answers.json written by postern: positives only, no negatives")
    args = parser.parse_args()

    with open(args.labels) as handle:
        labels = {str(k): set(v) for k, v in json.load(handle).items()}

    # Answers are not labels and are not merged with them. postern writes them
    # when a challenge produced a token, so every square listed was ticked and
    # accepted — but reCAPTCHA hands out tokens for incomplete answers, measured
    # on a live run where a school bus sat unticked through a bus challenge that
    # passed. So the squares it does not mention are unknown, not empty, and
    # only the positives are read.
    positives = {}
    if args.answers:
        with open(args.answers) as handle:
            positives = {str(k): set(v) for k, v in json.load(handle).items() if v}

    encoder = os.path.join(args.models, "clip-vision.onnx")
    options = ort.SessionOptions()
    options.log_severity_level = 3
    vision = ort.InferenceSession(encoder, options, providers=["CPUExecutionProvider"])
    print(f"fitting against {encoder} ({fingerprint(encoder)[:12]})")

    held = {k: v for k, v in labels.items() if k in set(args.hold)}
    training = {k: v for k, v in labels.items() if k not in held}

    seen = set()
    embeds, wanted, source = read(args.panels, training, vision, seen=seen)
    if positives:
        extra = read_positives(args.panels, positives, vision, seen=seen)
        if extra is not None:
            embeds = np.concatenate([embeds, extra[0]])
            wanted = np.concatenate([wanted, extra[1]])
            source = np.concatenate([source, extra[2]])
            print(f"plus {int(extra[1].sum())} squares postern was told it got right")
    print(f"fitting on {int(wanted.sum())} tiles that hold it, {len(wanted)} in all")
    weights, bias = fit(embeds, wanted)
    bar = calibrate(embeds, wanted, source)

    if held:
        print("held out:")
        # Not augmented: a mirrored copy is for learning from, not for being
        # marked on. Counting it would report every mistake twice.
        # `seen` carries over from training, so a square the head has already
        # been fitted on cannot be marked as if it were new.
        missing, excess = score(weights, bias,
                                *read(args.panels, held, vision, augment=False, seen=seen),
                                bar=bar)
        print(f"held out: -{missing} +{excess}")
    else:
        print("on its own training tiles, which is not a measurement:")
        score(weights, bias, embeds, wanted, source, bar=bar)
        print("nothing was held out — pass --hold to find out whether it generalises")

    out = os.path.join(args.models, f"probe-{args.category.replace(' ', '-')}.json")
    with open(out, "w") as handle:
        json.dump({
            "category": args.category,
            "model": args.model,
            "encoder": fingerprint(encoder),
            "bias": float(bias),
            "weights": [round(float(v), 6) for v in weights],
            "bar": round(bar, 2) if bar is not None else None,
            "tiles": int(len(wanted)),
            "positive": int(wanted.sum()),
        }, handle)
    print(f"wrote {out}")


if __name__ == "__main__":
    main()
