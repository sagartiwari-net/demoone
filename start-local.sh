#!/bin/bash
set -e
cd "$(dirname "$0")"
export CONFIG_FILE="${CONFIG_FILE:-config.local.json}"
PORT=$(grep -o '"port"[[:space:]]*:[[:space:]]*"[^"]*"' "$CONFIG_FILE" | grep -o '[0-9]\+' | head -1)
PORT=${PORT:-7851}

if [ ! -s cookie.txt ]; then
  echo "WARN: cookie.txt is empty — export Semrush cookies from browser first."
fi

echo "Using config: $CONFIG_FILE"
echo "Killing port $PORT..."
lsof -ti :"$PORT" | xargs kill -9 2>/dev/null || true
sleep 1

echo "Building..."
go build -o semrush-new-go-proxy .

echo ""
echo "=========================================="
echo "  Semrush NEW — LOCAL dev server"
echo "  URL: http://127.0.0.1:$PORT"
echo "  Target: https://www.semrush.com"
echo "  Cookies: cookie.txt"
echo "  (no /etc/hosts needed — hostname spoofed in JS)"
echo "=========================================="
echo ""
exec ./semrush-new-go-proxy
