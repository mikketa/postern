#!/bin/sh
# Sets up examples/solver-clip.py: a virtualenv, three model files, and a
# wrapper to hand to -image-solver.
#
#     examples/install-clip.sh ~/.cache/postern-clip
#     postern solve -kind recaptcha-v2 -url ... -sitekey ... \
#         -image-solver ~/.cache/postern-clip/solve
#
# Nothing here is required to use postern — it ships no vision model and works
# without one, reporting picture challenges rather than answering them. This is
# for when you want the example working in one command instead of five.
set -eu

DIR=${1:-${XDG_CACHE_HOME:-$HOME/.cache}/postern-clip}
SCRIPT=$(cd "$(dirname "$0")" && pwd)/solver-clip.py
BASE=https://huggingface.co/Xenova/clip-vit-base-patch32/resolve/main

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
exec "$DIR/venv/bin/python" "$SCRIPT" "\$@"
EOF
chmod +x solve

echo
echo "ready. hand this to postern:"
echo "  postern solve -kind recaptcha-v2 -url <page> -sitekey <key> \\"
echo "      -image-solver $DIR/solve"
