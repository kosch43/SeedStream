# Torrent Streaming Guide (SeedStream)

This guide adds **torrent streaming** to a VPS stack: play movies through Stremio that download from torrents on-demand, **while seeding** — no debrid service required. It assumes the stack layout from the [docker-compose-template](https://github.com/MarshmellowXD/docker-compose-template) (per-app `compose.yaml` files under `apps/`, a root `.env`, Traefik, Gluetun, Prowlarr, AIOStreams).

How it works:
- AIOStreams finds a release → hands it to **SeedStream**
- SeedStream asks **qBittorrent** (behind your VPN) to grab it with sequential download + first/last piece priority
- SeedStream starts streaming the file to your player **while it's still downloading**
- qBittorrent keeps seeding afterward, so your private-tracker ratio stays healthy

By the end you'll have:
- **qBittorrent** — torrent client, traffic routed through Gluetun
- **SeedStream** — the streaming addon that ties search → download → stream together
- **Disk watchdog** — stops torrents if the disk fills up

---

## Prerequisites

- The stack from the [VPS Setup Guide](https://github.com/MarshmellowXD/docker-compose-template/blob/main/guides/vps-setup.md) and [Template Deployment Guide](https://github.com/MarshmellowXD/docker-compose-template/blob/main/guides/template-deployment.md), running
- **Gluetun** (VPN) running — qBittorrent shares its network namespace, so your IP never leaks
- **Prowlarr** running (you'll point SeedStream at its Torznab API)
- **AIOStreams** set up (this is where SeedStream gets wired in)
- About **30 minutes**

---

## Step 1: Add qBittorrent

Create `apps/qbittorrent/compose.yaml`:

```yaml
services:
  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent:latest
    container_name: qbittorrent
    restart: unless-stopped
    network_mode: "service:gluetun"   # routes all torrent traffic through the VPN
    depends_on:
      gluetun:
        condition: service_started
    environment:
      PUID: ${PUID}
      PGID: ${PGID}
      TZ: ${TZ}
      WEBUI_PORT: 8080
    volumes:
      - ${DOCKER_DATA_DIR}/qbittorrent:/config
      - ${DOCKER_DATA_DIR}/media:/data
    healthcheck:
      test: ["CMD-SHELL", "wget -q --tries=1 --spider http://127.0.0.1:8080/ || exit 1"]
      interval: 60s
      timeout: 10s
      retries: 3
      start_period: 60s
    deploy:
      resources:
        limits:
          memory: 2G
          cpus: "1.0"
    profiles:
      - arr
      - all
```

Because qBittorrent shares Gluetun's network, the WebUI has to be exposed through Traefik on the bridge network. Add these labels to the **gluetun** service (not qbittorrent — it has no DNS name of its own):

```yaml
labels:
  - "traefik.enable=true"
  - "traefik.http.routers.qbittorrent.rule=Host(`${QBIT_HOSTNAME?}`)"
  - "traefik.http.routers.qbittorrent.entrypoints=websecure"
  - "traefik.http.routers.qbittorrent.tls.certresolver=letsencrypt"
  - "traefik.http.routers.qbittorrent.middlewares=tinyauth@docker"
  - "traefik.http.services.qbittorrent.loadbalancer.server.port=8080"
```

Then:

1. Add `- apps/qbittorrent/compose.yaml` to your root `compose.yaml` `include:` list
2. Add `QBIT_HOSTNAME=qbit.YOURDOMAIN` to your root `.env`
3. Add `qbit.YOURDOMAIN` to `apps/cloudflare-ddns/compose.yaml` `DOMAINS`
4. Deploy: `docker compose up -d qbittorrent gluetun`

> The services declare profiles `arr` and `all`, so they're picked up by the template's default `COMPOSE_PROFILES=all`. If you run a slimmer profile set, add `arr` to it.

You'll reach the WebUI at `https://qbit.YOURDOMAIN` (behind your SSO) once the VPN is connected.

---

## Step 2: Build SeedStream

SeedStream is a Go addon — build it into a local image. Clone this repo on the VPS:

```bash
cd /opt
git clone <your-seedstream-repo> seedstream
```

Create `apps/seedstream/compose.yaml`:

```yaml
services:
  seedstream:
    build:
      context: /opt/seedstream
      dockerfile: Dockerfile.source
    image: seedstream:local
    container_name: seedstream
    restart: unless-stopped
    environment:
      - TZ=${TZ}
    expose:
      - 7000
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.seedstream.rule=Host(`${SEEDSTREAM_HOSTNAME?}`)"
      - "traefik.http.routers.seedstream.entrypoints=websecure"
      - "traefik.http.routers.seedstream.tls.certresolver=letsencrypt"
      - "traefik.http.routers.seedstream.middlewares=tinyauth@docker"
      - "traefik.http.services.seedstream.loadbalancer.server.port=7000"
    volumes:
      - ${DOCKER_DATA_DIR}/seedstream:/app/data
      - ${DOCKER_DATA_DIR}/media:/data:ro   # read access to qBittorrent's downloads
    healthcheck:
      test: ["CMD-SHELL", "wget -q --spider http://127.0.0.1:7000/health || exit 1"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 15s
    profiles:
      - seedstream
      - all
```

Build and deploy:

```bash
docker compose up -d seedstream
```

Add `SEEDSTREAM_HOSTNAME=seedstream.YOURDOMAIN` to your root `.env`, plus the subdomain to `apps/cloudflare-ddns/compose.yaml` `DOMAINS`.

> The runtime image is a bare `alpine:latest` — it ships BusyBox, so `wget` exists but **`curl` does not**. Keep that in mind when running the health/sidecar commands in later steps.

---

## Step 3: Configure qBittorrent

Open `https://qbit.YOURDOMAIN` in your browser (behind your SSO). Log in (default `admin` / `adminadmin` — change it right away) and set:

**Tools → Options → Downloads:**

- **Pre-allocate disk space:** off (preallocation breaks streaming reads in some setups)
- **Save path:** `/data/downloads`
- **Max active downloads:** `3`

**Tools → Options → BitTorrent:**

- **Download in sequential order:** on
- **Download first and last pieces first:** on
- **When ratio reaches:** `2` → **Stop and remove the torrent and its files** (your VPS disk is precious — don't hoard)

> These two toggles are the whole trick: the file's start is ready almost immediately, and SeedStream can stream the tail without waiting for the whole file.

---

## Step 4: Configure SeedStream

Browse to `https://seedstream.YOURDOMAIN`, log in with the admin account, and go to **Settings**:

**Indexers** — add your Prowlarr Torznab source:

| Field | Value |
|-------|-------|
| Name | `Prowlarr` |
| Type | Torznab |
| URL | `http://prowlarr:9696` |
| API path | `/1/api` |
| API key | *(from Prowlarr → Settings → General)* |

**Torrent Clients** — add qBittorrent:

| Field | Value |
|-------|-------|
| Name | `qbit` |
| Type | qBittorrent |
| URL | `http://gluetun:8080` |
| Username / Password | *(the WebUI creds from Step 3)* |
| Category | `seedstream` |
| Save path | `/data/downloads` |

> SeedStream reaches qBittorrent at `gluetun:8080` because they share the same network namespace. It reads the files from `/data` — the same folder qBittorrent writes to.

**Streams** — under your default stream, note the addon URL:
```
https://seedstream.YOURDOMAIN/<token>/manifest.json
```

---

## Step 5: Wire It Into AIOStreams

1. Open `https://aiostreams.YOURDOMAIN`
2. Go to **Addons** → add the **SeedStream preset** (per the [README](../README.md)) and paste the manifest URL from Step 4
3. Save

Now when you search something in Stremio, SeedStream's releases appear alongside your debrid results — pick the SeedStream one and playback starts within seconds while the torrent downloads.

---

## Step 6: Disk Watchdog (recommended)

Torrents downloading can still fill the disk before the ratio limit kicks in. Install a simple watchdog that stops all torrents at 85% usage:

Create `/opt/scripts/disk-guard.sh`:

```bash
#!/bin/bash
set -uo pipefail

LOGFILE="/opt/docker/data/disk-guard.log"
exec >> "$LOGFILE" 2>&1

QBIT_HOST="127.0.0.1"        # qBittorrent only exists inside gluetun's namespace
QBIT_PORT=8080
QBIT_USER="admin"            # your qBittorrent WebUI credentials (Step 3)
QBIT_PASS="YOURPASSWORD"
STOP_THRESHOLD=85
NTFY_TOPIC="critical"
TARGET="/opt/docker/data/media"   # the disk that holds torrents

pct=$(df -P "$TARGET" | awk 'NR==2 {gsub(/%/,"",$5); print $5}')
echo "[disk-guard] $(date '+%F %T') used=${pct}% limit=${STOP_THRESHOLD}%"

[ "$pct" -ge "$STOP_THRESHOLD" ] || exit 0

SID=$(docker exec gluetun sh -c \
  "wget -S -O - --post-data='username=${QBIT_USER}&password=${QBIT_PASS}' \
   'http://${QBIT_HOST}:${QBIT_PORT}/api/v2/auth/login'" 2>&1 \
  | grep -oP 'QBT_SID_[0-9]+=[^;]+' | head -1)

if [ -z "$SID" ]; then
  echo "[disk-guard] ERROR: qBittorrent login failed (no SID)"
  exit 1
fi

docker exec gluetun sh -c "wget -q -O - --post-data='hashes=all' \
  --header='Cookie: SID=$SID' \
  'http://${QBIT_HOST}:${QBIT_PORT}/api/v2/torrents/stop'"
echo "[disk-guard] stopped all torrents (${pct}%)"

docker exec ntfy wget -q -O - --post-data="Disk guard: disk usage ${pct}%. Stopped all qBittorrent downloads." \
  "http://localhost/${NTFY_TOPIC}" && echo "[disk-guard] ntfy notified"
```

> qBittorrent isn't reachable on the host — it lives inside Gluetun's network namespace, so every call goes through `docker exec gluetun`. Likewise the ntfy alert runs via `docker exec ntfy` (its port is **not** 8080 — that's qBittorrent's). Set `QBIT_PASS` to the WebUI password from Step 3 and make the script executable: `chmod +x /opt/scripts/disk-guard.sh`.

Create a systemd timer to run it every 5 minutes:

```bash
sudo systemctl edit --force --full disk-guard.timer
```

```ini
[Unit]
Description=Run disk guard every 5 minutes

[Timer]
OnBootSec=5min
OnUnitActiveSec=5min

[Install]
WantedBy=timers.target
```

```bash
sudo systemctl edit --force --full disk-guard.service
```

```ini
[Unit]
Description=Pause torrents when disk is full

[Service]
Type=oneshot
ExecStart=/opt/scripts/disk-guard.sh
```

```bash
sudo systemctl enable --now disk-guard.timer
```

---

## Verification

1. In Stremio, search for a well-seeded movie and pick the SeedStream stream
2. Playback should start within a few seconds — **even though the file is still downloading**
3. Check it's really seeding:
   ```bash
   cd /opt/docker && docker exec gluetun sh -c "wget -qO- http://127.0.0.1:8080/api/v2/torrents/info" | jq '.[].state'
   ```
   You should see `stalledUP` / `uploading` after the movie finishes
4. Confirm your torrent traffic is behind the VPN (Gluetun has `wget`, not `curl`):
   ```bash
   docker exec gluetun sh -c "wget -qO- https://api.ipify.org"
   ```
   This should return **your VPN IP**, not your VPS's public IP.
5. Check the watchdog when it fires:
   ```bash
   journalctl -u disk-guard.service -e
   ```
   and look in `/opt/docker/data/disk-guard.log`.

---

## Troubleshooting

- **Playback hangs at the start** — the first pieces are still downloading. Wait a few seconds (sequential mode makes this short) or pick a release with more seeds.
- **Playback starts but dies after a few seconds** — the player is probing the *end* of the file for metadata. This is the known stock-SeedStream bug; make sure you built the **patched fork** (piece-aware streaming) from Step 2.
- **No SeedStream results in Stremio** — check the indexer in SeedStream Settings, and that Prowlarr's API key matches. SeedStream only shows releases it can actually grab.
- **Disk fills up** — check the watchdog fired: `journalctl -u disk-guard.service -e`. You can also lower the 85% threshold.
- **Torrents never start** — qBittorrent is behind the VPN; if Gluetun is down, so is qBittorrent. Check `docker compose ps` shows both up.
