#!/bin/sh
# Sets up examples/solver-vision.py: a virtualenv, the models, and a wrapper
# to hand to -image-solver.
#
#     examples/install-vision.sh ~/.cache/postern-vision
#     postern solve -kind recaptcha-v2 -url ... -sitekey ... \
#         -image-solver ~/.cache/postern-vision/solve
#
# -export builds the detector and the segmenter from PyTorch weights instead of
# downloading the published ONNX builds, which is worth ten grids of the
# 48-grid bench — 38/48 against 48/48, same solver, same everything else. It
# costs about 430MB of models, a few minutes of CPU and a 1.4GB virtualenv of
# PyTorch to produce them, which is why it is not what happens by default.
# examples/export-models.py says what the published builds get wrong.
#
#     examples/install-vision.sh -export ~/.cache/postern-vision
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

EXPORT=no
while [ $# -gt 0 ]; do
	case $1 in
	-export) EXPORT=yes; shift ;;
	-*) echo "unknown option $1, want -export" >&2; exit 1 ;;
	*) break ;;
	esac
done

DIR=${1:-${XDG_CACHE_HOME:-$HOME/.cache}/postern-vision}
MODEL=${2:-patch16}
EXAMPLES=$(cd "$(dirname "$0")" && pwd)
SCRIPT=$EXAMPLES/solver-vision.py

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
# Everything below writes into here, and the wrapper at the end has to name it
# from wherever postern is run — so the path it holds cannot be the relative one
# that was typed.
DIR=$(pwd)

if [ ! -x venv/bin/python ]; then
	echo "creating a virtualenv in $DIR/venv"
	python3 -m venv venv
	./venv/bin/pip install --quiet --upgrade pip
	./venv/bin/pip install --quiet onnxruntime numpy pillow tokenizers
fi

# The quantised encoders: 84MB and 64MB rather than 345MB and 254MB. Postern
# starts a solver per grid, so the load is paid every round, and that is what the
# quantisation buys: measured on this machine, 161ms to load the quantised
# vision encoder and 347ms to run nine tiles through it, against 475ms and 550ms
# for the full-precision export. Half a second a grid either way — the file size
# is the real cost, not the arithmetic.
fetch() {
	[ -s "$2" ] && return 0
	echo "fetching $2"
	curl -fsSL --retry 3 -o "$2.part" "$1"
	mv "$2.part" "$2"
}

fetch "$BASE/onnx/vision_model_quantized.onnx" clip-vision.onnx
fetch "$BASE/onnx/text_model_quantized.onnx" clip-text.onnx
fetch "$BASE/tokenizer.json" clip-tokenizer.json

# The segmentation model. It answers a 4x4 grid outright — those squares are one
# photograph cut up, and which of them hold the bus is a question about pixels —
# and it is asked again, tile by tile, about the 3x3 categories CLIP is worst at.
# SegFormer-B0 on ADE20K, 15MB, and neither path is taken without it.
#
# B4 is markedly better and is what -export builds. Over saved grids with the
# answers checked by eye it ticks one square in excess against five for B0, and
# the tile pass is where the gap shows: over six labelled grids of bridges and
# hills, B0 found every hill and not one bridge — the class is there, the model
# simply never predicts it at this size — while B4 found both. On the 48-grid
# bench that is one whole grid, 47 against 48. It is 257MB against 15MB and
# ships only as PyTorch weights, which is the whole reason for -export.
if [ "$EXPORT" = no ]; then
	fetch https://huggingface.co/Xenova/segformer-b0-finetuned-ade-512-512/resolve/main/onnx/model.onnx segment.onnx
fi

# The object detector, which is asked before either of those and answers most
# of what reCAPTCHA asks: buses, cars, bicycles, motorcycles, fire hydrants,
# parking meters and traffic lights were 73% of the challenges served over a
# night of measuring. RT-DETR r50 on COCO, 45MB quantised.
#
# Measured over grids with the answers checked by eye: exactly right on six of
# them, where scoring tiles with CLIP was nine ticks in excess over three grids
# and the mask was one short and one over.
#
# This published build declares a fully dynamic input and then refuses anything
# but 640, deep in the graph: the feature map's size was computed once while
# tracing and frozen as a literal, so `Reshape` asks for 400 positions (20x20,
# and 20 is 640/32) whatever it is given. Marking the outer axes dynamic does not
# reach constants inside, and since it works perfectly at 640 nobody noticed.
#
# The solver copes by laying a tile on a field rather than stretching it to fill
# one, which is most of the difference. Exporting the model is the rest of it:
# it lets the field be 288 pixels rather than 640, which is a three-fold
# enlargement of a 96px tile instead of a seven-fold one. Measured over the
# 48-grid bench, same solver, same segmenter:
#
#     published build, stuck at a 640 field      38/48 grids exact
#     exported, tile laid on a 288 field         47/48
#
# It is 172MB against 45MB, unquantised. -export builds it; without that the
# solver takes the published one and falls back to 640 whenever it is asked for
# anything else, which it says on stderr rather than silently.
DETECTOR=https://huggingface.co/onnx-community/rtdetr_r50vd_coco_o365/resolve/main
if [ "$EXPORT" = no ]; then
	fetch "$DETECTOR/onnx/model_quantized.onnx" detect.onnx
fi

# The class numbers the detector answers with mean nothing on their own.
if [ ! -s detect-labels.json ]; then
	echo "fetching detect-labels.json"
	curl -fsSL --retry 3 -o detect-config.json "$DETECTOR/config.json"
	./venv/bin/python -c "
import json
labels = json.load(open('detect-config.json'))['id2label']
json.dump(labels, open('detect-labels.json', 'w'))"
	rm -f detect-config.json
fi

# The two models the hub has no build of that will do — see export-models.py for
# what is wrong with the ones it has. PyTorch gets a virtualenv of its own
# because it is a build tool here and not a dependency: 1.4GB to produce 430MB
# of ONNX, after which the solver never imports it again. The exporter checks
# what is already installed before building anything, so running this twice
# costs nothing.
if [ "$EXPORT" = yes ]; then
	if [ ! -x export-venv/bin/python ]; then
		echo "creating a build virtualenv in $DIR/export-venv, about 1.4GB"
		python3 -m venv export-venv
		./export-venv/bin/pip install --quiet --upgrade pip
		# The CPU wheel by name: the default index gives the CUDA build, which
		# is 2GB of libraries to run a two-minute export on the processor.
		./export-venv/bin/pip install --quiet --index-url https://download.pytorch.org/whl/cpu torch
		./export-venv/bin/pip install --quiet transformers onnx onnxruntime
	fi
	./export-venv/bin/python "$EXAMPLES/export-models.py" "$DIR"
	echo "(rm -rf $DIR/export-venv for the 1.4GB back; the models stay)"
fi

# Heads fitted for the categories nothing off the shelf answers. Only the ones
# fitted on this encoder are copied; the solver checks anyway, but a directory
# holding heads that will never be used is a puzzle for whoever looks in it.
for head in "$EXAMPLES"/probe-*.json; do
	[ -e "$head" ] || continue
	if grep -q "\"model\": *\"$MODEL\"" "$head"; then
		cp "$head" .
		echo "installed $(basename "$head")"
	fi
done

# A head may name an encoder of its own, and the full-precision patch16 is the
# one that does: measured over twelve labelled crosswalk grids, ten answered
# exactly against eight for the quantised export, and the tile it stops missing
# is a crossing painted across a whole carriageway — not a borderline call.
#
# It is 345MB against 84MB and half a second a grid against a fifth of one,
# which is why it is not simply made the default: it buys nothing measurable on
# the zero-shot path, whose thresholds are calibrated against the quantised
# export anyway. Only the heads that ask for it pay for it, and only the
# categories that have a head reach it.
for head in probe-*.json; do
	[ -e "$head" ] || continue
	wants=$(sed -n 's/.*"encoder_file": *"\([^"]*\)".*/\1/p' "$head")
	case $wants in
	'' | clip-vision.onnx) continue ;;
	clip-vision-fp32.onnx) fetch "$BASE/onnx/vision_model.onnx" "$wants" ;;
	*) echo "$head wants $wants, which this script does not know how to fetch" >&2 ;;
	esac
done

cat > solve <<EOF
#!/bin/sh
export POSTERN_CLIP_DIR="$DIR"
export POSTERN_CLIP_CONFIDENCE="$CONFIDENCE"
export POSTERN_CLIP_LAYOUT="$LAYOUT"
export POSTERN_CLIP_MODEL="$MODEL"
exec "$DIR/venv/bin/python" "$SCRIPT" "\$@"
EOF
chmod +x solve

echo
echo "ready. hand this to postern:"
echo "  postern solve -kind recaptcha-v2 -url <page> -sitekey <key> \\"
echo "      -image-solver $DIR/solve"
