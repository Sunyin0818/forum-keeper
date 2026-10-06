# Load ./.env for a host run and adapt it for the binary directly on the host
# (as opposed to inside a container). Must be *sourced*, not executed:
#
#   cd <repo root>
#   . ./scripts/local-env.sh
#
# Two reasons this is not a plain `source .env`:
#
# 1. Values must not be shell-interpreted. A V2EX cookie contains many ';', so
#    sourcing would cut it at the first one (1356 chars -> 90) and the sign-in
#    would silently report "not logged in". Here every value is taken literally,
#    exactly like docker compose's env_file parser, so quoted and unquoted
#    values both work.
# 2. .env is written for the container, where the host is reachable as
#    host.docker.internal (Docker) or host.containers.internal (Podman).
#    Neither name resolves on the host itself, so a local run would die with
#    `lookup host.docker.internal: no such host`. Rewrite them to 127.0.0.1.

if [ ! -f ./.env ]; then
  echo "缺少 .env：先执行 ./scripts/init-env.sh 并填写凭据" >&2
  return 1 2>/dev/null || exit 1
fi

while IFS= read -r _le_line || [ -n "$_le_line" ]; do
  case "$_le_line" in
    '' | \#*) continue ;; # blank line or comment
  esac
  _le_line=${_le_line#export }
  _le_key=${_le_line%%=*}
  _le_val=${_le_line#*=}
  case "$_le_key" in
    '' | *[!A-Za-z0-9_]*) continue ;; # not a KEY=value line
  esac
  # Trim surrounding whitespace, then strip one pair of matching quotes.
  _le_val=$(printf '%s' "$_le_val" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')
  case "$_le_val" in
    \"*\") _le_val=${_le_val#\"}; _le_val=${_le_val%\"} ;;
    \'*\') _le_val=${_le_val#\'}; _le_val=${_le_val%\'} ;;
  esac
  export "$_le_key=$_le_val"
done < ./.env
unset _le_line _le_key _le_val

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
