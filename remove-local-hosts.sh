#!/bin/bash
set -e
if grep -qE '^127\.0\.0\.1[[:space:]]+www\.semrush\.com' /etc/hosts 2>/dev/null; then
  echo "Removing www.semrush.com from /etc/hosts..."
  sudo sed -i '' '/^127\.0\.0\.1[[:space:]]\+www\.semrush\.com/d' /etc/hosts 2>/dev/null || \
    sudo sed -i '/^127\.0\.0\.1[[:space:]]\+www\.semrush\.com/d' /etc/hosts
  sudo dscacheutil -flushcache 2>/dev/null || true
  sudo killall -HUP mDNSResponder 2>/dev/null || true
  echo "✅ Removed"
else
  echo "Nothing to remove"
fi
