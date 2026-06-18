#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD_OUTPUT="${BUILD_OUTPUT:-$ROOT_DIR/build/mateway}"
CLI_OUTPUT="${CLI_OUTPUT:-$HOME/.local/bin/mateway}"
PLIST_PATH="${PLIST_PATH:-$HOME/Library/LaunchAgents/com.dongping.mateway.gateway.plist}"
LAUNCH_DOMAIN="${LAUNCH_DOMAIN:-gui/$(id -u)}"
ENV_FILE="${ENV_FILE:-}"
MATEWAY_HOME="${MATEWAY_HOME:-$HOME/.mateway}"
STDOUT_LOG="${STDOUT_LOG:-$MATEWAY_HOME/logs/mateway-gateway.out.log}"
STDERR_LOG="${STDERR_LOG:-$MATEWAY_HOME/logs/mateway-gateway.err.log}"

usage() {
  cat <<'EOF'
Usage:
  scripts/restart-plist.sh [--plist /path/to/agent.plist] [--build-output /path/to/mateway] [--cli-output /path/to/mateway] [--env-file /path/to/env] [--no-build] [--no-sync-cli]

Description:
  Rebuilds the local mateway binary, syncs the CLI binary, and restarts an existing macOS LaunchAgent plist.

Environment overrides:
  BUILD_OUTPUT   Target binary path. Default: ./build/mateway
  CLI_OUTPUT     Optional CLI binary copy target. Default: ~/.local/bin/mateway
  PLIST_PATH     LaunchAgent plist path. Default: ~/Library/LaunchAgents/com.dongping.mateway.gateway.plist
  LAUNCH_DOMAIN  launchctl domain. Default: gui/<current uid>
  ENV_FILE       Optional shell env file to source before starting mateway
  MATEWAY_HOME   Base home for default log paths. Default: ~/.mateway
  STDOUT_LOG     LaunchAgent stdout log path. Default: ~/.mateway/logs/mateway-gateway.out.log
  STDERR_LOG     LaunchAgent stderr log path. Default: ~/.mateway/logs/mateway-gateway.err.log
EOF
}

DO_BUILD=1
SYNC_CLI=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    --plist)
      PLIST_PATH="$2"
      shift 2
      ;;
    --build-output)
      BUILD_OUTPUT="$2"
      shift 2
      ;;
    --cli-output)
      CLI_OUTPUT="$2"
      shift 2
      ;;
    --env-file)
      ENV_FILE="$2"
      shift 2
      ;;
    --no-build)
      DO_BUILD=0
      shift
      ;;
    --no-sync-cli)
      SYNC_CLI=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "this helper only supports macOS launchd" >&2
  exit 1
fi

if [[ ! -f "$PLIST_PATH" ]]; then
  echo "plist not found: $PLIST_PATH" >&2
  exit 1
fi

if [[ -n "$ENV_FILE" && ! -f "$ENV_FILE" ]]; then
  echo "env file not found: $ENV_FILE" >&2
  exit 1
fi

mkdir -p "$(dirname "$BUILD_OUTPUT")"
if [[ "$SYNC_CLI" == "1" && -n "$CLI_OUTPUT" ]]; then
  mkdir -p "$(dirname "$CLI_OUTPUT")"
fi
mkdir -p "$(dirname "$STDOUT_LOG")"
mkdir -p "$(dirname "$STDERR_LOG")"

if [[ "$DO_BUILD" == "1" ]]; then
  echo "building mateway -> $BUILD_OUTPUT"
  (cd "$ROOT_DIR" && go build -o "$BUILD_OUTPUT" ./cmd/mateway)
fi

if [[ "$SYNC_CLI" == "1" && -n "$CLI_OUTPUT" ]]; then
  if [[ "$BUILD_OUTPUT" != "$CLI_OUTPUT" ]]; then
    echo "syncing CLI binary -> $CLI_OUTPUT"
    cp "$BUILD_OUTPUT" "$CLI_OUTPUT"
  fi
fi

LABEL="$(/usr/libexec/PlistBuddy -c 'Print :Label' "$PLIST_PATH")"
if [[ -n "$ENV_FILE" ]]; then
  COMMAND="source \"$ENV_FILE\"; exec \"$BUILD_OUTPUT\" gateway serve"
else
  COMMAND="exec \"$BUILD_OUTPUT\" gateway serve"
fi

/usr/libexec/PlistBuddy -c "Set :ProgramArguments:2 $COMMAND" "$PLIST_PATH"
/usr/libexec/PlistBuddy -c "Set :WorkingDirectory $ROOT_DIR" "$PLIST_PATH"
/usr/libexec/PlistBuddy -c "Set :StandardOutPath $STDOUT_LOG" "$PLIST_PATH"
/usr/libexec/PlistBuddy -c "Set :StandardErrorPath $STDERR_LOG" "$PLIST_PATH"

echo "restarting launch agent: $PLIST_PATH"
launchctl bootout "$LAUNCH_DOMAIN" "$PLIST_PATH" >/dev/null 2>&1 || true
launchctl bootstrap "$LAUNCH_DOMAIN" "$PLIST_PATH"

echo "launchctl print:"
launchctl print "$LAUNCH_DOMAIN/$LABEL" | sed -n '1,40p'
