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
export STATE_PATH="$WORK/state.db"
export FIRST_RUN=skip
export LOG_LEVEL=warn
# Keep the mock traffic off any system-wide proxy (CI runners often have one set).
export NO_PROXY="127.0.0.1,localhost"
export no_proxy="127.0.0.1,localhost"

echo "==> run 0: startup message (webhook smoke test)"
"$WORK/notifier" --notify-startup

echo "==> run 1: first run records existing notifications without pushing"
"$WORK/notifier" --once

echo "==> run 2: pushes the newly arrived notification"
"$WORK/notifier" --once

echo "==> run 3: MARK_READ deletes the pushed notification"
MARK_READ=true "$WORK/notifier" --once

echo "==> run 4: daily check-in for both sites"
V2EX_COOKIE="A2=smoke" V2EX_WEB_BASE_URL="$BASE" \
  LIBRA_COOKIE="access_token=smoke" LIBRA_BASE_URL="$BASE" \
  "$WORK/notifier" --checkin

python3 - "$BASE" <<'PY'
import json
import sys
import urllib.request

state = json.load(urllib.request.urlopen(sys.argv[1] + "/__hooks"))
hooks, deleted = state["hooks"], state["deleted"]

if len(hooks) != 4:
    raise SystemExit("expected 4 cards, got %d: %s" % (len(hooks), hooks))

def blob(card):
    return json.dumps(card, ensure_ascii=False)

startup, first, second, checkin = (blob(h) for h in hooks)

# run 0: startup card
assert hooks[0]["msg_type"] == "interactive", startup
assert "服务已启动" in startup, startup
assert hooks[0]["card"]["header"]["template"] == "green", startup

# run 2: the newly arrived notification
assert "carol" in first, first
assert "感谢" in first, first
assert "关于 xxx 的讨论" in first, first
assert "https://www.v2ex.com/t/555" in first, first

# run 3: MARK_READ deletes it
assert "dave" in second, second
assert deleted == [4], "expected [4] deleted, got %s" % deleted

# run 4: daily check-in card for V2EX + 2libra
assert hooks[3]["msg_type"] == "interactive", checkin
assert hooks[3]["card"]["header"]["template"] == "green", checkin
assert "每日签到" in checkin, checkin
assert "V2EX" in checkin and "2libra" in checkin, checkin
assert "42 铜币" in checkin, checkin
assert "签到勤勉检定" in checkin, checkin

# Real V2EX notifications carry an HTML fragment in `text`. None of that markup
# may reach the card.
for name, payload in (("startup", startup), ("carol", first), ("dave", second), ("check-in", checkin)):
    for bad in ("<a ", "</a>", "<strong>", "topic-link", "target=", "<p>"):
        assert bad not in payload, "%s card leaked markup %r: %s" % (name, bad, payload)

# The action label must be the derived short phrase, not the raw text.
assert "感谢了你的主题" in first, first
assert "回复了你" in second, second

print("smoke OK: startup card + 2 notification cards + check-in card, no HTML leaked, 1 marked read")
PY
