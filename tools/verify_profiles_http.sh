#!/usr/bin/env bash
# Verify the core promise of the generalisation: the same document rendered
# under different profiles must come out visibly different.
#
# If selecting a different profile produced the same layout, the whole profile
# mechanism would be decoration. So this walks the real HTTP flow — upload,
# export, then read the .docx back with python-docx — and compares what the
# *file* contains, not what the profile claims. A profile that reports 宋体 12pt
# but renders 仿宋 16pt is exactly the bug worth catching.
#
# Usage: bash tools/verify_profiles_http.sh [port]
set -euo pipefail

export GOWORK=off
export PYTHONIOENCODING=utf-8
export PYTHONUTF8=1

PORT="${1:-8895}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(dirname "$HERE")"
cd "$ROOT"

PYTHON_BIN="${PYTHON_BIN:-python3}"
PY_SCRIPT="$HERE/verify_profiles_http.py"

echo "== building =="
go build -o govdoc.exe ./cmd/govdoc

echo "== starting stub model + server =="
go run ./tools/stublm -addr "127.0.0.1:$((PORT + 4))" >/dev/null 2>&1 &
STUB=$!
./govdoc.exe -addr "127.0.0.1:$PORT" \
  -endpoint "http://127.0.0.1:$((PORT + 4))/v1" -api-key k -model m -no-save \
  >/dev/null 2>&1 &
SRV=$!
trap 'kill "$STUB" "$SRV" 2>/dev/null || true' EXIT

for _ in $(seq 1 60); do
  curl -sf "http://127.0.0.1:$PORT/api/health" >/dev/null 2>&1 && break
  sleep 0.25
done

echo "== available profiles =="
"$PYTHON_BIN" -c "
import json, urllib.request
d = json.load(urllib.request.urlopen('http://127.0.0.1:$PORT/api/profiles'))
print('  active:', d['active'])
for p in d['profiles']:
    b = p['spec']['styles']['body']; pg = p['spec']['page']
    print('  - %-8s %-4s body=%s/%s leading=%spt margins=%s/%s/%s/%s' % (
        p['id'], p['name'], b['font'], b['size'],
        round(b['line_spacing_pt'], 2),
        pg['margin_top_mm'], pg['margin_bottom_mm'],
        pg['margin_left_mm'], pg['margin_right_mm']))
"

OUT="$(mktemp -d)"
for prof in govdoc generic paper report; do
  echo "== export with profile: $prof =="
  "$PYTHON_BIN" "$PY_SCRIPT" "http://127.0.0.1:$PORT" "$prof" "$OUT"
done

echo "== measured from the exported files =="
"$PYTHON_BIN" "$PY_SCRIPT" --summarize "$OUT"

echo
echo "PASS: each profile renders differently, and the .docx files say so."