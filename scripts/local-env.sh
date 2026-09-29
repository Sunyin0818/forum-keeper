# Source ./.env and adapt it for running the binary directly on the host (as
# opposed to inside a container). Must be *sourced*, not executed:
#
#   cd <repo root>
#   . ./scripts/local-env.sh
#
# Why the adaptation: .env is written for the container, where the host is
# reachable as host.docker.internal (Docker) or host.containers.internal
# (Podman). Neither name resolves on the host itself, so a local run would die
# with `lookup host.docker.internal: no such host`. Rewrite them to 127.0.0.1.

if [ ! -f ./.env ]; then
  echo "缺少 .env：先执行 ./scripts/init-env.sh 并填写 V2EX_TOKEN / FEISHU_WEBHOOK" >&2
  return 1 2>/dev/null || exit 1
fi

set -a
# shellcheck disable=SC1091
. ./.env
set +a

for _le_var in HTTPS_PROXY https_proxy HTTP_PROXY http_proxy; do
  eval "_le_val=\${${_le_var}:-}"
  [ -n "$_le_val" ] || continue
  _le_new=$(printf '%s' "$_le_val" | sed -e 's/host\.docker\.internal/127.0.0.1/g' \
                                          -e 's/host\.containers\.internal/127.0.0.1/g')
  if [ "$_le_new" != "$_le_val" ]; then
    eval "export ${_le_var}=\"\$_le_new\""
  fi
done
unset _le_var _le_val _le_new
