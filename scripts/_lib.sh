#!/usr/bin/env bash
# Shared helpers for scripts/*.sh. Source it, don't execute it:
#
#   #!/usr/bin/env bash
#   set -euo pipefail
#   cd "$(dirname "$0")/.."
#   source scripts/_lib.sh
#   load_env
#
# Provides: colors, say/ok/warn/err/step/die, load_env, has_cli, require_cli,
# check_cli, require_env, check_env, confirm, and a dev-server port guard.
# Works on macOS bash 3.2 and on Linux. Do not edit per project; put project
# specific helpers in a sibling file (scripts/_project.sh) and source that too.

# ─── Colors (off when piped or NO_COLOR is set) ──────────
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  BOLD=$'\033[1m'; DIM=$'\033[2m'; RED=$'\033[31m'; GREEN=$'\033[32m'
  YELLOW=$'\033[33m'; CYAN=$'\033[36m'; RESET=$'\033[0m'
else
  BOLD=''; DIM=''; RED=''; GREEN=''; YELLOW=''; CYAN=''; RESET=''
fi

# ─── Logging ─────────────────────────────────────────────
say()  { printf '%s\n' "$*"; }
step() { printf '\n%s▸ %s%s\n' "$BOLD$CYAN" "$*" "$RESET"; }
ok()   { printf '%s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
warn() { printf '%s!%s %s\n' "$YELLOW" "$RESET" "$*" >&2; }
err()  { printf '%s✗%s %s\n' "$RED" "$RESET" "$*" >&2; }
die()  { err "$*"; exit 1; }

# ─── Env ─────────────────────────────────────────────────
# load_env [file]: export every KEY=value from .env WITHOUT shell expansion.
# It deliberately does not `source` the file: values such as a bcrypt hash
# contain '$' and sourcing would expand them into empty strings.
load_env() {
  local file="${1:-.env}" line key val
  [ -f "$file" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    line="${line#"${line%%[![:space:]]*}"}"
    [ -z "$line" ] && continue
    case "$line" in
      \#*) continue ;;
      export\ *) line="${line#export }" ;;
    esac
    case "$line" in *=*) ;; *) continue ;; esac
    key="${line%%=*}"; val="${line#*=}"
    key="${key%"${key##*[![:space:]]}"}"
    val="${val#"${val%%[![:space:]]*}"}"
    case "$val" in
      \"*\") val="${val#\"}"; val="${val%\"}" ;;
      \'*\') val="${val#\'}"; val="${val%\'}" ;;
    esac
    # Do not override a value already set in the real environment.
    if [ -z "${!key:-}" ]; then export "$key=$val"; fi
  done < "$file"
}

# require_env NAME "where to get it": exit with a hint if NAME is unset.
# Interactive (no CI=1, a terminal on stdin): offer to paste it once and
# append it to .env. CI=1: just exit non-zero, never prompt.
require_env() {
  local name="$1" hint="${2:-}"
  [ -n "${!name:-}" ] && return 0
  err "missing env var: ${BOLD}${name}${RESET}"
  [ -n "$hint" ] && say "  ${DIM}${hint}${RESET}"
  if [ "${CI:-}" != "1" ] && [ -t 0 ]; then
    local value
    printf '%s?%s paste %s (empty to abort): ' "$YELLOW" "$RESET" "$name"
    IFS= read -rs value; printf '\n'
    if [ -n "$value" ]; then
      export "$name=$value"
      printf '%s=%s\n' "$name" "$value" >> .env
      ok "saved $name to .env"
      return 0
    fi
  fi
  exit 1
}

# check_env NAME "hint": report only (for doctor). Returns 1 if unset.
check_env() {
  if [ -n "${!1:-}" ]; then ok "$1 set"; return 0; fi
  warn "$1 not set${2:+ — $2}"; return 1
}

# ─── CLI presence ────────────────────────────────────────
has_cli() { command -v "$1" >/dev/null 2>&1; }

# require_cli name "install hint": exit if missing.
require_cli() {
  has_cli "$1" && return 0
  err "missing CLI: ${BOLD}$1${RESET}"
  [ -n "${2:-}" ] && say "  ${DIM}$2${RESET}"
  exit 1
}

# check_cli name "install hint": report only (for doctor). Returns 1 if missing.
check_cli() {
  if has_cli "$1"; then ok "$1 ($(command -v "$1"))"; return 0; fi
  warn "missing CLI: $1${2:+ — $2}"; return 1
}

# ─── Confirmation ────────────────────────────────────────
# confirm "question?": returns 0 on yes; auto-yes when CI=1 or YES=1.
# Never waits without a terminal: agent shells keep stdin open with no EOF, so a
# bare `read` would hang forever. No terminal means "no" unless YES=1.
confirm() {
  [ "${CI:-}" = "1" ] || [ "${YES:-}" = "1" ] && return 0
  local reply
  printf '%s?%s %s [y/N] ' "$YELLOW" "$RESET" "$*"
  if [ ! -t 0 ]; then echo "no terminal, answering no (rerun with YES=1 to accept)"; return 1; fi
  read -r reply || return 1
  case "$reply" in [yY]|[yY][eE][sS]) return 0 ;; *) return 1 ;; esac
}

# ─── Dev-server port guard ───────────────────────────────
# Refuse to start a second server on a busy port (orphaned `go run` / node
# children are the usual cause). FORCE=1 kills the holder first.
port_pids() { lsof -ti "tcp:$1" -sTCP:LISTEN 2>/dev/null || true; }

kill_port() {
  local port="$1" pids i
  pids="$(port_pids "$port")"
  [ -z "$pids" ] && return 0
  # shellcheck disable=SC2086
  kill $pids 2>/dev/null || true
  for i in 1 2 3 4 5 6 7 8 9 10; do
    [ -z "$(port_pids "$port")" ] && return 0
    sleep 0.5
  done
  warn "port $port: still held after SIGTERM, sending SIGKILL"
  # shellcheck disable=SC2086
  kill -9 $(port_pids "$port") 2>/dev/null || true
  sleep 1
  [ -z "$(port_pids "$port")" ] || die "port $port: could not free it, kill it by hand and retry"
}

# require_free_port PORT [label]
require_free_port() {
  local port="$1" label="${2:-service}" pids pid
  pids="$(port_pids "$port")"
  [ -z "$pids" ] && return 0
  if [ "${FORCE:-}" = "1" ]; then
    warn "$label: port $port busy, FORCE=1 so stopping the old process first"
    kill_port "$port"; return 0
  fi
  err "$label: port $port is already in use. Holding it now:"
  for pid in $pids; do
    ps -o pid=,etime=,command= -p "$pid" 2>/dev/null | sed 's/^/    /' >&2
  done
  say "  Use that one, or take the port with: FORCE=1 make dev" >&2
  exit 1
}
