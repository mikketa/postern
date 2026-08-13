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
  * Keep reloads of the same grid together. reCAPTCHA serves the same tiles
    again after a reload, and a head validated on its own training tiles looks
    much better than it is. --hold takes a series out for testing.

The head lands next to the models as probe-<category>.json and is picked up
automatically. It is tied to the encoder it was fitted on: a head fitted on
patch16 is ignored under patch32, which is why the file names its model.
"""

import argparse
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


def encode(vision, images):
    batch = np.stack([
        ((np.asarray(image.convert("RGB").resize((IMAGE_SIZE, IMAGE_SIZE),
                                                 Image.Resampling.BICUBIC),
                     dtype=np.float32) / 255.0) - MEAN) / STD
        for image in images
    ]).transpose(0, 3, 1, 2)
    embeds = vision.run(None, {"pixel_values": batch})[0]
    return embeds / np.linalg.norm(embeds, axis=-1, keepdims=True)


def read(panels, labels, vision):
    """Every labelled tile, as an embedding and a yes or no."""
    embeds, wanted, source = [], [], []
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
        embeds.append(encode(vision, [panel.crop((x, y, x + w, y + h)) for x, y, w, h in boxes]))
        wanted += [1.0 if i in tiles else 0.0 for i in range(len(boxes))]
        source += [stem] * len(boxes)

    if not embeds:
        raise SystemExit("nothing to train on")
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


def score(weights, bias, embeds, wanted, source, share=0.70, floor=0.30):
    """What the head gets right and wrong, grid by grid."""
    predicted = 1.0 / (1.0 + np.exp(-(embeds @ weights + bias)))
    missing = excess = 0
    for stem in sorted(set(source)):
        here = source == stem
        scores = predicted[here]
        bar = max(floor, share * float(scores.max()))
        picked = {i for i, v in enumerate(scores) if v >= bar}
        truth = {i for i, v in enumerate(wanted[here]) if v}
        missing += len(truth - picked)
        excess += len(picked - truth)
        mark = "exact" if picked == truth else f"picked {sorted(picked)} want {sorted(truth)}"
        print(f"  {stem}: {mark}")
    return missing, excess


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
    args = parser.parse_args()

    with open(args.labels) as handle:
        labels = {str(k): set(v) for k, v in json.load(handle).items()}

    options = ort.SessionOptions()
    options.log_severity_level = 3
    vision = ort.InferenceSession(os.path.join(args.models, "clip-vision.onnx"), options,
                                  providers=["CPUExecutionProvider"])

    held = {k: v for k, v in labels.items() if k in set(args.hold)}
    training = {k: v for k, v in labels.items() if k not in held}

    embeds, wanted, source = read(args.panels, training, vision)
    print(f"fitting on {int(wanted.sum())} tiles that hold it, {len(wanted)} in all")
    weights, bias = fit(embeds, wanted)

    if held:
        print("held out:")
        missing, excess = score(weights, bias, *read(args.panels, held, vision))
        print(f"held out: -{missing} +{excess}")
    else:
        print("on its own training tiles, which is not a measurement:")
        score(weights, bias, embeds, wanted, source)
        print("nothing was held out — pass --hold to find out whether it generalises")

    out = os.path.join(args.models, f"probe-{args.category.replace(' ', '-')}.json")
    with open(out, "w") as handle:
        json.dump({
            "category": args.category,
            "model": args.model,
            "bias": float(bias),
            "weights": [round(float(v), 6) for v in weights],
            "tiles": int(len(wanted)),
            "positive": int(wanted.sum()),
        }, handle)
    print(f"wrote {out}")


if __name__ == "__main__":
    main()
