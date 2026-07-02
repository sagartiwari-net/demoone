# Semrush Go Proxy (multi-domain)

Official upstream: `https://www.semrush.com`

One process on **port 7850** serves multiple reseller domains. Each domain gets its own cookies from MySQL (`ahrefs_accounts` by `website_id`).

## Supported domains (example)

| website_id | Domain |
|------------|--------|
| 3 | semr.toolsmandi.com |
| 4 | smrs.toolsfrog.com |
| 5 | sem.toolcookies.com |

Handshake secret (per row in `ahrefs_websites`): `toolsmandi_semrush_secret_xyz123`

## Local test

```bash
cd semrush-new-go-proxy
./start-local.sh
```

Open: http://127.0.0.1:7851 (uses `cookie.txt`, no MySQL auth)

**Server par Node.js ki zaroorat nahi** — sirf Go binary chalti hai. `export-tool.js` HTML mein inject hoti hai.

## Server deploy

| Field | Value |
|-------|--------|
| Folder | `/www/wwwroot/toolsmandi.com/semr` |
| Port | **7850** |
| Service | `semrush-go-proxy` |
| DB | `toolsmandirefct` (shared ToolsMandi) |

### First-time setup

```bash
mkdir -p /www/wwwroot/toolsmandi.com/semr
cd /www/wwwroot/toolsmandi.com/semr
git clone https://github.com/sagartiwari-net/demoone.git .
```

Edit `config.production.json` — set real `mysql_password`, then:

```bash
chmod +x reset-server.sh
./reset-server.sh
```

### Update after git pull

```bash
cd /www/wwwroot/toolsmandi.com/semr
git pull
./reset-server.sh
```

### aaPanel / Nginx

Point each domain to the **same** backend:

```
proxy_pass http://127.0.0.1:7850;
```

Each friend updates cookies in the panel for their own `website_id` — no separate server setup.

## Security

- HMAC-SHA256 handshake (`/api/auth-handshake`)
- One-time token + `sem_session` cookie
- Host must match `ahrefs_websites.domain` for session/OTT
- OTT IP binding
- Per-request tenant isolation (no cross-domain cookie bleed)

## Features

- Parallel Excel/CSV export (`export-tool.js`)
- PDF export (local)
- Guru modal block
- Account rotation on login/limit
