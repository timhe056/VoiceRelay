#!/bin/bash
# push.sh — commit and push VoiceRelay changes to GitHub
# Usage: ./push.sh "commit message"

set -e

if [ -z "$1" ]; then
    echo "Usage: ./push.sh \"commit message\""
    exit 1
fi

cd "$(dirname "$0")"
git add -A
git commit -m "$1" || echo "[info] nothing to commit"
git push origin master
echo "[OK] Pushed to GitHub"
