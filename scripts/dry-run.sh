#!/usr/bin/env bash
# Single dry-run poll: prints what would be pushed, sends nothing, mutates no state.
set -euo pipefail

cd "$(dirname "$0")/.."

# Sources .env.
# shellcheck source=scripts/local-env.sh
. ./scripts/local-env.sh

if command -v go >/dev/null 2>&1; then
  exec go run ./cmd/forum-keeper --once --dry-run
fi

if [ -x ./bin/forum-keeper ]; then
  exec ./bin/forum-keeper --once --dry-run
fi

echo "既找不到 go，也没有 ./bin/forum-keeper。请安装 Go 或先构建二进制。" >&2
exit 1
