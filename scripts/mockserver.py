#!/usr/bin/env python3
"""Mock V2EX API 2.0 + Feishu webhook for offline smoke testing.

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

STATE = {"polls": 0, "hooks": [], "deleted": []}

FIXTURES = [
    (1, "alice", "回复了你的主题", "/t/555#reply1"),
    (2, "bob", "在回复中提到了你", "/t/555#reply2"),
    (3, "carol", "感谢了你的主题", "/t/555#reply3"),
    (4, "dave", "回复了你的主题", "/t/555#reply4"),
]


def notification(nid, user, text, payload):
    return {
        "id": nid,
        "member_id": 100 + nid,
        "for_member_id": 1,
        "text": text,
        "payload": payload,
        "payload_rendered": '<a href="/member/%s">@%s</a> 我也遇到过同样的问题' % (user, user),
        "created": 1700000000 + nid,
        "member": {"id": 100 + nid, "username": user, "url": "/member/" + user},
    }


def current_notifications(polls):
    """polls counts how many times /notifications has been served."""
    items = [notification(*FIXTURES[1]), notification(*FIXTURES[0])]
    if polls >= 2:
        items.insert(0, notification(*FIXTURES[2]))
    if polls >= 3:
        items.insert(0, notification(*FIXTURES[3]))
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

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        if path == "/api/v2/member":
            return self._json({"result": {"id": 1, "username": "tester"}})
        if path == "/api/v2/notifications":
            STATE["polls"] += 1
            items = current_notifications(STATE["polls"])
            return self._json({"result": items, "message": "20/%d" % len(items)})
        if path.startswith("/api/v2/topics/"):
            tid = int(path.rsplit("/", 1)[1])
            return self._json({"result": {"id": tid, "title": "关于 xxx 的讨论", "url": "/t/%d" % tid}})
        if path == "/__hooks":
            return self._json(STATE)
        return self._json({"message": "not found"}, 404)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length)
        if self.path == "/hook":
            STATE["hooks"].append(json.loads(raw))
            return self._json({"code": 0, "msg": "success"})
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
