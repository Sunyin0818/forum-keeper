# Load config/.env for a host run. Must be *sourced*, not executed:
#
#   cd <repo root>
#   . ./scripts/local-env.sh
#
# config/.env is used by both the local runner and docker compose. The old
# ./.env path is still accepted as a fallback for existing checkouts.
# Values are read literally instead of `source`d: a V2EX cookie contains ';', so
# sourcing would truncate it at the first one and sign-in would silently fail.

_ENV_FILE="./config/.env"
if [ ! -f "$_ENV_FILE" ] && [ -f "./.env" ]; then
  _ENV_FILE="./.env"
fi
if [ ! -f "$_ENV_FILE" ]; then
  echo "缺少 config/.env：cp -n config/.env.example config/.env 后填写凭据" >&2
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
done < "$_ENV_FILE"
unset _le_line _le_key _le_val _ENV_FILE
