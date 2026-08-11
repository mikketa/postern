#!/usr/bin/env python3
"""Template for a postern image solver.

Postern runs this with the path to a PNG of the challenge panel — prompt and
grid included — and reads click positions from stdout:

    one "x,y" per line, in pixels within that image
    no output at all means "nothing to click"
    a non-zero exit means "could not solve"

Wire it up with:

    postern solve -kind recaptcha-v2 -url ... -sitekey ... \\
        -image-solver "python3 examples/solver-template.py"

The vision model is yours to choose. What matters is the shape of the answer,
and the speed: reCAPTCHA expires a challenge while you think about it, so a
solver that takes half a minute will be told the validation has expired even
when its answer was right. Aim for a couple of seconds.

Two things worth knowing before you write the model call:

  * The panel is not one fixed size. It came out 300x480 for one challenge type
    and 400x580 for another, so derive the grid from the image you were handed
    rather than hardcoding a layout.
  * Some challenges are dynamic: clicking a correct tile replaces it with a new
    picture rather than ending the round. Postern will call you again with the
    fresh panel, up to three times per solve.
"""

import sys


def solve(image_path: str) -> list[tuple[float, float]]:
    """Return the points to click, in pixels within the given image.

    Replace the body with a call to whatever is doing the looking. A rough
    starting point for the usual 3x3 layout, measured on a live challenge:
    the grid sits below the prompt, roughly square, and spans the full width
    of the panel minus a small margin.
    """
    raise NotImplementedError(
        f"point this at a vision model: read the prompt out of {image_path}, "
        "find the tiles that match it, return their centres"
    )


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: solver.py <challenge.png>", file=sys.stderr)
        return 2

    try:
        points = solve(sys.argv[1])
    except Exception as exc:  # a solver that cannot answer says so by failing
        print(exc, file=sys.stderr)
        return 1

    for x, y in points:
        print(f"{x:.0f},{y:.0f}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
