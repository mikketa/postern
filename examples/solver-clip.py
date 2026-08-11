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
CONFIDENCE = 0.34

# For the 4x4 layout, how much of the neighbouring squares to include. One
# sixteenth of a bus is not a bus to anything that looks at pictures; with a
# margin the square is recognisable, and the object still has to be in the
# middle of it to score.
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

    panel = Image.open(image_path)
    boxes = tiles()

    # A 3x3 grid is nine separate photographs; a 4x4 is one picture cut up, so
    # each square is shown with its surroundings to be recognisable at all.
    margin = TILE_MARGIN if len(boxes) == 16 else 0.0
    crops = [crop(panel, box, margin) for box in boxes]

    # One vector for the question and one per thing it might be instead. The
    # question's vector is the average of its phrasings — several ways of asking
    # cancel out what is peculiar to any one of them, and averaging rather than
    # scoring each separately keeps this a choice between seven things, which is
    # what CONFIDENCE is calibrated against.
    texts = np.stack([
        normalise(embed_texts(phrasings(wanted)).mean(axis=0)),
        *embed_texts(BACKGROUNDS),
    ])

    scores = embed_images(session("clip-vision.onnx"), crops) @ texts.T

    # Softmax across those: how much better the answer fits this tile than any
    # of the things it might otherwise be.
    scaled = np.exp(100.0 * (scores - scores.max(axis=-1, keepdims=True)))
    confidence = (scaled / scaled.sum(axis=-1, keepdims=True))[:, 0]

    hits = []
    for index, (box, score) in enumerate(zip(boxes, confidence)):
        if score >= CONFIDENCE:
            x, y, w, h = box
            hits.append((x + w / 2, y + h / 2))
            print(f"tile {index}: {score:.2f}", file=sys.stderr)

    if not hits:
        print(f"best was {confidence.max():.2f}, below {CONFIDENCE}", file=sys.stderr)
    return hits


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
