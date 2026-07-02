#!/bin/bash
# Quick install when binary already built — run as root in /www/wwwroot/toolsmandi.com/semr
set -e
DIR="/www/wwwroot/toolsmandi.com/semr"
cd "$DIR"

if [ ! -x semrush-go-proxy ]; then
  echo "Building semrush-go-proxy..."
  CGO_ENABLED=0 go build -buildvcs=false -ldflags="-s -w" -o semrush-go-proxy .
fi

cp config.production.json config.json 2>/dev/null || true
cp semrush-go-proxy.service /etc/systemd/system/semrush-go-proxy.service
systemctl daemon-reload
systemctl enable semrush-go-proxy
systemctl restart semrush-go-proxy
sleep 2
systemctl status semrush-go-proxy --no-pager | head -15
curl -sI http://127.0.0.1:7850/ | head -5 || true
