#!/usr/bin/env python3
"""Exports the two models this example wants and cannot download ready-made.

    python examples/export-models.py ~/.cache/postern-vision

Both are published as PyTorch weights, and what is on the hub as ONNX is either
a different model or a build that will not do what the solver asks of it:

  * **The detector.** RT-DETR's published ONNX declares a fully dynamic input
    and then refuses anything but 640x640, deep inside the graph — the feature
    map's size was computed once while tracing and frozen as a literal, so
    `Reshape` asks for 400 positions (20x20, and 20 is 640/32) whatever it is
    given. Marking the outer axes dynamic does not reach constants inside, and
    since it works perfectly at 640 nobody noticed. Exporting it here keeps
    those computations symbolic, which lets a 96px tile be laid on a 288px
    field — a three-fold enlargement rather than a seven-fold one.

  * **The segmenter.** SegFormer-B0 is on the hub as ONNX and B4 is not. B0
    sees hills well and bridges not at all: over six labelled grids of bridges
    and hills it found every hill and never once predicted the bridge class,
    which is in its vocabulary.

Measured over the 48-grid bench, same solver, same everything else:

    published detector, SegFormer-B0          38/48 grids exact
    exported detector, SegFormer-B0           47/48
    exported detector, SegFormer-B4           48/48

Nothing here is required. The solver works with the published builds and this
is what the difference costs: about 430MB of models, a few minutes of CPU, and
roughly 1.4GB of PyTorch to produce them, which is why `install-vision.sh` only
does this when asked with `-export`.

Re-running is free: each model is checked before it is built, and checked for
what it must be able to do rather than for its name.
"""

import argparse
import os
import sys

DETECTOR = "PekingU/rtdetr_r50vd_coco_o365"
SEGMENTER = "nvidia/segformer-b4-finetuned-ade-512-512"

# The size the solver lays a tile on, and the whole reason for re-exporting.
DETECT_SIZE = 288
# What SegFormer is always asked at here.
SEGMENT_SIZE = 512

# B0 is 3.7M parameters and B4 is 64M. Anything in between is neither, and the
# point of the check is to tell them apart, not to be precise.
B4_PARAMETERS = 20_000_000


def detector_takes(path: str, size: int) -> bool:
    """Whether the detector at `path` will run at `size`, which is the question.

    Asked by running it rather than by looking at the file, because the file
    says yes and means no: the published build advertises a dynamic input and
    fails at run time, which is exactly the trap this script exists to get out
    of. A model that cannot be loaded at all counts as not there.
    """
    try:
        import numpy as np
        import onnxruntime
    except ImportError:
        print("no onnxruntime here, so the detector cannot be tried before rebuilding it",
              file=sys.stderr)
        return False
    # Quiet, because the interesting failure prints a page of graph internals
    # and it is an expected answer here, not an error.
    options = onnxruntime.SessionOptions()
    options.log_severity_level = 3
    try:
        session = onnxruntime.InferenceSession(path, options, providers=["CPUExecutionProvider"])
        session.run(None, {"pixel_values": np.zeros((1, 3, size, size), dtype=np.float32)})
    except Exception:
        return False
    return True


def parameters(path: str) -> int:
    """How many weights the model at `path` holds, or 0 if it is not readable."""
    try:
        import onnx
    except ImportError:
        return 0
    try:
        model = onnx.load(path)
    except Exception:
        return 0
    total = 0
    for weight in model.graph.initializer:
        size = 1
        for dimension in weight.dims:
            size *= dimension
        total += size
    return total


def export_detector(out: str) -> None:
    import torch
    import transformers.models.rt_detr.modeling_rt_detr as rt_detr
    from transformers import AutoModelForObjectDetection

    # RT-DETR builds its position embedding in double precision, which ONNX
    # Runtime has no Cos kernel for. The name has moved between transformers
    # releases; if it is not there, the export either does not need the patch
    # or will say so loudly, and guessing is worse than either.
    inner = getattr(rt_detr, "build_2d_sinusoidal_position_embedding", None)
    if inner is not None:
        def in_float32(*args, **kwargs):
            real, torch.float64 = torch.float64, torch.float32
            try:
                return inner(*args, **kwargs)
            finally:
                torch.float64 = real

        rt_detr.build_2d_sinusoidal_position_embedding = in_float32

    model = AutoModelForObjectDetection.from_pretrained(DETECTOR).eval()

    class Wrapped(torch.nn.Module):
        """Two tensors out instead of a dataclass, which ONNX has no notion of."""

        def __init__(self):
            super().__init__()
            self.model = model

        def forward(self, pixel_values):
            out = self.model(pixel_values=pixel_values)
            return out.logits, out.pred_boxes

    # Traced at the size it is meant for, and with the batch axis dynamic
    # because the solver asks about nine or sixteen squares at once.
    torch.onnx.export(
        Wrapped(),
        (torch.zeros(1, 3, DETECT_SIZE, DETECT_SIZE),),
        out,
        input_names=["pixel_values"],
        output_names=["logits", "pred_boxes"],
        dynamic_axes={
            "pixel_values": {0: "batch", 2: "height", 3: "width"},
            "logits": {0: "batch"},
            "pred_boxes": {0: "batch"},
        },
        opset_version=17,
        dynamo=False,
    )


def export_segmenter(out: str) -> None:
    import torch
    from transformers import SegformerForSemanticSegmentation

    model = SegformerForSemanticSegmentation.from_pretrained(SEGMENTER).eval()
    torch.onnx.export(
        model,
        (torch.randn(1, 3, SEGMENT_SIZE, SEGMENT_SIZE),),
        out,
        input_names=["pixel_values"],
        output_names=["logits"],
        dynamic_axes={"pixel_values": {0: "batch"}, "logits": {0: "batch"}},
        opset_version=17,
        dynamo=False,
    )


def megabytes(path: str) -> str:
    return f"{os.path.getsize(path) / 1e6:.0f}MB"


def main() -> int:
    parse = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parse.add_argument("models", nargs="?",
                       default=os.path.join(os.environ.get("XDG_CACHE_HOME")
                                            or os.path.expanduser("~/.cache"),
                                            "postern-vision"),
                       help="where install-vision.sh put the models")
    parse.add_argument("--force", action="store_true",
                       help="export even if the models already there would do")
    args = parse.parse_args()

    try:
        import torch  # noqa: F401
        import transformers  # noqa: F401
    except ImportError:
        print("this needs torch and transformers:\n"
              "  pip install --extra-index-url https://download.pytorch.org/whl/cpu \\\n"
              "      torch transformers onnx onnxruntime\n"
              "or let install-vision.sh -export build a virtualenv for it.", file=sys.stderr)
        return 1

    os.makedirs(args.models, exist_ok=True)
    detect = os.path.join(args.models, "detect.onnx")
    segment = os.path.join(args.models, "segment.onnx")

    # Written beside the real name and moved over it, so an export interrupted
    # halfway leaves the working model in place rather than a truncated file.
    if not args.force and detector_takes(detect, DETECT_SIZE):
        print(f"detect.onnx already takes {DETECT_SIZE}px, leaving it")
    else:
        print(f"exporting {DETECTOR}, a few minutes")
        export_detector(detect + ".part")
        os.replace(detect + ".part", detect)
        print(f"wrote {detect}, {megabytes(detect)}")

    if not args.force and parameters(segment) >= B4_PARAMETERS:
        print("segment.onnx is already the larger model, leaving it")
    else:
        print(f"exporting {SEGMENTER}, a few minutes")
        export_segmenter(segment + ".part")
        os.replace(segment + ".part", segment)
        print(f"wrote {segment}, {megabytes(segment)}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
