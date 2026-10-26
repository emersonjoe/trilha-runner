#!/usr/bin/env bash
# The Anthropic API key for the opt-in live tests, kept in the system keychain
# instead of a shell profile, a .env file or the shell history.
#
#   scripts/anthropic-key.sh set       prompt (hidden), check it with the API, store it
#   scripts/anthropic-key.sh status    where it lives, masked, and whether the API accepts it
#   scripts/anthropic-key.sh forget    remove it
#   scripts/anthropic-key.sh exec -- CMD...   run CMD with ANTHROPIC_API_KEY set for it alone
#
# `set` also reads the key from stdin, so a password manager can feed it:
#   op read 'op://Private/Anthropic e2e/credential' | scripts/anthropic-key.sh set
#
# Where it is kept: the macOS Keychain (`security`), the Secret Service on
# Linux (`secret-tool`), or else a 0600 file under $XDG_CONFIG_HOME/trilha.
# The key never goes on a command line (where `ps` shows it), never on
# stdout except to the one process `exec` starts, and never into a file in
# the repository.
set -euo pipefail

SERVICE=trilha-runner-e2e-anthropic
ACCOUNT=${USER:-$(id -un)}
LABEL="trilha-runner live tests: Anthropic API key"
KEY_FILE="${XDG_CONFIG_HOME:-$HOME/.config}/trilha/anthropic-e2e.key"
API=${ANTHROPIC_BASE_URL:-https://api.anthropic.com}

say() { printf '%s\n' "$*" >&2; }
die() { say "anthropic-key: $*"; exit 1; }

backend() {
  # ANTHROPIC_KEY_BACKEND=file forces the file store (the script's own tests).
  if [ -n "${ANTHROPIC_KEY_BACKEND:-}" ]; then
    echo "$ANTHROPIC_KEY_BACKEND"
  elif [ "$(uname -s)" = Darwin ] && command -v security >/dev/null 2>&1; then
    echo keychain
  elif command -v secret-tool >/dev/null 2>&1 && [ -n "${DBUS_SESSION_BUS_ADDRESS:-}" ]; then
    echo secret-service
  else
    echo file
  fi
}

where() {
  case $1 in
    keychain) echo "macOS Keychain (service $SERVICE, account $ACCOUNT)" ;;
    secret-service) echo "Secret Service (service $SERVICE, account $ACCOUNT)" ;;
    file) echo "$KEY_FILE (mode 0600)" ;;
  esac
}

# GNU stat first: on Linux `stat -f` means "file system" and prints before failing.
file_mode() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }

load() {
  case $(backend) in
    keychain) security find-generic-password -s "$SERVICE" -a "$ACCOUNT" -w 2>/dev/null ;;
    secret-service) secret-tool lookup service "$SERVICE" account "$ACCOUNT" 2>/dev/null ;;
    file)
      [ -f "$KEY_FILE" ] || return 1
      [ "$(file_mode "$KEY_FILE")" = 600 ] || die "$KEY_FILE must be mode 0600 (chmod 600 it, or run forget and set again)"
      cat "$KEY_FILE"
      ;;
  esac
}

store() {
  key=$1
  case $(backend) in
    keychain)
      # `security -i` reads its command from stdin, so the key stays off argv.
      printf 'add-generic-password -U -s "%s" -a "%s" -l "%s" -w "%s"\n' "$SERVICE" "$ACCOUNT" "$LABEL" "$key" |
        security -i >/dev/null
      ;;
    secret-service)
      printf '%s' "$key" | secret-tool store --label "$LABEL" service "$SERVICE" account "$ACCOUNT"
      ;;
    file)
      (umask 077 && mkdir -p "$(dirname "$KEY_FILE")" && printf '%s\n' "$key" >"$KEY_FILE.tmp" && mv "$KEY_FILE.tmp" "$KEY_FILE")
      chmod 600 "$KEY_FILE"
      ;;
  esac
}

remove() {
  case $(backend) in
    keychain) security delete-generic-password -s "$SERVICE" -a "$ACCOUNT" >/dev/null 2>&1 ;;
    secret-service) secret-tool clear service "$SERVICE" account "$ACCOUNT" 2>/dev/null ;;
    file) [ -f "$KEY_FILE" ] && rm -f "$KEY_FILE" ;;
  esac
}

mask() { printf 'sk-ant-…%s' "${1: -4}"; }

# check answers the HTTP status of a free, read-only call (the model list).
# The header goes through stdin (-H @-), not argv.
check() {
  command -v curl >/dev/null 2>&1 || { echo "no-curl"; return; }
  printf 'x-api-key: %s\n' "$1" |
    curl -sS -o /dev/null -w '%{http_code}' --max-time 15 \
      -H @- -H 'anthropic-version: 2023-06-01' "$API/v1/models" 2>/dev/null || echo "offline"
}

explain() {
  case $1 in
    200) say "  the API accepts it" ;;
    401) say "  the API refuses it (401): revoked, mistyped, or from another organization" ;;
    403) say "  the API answers 403: the key exists but lacks permission" ;;
    offline|000) say "  could not reach $API to check it" ;;
    no-curl) say "  curl is not installed, so it was not checked" ;;
    *) say "  the API answered $1" ;;
  esac
}

cmd_set() {
  if [ -t 0 ]; then
    printf 'Anthropic API key (input hidden): ' >&2
    IFS= read -rs key
    printf '\n' >&2
  else
    IFS= read -r key || true
  fi
  key=$(printf '%s' "$key" | tr -d '[:space:]')
  [ -n "$key" ] || die "no key given"
  [[ $key =~ ^sk-ant-[A-Za-z0-9_-]{20,}$ ]] || die "that does not look like an Anthropic API key (sk-ant-…); nothing stored"
  status=$(check "$key")
  if [ "$status" = 401 ] || [ "$status" = 403 ]; then
    explain "$status"
    die "nothing stored"
  fi
  store "$key"
  say "stored $(mask "$key") in $(where "$(backend)")"
  explain "$status"
  say "next: make e2e-claude   (forget it with: make claude-key-forget)"
}

cmd_status() {
  b=$(backend)
  if ! key=$(load) || [ -z "$key" ]; then
    say "no key stored; $(where "$b") is where make claude-key will put it"
    exit 1
  fi
  say "$(mask "$key") in $(where "$b")"
  explain "$(check "$key")"
}

cmd_forget() {
  if remove; then say "removed from $(where "$(backend)")"; else say "nothing to remove"; fi
  say "revoke it as well in the Anthropic Console if you will not use it again"
}

cmd_exec() {
  [ "${1:-}" = -- ] && shift
  [ $# -gt 0 ] || die "usage: anthropic-key.sh exec -- CMD..."
  key=$(load) && [ -n "$key" ] || die "no key stored: run make claude-key first"
  ANTHROPIC_API_KEY=$key exec "$@"
}

case ${1:-} in
  set) cmd_set ;;
  status) cmd_status ;;
  forget) cmd_forget ;;
  exec) shift; cmd_exec "$@" ;;
  *) say "usage: anthropic-key.sh set | status | forget | exec -- CMD..."; exit 2 ;;
esac
