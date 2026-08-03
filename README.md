# SeedStream



SeedStream is a self-hosted torrent streaming addon for [Stremio](https://www.stremio.com/) and [AIOStreams](https://github.com/Viren070/AIOStreams). It searches your own Torznab trackers and streams the results from a Docker container running on your seedbox or server.

Torrents are handed to a qBittorrent running on your seedbox, which downloads sequentially for instant playback and **keeps seeding** so your private-tracker ratio stays healthy. SeedStream never seeds or discards data itself.

---

## Install

You need a seedbox or server with **Docker** and **Docker Compose**. Then:

```bash
git clone https://github.com/kosch43/SeedStream.git
cd SeedStream
docker compose up -d --build
```

The image builds from source on first run (a few minutes), then starts automatically and restarts on reboot.

Open **`http://<your-server-ip>:7000`** in a browser. Default login is **`admin` / `admin`** — you'll be asked to change the password on first sign-in.

To update later:

```bash
git pull
docker compose up -d --build
```

Your configuration and login live in the `./data` folder (mounted into the container), so they survive updates.

> SeedStream serves torrent files straight from the folder your seedbox qBittorrent downloads to, so it needs to read that folder. Uncomment and edit the downloads volume in `docker-compose.yml` to mount it (read-only) at the **same path** you'll enter under Settings → Torrent Clients.

---

## First-time setup

After logging in:

1. **Settings → Network** — set your addon **Base URL** and **Port** (how clients reach this server). Examples: `http://192.168.1.50:7000`, `http://seedstream.example.com:7000`, or `https://seedstream.example.com`. See [HTTPS](#https) below.
2. **Settings → Torrent Trackers** — add your Torznab trackers (URL + API key), for example from Prowlarr or Jackett. Set per-tracker Hit & Run rules here if your tracker enforces them.
3. **Settings → Torrent Clients** — add your seedbox qBittorrent (WebUI URL, login, category, and the save path SeedStream can read). If qBittorrent runs on a different host, also set the remote path so SeedStream can translate it to your local mount.
4. **Settings → Search** — create at least one movie and/or TV search request.
5. **Streams** — create a stream, choose which trackers/clients it uses, then copy its manifest URL.
6. Add that manifest URL to your **Stremio** client or **AIOStreams**.

## HTTPS

Streams are served over whatever scheme you put in **Base URL**, and SeedStream speaks plain HTTP by default. Over the open internet that means the traffic — including your stream token — is readable in transit, so it is worth turning on encryption.

There are two ways to get it:

**Behind a reverse proxy.** If Caddy, nginx, Traefik or Cloudflare already terminates TLS for you, leave HTTPS off and simply set Base URL to your `https://` address. Nothing else changes.

**Directly, in Settings → Network → HTTPS.** Turn on *Serve HTTPS directly* and give SeedStream a certificate, then set Base URL to `https://`. Either:

- **Automatic certificate domain** — requests a free Let's Encrypt certificate. Let's Encrypt validates only on port 80 or 443, so publishing just `7000:7000` is not enough: add `"80:80"` to the `ports:` list in `docker-compose.yml` and point the domain's DNS at this host, or issuance will never complete. Certificates are cached under your data directory, so they survive restarts and are not re-requested on every boot.
- **Certificate file / key file** — PEM paths for a certificate you already have, e.g. from certbot or a Cloudflare origin certificate.

A restart is required for certificate changes to take effect. If the Base URL scheme and the HTTPS setting disagree, SeedStream logs a warning at startup, because that combination hands Stremio links it cannot fetch.

> **Stremio Web needs HTTPS.** The desktop and Android apps play plain-HTTP streams happily. `web.stremio.com` is itself served over HTTPS, so browsers block an `http://` stream as mixed content — use HTTPS if you watch there.

### Force a password reset on next startup

```env
ADMIN_FORCE_PASSWORD_RESET=true
```

Set this, restart, change the password, then remove it (otherwise it keeps prompting on every startup).

---

## How streaming works

Torrents download to your seedbox qBittorrent with sequential + first/last-piece priority, so the start of the file is ready fast. SeedStream streams that file with HTTP range support while qBittorrent keeps seeding it. This is what protects your ratio on private trackers.

Seeking into a part of the file that hasn't downloaded yet waits for those bytes rather than failing, so scrubbing ahead works while the torrent is still in progress.

---

## Stream model

SeedStream separates global configuration from per-stream behavior:

- **Settings → Torrent Trackers / Torrent Clients / Search** store resources globally.
- **Streams** choose which of those resources are active for a specific manifest token.

Each stream also controls its search pipeline: tracker mode (`Combine` / `Failover`), search request mode (`Combine` / `First hit`), results mode, and internal failover. This lets one SeedStream instance serve multiple manifests with different behavior.

---

## Using with AIOStreams

[AIOStreams](https://github.com/Viren070/AIOStreams) consolidates multiple Stremio addons into one super-addon with advanced filtering, sorting, and formatting.

1. In SeedStream, create or choose the stream you want AIOStreams to use, and set its **Filter/Sorting mode to `AIOStreams`** (Streams → edit stream → General). This matters: without it SeedStream returns a single combined entry, so AIOStreams has nothing to filter or sort. With it, every torrent comes back as its own entry.
2. Copy that stream's manifest URL (e.g. `https://your-host:7000/<token>/manifest.json`).
3. In AIOStreams, use the **StreamNZB preset** and paste the manifest URL. There is no SeedStream preset — this is a StreamNZB fork and the addon interface is unchanged, so that preset builds URLs in exactly the shape SeedStream serves. If the preset rejects it, add it as a custom/manual addon instead; the result is the same.
4. No debrid or torrent client needed in AIOStreams — SeedStream handles tracker search, the seedbox handoff, and streaming internally.
5. Configure filtering/sorting/formatting in the AIOStreams UI.

AIOStreams has to be able to reach SeedStream over the network: a cloud-hosted AIOStreams cannot fetch a manifest from a SeedStream on your LAN. If AIOStreams is served over HTTPS, serve SeedStream over HTTPS too — see [HTTPS](#https) — otherwise the stream URLs can be blocked as mixed content.

---

## Cerberus torrent health

Cerberus is SeedStream's built-in torrent watchdog. It records which info-hash served which content, watches your seedbox for stalled torrents, and keeps a local blocklist so a torrent that already failed is not offered again — at search time and at playback time.

Torrents with downloaded data are never deleted, only re-announced, so partial downloads can't turn into a Hit & Run on a private tracker. Per-tracker seeding time and ratio rules are configured under **Settings → Torrent Trackers**.

Cerberus runs fully locally by default. **Settings → Advanced** can optionally point it at a central Cerberus server to share failure reports and pull a community blocklist.

---

## Troubleshooting

Open a [GitHub issue](https://github.com/kosch43/SeedStream/issues). Include downloaded logs when relevant. Sensitive data is auto-redacted — double-check before posting.

---

## Credits

SeedStream is built and maintained by **Kosch**. Licensed under GPL-3.0 — see [LICENSE](LICENSE).
