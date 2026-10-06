#!/usr/bin/env bash
# Delete an entire GHCR package - e.g. the old `v2ex-notifier` left behind
# after the repo was renamed to `forum-keeper`.
#
# Renaming a GitHub repo does NOT rename or remove its container packages, and
# packages cannot be deleted with the Actions GITHUB_TOKEN. You need a PAT with
# the `delete:packages` scope (classic) or Packages: Read and write
# (fine-grained).
#
# Usage:
#   GHCR_TOKEN=<pat> ./scripts/ghcr-delete-package.sh v2ex-notifier           # 预演
#   GHCR_TOKEN=<pat> ./scripts/ghcr-delete-package.sh v2ex-notifier --apply   # 删除
#   GHCR_TOKEN=<pat> ./scripts/ghcr-delete-package.sh Sunyin0818/v2ex-notifier
#
# No token? Delete it in the UI (30 秒):
#   https://github.com/users/<owner>/packages/container/<package>/settings
#   -> Danger Zone -> Delete this package
set -euo pipefail

pkg=${1:?用法: $0 <package-name> [--apply]}
apply=${2:-}
pkg=${pkg##*/} # 允许写成 owner/name
owner=${GHCR_OWNER:-Sunyin0818}
api=https://api.github.com

if [ -z "${GHCR_TOKEN:-}" ]; then
  cat >&2 <<EOF
缺少 GHCR_TOKEN（需要 delete:packages 权限）。
也可以直接在网页删除：
  https://github.com/users/${owner}/packages/container/${pkg}/settings
EOF
  exit 2
fi

auth=(-H "Authorization: Bearer ${GHCR_TOKEN}" -H "Accept: application/vnd.github+json")

echo "package: ${pkg}"
echo
echo "版本（最多列 100 个）："
versions=$(curl -sS -m 30 "${auth[@]}" "${api}/user/packages/container/${pkg}/versions?per_page=100")
printf '%s' "$versions" | python3 -c '
import json, sys
try:
    data = json.load(sys.stdin)
except Exception:
    sys.exit("拿不到版本列表：检查 token 权限与包名")
if isinstance(data, dict) and data.get("message"):
    sys.exit("GitHub 返回：" + data["message"])
for v in data:
    tags = ((v.get("metadata") or {}).get("container") or {}).get("tags") or []
    print("  -", (v.get("name") or "?")[:19], "tags:", ", ".join(tags) or "(none)")
'

if [ "$apply" != "--apply" ]; then
  echo
  echo "预演结束。加 --apply 删除整个 package（所有 tag/版本）。"
  exit 0
fi

code=$(curl -sS -m 30 -o /dev/null -w '%{http_code}' -X DELETE "${auth[@]}" \
  "${api}/user/packages/container/${pkg}")
case "$code" in
  204 | 200) echo "已删除 package ${pkg}" ;;
  404) echo "404：包不存在，或 token 无权访问 ${owner} 的包" >&2 ; exit 1 ;;
  403) echo "403：token 缺少 delete:packages 权限" >&2 ; exit 1 ;;
  *) echo "删除失败：HTTP ${code}" >&2 ; exit 1 ;;
esac
