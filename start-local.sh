#!/bin/bash
set -e
cd "$(dirname "$0")"
export HL_CONFIG="config.local.json"

echo "Building Helium Learning proxy..."
go build -o helium-learning-go-proxy .

PORT=$(python3 -c "import json; print(json.load(open('config.local.json')).get('port','4500'))")
if lsof -tiTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "Killing old process on :$PORT ..."
  lsof -tiTCP:"$PORT" -sTCP:LISTEN | xargs kill -9 2>/dev/null || true
  sleep 1
fi

echo "=========================================="
echo "  Helium Learning LOCAL"
echo "  Open:   http://127.0.0.1:$PORT/library"
echo "  Target: https://learning.helium10.com"
echo "  Cookies: cookie.txt"
echo "=========================================="
exec ./helium-learning-go-proxy
