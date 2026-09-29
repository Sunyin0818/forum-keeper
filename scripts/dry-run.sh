#!/usr/bin/env bash
# Single dry-run poll: prints what would be pushed, sends nothing, mutates no
# state. Equivalent to `make dry-run` but without needing GNU make.
set -euo pipefail

cd "$(dirname "$0")/.."

if [ ! -f .env ]; then
  echo "缺少 .env：先执行 cp .env.example .env 并填写 V2EX_TOKEN / FEISHU_WEBHOOK" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1091
. ./.env
set +a

if command -v go >/dev/null 2>&1; then
  exec go run ./cmd/notifier --once --dry-run
fi

if [ -x ./bin/notifier ]; then
  exec ./bin/notifier --once --dry-run
fi

echo "既找不到 go，也没有 ./bin/notifier。请安装 Go 或先构建二进制。" >&2
exit 1
