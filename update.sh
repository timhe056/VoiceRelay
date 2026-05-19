#!/bin/bash
# update.sh — pull latest from GitHub, build, stop old, deploy, restart
# Usage: cd ~/VoiceRelay && ./update.sh

set -e

echo "=== Pulling ==="
git pull origin master

echo "=== Building ==="
CGO_ENABLED=0 go build -ldflags="-s -w" -o voicerelay ./cmd/voicerelay

echo "=== Stopping ==="
systemctl stop voicerelay 2>/dev/null || true
pkill -9 voicerelay 2>/dev/null || true
sleep 1

echo "=== Deploying ==="
cp voicerelay /usr/local/bin/voicerelay

echo "=== Starting ==="
systemctl start voicerelay
sleep 1

echo "=== Health ==="
curl -s http://localhost:8080/healthz
echo ""
echo "[OK] Done"
