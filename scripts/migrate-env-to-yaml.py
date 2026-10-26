#!/usr/bin/env python3
"""Convert the old .env + pt-sites.json layout into a single config.yaml.

The service used to read its configuration from two files: config/.env for the
credentials and config/pt-sites.json for the tracker list. Both are still
accepted by hand, but one config.yaml is the supported layout now.

    scripts/migrate-env-to-yaml.py config/.env config/pt-sites.json config.yaml

Cookies and tokens are copied verbatim into the new file, so the result is a
secret: keep it at mode 600.
"""

from __future__ import annotations

import argparse
import json
import os
import sys

# The old environment variable names and where they live in config.yaml.
ENV_MAP = {
    "FEISHU_WEBHOOK": ("feishu", "webhook"),
    "FEISHU_SECRET": ("feishu", "secret"),
    "V2EX_COOKIE": ("v2ex", "cookie"),
    "V2EX_TOKEN": ("v2ex", "token"),
    "LIBRA_COOKIE": ("libra", "cookie"),
    "CHECKIN_TIME": ("checkin", "time"),
    "TZ": ("checkin", "timezone"),
    "V2EX_POLL_INTERVAL": ("reminders", "interval"),
    "V2EX_MAX_PAGES": ("reminders", "max_pages"),
    "V2EX_FIRST_RUN": ("reminders", "first_run"),
    "V2EX_MARK_READ": ("reminders", "mark_read"),
    "V2EX_FILTER_TYPES": ("reminders", "filter_types"),
    "NOTIFY_STARTUP": ("notify", "startup"),
    "NOTIFY_STARTUP_COOLDOWN": ("notify", "startup_cooldown"),
    "NOTIFY_ALERT_ON_ERROR": ("notify", "alert_on_error"),
    "PT_POLL_INTERVAL": ("pt", "poll_interval"),
    "PT_FIRST_RUN": ("pt", "first_run"),
    "PT_CHECKIN_RETRIES": ("pt", "retries"),
    "PT_CONCURRENCY": ("pt", "concurrency"),
    "HTTP_TIMEOUT": ("server", "http_timeout"),
    "STATE_PATH": ("server", "state_path"),
    "LOG_LEVEL": ("server", "log_level"),
}

INT_KEYS = {"reminders.max_pages", "pt.retries", "pt.concurrency"}
BOOL_KEYS = {"reminders.mark_read", "notify.startup", "notify.alert_on_error"}
LIST_KEYS = {"reminders.filter_types"}

# Keys that only exist in the old layout.
DROP = {"PT_SITES_FILE"}


def read_env(path: str) -> dict[str, str]:
    values: dict[str, str] = {}
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.rstrip("\n")
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            if "=" not in line:
                continue
            key, value = line.split("=", 1)
            key = key.strip()
            value = value.strip()
            if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
                value = value[1:-1]
            values[key] = value
    return values


def convert_value(dotted: str, raw: str):
    if dotted in INT_KEYS:
        return int(raw)
    if dotted in BOOL_KEYS:
        return raw.strip().lower() in ("1", "true", "yes", "on")
    if dotted in LIST_KEYS:
        return [part for part in raw.replace(";", ",").split(",") if part.strip()]
    return raw


def convert_sites(path: str, env: dict[str, str]) -> list[dict]:
    with open(path, encoding="utf-8") as fh:
        sites = json.load(fh).get("sites", [])
    out = []
    for site in sites:
        converted: dict = {"url": site["url"]}
        cookie = site.get("cookie", "")
        if site.get("cookie_env"):
            cookie = env.get(site["cookie_env"], "")
        converted["cookie"] = cookie
        for key in ("name", "preset", "ua", "timeout"):
            if site.get(key):
                converted[key] = site[key]
        for section in ("signin", "messages"):
            values = {
                key: value
                for key, value in (site.get(section) or {}).items()
                if value not in (None, "", {}, [])
            }
            if values:
                converted[section] = values
        out.append(converted)
    return out


def dump_yaml(data: dict) -> str:
    """Write YAML by hand: the file has to be readable and stable, and the
    values are mostly opaque strings that must not be re-quoted wrongly."""
    lines: list[str] = []

    def scalar(value) -> str:
        if isinstance(value, bool):
            return "true" if value else "false"
        if isinstance(value, (int, float)):
            return str(value)
        text = str(value)
        if text == "":
            return "''"
        # Single quotes keep $, :, # and ; literal; the only escape needed is
        # for a single quote itself.
        if "'" in text:
            return '"' + text.replace("\\", "\\\\").replace('"', '\\"') + '"'
        return "'" + text + "'"

    def emit(node, indent: int) -> None:
        pad = "  " * indent
        for key, value in node.items():
            if isinstance(value, dict):
                lines.append(f"{pad}{key}:")
                emit(value, indent + 1)
            elif isinstance(value, list):
                if not value:
                    lines.append(f"{pad}{key}: []")
                else:
                    lines.append(f"{pad}{key}:")
                    for item in value:
                        if isinstance(item, dict):
                            first = True
                            for item_key, item_value in item.items():
                                prefix = f"{pad}  - " if first else f"{pad}    "
                                if isinstance(item_value, dict):
                                    lines.append(f"{prefix}{item_key}:")
                                    emit(item_value, indent + 3)
                                else:
                                    lines.append(f"{prefix}{item_key}: {scalar(item_value)}")
                                first = False
                        else:
                            lines.append(f"{pad}  - {scalar(item)}")
            else:
                lines.append(f"{pad}{key}: {scalar(value)}")

    emit(data, 0)
    return "\n".join(lines) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("env", help="the old .env file")
    parser.add_argument("sites", nargs="?", help="the old pt-sites.json file")
    parser.add_argument("out", help="the config.yaml to write")
    args = parser.parse_args()

    env = read_env(args.env)
    config: dict = {
        "feishu": {"webhook": "", "secret": ""},
        "v2ex": {"cookie": "", "token": ""},
        "libra": {"cookie": ""},
        "checkin": {"time": "08:10", "on_start": True, "timezone": "Asia/Shanghai"},
        "reminders": {
            "interval": "60s",
            "max_pages": 3,
            "first_run": "skip",
            "mark_read": False,
            "filter_types": [],
        },
        "notify": {
            "startup": True,
            "startup_cooldown": "10m",
            "alert_on_error": True,
        },
        "pt": {
            "poll_interval": "2h",
            "first_run": "skip",
            "retries": 2,
            "concurrency": 4,
            "sites": [],
        },
        "server": {"http_timeout": "20s", "state_path": "state.db", "log_level": "info"},
    }

    for key, (section, name) in ENV_MAP.items():
        raw = env.get(key)
        if raw is None or raw == "":
            continue
        config[section][name] = convert_value(f"{section}.{name}", raw)

    if args.sites and os.path.exists(args.sites):
        config["pt"]["sites"] = convert_sites(args.sites, env)

    with open(args.out, "w", encoding="utf-8") as fh:
        fh.write(dump_yaml(config))
    os.chmod(args.out, 0o600)
    print(f"wrote {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
