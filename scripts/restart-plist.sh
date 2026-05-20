#!/usr/bin/env bash
set -euo pipefail

ENV_FILE=""
LABEL="${MATEWAY_LAUNCH_AGENT_LABEL:-com.dongping.mateway.gateway}"

usage() {
  cat <<'USAGE'
Usage:
  scripts/restart-plist.sh --env-file ~/.mateway/config/mateway.env

Builds build/mateway and restarts an existing LaunchAgent with launchctl.
This script does not create, install, or edit plist files.

Options:
  --env-file <path>   Env file to source before building/restarting
  --label <label>     LaunchAgent label, default com.dongping.mateway.gateway
  -h, --help          Show this help
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      [[ $# -ge 2 ]] || { echo "--env-file requires a path" >&2; exit 2; }
      ENV_FILE="$2"
      shift 2
      ;;
    --label)
      [[ $# -ge 2 ]] || { echo "--label requires a label" >&2; exit 2; }
      LABEL="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

if [[ -n "${ENV_FILE}" ]]; then
  ENV_FILE="${ENV_FILE/#\~/${HOME}}"
  if [[ ! -f "${ENV_FILE}" ]]; then
    echo "env file not found: ${ENV_FILE}" >&2
    exit 1
  fi
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
fi

cd "${REPO_ROOT}"
mkdir -p build

echo "[mateway] building build/mateway"
go build -o build/mateway ./cmd/mateway

DOMAIN="gui/$(id -u)"
TARGET="${DOMAIN}/${LABEL}"

echo "[mateway] restarting existing LaunchAgent ${TARGET}"
launchctl kickstart -k "${TARGET}"

echo "[mateway] restarted ${TARGET}"
