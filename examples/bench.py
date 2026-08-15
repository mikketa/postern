#!/usr/bin/env python3
"""Scores an image solver against grids whose answers are known.

    python examples/bench.py ~/panels labels.json --solver ~/.cache/postern-vision/solve

A live run measures the solver, the address, Google's mood and the weather all
at once, and reports one bit at the end. Six runs in an evening produced 0, 1
and 2 tokens out of three with nothing changed in between — a difference that
would swamp any change worth making. So changes are judged here instead:
the same grids, the same answers, deterministic, and in seconds rather than
minutes.

`labels.json` is `{"<panel name>": [tile numbers that match], ...}`, written by
eye — the same format `train-probe.py` takes, and an empty list is a real
answer meaning the grid holds none of what was asked.

What it reports is grids answered *exactly*, because that is what reCAPTCHA
grades. A grid with one square missed is not 89% right, it is wrong: on a
dynamic grid a missed square brings the challenge back, and on a static one it
fails. Missed and excess squares are reported alongside, since they say which
way a solver is wrong and they are what a threshold trades between.

Exit status is 0 whatever the score. This measures, it does not judge.
"""

import argparse
import json
import os
import subprocess
import sys
from collections import defaultdict


def tiles_of(meta):
    return [tuple(int(v) for v in part.split(",")) for part in meta["tiles"].split(";")]


def square_at(boxes, x, y):
    """Which tile a point falls in, or None between tiles."""
    for i, (bx, by, bw, bh) in enumerate(boxes):
        if bx <= x < bx + bw and by <= y < by + bh:
            return i
    return None


def find(dirs, stem):
    for directory in dirs:
        shot = os.path.join(directory, stem + ".png")
        meta = os.path.join(directory, stem + ".json")
        if os.path.exists(shot) and os.path.exists(meta):
            return shot, meta
    return None


def ask(solver, shot, meta, timeout):
    """Run the solver exactly as postern runs it."""
    environment = dict(os.environ)
    environment["POSTERN_PROMPT"] = meta.get("prompt", "")
    environment["POSTERN_TILES"] = meta["tiles"]
    environment["POSTERN_COLUMNS"] = str(meta.get("columns", ""))

    try:
        done = subprocess.run([solver, shot], env=environment, timeout=timeout,
                              capture_output=True, text=True)
    except subprocess.TimeoutExpired:
        return None, "timed out"
    # Exit 2 is the solver saying it cannot answer this category, which is not
    # a wrong answer and must not be scored as one.
    if done.returncode == 2:
        return None, "passed"
    if done.returncode != 0:
        return None, f"exit {done.returncode}"

    points = []
    for line in done.stdout.splitlines():
        line = line.strip()
        if not line or "," not in line:
            continue
        try:
            x, y = (float(v) for v in line.split(",", 1))
        except ValueError:
            continue
        points.append((x, y))
    return points, ""


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("panels", nargs="+", help="directories holding the saved panels")
    parser.add_argument("labels", help="json of panel name to the tiles that match")
    parser.add_argument("--solver", required=True, help="the -image-solver command")
    parser.add_argument("--timeout", type=float, default=120.0)
    parser.add_argument("--only", default="", help="only grids whose prompt contains this")
    parser.add_argument("--quiet", action="store_true", help="totals only")
    args = parser.parse_args()

    with open(args.labels) as handle:
        labels = {str(k): set(v) for k, v in json.load(handle).items()}

    exact = graded = missed = excess = 0
    passed = broken = 0
    by_category = defaultdict(lambda: [0, 0])

    for stem in sorted(labels):
        found = find(args.panels, stem)
        if found is None:
            print(f"{stem}: no panel found", file=sys.stderr)
            continue
        shot, meta_path = found
        with open(meta_path) as handle:
            meta = json.load(handle)
        prompt = meta.get("prompt", "")
        if args.only and args.only.lower() not in prompt.lower():
            continue

        boxes = tiles_of(meta)
        points, trouble = ask(args.solver, shot, meta, args.timeout)
        # A category the solver refuses is counted apart. Folding a refusal into
        # the score would say a solver that answers nothing is as wrong as one
        # that answers badly, and postern treats the two very differently — it
        # asks for another challenge rather than submitting.
        if points is None:
            if trouble == "passed":
                passed += 1
            else:
                broken += 1
                print(f"{stem}: {trouble}", file=sys.stderr)
            continue

        picked = {i for i in (square_at(boxes, x, y) for x, y in points) if i is not None}
        truth = labels[stem]
        short, over = len(truth - picked), len(picked - truth)

        graded += 1
        missed += short
        excess += over
        # Category as reCAPTCHA words it, trimmed to the noun it asks about.
        name = prompt.split("montrant", 1)[-1].strip()[:28] or prompt[:28]
        by_category[name][1] += 1
        if not short and not over:
            exact += 1
            by_category[name][0] += 1
        elif not args.quiet:
            print(f"  {stem} {name}: picked {sorted(picked)} want {sorted(truth)}"
                  f"  (-{short} +{over})")

    if not graded:
        print("nothing graded")
        return

    if not args.quiet:
        print()
        for name in sorted(by_category, key=lambda n: -by_category[n][1]):
            good, total = by_category[name]
            print(f"  {good:2d}/{total:2d}  {name}")
        print()
    print(f"{exact}/{graded} grids exact ({100 * exact / graded:.0f}%), "
          f"-{missed} +{excess} squares"
          + (f", {passed} passed" if passed else "")
          + (f", {broken} failed" if broken else ""))


if __name__ == "__main__":
    main()
