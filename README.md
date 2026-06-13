# SeedStream

SeedStream is a self-hosted streaming addon for [Stremio](https://www.stremio.com/) and [AIOStreams](https://github.com/Viren070/AIOStreams). It searches your own indexers and streams the results — **both Usenet (NZB) and torrents** — from one small service you run on your own server.

- **Usenet** streams on-the-fly directly from your providers (no full download needed).
- **Torrents** are handed to a qBittorrent on your seedbox, which downloads sequentially for instant playback and **keeps seeding** so your private-tracker ratio stays healthy. SeedStream never seeds or discards data itself.

One binary provides the web UI, stream management, NNTP proxy, and playback pipeline.

---

## Install (Docker — easiest)

You need a server with **Docker** and **Docker Compose**. Then:

```bash
git clone https://github.com/kosch43/SeedStream.git
cd SeedStream
docker compose up -d --build
```

That's it. The image builds from source on first run (a few minutes), then starts automatically and restarts on reboot.

Open **`http://<your-server-ip>:7000`** in a browser. Default login is **`admin` / `admin`** — you'll be asked to change the password on first sign-in.

To update later:

```bash
git pull
docker compose up -d --build
```

Your configuration and login live in the `./data` folder (mounted into the container), so they survive updates.

> **Streaming torrents?** SeedStream serves torrent files straight from the folder your seedbox qBittorrent downloads to, so it needs to read that folder. Uncomment and edit the downloads volume in `docker-compose.yml` to mount it (read-only) at the **same path** you'll enter under Settings → Torrent Clients.

### Running the binary instead of Docker

Prefer no Docker? Build it directly (needs Go 1.25+ and Node 20+):

```bash
cd frontend && npm ci && npm run build && cd ..
mkdir -p pkg/server/web/static && cp -r frontend/dist/* pkg/server/web/static/
go build -o seedstream ./cmd/seedstream/
./seedstream
```

Configuration can also be supplied via environment variables — see `.env.example`.

---

## First-time setup

After logging in:

1. **Settings → Network** — set your addon **Base URL** and **Port** (how clients reach this server). Examples: `http://192.168.1.50:7000`, `http://seedstream.example.com:7000`, or `https://seedstream.example.com` behind a reverse proxy.
2. **Settings → Indexers** — add your indexers (URL + API key). A Prowlarr/Jackett **Torznab** feed gives you torrents; a **Newznab** feed gives you Usenet. SeedStream auto-detects which is which.
3. **For Usenet:** **Settings → Providers** — add at least one Usenet provider (host, port, login, connections).
4. **For torrents:** **Settings → Torrent Clients** — add your seedbox qBittorrent (WebUI URL, login, category, and the save path SeedStream can read).
5. **Settings → Search** — create at least one movie and/or TV search request.
6. **Streams** — create a stream, choose which indexers/providers/clients it uses, then copy its manifest URL.
7. Add that manifest URL to your **Stremio** client or **AIOStreams**.

### Force a password reset on next startup

```env
ADMIN_FORCE_PASSWORD_RESET=true
```

Set this, restart, change the password, then remove it (otherwise it keeps prompting on every startup).

---

## How streaming works

**Torrents** download to your seedbox qBittorrent with sequential + first/last-piece priority, so the start of the file is ready fast. SeedStream streams that file with HTTP range support while qBittorrent keeps seeding it. This is what protects your ratio on private trackers.

**Usenet** streams on-the-fly from archive segments. That only works when the inner file is stored **uncompressed**:

- **Compressed RAR** won't play — RAR must be STORE (no compression).
- **Compressed 7z** won't play — only uncompressed (copy/store) content is streamable.

---

## Stream model

SeedStream separates global configuration from per-stream behavior:

- **Settings → Providers / Indexers / Torrent Clients / Search** store resources globally.
- **Streams** choose which of those resources are active for a specific manifest token.

Each stream also controls its search pipeline: indexer mode (`Combine` / `Failover`), search request mode (`Combine` / `First hit`), results mode, internal failover, and AvailNZB behavior. This lets one SeedStream instance serve multiple manifests with different behavior.

---

## Using with AIOStreams

[AIOStreams](https://github.com/Viren070/AIOStreams) consolidates multiple Stremio addons into one super-addon with advanced filtering, sorting, and formatting.

1. In SeedStream, create or choose the stream you want AIOStreams to use.
2. Copy that stream's manifest URL (e.g. `https://your-host:7000/<token>/manifest.json`).
3. In AIOStreams, add the SeedStream preset and paste the manifest URL.
4. No Usenet service needed in AIOStreams — SeedStream handles providers, NZB fetching, and streaming internally.
5. Configure filtering/sorting/formatting in the AIOStreams UI.

---

## AvailNZB

[AvailNZB](https://check.snzb.stream) is a community Usenet-availability database. SeedStream builds an ordered play list from indexer search plus AvailNZB (skipping releases reported bad), then tries on play and reports success/failure so the shared DB stays current. It's controlled globally in **Settings → Advanced** and per stream in **Streams → General**, and only used when both allow it.

---

## Troubleshooting

Open a [GitHub issue](https://github.com/kosch43/SeedStream/issues). Include downloaded logs when relevant, and the copied bad-match report from **NZB History** for wrong/poor release matches. Sensitive data is auto-redacted — double-check before posting.

---

## Credits

SeedStream is a fork of **[StreamNZB](https://github.com/Gaisberg/streamnzb)** by Gaisberg (GPL-3.0), extended with torrent streaming. Licensed under GPL-3.0 — see [LICENSE](LICENSE).

- [javi11](https://github.com/javi11) for Go-based RAR and 7z streaming ([altmount](https://github.com/javi11/altmount)).
