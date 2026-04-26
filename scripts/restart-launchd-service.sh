#!/usr/bin/env bash
set -euo pipefail

LABEL="com.dongping.mateway.gateway"
TARGET="gui/$(id -u)/${LABEL}"
PLIST_PATH="${HOME}/Library/LaunchAgents/${LABEL}.plist"

if ! launchctl print "${TARGET}" >/dev/null 2>&1; then
  if [ -f "${PLIST_PATH}" ]; then
    launchctl bootstrap "gui/$(id -u)" "${PLIST_PATH}" 2>/dev/null || true
  fi
fi

launchctl kickstart -k "${TARGET}"
sleep 1
launchctl kickstart -k "${TARGET}" 2>/dev/null || true

for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
  if curl -sS http://127.0.0.1:8787/health; then
    exit 0
  fi
  sleep 1
done

exit 1
