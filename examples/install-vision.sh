#!/bin/sh
# Sets up examples/solver-vision.py: a virtualenv, the models, and a wrapper
# to hand to -image-solver.
#
#     examples/install-vision.sh ~/.cache/postern-vision
#     postern solve -kind recaptcha-v2 -url ... -sitekey ... \
#         -image-solver ~/.cache/postern-vision/solve
#
# A second argument picks the model. patch16 is the default because it is what
# the 4x4 pass needs: those squares are found by covering them up and seeing how
# much of the picture goes with them, and patch32 is not sure enough about
# anything for covering a square to move its score. It costs roughly four times
# the CPU per grid, which is seconds against a budget of ninety.
#
# patch32 is there for a machine that cannot spare that. It falls back to
# scoring squares one at a time, which ticks more squares than it should on a
# 4x4 — measured, five ticks in excess where patch16 has one.
#
# Nothing here is required to use postern — it ships no vision model and works
# without one, reporting picture challenges rather than answering them. This is
# for when you want the example working in one command instead of five.
set -eu

DIR=${1:-${XDG_CACHE_HOME:-$HOME/.cache}/postern-vision}
MODEL=${2:-patch16}
SCRIPT=$(cd "$(dirname "$0")" && pwd)/solver-vision.py

# Both calibrated over saved grids with the answers checked by eye. LAYOUT
# forces the one-square-at-a-time path on the model that needs it.
case $MODEL in
patch16) CONFIDENCE=0.55; LAYOUT=tiles ;;
patch32) CONFIDENCE=0.34; LAYOUT=tiles ;;
*) echo "unknown model $MODEL, want patch16 or patch32" >&2; exit 1 ;;
esac
BASE=https://huggingface.co/Xenova/clip-vit-base-$MODEL/resolve/main

command -v python3 >/dev/null || { echo "python3 not found" >&2; exit 1; }
command -v curl >/dev/null || { echo "curl not found" >&2; exit 1; }

mkdir -p "$DIR"
cd "$DIR"

if [ ! -x venv/bin/python ]; then
	echo "creating a virtualenv in $DIR/venv"
	python3 -m venv venv
	./venv/bin/pip install --quiet --upgrade pip
	./venv/bin/pip install --quiet onnxruntime numpy pillow tokenizers
fi

# The quantised encoders: 90MB and 64MB rather than 350MB and 254MB. Postern
# starts a solver per grid, so load time is paid every round — the full-precision
# pair took two minutes a grid on a CPU, which is longer than a challenge stays
# valid.
fetch() {
	[ -s "$2" ] && return 0
	echo "fetching $2"
	curl -fsSL --retry 3 -o "$2.part" "$1"
	mv "$2.part" "$2"
}

fetch "$BASE/onnx/vision_model_quantized.onnx" clip-vision.onnx
fetch "$BASE/onnx/text_model_quantized.onnx" clip-text.onnx
fetch "$BASE/tokenizer.json" clip-tokenizer.json

# The segmentation model, which is what answers a 4x4 grid: those squares are
# one photograph cut up, and which of them hold the bus is a question about
# pixels. SegFormer-B0 on ADE20K, 15MB, and the 4x4 path is simply not taken
# without it.
#
# B4 is markedly better — over saved grids with the answers checked by eye, one
# tick in excess against five for B0 — but it is 257MB and ships only as
# PyTorch weights. To use it instead:
#
#   pip install torch transformers onnx
#   python -c "
#   import torch; from transformers import SegformerForSemanticSegmentation
#   m = SegformerForSemanticSegmentation.from_pretrained(
#       'nvidia/segformer-b4-finetuned-ade-512-512').eval()
#   torch.onnx.export(m, (torch.randn(1,3,512,512),), 'segment.onnx',
#                     input_names=['pixel_values'], output_names=['logits'],
#                     opset_version=17, dynamo=False)"
fetch https://huggingface.co/Xenova/segformer-b0-finetuned-ade-512-512/resolve/main/onnx/model.onnx segment.onnx

cat > solve <<EOF
#!/bin/sh
export POSTERN_CLIP_DIR="$DIR"
export POSTERN_CLIP_CONFIDENCE="$CONFIDENCE"
export POSTERN_CLIP_LAYOUT="$LAYOUT"
exec "$DIR/venv/bin/python" "$SCRIPT" "\$@"
EOF
chmod +x solve

echo
echo "ready. hand this to postern:"
echo "  postern solve -kind recaptcha-v2 -url <page> -sitekey <key> \\"
echo "      -image-solver $DIR/solve"
