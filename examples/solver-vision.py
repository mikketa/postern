#!/usr/bin/env python3
"""A postern image solver that answers whatever the challenge asks.

Object detectors know a fixed list of things. COCO has eighty, which sounds
generous until reCAPTCHA asks for crosswalks, stairs, chimneys or fire escapes —
none of which are on it. A detector-based solver answers the questions it was
trained for and passes on the rest.

CLIP has no list. It scores how well a picture matches a sentence, so the
category can come from the prompt at runtime: postern reads "Sélectionnez toutes
les images montrant des passages pour piétons" out of the challenge document,
this script turns it into "a photo of a crosswalk", and every tile is scored
against that and against a set of things it might be instead.

    postern solve -kind recaptcha-v2 -url ... -sitekey ... \\
        -image-solver "/path/to/venv/bin/python examples/solver-clip.py"

Setup:

    python3 -m venv venv
    venv/bin/pip install onnxruntime numpy pillow tokenizers

    base=https://huggingface.co/Xenova/clip-vit-base-patch32/resolve/main
    curl -L -o clip-vision.onnx    $base/onnx/vision_model_quantized.onnx
    curl -L -o clip-text.onnx      $base/onnx/text_model_quantized.onnx
    curl -L -o clip-tokenizer.json $base/tokenizer.json

    # POSTERN_CLIP_DIR points at wherever those three live; defaults to this
    # script's directory.

The quantised models are 90MB and 64MB rather than 350MB and 254MB. Postern
starts a solver per grid, so load time is paid on every round and dominates
everything else: the full models took two minutes a grid on CPU, which is longer
than the challenge stays valid. Sentence embeddings are cached next to the model
for the same reason — the text encoder is then loaded once ever, not once a
round.

It is a starting point, not a ceiling. A larger CLIP, a fine-tune on captcha
tiles, or a hosted vision model all plug in the same way — the protocol is a PNG
in and coordinates out.
"""

import hashlib
import json
import os
import re
import sys

import numpy as np
import onnxruntime as ort
from PIL import Image
from tokenizers import Tokenizer

# CLIP's own preprocessing. Getting any of these wrong quietly costs accuracy
# rather than failing, which is the worst way to be wrong.
IMAGE_SIZE = 224
MEAN = np.array([0.48145466, 0.4578275, 0.40821073], dtype=np.float32)
STD = np.array([0.26862954, 0.26130258, 0.27577711], dtype=np.float32)

# How sure to be before ticking a tile. reCAPTCHA punishes a miss and a false
# positive equally — both fail the grid — so this sits where the two are
# balanced rather than being generous in either direction.
#
# It is calibrated for ViT-B/32, and it does not carry over to another model: a
# sharper one is more confident about everything, so the same number lets more
# through. Swapping in ViT-B/16 turned a grid of cars that scored nothing above
# 0.20 into seven tiles above 0.34, and a bus tile from 0.54 to 0.94 — better
# sight and a different scale at once. Set POSTERN_CLIP_CONFIDENCE when you
# change models.
CONFIDENCE = float(os.environ.get("POSTERN_CLIP_CONFIDENCE") or 0.34)

# For the 4x4 layout, where squares are found by covering them up rather than
# by looking at them one at a time. OCCLUSION_STOP is how much of the thing has
# to still be visible for there to be another square worth finding, and
# OCCLUSION_DROP how much of the picture a square has to take with it to count.
#
# Both are calibrated on ViT-B/16 against saved grids with the answers checked
# by eye, and both depend on the model being able to tell: on ViT-B/32 the same
# procedure finds almost nothing, because covering a square barely moves a score
# it was never confident about. Use patch16 for this, or POSTERN_CLIP_LAYOUT=
# tiles to score squares one at a time instead.
OCCLUSION_STOP = float(os.environ.get("POSTERN_CLIP_STOP") or 0.45)
OCCLUSION_DROP = float(os.environ.get("POSTERN_CLIP_DROP") or 0.10)

# Mid-grey, which is what the preprocessing normalises closest to nothing.
OCCLUSION_GREY = (128, 128, 128)

# How much of the neighbouring squares the fallback includes when it has to
# score a 4x4 one square at a time. One sixteenth of a bus is not a bus to
# anything that looks at pictures.
TILE_MARGIN = 0.55

# What reCAPTCHA asks for, in the language it asks. CLIP thinks in English, so
# the prompt is translated before it is scored. Where one English word is a poor
# handle on the thing, the alternatives are listed with it — "bridge" alone
# scores a river crossing seen from the road at 0.23, which is below anything
# worth clicking, while "an overpass" describes the same photograph well.
# Anything not listed is passed through as written; CLIP has seen French.
CATEGORIES = [
    (r"passages? (pour |pi[ée]tons|clout[ée])|clous|crosswalk|cross ?walk",
     ("crosswalk", "zebra crossing", "a pedestrian crossing painted on the road")),
    (r"feux? de circulation|feux? tricolores?|traffic light",
     ("traffic light", "a set of traffic lights on a pole")),
    (r"bornes? d.incendie|bouches? d.incendie|fire hydrant",
     ("fire hydrant",)),
    (r"v[ée]los|bicyclettes?|bicycle", ("bicycle", "a parked bike")),
    (r"motos|motocyclettes?|motorcycle", ("motorcycle", "a scooter")),
    (r"voitures?|automobiles?|\bcars?\b", ("car", "a parked car")),
    (r"autobus|\bbus\b", ("bus", "a city bus")),
    (r"camions?|trucks?", ("truck", "a lorry")),
    (r"bateaux|navires?|boats?", ("boat", "a ship on the water")),
    (r"parcm[èe]tres?|parking meter", ("parking meter",)),
    (r"escaliers?|marches|stairs",
     ("staircase", "a flight of stairs", "steps leading up")),
    (r"chemin[ée]es?|chimney", ("chimney", "a chimney on a roof")),
    (r"montagnes?|collines?|mountains?|hills?", ("mountain", "a hill on the horizon")),
    (r"ponts?|bridges?", ("bridge", "an overpass", "a bridge over water")),
    (r"palmiers?|palm tree", ("palm tree",)),
    (r"taxis?", ("taxi", "a yellow cab")),
    (r"tracteurs?|tractor", ("tractor", "farm machinery")),
    (r"trains?|locomotives?", ("train", "a railway carriage")),
    (r"statues?", ("statue", "a monument")),
    (r"horloges?|clocks?", ("clock", "a clock on a tower")),
    (r"vitrines?|devantures?|storefront", ("shop front", "a store window")),
    (r"panneaux? de signalisation|street sign", ("street sign", "a road sign")),
    (r"bo[îi]tes? aux lettres|mailbox", ("mailbox", "a post box")),
]

# CLIP scores a picture against a sentence, and which sentence matters more than
# it should: the same photograph reads differently to "a crosswalk" and to "a
# photo of a crosswalk". The paper's own answer is to score several phrasings
# and add up what they say, which is what these are. They cost nothing at run
# time — sentence embeddings are cached on disk.
TEMPLATES = [
    "a photo of {}",
    "a street photo containing {}",
    "a cropped photo of {}",
    "a blurry photo of {}",
]

# What a tile is when it is not the answer. Scoring against these rather than
# against a single "something else" is what keeps a photograph of an empty road
# from looking like a weak match for whatever was asked.
BACKGROUNDS = [
    "a photo of an empty road",
    "a photo of grass and trees",
    "a photo of the sky",
    "a photo of a building wall",
    "a photo of a pavement",
    "a blurred photo of nothing in particular",
]

# COCO classes, for the object detector — the first thing asked, because it
# answers the question the challenge actually poses. CLIP is asked how much a
# picture looks like a sentence, which on a grid of street photographs is a
# question about the street: measured over grids checked by eye, a tile with no
# crosswalk in it scored 0.69 while a tile with one scored 0.48, and no
# threshold separates those. A detector is asked whether the thing is in the
# picture and where, and it either finds it or does not.
#
# Two spellings per class because detectors disagree on names: COCO says
# "motorcycle" and the darknet lineage says "motorbike", and a model trained on
# one and labelled with the other is common enough to be worth allowing for.
DETECT_CLASSES = {
    "bus": ("bus",),
    "car": ("car", "truck"),
    "taxi": ("car",),
    "truck": ("truck",),
    "bicycle": ("bicycle",),
    "motorcycle": ("motorcycle", "motorbike"),
    "traffic light": ("traffic light", "trafficlight"),
    "fire hydrant": ("fire hydrant", "firehydrant"),
    "parking meter": ("parking meter", "parkingmeter"),
    "boat": ("boat",),
    "train": ("train",),
    "bench": ("bench",),
    "clock": ("clock",),
}

# The detector, and the labels its class numbers mean. Absent either, the
# detector path is simply not taken.
DETECT_MODEL = "detect.onnx"
DETECT_LABELS = "detect-labels.json"

# How sure the detector has to be. Lower than it looks: a detector's confidence
# is a different scale from a classifier's, and on tiles this small a real bus
# at 0.4 is common while a hallucinated one above 0.35 is not. Measured over the
# grids checked by eye, this is where the fire hydrant that is there (0.86)
# clears and the one that is not (0.32) does not.
DETECT_CONFIDENCE = float(os.environ.get("POSTERN_DETECT_CONFIDENCE") or 0.35)

# What each layout is fed. A 3x3 tile is a whole photograph, so it goes in on
# its own at the size the detector likes; a 4x4 grid is one photograph cut up,
# so it goes in whole and the squares are read off the boxes.
#
# Bigger is not better for either. reCAPTCHA serves tiles about 96 pixels
# square, so 288 is already a 3x enlargement and 640 is a 6.7x one — and
# measured over the grids checked by eye, feeding it 640 cost five ticks it
# should have made and took seven times as long. A model exported at a fixed
# 640, which is how the published ONNX build comes, is used at 640 anyway;
# there is nothing else to do with it.
#
# The size does not behave smoothly, and the reason is worth knowing before
# anyone tries to tune it. Over the 48-grid bench:
#
#     224  40/48      256  36/48
#     288  42/48      320  36/48
#     352  42/48      384  37/48
#
# The left column is 32x7, 32x9 and 32x11, the right 32x8, 32x10 and 32x12.
# The backbone strides by 32, so the left column gives a feature map with an
# odd number of cells and therefore a centre one — and a tile laid centred on
# the field is exactly the case where that matters. Sizes here are multiples of
# 32 by an odd factor on purpose, not by accident.
DETECT_TILE_SIZE = 288
DETECT_GRID_SIZE = 288

# What the published build was traced at, and the only size it accepts.
DETECT_FIXED_SIZE = 640

# How much of a square a box has to cover for the square to count. A sixteenth
# of a bus is still a bus, but a box that merely clips the corner of a square is
# not: measured, the square above a bus that only its wing mirror reached came
# to 0.11 and the squares the bus was in to 0.42 and 0.69.
DETECT_OVERLAP = float(os.environ.get("POSTERN_DETECT_OVERLAP") or 0.05)

# How much of its neighbours a 4x4 square is shown with when it is read on its
# own. See the frames built in detected(); 0.05 above is still the right bar for
# the boxes those frames return — measured, raising it to 0.08 costs seven
# squares that are there and saves one that is not.
DETECT_TILE_MARGIN = float(os.environ.get("POSTERN_DETECT_TILE_MARGIN") or 0.55)

# How much two boxes have to share, as intersection over union, before they are
# taken to be two views of one thing rather than two things. High, because the
# views being merged are of the same object from frames at different scales, and
# the whole point is to keep the tightest of them.
DETECT_SAME_THING = float(os.environ.get("POSTERN_DETECT_SAME_THING") or 0.75)

# A trained head, for the categories nothing off the shelf can answer.
#
# Crosswalks are the case that forced this. COCO has no class for one, ADE20K
# has no class for one, an open-vocabulary detector asked for "a zebra crossing"
# scores lane markings higher than crossings, and CLIP asked whether a tile is a
# crosswalk answers a question about the street rather than about the paint —
# measured over grids checked by eye, it ticked nine squares in excess over
# three grids while missing none, because on a road every tile looks a bit like
# the answer.
#
# What works is the oldest trick in the transfer-learning book: keep CLIP's
# picture embedding, throw away its text side, and fit a logistic regression on
# top of it from labelled tiles. It is 512 numbers and a bias, it trains in
# seconds on a CPU, and examples/train-probe.py builds one from saved panels.
# Measured over seven labelled grids, trained on one series and tested on
# another so no tile appears in both: five ticks short and one in excess,
# against roughly four in excess per grid for zero-shot CLIP.
#
# The weights only mean anything against the encoder they were fitted on, so
# each file names its model and is ignored under any other.
PROBE_SHARE = float(os.environ.get("POSTERN_PROBE_SHARE") or 0.70)
PROBE_FLOOR = float(os.environ.get("POSTERN_PROBE_FLOOR") or 0.50)

# ADE20K classes, for the segmentation model. A 4x4 grid is one photograph, and
# the question "which squares hold the bus" is a question about pixels — so it
# is answered with a model that labels pixels, and the squares follow from the
# mask. Several classes per category because ADE20K splits what reCAPTCHA does
# not: a van and a truck are both a "camion", and a motorbike photographed at
# distance is labelled a bicycle about as often as not.
SEGMENT_CLASSES = {
    "bus": (80, 102, 83),
    "car": (20, 102, 83),
    "taxi": (20, 102),
    "truck": (83, 102),
    "bicycle": (127,),
    "motorcycle": (116, 127),
    "traffic light": (136,),
    "bridge": (61,),
    "mountain": (16, 68),
    "staircase": (53, 59),
    "boat": (76,),
    "palm tree": (72,),
}

# ImageNet normalisation, which is what the segmentation model was trained with
# — not CLIP's, which is different and would quietly cost accuracy.
SEGMENT_SIZE = 512
SEGMENT_MEAN = np.array([0.485, 0.456, 0.406], dtype=np.float32)
SEGMENT_STD = np.array([0.229, 0.224, 0.225], dtype=np.float32)

# How much of a square has to be covered by the mask for it to count, as a
# fraction of the most-covered square and as an absolute floor. Relative,
# because a bus fills half a square and a traffic light a twentieth of one;
# absolute, so that a grid holding none of the thing does not tick its least
# empty square.
SEGMENT_SHARE = float(os.environ.get("POSTERN_SEGMENT_SHARE") or 0.05)
SEGMENT_FLOOR = float(os.environ.get("POSTERN_SEGMENT_FLOOR") or 0.01)

# And how much of the grid has to be the thing at all before any square counts.
SEGMENT_PRESENT = float(os.environ.get("POSTERN_SEGMENT_PRESENT") or 0.03)

# A 3x3 tile is a photograph of its own, so there is no most-covered square to
# measure the others against and no grid-wide presence to check — only how much
# of this one tile is the thing. The bar is far lower than the grid's for a
# reason worth recording: over six labelled grids of bridges and hills, the
# squares that wanted ticking came in from 0.001 to 0.15, and every square that
# did not came in at exactly zero. There is no borderline case to split, so this
# sits an order of magnitude below the lowest real one and well clear of a
# handful of stray pixels.
SEGMENT_TILE_FLOOR = float(os.environ.get("POSTERN_SEGMENT_TILE_FLOOR") or 0.001)

# The segmentation model, looked for next to the CLIP one. Absent, the 4x4
# path is simply not taken.
SEGMENT_MODEL = "segment.onnx"

PASS = 2


class Pass(Exception):
    """Raised for a challenge this script cannot make sense of."""


def directory() -> str:
    return os.environ.get("POSTERN_CLIP_DIR") or os.path.dirname(os.path.abspath(__file__))


def session(name: str) -> ort.InferenceSession:
    options = ort.SessionOptions()
    options.log_severity_level = 3
    return ort.InferenceSession(
        os.path.join(directory(), name), options, providers=["CPUExecutionProvider"]
    )


def subject(prompt: str) -> tuple[str, ...]:
    """What the challenge is asking for, in English, in every way it is worth
    asking."""
    for pattern, english in CATEGORIES:
        if re.search(pattern, prompt, re.IGNORECASE):
            return english

    # Not in the table. Take whatever follows the verb and hope CLIP has seen
    # the word — better than refusing outright, and the caller can tell the
    # difference by whether anything scores.
    after = re.search(
        r"(?:montrant|avec|contenant|comportant|containing|with|showing)\s+"
        r"(?:des?\s+|du\s+|de la\s+|les?\s+|la\s+|une?\s+|the\s+|an?\s+)?(.+?)"
        r"(?:\s+(?:lorsque|quand|si|when|once|if)\b|[.,]|$)",
        prompt,
        re.IGNORECASE,
    )
    if not after:
        raise Pass(f"cannot tell what {prompt!r} is asking for")
    return (after.group(1).strip(),)


def phrasings(wanted: tuple[str, ...]) -> list[str]:
    """Every sentence that counts as asking for this."""
    sentences = []
    for name in wanted:
        # A name that already reads as a sentence ("a bridge over water") is
        # used as it stands; a bare noun gets the articles and the templates.
        article = "" if name.startswith(("a ", "an ", "the ")) else "a "
        for template in TEMPLATES:
            sentences.append(template.format(article + name))
    return sentences


def tiles() -> list[tuple[float, float, float, float]]:
    """The tile rectangles, as measured by postern."""
    given = os.environ.get("POSTERN_TILES", "").strip()
    if not given:
        raise Pass("no tile geometry: run this through postern, not by hand")

    boxes = []
    for part in given.split(";"):
        x, y, w, h = (float(n) for n in part.split(","))
        boxes.append((x, y, w, h))
    return boxes


def crop(panel: Image.Image, box: tuple, margin: float) -> Image.Image:
    """One tile, with some of its surroundings when asked for."""
    x, y, w, h = box
    pad_x, pad_y = w * margin, h * margin

    return panel.crop((
        int(max(0, x - pad_x)),
        int(max(0, y - pad_y)),
        int(min(panel.width, x + w + pad_x)),
        int(min(panel.height, y + h + pad_y)),
    ))


def embed_images(vision: ort.InferenceSession, images: list[Image.Image]) -> np.ndarray:
    batch = np.stack([preprocess(image) for image in images])
    embeds = vision.run(None, {"pixel_values": batch})[0]
    return normalise(embeds)


def preprocess(image: Image.Image) -> np.ndarray:
    resized = image.convert("RGB").resize((IMAGE_SIZE, IMAGE_SIZE), Image.Resampling.BICUBIC)
    pixels = (np.asarray(resized, dtype=np.float32) / 255.0 - MEAN) / STD
    return pixels.transpose(2, 0, 1)


def embed_texts(sentences: list[str]) -> np.ndarray:
    """Sentence embeddings, from the cache when they are in it.

    A sentence embedding never changes, and postern runs this script once per
    grid — so the text encoder would otherwise be loaded, run and thrown away
    for every round, to compute the same seven vectors it computed last time.
    """
    cache_path = os.path.join(directory(), "clip-text-cache.npz")
    cache = dict(np.load(cache_path)) if os.path.exists(cache_path) else {}

    missing = [s for s in sentences if s not in cache]
    if missing:
        tokenizer = Tokenizer.from_file(os.path.join(directory(), "clip-tokenizer.json"))
        tokenizer.enable_padding(pad_id=0, length=77)
        tokenizer.enable_truncation(max_length=77)

        ids = np.array([e.ids for e in tokenizer.encode_batch(missing)], dtype=np.int64)
        computed = normalise(session("clip-text.onnx").run(None, {"input_ids": ids})[0])

        cache.update(zip(missing, computed))
        np.savez(cache_path, **cache)

    return np.stack([cache[s] for s in sentences])


def normalise(embeds: np.ndarray) -> np.ndarray:
    return embeds / np.linalg.norm(embeds, axis=-1, keepdims=True)


def solve(image_path: str) -> list[tuple[float, float]]:
    prompt = os.environ.get("POSTERN_PROMPT", "").strip()
    if not prompt:
        raise Pass("no prompt: run this through postern, not by hand")

    wanted = subject(prompt)
    print(f"prompt: {prompt!r} -> {wanted[0]!r}", file=sys.stderr)

    panel = Image.open(image_path).convert("RGB")
    boxes = tiles()

    # Ask the detector first. It answers most of what reCAPTCHA asks — buses,
    # cars, bicycles, motorcycles, fire hydrants, parking meters and traffic
    # lights were 73% of the challenges served over a night of measuring — and
    # it answers it far better than the other two: on grids checked by eye it
    # was exactly right where scoring tiles with CLIP ticked nine squares in
    # excess over three grids, and where the mask below was one short and one
    # over.
    found = detected(panel, boxes, wanted)
    if found is None:
        # Then a head trained for this category, which is how the categories no
        # detector has a class for get answered.
        found = probed(panel, boxes, wanted)
    if found is not None:
        return [(x + w / 2, y + h / 2) for x, y, w, h in (boxes[i] for i in sorted(found))]

    # A 4x4 grid is one photograph, and which squares to tick is a question
    # about where the thing is rather than what each square looks like. That is
    # what a segmentation model answers, so it gets asked when there is one to
    # ask and the category is one it knows — bridges, mountains, stairs and palm
    # trees, which are the ones COCO has no word for. Measured over saved grids:
    # this ticks 3.5 squares out of sixteen on average, against 8.7 for scoring
    # each square — half the grid — and 1.8 for covering squares up.
    if len(boxes) == 16 and os.path.exists(os.path.join(directory(), SEGMENT_MODEL)):
        squares = segmented(panel, boxes, wanted)
        if squares is not None:
            return [(x + w / 2, y + h / 2) for x, y, w, h in (boxes[i] for i in sorted(squares))]

    # One vector for the question and one per thing it might be instead. The
    # question's vector is the average of its phrasings — several ways of asking
    # cancel out what is peculiar to any one of them, and averaging rather than
    # scoring each separately keeps this a choice between seven things, which is
    # what the thresholds are calibrated against.
    texts = np.stack([
        normalise(embed_texts(phrasings(wanted)).mean(axis=0)),
        *embed_texts(BACKGROUNDS),
    ])
    vision = session("clip-vision.onnx")

    def probability(images):
        s = embed_images(vision, images) @ texts.T
        scaled = np.exp(100.0 * (s - s.max(axis=-1, keepdims=True)))
        return (scaled / scaled.sum(axis=-1, keepdims=True))[:, 0]

    # The two layouts are different questions and want different answers. Nine
    # tiles are nine separate photographs: "is there a bus in this one" is a
    # question each of them can be asked on its own. Sixteen are one photograph
    # cut up, where a quarter of a bus fills four squares and none of them is a
    # picture of a bus — asking each square on its own gets you the middle of
    # the object and misses its edges, and asking each square plus a margin of
    # its neighbours gets you the empty tarmac beside it too.
    # Covering squares up is the better idea and the worse solver, for now: over
    # eighteen saved 4x4 grids it ticks 1.3 squares on average where those grids
    # want three to six, and eight of them come back empty. It is right when it
    # answers — one tick in excess over the grids checked by eye — but it stops
    # too early far too often, because it can only find a square whose covering
    # changes what the picture is *of*, and half a bus does not. Scoring each
    # square is cruder and ticks too much, but it ticks. Until that is fixed,
    # POSTERN_CLIP_LAYOUT=occlusion opts in.
    cut_up = len(boxes) == 16 and os.environ.get("POSTERN_CLIP_LAYOUT") == "occlusion"
    chosen = occluded(panel, boxes, probability) if cut_up else scored(panel, boxes, probability)

    # And, for the categories a pixel model knows, whatever it saw that CLIP did
    # not. Only on a 3x3 — a 4x4 was answered by the mask above and never got
    # here — and only ever adding: see segmented_tiles for why that is safe.
    if len(boxes) != 16 and os.path.exists(os.path.join(directory(), SEGMENT_MODEL)):
        chosen |= segmented_tiles(panel, boxes, wanted)

    hits = []
    for index in sorted(chosen):
        x, y, w, h = boxes[index]
        hits.append((x + w / 2, y + h / 2))
    return hits


def scored(panel: Image.Image, boxes: list, probability) -> set:
    """Every tile that is a picture of the thing, asked one at a time.

    For a 3x3 grid, which is what it is: nine photographs that happen to be
    displayed together. Also the fallback for a 4x4 on a model too coarse for
    the occlusion pass, where each square is scored twice — with a margin of
    its neighbours, so a sixteenth of a bus is recognisable at all, and bare,
    so the tarmac that merely borrows one is dropped — and combined with their
    geometric mean.
    """
    confidence = probability([crop(panel, box, 0.0) for box in boxes])
    if len(boxes) == 16:
        confidence = np.sqrt(confidence * probability(
            [crop(panel, box, TILE_MARGIN) for box in boxes]))

    chosen = {i for i, score in enumerate(confidence) if score >= CONFIDENCE}
    for i in sorted(chosen):
        print(f"tile {i}: {confidence[i]:.2f}", file=sys.stderr)
    if not chosen:
        print(f"best was {confidence.max():.2f}, below {CONFIDENCE}", file=sys.stderr)
    return chosen


def occluded(panel: Image.Image, boxes: list, probability) -> set:
    """Every square the thing is actually in, found by taking squares away.

    For a 4x4 grid, which is one photograph. Rather than ask what each square
    is, this asks what the picture stops being without it: grey out a square,
    score the whole picture again, and the drop is how much of the answer was
    in there. A square holding the front of a bus takes the bus with it; a
    square of tarmac beside it changes nothing.

    It is done one square at a time, greedily, because a picture with two
    bicycles in it does not stop being a picture of a bicycle when you cover
    one — measured, the second bicycle's own square dropped the score by 0.01,
    which is indistinguishable from tarmac. Covering the strongest square for
    good and asking again is what makes the next one visible: on that grid the
    same square then dropped it by 0.39.

    Stopping is the other half. The loop ends when what is left no longer looks
    like the thing at all, which is the signal that every square holding it has
    been taken — not when the drops get small, since the first drop on a
    crowded grid is the smallest one there is.
    """
    x0 = min(b[0] for b in boxes)
    y0 = min(b[1] for b in boxes)
    x1 = max(b[0] + b[2] for b in boxes)
    y1 = max(b[1] + b[3] for b in boxes)
    whole = panel.crop((int(x0), int(y0), int(x1), int(y1)))

    rects = [(int(x - x0), int(y - y0), int(x - x0 + w), int(y - y0 + h)) for x, y, w, h in boxes]

    def without(covered):
        out = whole.copy()
        for i in covered:
            out.paste(OCCLUSION_GREY, rects[i])
        return out

    chosen: list[int] = []
    for _ in range(len(boxes)):
        remaining = probability([without(chosen)])[0]
        if remaining < OCCLUSION_STOP:
            print(f"what is left scores {remaining:.2f}, nothing more to find", file=sys.stderr)
            break

        rest = [i for i in range(len(boxes)) if i not in chosen]
        drops = remaining - probability([without(chosen + [i]) for i in rest])

        best = int(np.argmax(drops))
        if drops[best] < OCCLUSION_DROP:
            print(f"best square only drops {drops[best]:.2f}, stopping", file=sys.stderr)
            break

        print(f"tile {rest[best]}: takes {drops[best]:.2f} of {remaining:.2f} with it",
              file=sys.stderr)
        chosen.append(rest[best])

    return set(chosen)


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: solver-clip.py <challenge.png>", file=sys.stderr)
        return 1

    try:
        points = solve(sys.argv[1])
    except Pass as exc:
        print(f"passing: {exc}", file=sys.stderr)
        return PASS
    except Exception as exc:
        print(exc, file=sys.stderr)
        return 1

    for x, y in points:
        print(f"{x:.0f},{y:.0f}")
    return 0



def encoder_fingerprint(name: str = "clip-vision.onnx") -> str:
    """What an installed picture encoder is, as a number a head can name.

    The file itself, hashed. Version strings and model names are not enough:
    the quantised and unquantised exports of one model share both, and so do
    two exports made by different tools from the same weights.
    """
    if name not in _fingerprints:
        digest = hashlib.sha256()
        with open(os.path.join(directory(), name), "rb") as handle:
            for block in iter(lambda: handle.read(1 << 20), b""):
                digest.update(block)
        _fingerprints[name] = digest.hexdigest()
    return _fingerprints[name]


_fingerprints: dict[str, str] = {}


def probed(panel: Image.Image, boxes: list, wanted: tuple) -> set | None:
    """Which tiles hold the thing, according to a head trained for it.

    Returns None when there is no head for this category, or when the head was
    fitted on a different encoder from the one installed.
    """
    for name in wanted:
        path = os.path.join(directory(), f"probe-{name.replace(' ', '-')}.json")
        if not os.path.exists(path):
            continue

        with open(path) as handle:
            head = json.load(handle)
        if head.get("model") and head["model"] != os.environ.get("POSTERN_CLIP_MODEL", "patch16"):
            print(f"ignoring {os.path.basename(path)}: fitted on {head['model']}", file=sys.stderr)
            continue

        # And the same encoder, not merely the same size of encoder. A head is
        # 512 numbers read against one particular set of embeddings; point it at
        # another export of nominally the same model and the numbers still
        # multiply, still come out between zero and one, and mean nothing. That
        # is not hypothetical — a head measured at 0.83 on a tile scored 0.16 on
        # the same tile under a different export of patch16, so it ticked
        # nothing, passed on every crosswalk grid, and two live runs died asking
        # for a category the solver was supposed to be able to answer. Nothing
        # in the output looked wrong; the scores were simply low.
        # Which encoder, before whether it is the right one. A head may be
        # fitted against an export nothing else here uses: the full-precision
        # patch16 is worth a grid in twelve to this head — a wide, plainly
        # painted crossing it otherwise scores at 0.30 against a bar of 0.33 —
        # and is worth nothing measurable to the zero-shot path, whose
        # thresholds are calibrated against the quantised one. So the head says
        # what to load, and only the grids that reach a head pay for it.
        wants = head.get("encoder_file", "clip-vision.onnx")
        if not os.path.exists(os.path.join(directory(), wants)):
            print(f"ignoring {os.path.basename(path)}: it was fitted against {wants}, "
                  f"which is not installed. Re-run examples/install-vision.sh.",
                  file=sys.stderr)
            continue

        fitted = head.get("encoder")
        if fitted and fitted != encoder_fingerprint(wants):
            print(f"ignoring {os.path.basename(path)}: fitted on a different export of "
                  f"{head.get('model', 'clip')} ({fitted[:12]}, {wants} is "
                  f"{encoder_fingerprint(wants)[:12]}). Refit it with examples/train-probe.py "
                  f"against the installed encoder.", file=sys.stderr)
            continue

        weights = np.asarray(head["weights"], dtype=np.float32)
        embeds = embed_images(session(wants),
                              [crop(panel, box, 0.0) for box in boxes])
        scores = 1.0 / (1.0 + np.exp(-(embeds @ weights + head["bias"])))

        # A head that was calibrated says where to read it, and that is better
        # than anything decided here: how sure a head is depends on what it was
        # fitted on, so one number cannot serve them all. Measured on grids the
        # head had never seen, its own bar halved the mistakes — six squares in
        # excess became three.
        #
        # Without one, fall back to a floor and a share of the best tile: a grid
        # holding none of the thing must tick nothing, and how confident the head
        # is varies from photograph to photograph.
        bar = head.get("bar")
        if bar is None:
            bar = max(PROBE_FLOOR, PROBE_SHARE * float(scores.max()))
        chosen = {i for i, score in enumerate(scores) if score >= bar}
        for index in sorted(chosen):
            print(f"tile {index}: {scores[index]:.2f} (head for {name}, bar {bar:.2f})",
                  file=sys.stderr)
        if not chosen:
            print(f"best was {scores.max():.2f}, below {bar:.2f}", file=sys.stderr)
        return chosen

    return None


def detected(panel: Image.Image, boxes: list, wanted: tuple) -> set | None:
    """Which tiles hold the thing, from a detector that knows what it is.

    Returns None when this cannot be answered here — no detector installed, or
    a category COCO has never heard of — so the caller falls back to the mask
    or to CLIP.
    """
    classes = next((DETECT_CLASSES[name] for name in wanted if name in DETECT_CLASSES), None)
    labels_path = os.path.join(directory(), DETECT_LABELS)
    if classes is None or not os.path.exists(os.path.join(directory(), DETECT_MODEL)):
        return None
    if not os.path.exists(labels_path):
        return None

    with open(labels_path) as handle:
        labels = {int(k): v.lower() for k, v in json.load(handle).items()}
    wanted_ids = {i for i, name in labels.items() if name in classes}
    if not wanted_ids:
        return None

    model = session(DETECT_MODEL)

    def fit(image: Image.Image, size: int) -> Image.Image:
        """The picture at `size`, keeping its own scale rather than filling.

        The published build only takes 640x640, so a 96px tile asked on its own
        used to be stretched nearly seven times before the model saw it — an
        enlargement of a thumbnail, which is not a photograph of anything.
        Measured on a bicycle in a dark porch this solver kept missing: the same
        model, the same weights, scored it 0.026 stretched and 0.234 laid on a
        640 field at its own size.

        Enlarged when the picture is smaller than the field and shrunk when it
        is larger, never past its own scale: a 390px grid still has to fit.
        """
        scale = min(size / image.width, size / image.height)
        if scale < 1.0:
            image = image.resize((max(1, int(image.width * scale)),
                                  max(1, int(image.height * scale))),
                                 Image.Resampling.LANCZOS)
        if image.size == (size, size):
            return image
        # Mid-grey rather than black: an abrupt edge against black is an edge,
        # and a detector will happily find things along it.
        field = Image.new("RGB", (size, size), (114, 114, 114))
        field.paste(image, ((size - image.width) // 2, (size - image.height) // 2))
        return field

    def feed(images: list[Image.Image], size: int, pad: bool) -> tuple[np.ndarray, np.ndarray]:
        batch = np.stack([
            np.asarray(fit(image.convert("RGB"), size) if pad
                       else image.convert("RGB").resize((size, size), Image.Resampling.LANCZOS),
                       dtype=np.float32) / 255.0
            for image in images
        ]).transpose(0, 3, 1, 2)
        return model.run(None, {"pixel_values": batch})

    def run(images: list[Image.Image], size: int, pad: bool = False) -> tuple[np.ndarray, np.ndarray]:
        # Only where the answer is a score. Laying a picture on a field moves
        # everything in it, so a box that comes back in the field's coordinates
        # no longer maps onto the panel — which is why the grid pass, whose
        # whole answer is boxes, still stretches.
        try:
            logits, boxes = feed(images, size, pad)
        except Exception:
            # The published build declares a dynamic input and then refuses
            # anything but the size it was traced at, deep inside the graph —
            # so the only way to know is to try. Export it yourself for the
            # sizes this would rather use; see install-vision.sh.
            print(f"detector will not take {size}px, falling back to {DETECT_FIXED_SIZE}",
                  file=sys.stderr)
            logits, boxes = feed(images, DETECT_FIXED_SIZE, pad)

        # Detectors score each class on its own — a picture can hold a bus and
        # a bicycle — so the scores are logistic, not a softmax over classes.
        return 1.0 / (1.0 + np.exp(-logits)), boxes

    # Nine tiles are nine photographs: ask each one whether the thing is in it.
    if len(boxes) != 16:
        scores, _ = run([crop(panel, box, 0.0) for box in boxes], DETECT_TILE_SIZE, pad=True)
        chosen = set()
        for index, tile in enumerate(scores):
            best = 0.0
            for query in tile:
                if int(query.argmax()) in wanted_ids:
                    best = max(best, float(query.max()))
            if best >= DETECT_CONFIDENCE:
                print(f"tile {index}: {best:.2f}", file=sys.stderr)
                chosen.add(index)
        return chosen

    # Sixteen are one photograph: find the thing in it, then read off which
    # squares its box covers.
    x0 = min(b[0] for b in boxes)
    y0 = min(b[1] for b in boxes)
    x1 = max(b[0] + b[2] for b in boxes)
    y1 = max(b[1] + b[3] for b in boxes)

    # The whole picture, and then each square again with a margin of its
    # neighbours around it. The whole picture is what says where a bus is; the
    # close-ups are what find the things too small to survive being one
    # sixteenth of it. A 4x4 grid is about 390 pixels across, so a bicycle in
    # one square is ninety-odd pixels and perhaps seventy by the time the grid
    # has been fitted into the field — while the same bicycle in a 3x3 tile,
    # where it fills the frame, is found comfortably. Reading each square with
    # room around it gives it the whole field to itself.
    #
    # Measured over the 48-grid bench, this is what took the misses to nothing:
    # the whole-grid pass alone left two squares unfound — a bicycle half hidden
    # behind others in a corner, and motorcycles parked behind a car — and both
    # are found here. What it costs is excess: a magnified square sometimes
    # yields a box for something the whole picture put elsewhere.
    #
    # The margin is a fraction of a square, and it wants to be about half of
    # one. The bench score wanders by a couple of grids between 0.4 and 0.7,
    # which is more than the difference is worth; what is steady across that
    # range is that nothing goes unfound. 0.55 is the least wrong of them, and
    # is the fraction TILE_MARGIN already uses for the same reason.
    frames = [(x0, y0, x1, y1)]
    for sx, sy, sw, sh in boxes:
        mx, my = sw * DETECT_TILE_MARGIN, sh * DETECT_TILE_MARGIN
        frames.append((max(x0, sx - mx), max(y0, sy - my),
                       min(x1, sx + sw + mx), min(y1, sy + sh + my)))

    found: list[tuple[float, float, float, float, float]] = []
    for fx0, fy0, fx1, fy1 in frames:
        scores, coords = run([panel.crop((int(fx0), int(fy0), int(fx1), int(fy1)))],
                             DETECT_GRID_SIZE)

        for query, box in zip(scores[0], coords[0]):
            if int(query.argmax()) not in wanted_ids or float(query.max()) < DETECT_CONFIDENCE:
                continue

            # Boxes come back as centre, width and height, as a fraction of the
            # picture; the squares are in the panel's pixels.
            cx, cy, w, h = box
            left, right = (cx - w / 2) * (fx1 - fx0) + fx0, (cx + w / 2) * (fx1 - fx0) + fx0
            top, bottom = (cy - h / 2) * (fy1 - fy0) + fy0, (cy + h / 2) * (fy1 - fy0) + fy0
            print(f"found one at {left:.0f},{top:.0f} {right-left:.0f}x{bottom-top:.0f} "
                  f"({float(query.max()):.2f})", file=sys.stderr)

            found.append((float(query.max()), left, top, right, bottom))

    # Seventeen frames see the same objects, so one motorcycle comes back as a
    # dozen boxes of varying looseness — and it is the loosest of them that
    # reaches into a square the thing is not in. Keeping the best-scoring box of
    # each cluster is what a detector already does within a single frame; doing
    # it across frames is the same step, and without it this is the union of
    # every frame's worst localisation rather than of its best.
    #
    # Measured over the 48-grid bench it is worth a whole grid — the square that
    # went was one a tyre crossed by three pixels — and it behaves the way a
    # threshold ought to: 0.35 and 0.45 give 43, 0.55 gives 44, 0.65 through 0.85
    # give 45, and past 0.9 it suppresses nothing and comes back to 44, which is
    # the score without it. Fitting the bar by leaving one grid out at a time
    # gives 45 as well, so it is not the grid it fixes that is holding it up.
    kept: list[tuple[float, float, float, float, float]] = []
    for score, left, top, right, bottom in sorted(found, key=lambda box: -box[0]):
        for _, other_left, other_top, other_right, other_bottom in kept:
            across = max(0.0, min(right, other_right) - max(left, other_left))
            down = max(0.0, min(bottom, other_bottom) - max(top, other_top))
            both = (right - left) * (bottom - top) \
                + (other_right - other_left) * (other_bottom - other_top) - across * down
            if both > 0 and across * down / both >= DETECT_SAME_THING:
                break
        else:
            kept.append((score, left, top, right, bottom))

    covered: dict[int, float] = {}
    for _, left, top, right, bottom in kept:
        for index, (sx, sy, sw, sh) in enumerate(boxes):
            overlap_x = max(0.0, min(sx + sw, right) - max(sx, left))
            overlap_y = max(0.0, min(sy + sh, bottom) - max(sy, top))
            share = overlap_x * overlap_y / (sw * sh)
            covered[index] = max(covered.get(index, 0.0), share)

    chosen = {i for i, share in covered.items() if share >= DETECT_OVERLAP}
    for index in sorted(chosen):
        print(f"tile {index}: {covered[index]*100:.0f}% covered", file=sys.stderr)
    return chosen


def segmented(panel: Image.Image, boxes: list, wanted: tuple) -> set | None:
    """Which squares the thing is in, from a mask of where it is.

    Returns None when the category is not one the segmentation model was
    trained on — ADE20K has a bus and a bridge but no crosswalk and no fire
    hydrant — so the caller can fall back to scoring squares.
    """
    classes = next((SEGMENT_CLASSES[name] for name in wanted if name in SEGMENT_CLASSES), None)
    if classes is None:
        return None

    # The panel is a prompt above a grid; only the grid is a photograph.
    x0 = min(b[0] for b in boxes)
    y0 = min(b[1] for b in boxes)
    x1 = max(b[0] + b[2] for b in boxes)
    y1 = max(b[1] + b[3] for b in boxes)
    grid = panel.crop((int(x0), int(y0), int(x1), int(y1)))

    pixels = np.asarray(grid.resize((SEGMENT_SIZE, SEGMENT_SIZE), Image.Resampling.BICUBIC),
                        dtype=np.float32) / 255.0
    batch = ((pixels - SEGMENT_MEAN) / SEGMENT_STD).transpose(2, 0, 1)[None]

    logits = session(SEGMENT_MODEL).run(None, {"pixel_values": batch})[0][0]
    mask = np.isin(logits.argmax(0), classes)

    # The mask comes back at its own resolution, a quarter of the input; the
    # squares are in the panel's. Scaling the squares into the mask rather than
    # the mask into the panel keeps this to arithmetic.
    height, width = mask.shape
    scale_x, scale_y = width / (x1 - x0), height / (y1 - y0)

    share = []
    for x, y, w, h in boxes:
        square = mask[int((y - y0) * scale_y):int((y - y0 + h) * scale_y),
                      int((x - x0) * scale_x):int((x - x0 + w) * scale_x)]
        share.append(float(square.mean()) if square.size else 0.0)

    peak = max(share)
    if peak < SEGMENT_PRESENT:
        print(f"no {classes} in the grid at all, best square {peak:.2f}", file=sys.stderr)
        return set()

    chosen = {i for i, v in enumerate(share) if v >= max(SEGMENT_FLOOR, SEGMENT_SHARE * peak)}
    for i in sorted(chosen):
        print(f"tile {i}: {share[i]*100:.0f}% covered", file=sys.stderr)
    return chosen


def segmented_tiles(panel: Image.Image, boxes: list, wanted: tuple) -> set:
    """The same question asked of a 3x3 grid, one photograph at a time.

    Nine tiles are nine photographs, so there is no mask over the grid to read
    squares off — each tile gets its own. This is not a substitute for scoring
    them with CLIP; it is a second opinion, and the caller takes the union.

    Which is safe because of how the two are wrong. CLIP scores a whole tile
    against a sentence, so a motorway with an overpass in the distance reads as
    a photograph of a motorway: measured over the ponts grid, the real
    footbridge came to 0.30, below a tile with no bridge in it at 0.35, and no
    threshold separates them. A model that labels pixels has no such problem —
    it either finds bridge pixels or it does not. Over six labelled grids of
    bridges and hills, 54 tiles, it found some in nine of the squares that
    wanted them and in *none* of the squares that did not, with either model:
    perfect precision, partial recall. Union with CLIP recovered three squares
    and cost nothing, taking those grids from three exactly right to five.

    The recall is the part that depends on the model. B0, which install-vision.sh
    fetches by default, sees hills well and bridges not at all — every bridge
    tile came back at zero. B4 sees both. Nothing breaks without it; the bridges
    simply stay CLIP's problem.
    """
    classes = next((SEGMENT_CLASSES[name] for name in wanted if name in SEGMENT_CLASSES), None)
    if classes is None:
        return set()

    model = session(SEGMENT_MODEL)
    chosen = set()
    for index, box in enumerate(boxes):
        tile = crop(panel, box, 0.0).resize((SEGMENT_SIZE, SEGMENT_SIZE),
                                            Image.Resampling.BICUBIC)
        pixels = np.asarray(tile.convert("RGB"), dtype=np.float32) / 255.0
        batch = ((pixels - SEGMENT_MEAN) / SEGMENT_STD).transpose(2, 0, 1)[None]
        mask = model.run(None, {"pixel_values": batch})[0][0].argmax(0)
        share = float(np.isin(mask, classes).mean())
        if share >= SEGMENT_TILE_FLOOR:
            print(f"tile {index}: {share*100:.1f}% of it is the thing", file=sys.stderr)
            chosen.add(index)
    return chosen


if __name__ == "__main__":
    sys.exit(main())
