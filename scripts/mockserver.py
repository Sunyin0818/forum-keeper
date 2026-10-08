#!/usr/bin/env python3
"""Mock V2EX API 2.0 + Feishu webhook for offline smoke testing.

The fixtures deliberately mirror the *real* V2EX response shape, including the
fact that `text` is an HTML fragment and that `member` only carries a username.
Feeding idealized plain-text fixtures hides rendering bugs, so don't simplify
these.

Endpoints:
  GET    /api/v2/member
  GET    /api/v2/notifications?p=N   (a new notification appears on every poll)
  GET    /api/v2/topics/:id
  DELETE /api/v2/notifications/:id
  POST   /hook                       (Feishu webhook)
  GET    /__hooks                    (inspection: {"hooks": [...], "deleted": [...]})
"""

import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

STATE = {"polls": 0, "hooks": [], "deleted": [], "mission_claimed": False}

TOPIC_ID = 555
TOPIC_TITLE = "关于 xxx 的讨论"


def notification(nid, user, action, reply_body, *, topic_link=True, topic_id=TOPIC_ID):
    """Build a notification shaped exactly like the real API returns."""
    member_link = '<a href="/member/{u}" target="_blank"><strong>{u}</strong></a>'.format(u=user)
    if topic_link:
        topic_anchor = ' 在 <a href="/t/{t}#reply{n}" class="topic-link">{title}</a>'.format(
            t=topic_id, n=nid, title=TOPIC_TITLE
        )
    else:
        topic_anchor = " "
    return {
        "id": nid,
        "member_id": 100 + nid,
        "for_member_id": 1,
        "text": member_link + topic_anchor + action,
        "payload": reply_body,
        "payload_rendered": '@<a href="/member/Sunyin">Sunyin</a> ' + reply_body,
        "created": 1700000000 + nid,
        # Real responses only include the username here.
        "member": {"username": user},
    }


# (id, member, action, reply body, has topic link)
FIXTURES = [
    (1, "alice", "回复了你", "我也遇到过同样的问题", True),
    (2, "bob", "在回复中提到了你", "顺便问一下 @Sunyin 后来解决了吗？", True),
    (3, "carol", "感谢了你的主题", "感谢分享", True),
    (4, "dave", "回复了你", "按的时候屏幕是不是会闪一下？", True),
]


def fixture(index):
    """Build FIXTURES[index]; the 5th element selects the topic-link variant."""
    nid, user, action, body, has_topic_link = FIXTURES[index]
    return notification(nid, user, action, body, topic_link=has_topic_link)


def current_notifications(polls):
    """polls counts how many times /notifications has been served."""
    items = [fixture(1), fixture(0)]
    if polls >= 2:
        items.insert(0, fixture(2))
    if polls >= 3:
        items.insert(0, fixture(3))
    return items


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):  # silence access logs
        pass

    def _json(self, obj, code=200):
        body = json.dumps(obj, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _html(self, html_body, code=200):
        body = html_body.encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        # V2EX daily-bonus mission (cookie-authenticated web page).
        if path == "/mission/daily":
            if STATE["mission_claimed"]:
                return self._html("<html>每日登录奖励已领取</html>")
            return self._html('<html><a href="/mission/daily/redeem?once=4242">领取</a></html>')
        if path == "/mission/daily/redeem":
            STATE["mission_claimed"] = True
            return self._html("<html>已成功领取每日登录奖励 42 铜币</html>")
        if path == "/balance":
            return self._html(
                '<div id="money"><a href="/balance">29 '
                '<img src="/static/img/silver@2x.png" alt="S" /> 77 '
                '<img src="/static/img/bronze@2x.png" alt="B" /></a></div>'
                '<span class="gray">20261007 的每日登录奖励 42 铜币</span>'
            )
        if path == "/api/v2/member":
            return self._json({"result": {"id": 1, "username": "tester"}})
        # 2libra coin ledger behind https://2libra.com/coins (check-in reward).
        if path == "/api/coins/today-transaction":
            return self._json(
                {"c": 0, "m": "请求成功", "d": [{"amount": 110, "reason": "checkin"}]}
            )
        if path == "/api/v2/notifications":
            STATE["polls"] += 1
            items = current_notifications(STATE["polls"])
            return self._json({"result": items, "message": "Notifications 1-%d/4" % len(items)})
        if path.startswith("/api/v2/topics/"):
            tid = int(path.rsplit("/", 1)[1])
            return self._json({"result": {"id": tid, "title": TOPIC_TITLE, "url": "/t/%d" % tid}})
        if path == "/__hooks":
            return self._json(STATE)
        return self._json({"message": "not found"}, 404)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length)
        if self.path == "/hook":
            STATE["hooks"].append(json.loads(raw))
            return self._json({"code": 0, "msg": "success"})
        # 2libra check-in.
        if self.path == "/api/sign":
            return self._json(
                {"c": 201, "m": "签到成功", "d": "<p>签到勤勉检定 +1。</p>"}, 201
            )
        return self._json({"message": "not found"}, 404)

    def do_DELETE(self):
        if self.path.startswith("/api/v2/notifications/"):
            STATE["deleted"].append(int(self.path.rsplit("/", 1)[1]))
            return self._json({"success": True})
        return self._json({"message": "not found"}, 404)


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 18080
    print("mock listening on http://127.0.0.1:%d" % port, flush=True)
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
