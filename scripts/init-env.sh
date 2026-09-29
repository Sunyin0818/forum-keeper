#!/usr/bin/env bash
# Create .env from .env.example — and NEVER clobber an existing one.
#
# Plain `cp .env.example .env` silently destroys a filled-in .env, which is
# exactly the kind of accident that costs you a token and a webhook. Use this
# script instead.
set -euo pipefail

cd "$(dirname "$0")/.."

if [ -e .env ]; then
  echo "✋ .env 已存在，未做任何改动：" >&2
  ls -l .env >&2
  echo >&2
  echo "需要填写的字段请参考 .env.example。若要重新开始，请先自行备份并删除 .env。" >&2
  exit 1
fi

if [ ! -f .env.example ]; then
  echo "找不到 .env.example" >&2
  exit 1
fi

# Create it 0600 from the start: it will hold a V2EX token and a bot secret.
umask 077
cp .env.example .env
chmod 600 .env

echo "已创建 .env（权限 600）。接下来请填写："
echo "  V2EX_TOKEN       https://www.v2ex.com/settings/tokens"
echo "  FEISHU_WEBHOOK   飞书群 → 设置 → 群机器人 → 你的机器人"
echo "  FEISHU_SECRET    仅当机器人开启了签名校验"
echo
echo "填好后可先用只读方式验证： ./scripts/dry-run.sh"
