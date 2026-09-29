#!/usr/bin/env bash
# Offline end-to-end smoke test: mock V2EX API + mock Feishu webhook.
# Requires bash, curl, python3 and a Go toolchain.
set -euo pipefail

cd "$(dirname "$0")/.."

PORT="${SMOKE_PORT:-18080}"
BASE="http://127.0.0.1:${PORT}"
GO="${GO:-go}"
WORK="$(mktemp -d)"

cleanup() {
  [ -n "${MOCK_PID:-}" ] && kill "$MOCK_PID" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

python3 scripts/mockserver.py "$PORT" >"$WORK/mock.log" 2>&1 &
MOCK_PID=$!

for _ in $(seq 1 50); do
  curl -s -o /dev/null "$BASE/__hooks" && break
  sleep 0.1
done

echo "==> building notifier"
"$GO" build -o "$WORK/notifier" ./cmd/notifier

export V2EX_BASE_URL="$BASE/api/v2"
export V2EX_TOKEN="smoke-token"
export FEISHU_WEBHOOK="$BASE/hook"
export V2EX_PROXY=""
export STATE_PATH="$WORK/state.db"
export FIRST_RUN=skip
export LOG_LEVEL=warn
# Keep the mock traffic off any system-wide proxy.
export NO_PROXY="127.0.0.1,localhost"
export no_proxy="127.0.0.1,localhost"

echo "==> run 1: first run records existing notifications without pushing"
"$WORK/notifier" --once

echo "==> run 2: pushes the newly arrived notification"
"$WORK/notifier" --once

echo "==> run 3: MARK_READ deletes the pushed notification"
MARK_READ=true "$WORK/notifier" --once

python3 - "$BASE" <<'PY'
import json
import sys
import urllib.request

state = json.load(urllib.request.urlopen(sys.argv[1] + "/__hooks"))
hooks, deleted = state["hooks"], state["deleted"]

if len(hooks) != 2:
    raise SystemExit("expected 2 cards, got %d: %s" % (len(hooks), hooks))

def blob(card):
    return json.dumps(card, ensure_ascii=False)

first, second = blob(hooks[0]), blob(hooks[1])
assert "carol" in first, first
assert "感谢" in first, first
assert "关于 xxx 的讨论" in first, first
assert "https://www.v2ex.com/t/555" in first, first
assert "dave" in second, second
assert deleted == [4], "expected [4] deleted, got %s" % deleted

print("smoke OK: 2 cards pushed, topic title resolved, 1 notification marked read")
PY
