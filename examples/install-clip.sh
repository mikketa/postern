#!/bin/sh
# Sets up examples/solver-clip.py: a virtualenv, three model files, and a
# wrapper to hand to -image-solver.
#
#     examples/install-clip.sh ~/.cache/postern-clip
#     postern solve -kind recaptcha-v2 -url ... -sitekey ... \
#         -image-solver ~/.cache/postern-clip/solve
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

DIR=${1:-${XDG_CACHE_HOME:-$HOME/.cache}/postern-clip}
MODEL=${2:-patch16}
SCRIPT=$(cd "$(dirname "$0")" && pwd)/solver-clip.py

# Both calibrated over saved grids with the answers checked by eye. LAYOUT
# forces the one-square-at-a-time path on the model that needs it.
case $MODEL in
patch16) CONFIDENCE=0.55; LAYOUT=occlusion ;;
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
