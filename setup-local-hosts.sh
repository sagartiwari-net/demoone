#!/bin/bash
set -e
ENTRY="127.0.0.1 www.semrush.com"
if grep -qE '^127\.0\.0\.1[[:space:]]+www\.semrush\.com(\s|$)' /etc/hosts 2>/dev/null; then
  echo "✅ www.semrush.com already in /etc/hosts"
else
  echo "Adding: $ENTRY"
  echo "$ENTRY" | sudo tee -a /etc/hosts > /dev/null
fi
sudo dscacheutil -flushcache 2>/dev/null || true
sudo killall -HUP mDNSResponder 2>/dev/null || true
echo ""
echo "✅ Done. Open: http://www.semrush.com:7851"
echo "   (Dashboard APIs need real hostname — 127.0.0.1 alone breaks widget data)"
