#!/bin/bash
# Server setup — run inside /www/wwwroot/1clkaccess.store/hellearn
set -e

DIR="/www/wwwroot/1clkaccess.store/hellearn"
PORT="4500"
BIN="helium-learning-go-proxy"
SERVICE="helium-learning"

port_in_use() {
  ss -tlnp 2>/dev/null | grep -qE ":${PORT}([^0-9]|$)"
}

kill_port_holders() {
  systemctl stop "$SERVICE" 2>/dev/null || true
  sleep 1
  pids=$(ss -tlnp 2>/dev/null | grep -E ":${PORT}([^0-9]|$)" | grep -oE 'pid=[0-9]+' | cut -d= -f2 | sort -u || true)
  if [ -n "$pids" ]; then
    echo "Killing port ${PORT} PIDs: $pids"
    kill -9 $pids 2>/dev/null || true
  fi
  fuser -k ${PORT}/tcp 2>/dev/null || true
  pkill -9 -f "${DIR}/${BIN}" 2>/dev/null || true
  sleep 2
}

ensure_port_free() {
  for i in 1 2 3 4 5 6; do
    if ! port_in_use; then return 0; fi
    echo "WARN: port $PORT still in use (attempt $i)"
    kill_port_holders
  done
  if port_in_use; then
    echo "ERROR: port $PORT still in use"
    ss -tlnp | grep -E ":${PORT}([^0-9]|$)" || true
    exit 1
  fi
}

cd "$DIR"
export PATH="$PATH:/usr/local/go/bin:/root/go/bin"
echo "=== Helium Learning SERVER SETUP ==="
echo "Folder: $DIR | Port: $PORT | Domain: hellearn.1clkaccess.store"

git config --global --add safe.directory "$DIR" 2>/dev/null || true
if [ -d .git ]; then
  git pull --ff-only 2>/dev/null && echo "Git pull OK" || echo "WARN: git pull skipped"
fi

systemctl stop "$SERVICE" 2>/dev/null || true
systemctl reset-failed "$SERVICE" 2>/dev/null || true
kill_port_holders
ensure_port_free

if [ ! -f config.production.json ]; then
  echo "ERROR: config.production.json missing"
  exit 1
fi

PRESERVED_MYSQL_PASS=""
if [ -f config.json ]; then
  PRESERVED_MYSQL_PASS=$(python3 -c 'import json
try:
  print(json.load(open("config.json")).get("mysql_password",""))
except Exception:
  pass' 2>/dev/null || true)
fi
cp config.production.json config.json
echo "Applied config.production.json → config.json"

export PRESERVED_MYSQL_PASS
python3 << 'PY'
import json, os

cfg = json.load(open("config.json"))
pwd = str(cfg.get("mysql_password") or "")
old = os.environ.get("PRESERVED_MYSQL_PASS", "")

def take(src):
    try:
        s = json.load(open(src))
        p = str(s.get("mysql_password") or "").strip()
        if p and p not in ("", "CHANGE_ME", "CHANGE_ME_ON_SERVER"):
            cfg["mysql_password"] = p
            if s.get("mysql_user"):
                cfg["mysql_user"] = s["mysql_user"]
            if s.get("mysql_db"):
                cfg["mysql_db"] = s["mysql_db"]
            return src
    except Exception:
        pass
    return None

restored = None
if pwd in ("", "CHANGE_ME", "CHANGE_ME_ON_SERVER"):
    if old and old not in ("", "CHANGE_ME", "CHANGE_ME_ON_SERVER"):
        cfg["mysql_password"] = old
        restored = "config.json (previous)"
    else:
        for src in (
            "/www/wwwroot/1clkaccess.store/hm/config.json",
            "/www/wwwroot/1clkaccess.store/chat/config.json",
            "/www/wwwroot/1clkaccess.store/jungle/config.json",
            "/www/wwwroot/toolsmandi.com/ctrl/config.json",
        ):
            if os.path.exists(src):
                restored = take(src)
                if restored:
                    break

with open("config.json", "w") as f:
    json.dump(cfg, f, indent=2)
    f.write("\n")

if cfg.get("mysql_password") in ("", "CHANGE_ME", "CHANGE_ME_ON_SERVER"):
    print("ERROR: mysql_password still CHANGE_ME — set it in config.json")
    raise SystemExit(1)
print("OK: mysql password ready" + (f" (from {restored})" if restored else ""))
print(f"public_host={cfg.get('public_host')} port={cfg.get('port')} website_id={cfg.get('website_id')}")
PY

if ! command -v go &>/dev/null; then
  echo "ERROR: Go not installed. Try: export PATH=\$PATH:/usr/local/go/bin"
  exit 1
fi

echo "=== Build ==="
go mod tidy
go mod download
CGO_ENABLED=0 go build -buildvcs=false -ldflags="-s -w" -o "$BIN" .
chmod +x "$BIN"
file "$BIN"

cp helium-learning.service /etc/systemd/system/${SERVICE}.service
systemctl daemon-reload
systemctl enable "$SERVICE"

kill_port_holders
ensure_port_free
systemctl start "$SERVICE"
sleep 3

if ! systemctl is-active --quiet "$SERVICE"; then
  echo "ERROR: $SERVICE failed to start"
  journalctl -u "$SERVICE" -n 40 --no-pager
  exit 1
fi

systemctl status "$SERVICE" --no-pager | head -14
echo ""
echo "=== Health ==="
curl -sI "http://127.0.0.1:${PORT}/library" | head -15 || true

echo ""
echo "Domain: https://hellearn.1clkaccess.store/library"
echo "aaPanel reverse proxy → http://127.0.0.1:${PORT}"
echo "DB website_id=133 secret=toolsmandi_heliumlearn_secret_xyz123"
echo "Cookies: ahrefs_accounts for website_id=133"
echo "Logs: journalctl -u $SERVICE -f"
