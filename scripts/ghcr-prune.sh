#!/usr/bin/env bash
# Delete stale GHCR images for this project - safely.
#
# Why a script instead of `docker rmi`-style one-liners: GHCR deletes whole
# *manifests*, not individual tags. Several tags routinely point at one
# manifest (e.g. `0`, `0.1`, `0.1.0`, `latest` and a `sha-*` tag can all share
# a digest), so deleting "the sha- tag" can silently take the release with it.
#
# This only deletes a manifest when *none* of its tags look like a release.
# Manifests that carry a release tag are reported, never touched.
#
# Usage:
#   GHCR_TOKEN=<pat with delete:packages> ./scripts/ghcr-prune.sh          # dry run
#   GHCR_TOKEN=<pat with delete:packages> ./scripts/ghcr-prune.sh --apply  # delete
#
# The token can also come from `gh auth token` if gh is installed:
#   GHCR_TOKEN=$(gh auth token) ./scripts/ghcr-prune.sh
set -euo pipefail

PKG="${PKG:-sunyin0818/v2ex-notifier}"
APPLY=0
[ "${1:-}" = "--apply" ] && APPLY=1

# A tag "looks like a release" if it is `latest` or a dotted version (1.2, 1.2.3,
# 0.2.0-rc1). Bare aliases like `0` and non-versions like `main` / `sha-abc1234`
# do not count - that is what makes a manifest disposable.
release_like() {
  printf '%s' "$1" | grep -Eq '^(latest|v?[0-9]+\.[0-9]+(\.[0-9]+)?([-+].*)?)$'
}

if [ -z "${GHCR_TOKEN:-}" ]; then
  if [ "$APPLY" -eq 1 ]; then
    echo "缺少 GHCR_TOKEN：--apply 需要一个带 delete:packages 权限的 PAT" >&2
    echo "  classic: 勾选 delete:packages    fine-grained: Packages: Read and write" >&2
    echo "  或先 gh auth login，然后用 GHCR_TOKEN=\$(gh auth token) 调用" >&2
    exit 2
  fi
  echo "未提供 GHCR_TOKEN，回退到匿名 token（只读，够预演用）"
  GHCR_TOKEN=$(curl -sS -m 30 \
    "https://ghcr.io/token?scope=repository:${PKG}:pull&service=ghcr.io" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
fi

auth=(-H "Authorization: Bearer ${GHCR_TOKEN}")
accept=(-H "Accept: application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json")

echo "package: ${PKG}"
echo

tags=$(curl -sS -m 30 "${auth[@]}" "https://ghcr.io/v2/${PKG}/tags/list" \
  | python3 -c 'import json,sys; print("\n".join(sorted(json.load(sys.stdin).get("tags") or [])))')

if [ -z "$tags" ]; then
  echo "拿不到 tag 列表（检查 token 权限与包名）" >&2
  exit 1
fi

# Resolve every tag to its manifest digest.
declare -A DIGEST_OF
declare -A TAGS_OF
for t in $tags; do
  d=$(curl -sS -m 30 -D- -o /dev/null "${auth[@]}" "${accept[@]}" \
        "https://ghcr.io/v2/${PKG}/manifests/${t}" \
      | tr -d '\r' | awk 'tolower($1)=="docker-content-digest:"{print $2}')
  if [ -z "$d" ]; then
    echo "  ✗ 无法解析 tag ${t}（跳过）" >&2
    continue
  fi
  DIGEST_OF[$t]="$d"
  TAGS_OF[$d]="${TAGS_OF[$d]:-}${t} "
done

if [ "${#TAGS_OF[@]}" -eq 0 ]; then
  echo "一个 tag 都没解析出来，放弃。" >&2
  exit 1
fi

to_delete=()
kept=0
for d in $(printf '%s\n' "${!TAGS_OF[@]}" | sort); do
  group=(${TAGS_OF[$d]})
  releasable=0
  for t in "${group[@]}"; do
    release_like "$t" && releasable=1
  done

  if [ "$releasable" -eq 1 ]; then
    kept=$((kept + 1))
    extra=""
    for t in "${group[@]}"; do
      release_like "$t" || extra="${extra}${t} "
    done
    if [ -n "$extra" ]; then
      echo "保留 ${d:0:19}…  tags: ${group[*]}"
      echo "     ⚠ 其中 ${extra% } 是旧 tag，但和发布 tag 共用同一 manifest，"
      echo "       只能去 GitHub → Packages 里按 tag 处理，或留着（不影响拉取）"
    else
      echo "保留 ${d:0:19}…  tags: ${group[*]}"
    fi
  else
    echo "待删 ${d:0:19}…  tags: ${group[*]}"
    to_delete+=("$d")
  fi
done

echo
if [ "${#to_delete[@]}" -eq 0 ]; then
  echo "没有可安全删除的 manifest。"
  exit 0
fi

if [ "$APPLY" -ne 1 ]; then
  echo "预演：将删除 ${#to_delete[@]} 个 manifest。加 --apply 实际执行。"
  exit 0
fi

echo "删除 ${#to_delete[@]} 个 manifest…"
failed=0
for d in "${to_delete[@]}"; do
  code=$(curl -sS -m 30 -o /dev/null -w '%{http_code}' -X DELETE "${auth[@]}" \
          "https://ghcr.io/v2/${PKG}/manifests/${d}")
  if [ "$code" = "202" ] || [ "$code" = "200" ]; then
    echo "  ✓ ${d:0:19}…"
  else
    echo "  ✗ ${d:0:19}… HTTP ${code}" >&2
    failed=1
  fi
done
exit "$failed"
