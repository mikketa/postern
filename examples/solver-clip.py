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
    cut_up = len(boxes) == 16 and os.environ.get("POSTERN_CLIP_LAYOUT") != "tiles"
    chosen = occluded(panel, boxes, probability) if cut_up else scored(panel, boxes, probability)

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


if __name__ == "__main__":
    sys.exit(main())
