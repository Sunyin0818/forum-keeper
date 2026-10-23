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

echo "==> building forum-keeper"
"$GO" build -o "$WORK/forum-keeper" ./cmd/forum-keeper

export V2EX_API_BASE_URL="$BASE/api/v2"
export V2EX_TOKEN="smoke-token"
export FEISHU_WEBHOOK="$BASE/hook"
export STATE_PATH="$WORK/state.db"
export V2EX_FIRST_RUN=skip
export LOG_LEVEL=warn
# Keep the mock traffic off any system-wide proxy (CI runners often have one set).
export NO_PROXY="127.0.0.1,localhost"
export no_proxy="127.0.0.1,localhost"

echo "==> run 0: startup message (webhook smoke test)"
"$WORK/forum-keeper" --notify-startup

echo "==> run 1: first run records existing notifications without pushing"
"$WORK/forum-keeper" --once

echo "==> run 2: pushes the newly arrived notification"
"$WORK/forum-keeper" --once

echo "==> run 3: V2EX_MARK_READ deletes the pushed notification"
V2EX_MARK_READ=true "$WORK/forum-keeper" --once

echo "==> run 4: daily check-in for both sites"
V2EX_COOKIE="A2=smoke" V2EX_WEB_BASE_URL="$BASE" \
  LIBRA_COOKIE="access_token=smoke" LIBRA_BASE_URL="$BASE" \
  "$WORK/forum-keeper" --checkin

echo "==> run 5: PT site check-in"
cat >"$WORK/pt-sites.json" <<EOF
{
  "first_run": "skip",
  "sites": [
    {
      "name": "HDSky",
      "url": "$BASE",
      "cookie": "session=smoke",
      "signin": {"mode": "checkin"},
      "messages": {"mode": "full"}
    }
  ]
}
EOF
PT_SITES_FILE="$WORK/pt-sites.json" "$WORK/forum-keeper" --check-config
PT_SITES_FILE="$WORK/pt-sites.json" "$WORK/forum-keeper" --checkin

echo "==> run 6: first PT inbox poll records existing unread without pushing"
PT_SITES_FILE="$WORK/pt-sites.json" "$WORK/forum-keeper" --pt-messages

echo "==> run 7: a new PT message is pushed once"
PT_SITES_FILE="$WORK/pt-sites.json" "$WORK/forum-keeper" --pt-messages

echo "==> run 8: the same PT message is not pushed again"
PT_SITES_FILE="$WORK/pt-sites.json" "$WORK/forum-keeper" --pt-messages

python3 - "$BASE" <<'PY'
import json
import sys
import urllib.request

state = json.load(urllib.request.urlopen(sys.argv[1] + "/__hooks"))
hooks, deleted = state["hooks"], state["deleted"]

if len(hooks) != 6:
    raise SystemExit("expected 6 cards, got %d: %s" % (len(hooks), hooks))

def blob(card):
    return json.dumps(card, ensure_ascii=False)

startup, first, second, checkin, ptcheckin, ptmsg = (blob(h) for h in hooks)

# run 0: startup card
assert hooks[0]["msg_type"] == "interactive", startup
assert "forum-keeper" in startup, startup
assert "已启动" in startup, startup
assert hooks[0]["card"]["header"]["template"] == "green", startup

# run 2: the newly arrived notification
assert "carol" in first, first
assert "感谢" in first, first
assert "关于 xxx 的讨论" in first, first
assert "https://www.v2ex.com/t/555" in first, first

# run 3: V2EX_MARK_READ deletes it
assert "dave" in second, second
assert deleted == [4], "expected [4] deleted, got %s" % deleted

# run 4: daily check-in card for V2EX + 2libra
assert hooks[3]["msg_type"] == "interactive", checkin
assert hooks[3]["card"]["header"]["template"] == "green", checkin
assert "论坛签到" in checkin, checkin
assert "V2EX" in checkin and "2libra" in checkin, checkin
assert "42 铜币" in checkin, checkin
assert "余额 29 银币 77 铜币" in checkin, checkin
assert "签到勤勉检定" in checkin, checkin
assert "110 金币" in checkin, checkin

# run 5: PT check-in (separate card)
assert hooks[4]["card"]["header"]["template"] == "green", ptcheckin
assert "PT 签到" in ptcheckin, ptcheckin
assert "HDSky" in ptcheckin, ptcheckin
assert "20 魔力值" in ptcheckin, ptcheckin

# run 7: only the newly arrived message; run 6 recorded id 7 as history.
assert "Msg 8" in ptmsg, ptmsg
assert "hello from PT" in ptmsg, ptmsg
assert "second line" in ptmsg, ptmsg
assert "Msg 7" not in ptmsg, ptmsg
assert "HDSky" in ptmsg, ptmsg

# Real V2EX notifications carry an HTML fragment in `text`. None of that markup
# may reach the card.
for name, payload in (("startup", startup), ("carol", first), ("dave", second), ("check-in", checkin), ("pt-check-in", ptcheckin), ("pt-message", ptmsg)):
    for bad in ("<a ", "</a>", "<strong>", "topic-link", "target=", "<p>"):
        assert bad not in payload, "%s card leaked markup %r: %s" % (name, bad, payload)

# The action label must be the derived short phrase, not the raw text.
assert "感谢了你的主题" in first, first
assert "回复了你" in second, second

print("smoke OK: startup + 2 notification + forum/PT check-in + PT message cards, no HTML leaked, 1 marked read")
PY
