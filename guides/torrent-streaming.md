# Torrent Streaming Guide (SeedStream)

This guide turns SeedStream into a working **torrent → instant stream** setup: play movies through Stremio that download from torrents on-demand, **while seeding** — no debrid service required.

How it works:
- SeedStream searches your trackers (via Prowlarr / Torznab) for the release
- It hands the torrent to **qBittorrent**, which grabs it with sequential download + first/last piece priority
- SeedStream starts streaming the file to your player **while it's still downloading**
- qBittorrent keeps seeding afterward, so your ratio stays healthy

By the end you'll have:
- **qBittorrent** — the torrent client doing the downloading/seeding
- **SeedStream** — the addon tying search → download → stream together
- **Disk watchdog** — stops torrents if the disk fills up

You can run everything **locally on your LAN** (no domain, no HTTPS needed) and optionally expose it publicly later.

---

## Prerequisites

- A machine with **Docker** and **Docker Compose** (any OS — works on Linux VPS, home server, even a mini PC)
- **Prowlarr** or any Torznab-capable indexer (you'll point SeedStream at its API)
- **Stremio** on your player, and optionally **AIOStreams** to consolidate addons
- About **20 minutes**

> Not sure what an indexer is? Prowlarr is the "search engine" — it gathers torrent results from trackers into one API. If you don't have one, run Prowlarr as a container too (see the [Prowlarr docs](https://wiki.servarr.com/prowlarr)).

---

## Step 1: Set Up qBittorrent

Create a folder for the stack and add qBittorrent:

```bash
mkdir -p seedstream && cd seedstream
```

`docker-compose.yml`:

```yaml
services:
  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent:latest
    container_name: qbittorrent
    restart: unless-stopped
    environment:
      PUID: 1000
      PGID: 1000
      TZ: Etc/UTC
      WEBUI_PORT: 8080
    ports:
      - "8080:8080"      # the WebUI — visit http://YOUR-SERVER-IP:8080
    volumes:
      - ./qbit-config:/config
      - ./downloads:/data
    deploy:
      resources:
        limits:
          memory: 2G
          cpus: "1.0"
```

Start it and open the WebUI at `http://YOUR-SERVER-IP:8080`:

```bash
docker compose up -d
```

Log in (default `admin` / `adminadmin` — change it right away) and set:

**Tools → Options → Downloads:**

- **Pre-allocate disk space:** off (preallocation breaks streaming reads in some setups)
- **Save path:** `/data/downloads`
- **Max active downloads:** `3`

**Tools → Options → BitTorrent:**

- **Download in sequential order:** on
- **Download first and last pieces first:** on
- **When ratio reaches:** `2` → **Stop and remove the torrent and its files** (your disk is precious — don't hoard)

> These two toggles are the whole trick: the file's start is ready almost immediately, and SeedStream can stream the tail without waiting for the whole file.

> **Torrenting behind a VPN?** If you don't want your IP visible to trackers, run qBittorrent through a VPN container (e.g. Gluetun) instead of publishing port 8080 directly. The setup is the same — SeedStream just talks to qBittorrent over the Docker network instead of a published port. More on this in the "Expose it publicly" section.

---

## Step 2: Build and Run SeedStream

SeedStream is a Go addon — build it into a local image. Clone this repo and add it to the same `docker-compose.yml`:

```bash
git clone <your-seedstream-repo> seedstream-app
```

`docker-compose.yml` (add under `services:`):

```yaml
  seedstream:
    build:
      context: ./seedstream-app
      dockerfile: Dockerfile.source
    image: seedstream:local
    container_name: seedstream
    restart: unless-stopped
    environment:
      - TZ=Etc/UTC
    ports:
      - "7000:7000"      # addon UI + manifest
    volumes:
      - ./seedstream-data:/app/data
      - ./downloads:/data:ro   # read access to qBittorrent's downloads
    healthcheck:
      test: ["CMD-SHELL", "wget -q --spider http://127.0.0.1:7000/health || exit 1"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 15s
```

Build and start:

```bash
docker compose up -d --build
```

Open the admin UI at `http://YOUR-SERVER-IP:7000` and log in (default `admin` / `admin` — change it in Settings).

> The runtime image is a bare `alpine:latest` — it ships BusyBox, so `wget` exists but **`curl` does not**. Keep that in mind for the sidecar commands in later steps.

---

## Step 3: Configure SeedStream

In the SeedStream admin UI, go to **Settings**:

**Indexers** — add your Prowlarr Torznab source:

| Field | Value |
|-------|-------|
| Name | `Prowlarr` |
| Type | Torznab |
| URL | `http://prowlarr:9696` (container on same network) or `http://LAN-IP-OF-PROWLARR:9696` |
| API path | `/1/api` |
| API key | *(from Prowlarr → Settings → General)* |

**Torrent Clients** — add qBittorrent:

| Field | Value |
|-------|-------|
| Name | `qbit` |
| Type | qBittorrent |
| URL | `http://qbittorrent:8080` (same Docker network) or `http://LAN-IP:8080` |
| Username / Password | *(the WebUI creds from Step 1)* |
| Category | `seedstream` |
| Save path | `/data/downloads` |

> SeedStream reads the files from `/data` — the same folder qBittorrent writes to. As long as both containers mount the same host folder and the save path matches, they don't even need to reach each other over the network for streaming.

**Base URL** — set it to wherever SeedStream is reachable from your player:

- LAN only: `http://YOUR-SERVER-IP:7000`
- Public (later): `https://seedstream.YOURDOMAIN`

**Streams** — under your default stream, note the addon URL:
```
http://YOUR-SERVER-IP:7000/<token>/manifest.json
```

---

## Step 4: Install It In Stremio

**Directly:** open Stremio → **Addons** (puzzle piece) → **Install from URL** → paste the manifest URL from Step 3.

**Or through AIOStreams** (recommended — gives you filtering/sorting across addons):

1. Open your AIOStreams UI
2. Go to **Addons** → add the **SeedStream preset** (per the [README](../README.md)) and paste the manifest URL
3. Save

Now when you search something in Stremio, SeedStream's releases appear alongside your other sources — pick the SeedStream one and playback starts within seconds while the torrent downloads.

> Your player must be able to reach `YOUR-SERVER-IP:7000`. On the same LAN that's automatic; over the internet you need the exposed setup below (or a VPN like Tailscale).

---

## Step 5: Disk Guard (recommended)

The disk guard is integrated into Cerberus. Set **Settings → Advanced → Disk guard threshold (%)** to `85` (or another value) to pause SeedStream torrents when the filesystem containing a torrent client's local `save_path` reaches that usage. Set it to `0` to disable it.

Cerberus checks the filesystem during its normal watchdog pass, pauses torrents without deleting data, and does not immediately resume them while disk pressure remains. Normal watchdog behavior returns after usage drops five percentage points below the threshold.

The guard can inspect only paths mounted on the SeedStream host. With a remote seedbox, configure a local download mount as the torrent client's `save_path`; a remote-only path cannot be measured from this process.

---

## Step 6 (Optional): Expose It Publicly

Running locally is fine for your own network. To stream from anywhere — or share with friends — put SeedStream behind a reverse proxy with HTTPS:

### 6a. Add a Traefik + HTTPS router

If you use the [docker-compose-template](https://github.com/MarshmellowXD/docker-compose-template) (Traefik, Tinyauth, Cloudflare DDNS), your SeedStream service gets these labels instead of `ports:`:

```yaml
    expose:
      - 7000
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.seedstream.rule=Host(`${SEEDSTREAM_HOSTNAME?}`)"
      - "traefik.http.routers.seedstream.entrypoints=websecure"
      - "traefik.http.routers.seedstream.tls.certresolver=letsencrypt"
      - "traefik.http.routers.seedstream.middlewares=tinyauth@docker"
      - "traefik.http.services.seedstream.loadbalancer.server.port=7000"
```

1. Add `SEEDSTREAM_HOSTNAME=seedstream.YOURDOMAIN` to your root `.env`
2. Add `seedstream.YOURDOMAIN` to `apps/cloudflare-ddns/compose.yaml` `DOMAINS`
3. Set SeedStream's **Base URL** to `https://seedstream.YOURDOMAIN` (Settings)
4. Your new addon URL becomes `https://seedstream.YOURDOMAIN/<token>/manifest.json`

### 6b. (Optional) Route qBittorrent through a VPN

To keep your IP hidden from trackers, run qBittorrent inside Gluetun's network instead of publishing port 8080:

```yaml
  qbittorrent:
    network_mode: "service:gluetun"
    depends_on:
      gluetun:
        condition: service_started
```

Gluetun is then the one with Traefik labels (qBittorrent has no DNS name of its own):

```yaml
  gluetun:
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.qbittorrent.rule=Host(`${QBIT_HOSTNAME?}`)"
      - "traefik.http.routers.qbittorrent.entrypoints=websecure"
      - "traefik.http.routers.qbittorrent.tls.certresolver=letsencrypt"
      - "traefik.http.routers.qbittorrent.middlewares=tinyauth@docker"
      - "traefik.http.services.qbittorrent.loadbalancer.server.port=8080"
```

Update the SeedStream torrent client URL to `http://gluetun:8080` (same namespace). Disk-guard commands then run via `docker exec gluetun wget ...` since qBittorrent isn't reachable on the host.

---

## Verification

1. In Stremio, search for a well-seeded movie and pick the SeedStream stream
2. Playback should start within a few seconds — **even though the file is still downloading**
3. Check it's really seeding:
   ```bash
   docker compose exec qbittorrent sh -c "wget -qO- http://127.0.0.1:8080/api/v2/torrents/info" | jq '.[].state'
   ```
   You should see `stalledUP` / `uploading` after the movie finishes
4. Check the watchdog when it fires: `tail -f /opt/seedstream/disk-guard.log`

---

## Troubleshooting

- **Playback hangs at the start** — the first pieces are still downloading. Wait a few seconds (sequential mode makes this short) or pick a release with more seeds.
- **Playback starts but dies after a few seconds** — the player is probing the *end* of the file for metadata. This was the known stock-SeedStream bug; build from this repo (piece-aware streaming) as in Step 2.
- **No SeedStream results in Stremio** — check the indexer in SeedStream Settings, and that Prowlarr's API key matches. SeedStream only shows releases it can actually grab.
- **Stream won't open from your phone/another network** — the player must reach `:7000`. LAN-only setups can't stream remotely; use the exposed setup or a VPN like Tailscale.
- **Disk fills up** — check the watchdog: `tail -f /opt/seedstream/disk-guard.log`. You can also lower the 85% threshold.
- **Torrents never start** — confirm qBittorrent is up (`docker compose ps`) and the torrent client credentials in SeedStream Settings are right.
