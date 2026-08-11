#!/usr/bin/env python3
"""A working postern image solver, built on YOLOS-tiny.

It maps the prompt to a COCO class, runs object detection on each tile of the
grid, and prints the centre of every tile where that class shows up:

    postern solve -kind recaptcha-v2 -url ... -sitekey ... \\
        -image-solver "/path/to/venv/bin/python examples/solver-yolos.py"

Setup:

    python3 -m venv venv && venv/bin/pip install onnxruntime numpy pillow
    curl -L -o yolos-tiny.onnx \\
        https://huggingface.co/Xenova/yolos-tiny/resolve/main/onnx/model.onnx

    # point POSTERN_MODEL at the .onnx if it is not next to this script

Postern hands over the prompt and the exact tile rectangles in the environment,
so there is no OCR here and no guessing at the grid. Install pytesseract and set
POSTERN_OCR=1 to fall back to reading the panel, which is only useful when
running this script by hand on a saved screenshot.

What it can and cannot see: COCO gives it bicycles, cars, buses, motorcycles,
trucks, boats, trains, traffic lights, fire hydrants and parking meters, which
is most of what reCAPTCHA asks for. It has no answer for crosswalks, stairs,
chimneys or mountains — those need a model trained on them, and swapping this
file is the intended way to bring one.
"""

import os
import re
import sys

import numpy as np
import onnxruntime as ort
from PIL import Image

# Where the grid sits inside the panel, used only when postern has not said.
# Proportions rather than pixels because the panel is not one fixed size —
# 300x480 and 400x580 have both been seen in the wild. These are an estimate;
# POSTERN_TILES is a measurement, and is preferred whenever it is there.
GRID_TOP_RATIO = 0.27
GRID_BOTTOM_RATIO = 0.88
GRID_SIDE_MARGIN = 0.02

# Detection settings. The threshold is deliberately low: a missed tile fails the
# challenge outright, while a spurious one merely costs another round. reCAPTCHA
# degrades its photographs on purpose — 100 pixels a side, heavy noise — and a
# model reading them is far less sure of itself than the same model reading a
# clean picture.
SCORE_THRESHOLD = 0.15
MODEL_INPUT = 512

# How much of a square a detection has to cover before that square counts, in
# the 4x4 layout. Low, because reCAPTCHA marks a square correct when the object
# merely appears in it — a wing mirror is enough.
TILE_COVERAGE = 0.12

# What postern reads as "ask me a different one". Anything else non-zero is a
# failure that ends the solve.
PASS = 2


class Pass(Exception):
    """Raised for a challenge this model has no answer for."""


# COCO ids, as published in the model config.
COCO = {
    "bicycle": 2,
    "car": 3,
    "motorcycle": 4,
    "bus": 6,
    "train": 7,
    "truck": 8,
    "boat": 9,
    "traffic light": 10,
    "fire hydrant": 11,
    "parking meter": 14,
}

# What reCAPTCHA asks for, in the two languages it serves most, mapped to the
# classes that answer it. A bus challenge accepts trucks often enough to be
# worth including; a bicycle challenge does not accept motorcycles.
PROMPTS = [
    (r"borne.{0,10}incendie|fire hydrant", ["fire hydrant"]),
    (r"v[ée]lo|bicyclette|bicycle", ["bicycle"]),
    (r"feux?.{0,15}circulation|traffic light", ["traffic light"]),
    (r"bus|autobus", ["bus", "truck"]),
    (r"moto|motorcycle", ["motorcycle"]),
    (r"camion|truck", ["truck"]),
    (r"voiture|car\b|v[ée]hicule", ["car", "truck"]),
    (r"bateau|boat", ["boat"]),
    (r"parcm[èe]tre|parking meter", ["parking meter"]),
    (r"train", ["train"]),
]


def read_prompt(panel: Image.Image) -> str:
    """The instruction, straight from postern or, failing that, from the panel."""
    given = os.environ.get("POSTERN_PROMPT", "").strip()
    if given:
        return " ".join(given.split()).lower()

    if not os.environ.get("POSTERN_OCR"):
        return ""

    import pytesseract  # only needed for the fallback; see the module docstring

    header = panel.crop((0, 0, panel.width, int(panel.height * GRID_TOP_RATIO)))
    text = pytesseract.image_to_string(header, lang="fra+eng")
    return " ".join(text.split()).lower()


def read_tiles(panel: Image.Image, prompt: str) -> list[tuple[float, float, float, float]]:
    """The tile rectangles, measured by postern or estimated from the panel.

    Estimating is the lesser path: the grid is 3x3 or 4x4 depending on the
    challenge, both are square, and the wording is the only clue in the picture
    that tells them apart. Getting it wrong is not a near miss — the tiles no
    longer line up and every click lands between two of them.
    """
    given = os.environ.get("POSTERN_TILES", "").strip()
    if given:
        boxes = []
        for part in given.split(";"):
            x, y, w, h = (float(n) for n in part.split(","))
            boxes.append((x, y, w, h))
        return boxes

    columns = int(os.environ.get("POSTERN_COLUMNS") or 0) or grid_columns(prompt)
    margin = panel.width * GRID_SIDE_MARGIN
    left, width = margin, panel.width - 2 * margin
    top = panel.height * GRID_TOP_RATIO
    height = panel.height * GRID_BOTTOM_RATIO - top

    return [
        (left + width * col / columns, top + height * row / columns,
         width / columns, height / columns)
        for row in range(columns)
        for col in range(columns)
    ]


def wanted_classes(prompt: str) -> list[int]:
    """Map the prompt to the COCO ids that satisfy it."""
    for pattern, names in PROMPTS:
        if re.search(pattern, prompt):
            return [COCO[n] for n in names]
    return []


def grid_columns(prompt: str) -> int:
    """How many tiles per side, going by the wording alone.

    reCAPTCHA says "images" for the 3x3 grid of separate photographs and
    "squares"/"cases" for the 4x4 grid laid over one picture. Only used when
    postern has not measured the grid for us.
    """
    if re.search(r"\bcases?\b|\bsquares?\b|\bcarr[ée]s?\b", prompt):
        return 4
    return 3


def detect(session: ort.InferenceSession, image: Image.Image) -> list[tuple[int, float, tuple]]:
    """Return (class id, score, box) for everything found in one image.

    The box is (x0, y0, x1, y1) as a fraction of the image, which is how the
    model reports it and what makes it usable at any scale.
    """
    resized = image.convert("RGB").resize((MODEL_INPUT, MODEL_INPUT))

    pixels = np.asarray(resized, dtype=np.float32) / 255.0
    pixels = (pixels - np.array([0.485, 0.456, 0.406], dtype=np.float32)) / np.array(
        [0.229, 0.224, 0.225], dtype=np.float32
    )
    pixels = pixels.transpose(2, 0, 1)[None, ...]

    logits, boxes = session.run(None, {"pixel_values": pixels})

    # Softmax over the class axis; the final column is "no object" and is
    # dropped rather than competed with.
    scores = np.exp(logits[0] - logits[0].max(axis=-1, keepdims=True))
    scores /= scores.sum(axis=-1, keepdims=True)
    scores = scores[:, :-1]

    found = []
    for index, (class_id, score) in enumerate(zip(scores.argmax(axis=-1), scores.max(axis=-1))):
        if score < SCORE_THRESHOLD:
            continue
        cx, cy, w, h = boxes[0][index]
        found.append((int(class_id), float(score),
                      (cx - w / 2, cy - h / 2, cx + w / 2, cy + h / 2)))
    return found


def overlap(tile: tuple, box: tuple, width: float, height: float) -> float:
    """How much of a tile a detection covers, as a fraction of the tile."""
    x, y, w, h = tile
    bx0, by0, bx1, by1 = (box[0] * width, box[1] * height, box[2] * width, box[3] * height)

    dx = max(0.0, min(x + w, bx1) - max(x, bx0))
    dy = max(0.0, min(y + h, by1) - max(y, by0))
    return (dx * dy) / (w * h) if w and h else 0.0


def solve(image_path: str) -> list[tuple[float, float]]:
    panel = Image.open(image_path)

    prompt = read_prompt(panel)
    targets = wanted_classes(prompt)
    print(f"prompt: {prompt!r} -> classes {targets}", file=sys.stderr)
    if not targets:
        # COCO has no crosswalks, stairs, chimneys or mountains. Guessing at one
        # of those would be marked wrong; passing gets a grid we can read.
        raise Pass(prompt)

    model = os.environ.get("POSTERN_MODEL") or os.path.join(
        os.path.dirname(os.path.abspath(__file__)), "yolos-tiny.onnx"
    )
    session = ort.InferenceSession(model, providers=["CPUExecutionProvider"])

    tiles = read_tiles(panel, prompt)
    if len(tiles) == 16:
        return solve_whole(session, panel, tiles, targets)
    return solve_per_tile(session, panel, tiles, targets)


def solve_per_tile(session, panel, tiles, targets) -> list[tuple[float, float]]:
    """A 3x3 grid is nine separate photographs, so each is classified alone."""
    hits = []
    for index, (x, y, w, h) in enumerate(tiles):
        tile = panel.crop((int(x), int(y), int(x + w), int(y + h)))

        for class_id, score, _ in detect(session, tile):
            if class_id in targets:
                hits.append((x + w / 2, y + h / 2))
                print(f"tile {index}: class {class_id} at {score:.2f}", file=sys.stderr)
                break
    return hits


def solve_whole(session, panel, tiles, targets) -> list[tuple[float, float]]:
    """A 4x4 grid is one picture cut into sixteen, so it is read as one picture.

    Classifying a 72-pixel square on its own is close to hopeless: a bus spread
    across six of them is, in each, an unrecognisable slab of paintwork. Run the
    detector over the whole grid instead and tick whichever squares its boxes
    actually cover.
    """
    left = min(t[0] for t in tiles)
    top = min(t[1] for t in tiles)
    right = max(t[0] + t[2] for t in tiles)
    bottom = max(t[1] + t[3] for t in tiles)

    grid = panel.crop((int(left), int(top), int(right), int(bottom)))
    width, height = float(right - left), float(bottom - top)

    boxes = [(c, s, b) for c, s, b in detect(session, grid) if c in targets]
    for class_id, score, box in boxes:
        print(f"whole grid: class {class_id} at {score:.2f} box {box}", file=sys.stderr)

    hits = []
    for index, (x, y, w, h) in enumerate(tiles):
        local = (x - left, y - top, w, h)
        covered = max((overlap(local, b, width, height) for _, _, b in boxes), default=0.0)

        if covered >= TILE_COVERAGE:
            hits.append((x + w / 2, y + h / 2))
            print(f"tile {index}: {covered:.0%} covered", file=sys.stderr)
    return hits


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: solver-yolos.py <challenge.png>", file=sys.stderr)
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
