#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PLIST_PATH="${HOME}/Library/LaunchAgents/com.dongping.mateway.gateway.plist"
BINARY_PATH="${ROOT}/build/mateway"
CLI_DIR="${HOME}/.local/bin"

mkdir -p "${HOME}/Library/LaunchAgents"
mkdir -p "${CLI_DIR}"
ln -sf "${BINARY_PATH}" "${CLI_DIR}/mateway"

cat > "${PLIST_PATH}" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.dongping.mateway.gateway</string>
  <key>ProgramArguments</key>
  <array>
    <string>${BINARY_PATH}</string>
    <string>gateway</string>
  </array>
  <key>WorkingDirectory</key>
  <string>${ROOT}</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>${HOME}/Library/Logs/mateway-gateway.out.log</string>
  <key>StandardErrorPath</key>
  <string>${HOME}/Library/Logs/mateway-gateway.err.log</string>
</dict>
</plist>
PLIST

launchctl bootstrap "gui/$(id -u)" "${PLIST_PATH}" 2>/dev/null || true
launchctl kickstart -k "gui/$(id -u)/com.dongping.mateway.gateway"
