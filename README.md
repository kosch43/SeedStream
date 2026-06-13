# SeedStream

A **self-hosted Stremio addon** that streams from **private trackers** using your own **Prowlarr** + **qBittorrent** — no debrid required. Designed to run on a **single VPS**: it includes a **built-in file server**, so there's no separate nginx to configure. Works as a custom addon in **AIOStreams**, and in **Nuvio** / Stremio. Each person runs their own instance with their own credentials; nothing is shared or centrally hosted.

It separates **search** from **download**:

1. **Search** — Stremio asks for streams → addon queries **Prowlarr** → returns releases.
2. **Play** — you pick one → addon adds it to **qBittorrent** (sequential + first/last piece priority), waits for a small head buffer, then streams it from its **built-in file server** with **HTTP range** support.

## Architecture (single VPS)

```
Stremio / Nuvio / AIOStreams
        |  /stream      |  /play -> /files
        v               v
   SeedStream  --------------------> Prowlarr      (search your trackers)
        |  (built-in file server,    qBittorrent   (download + seed; reuse if present)
        |   serves qBit's dir
        |   with HTTP ranges)
        v
   Your player streams + seeks
```

Everything is one container talking to two others on the same box. The only thing
that must be shared is read access to qBit's download directory, which the
included compose handles for you.

## Quick start (fresh VPS — turnkey)

Brings up Prowlarr + qBittorrent + SeedStream together.

```bash
git clone https://github.com/youruser/seedstream.git
cd seedstream
cp .env.example .env

# 1. Start the stack
docker compose -f docker-compose.full.yml up -d --build

# 2. Configure the two services (one time):
#    - Prowlarr  http://<vps>:9696  -> add your indexers, copy the API key
#    - qBittorrent http://<vps>:8080 -> log in, set a permanent password
#      (linuxserver qBit prints a temporary password: `docker logs seedstream-qbit`)

# 3. Put the API key + qBit password in .env, plus ADDON_BASE_URL and ADDON_SECRET.
#    Leave FILE_SERVER_BASE_URL blank (use the built-in server).

# 4. Apply and verify
docker compose -f docker-compose.full.yml up -d
curl https://stream.yourdomain.com/<secret>/check
```

`.env` is pre-pointed at the bundled services (`http://prowlarr:9696`,
`http://qbittorrent:8080`, `QBIT_SAVE_PATH=/downloads/seedstream`), so you only
fill in credentials + your public URL.

## Quick start (you already run Prowlarr + qBittorrent)

```bash
cp .env.example .env
# Point PROWLARR_URL / QBIT_URL at your existing services.
# Set QBIT_SAVE_PATH to where qBit saves, and SEEDSTREAM_DOWNLOADS to its parent
# (mounted read-only so SeedStream can serve files). Leave FILE_SERVER_BASE_URL blank.
docker compose up -d --build
curl https://stream.yourdomain.com/<secret>/check
```

| Var | What |
|-----|------|
| `ADDON_BASE_URL` | Public HTTPS URL of this instance |
| `ADDON_SECRET` | Optional secret path gating the whole addon (recommended) |
| `PROWLARR_URL` / `PROWLARR_API_KEY` | Your Prowlarr |
| `QBIT_URL` / `QBIT_USERNAME` / `QBIT_PASSWORD` | Your qBittorrent |
| `QBIT_SAVE_PATH` | Where qBit saves; SeedStream serves files from here |
| `FILE_SERVER_BASE_URL` | **Leave blank** to use the built-in server |
| `SEEDSTREAM_DOWNLOADS` | (standalone compose) qBit download dir to mount read-only |

## Verify your setup

The startup log validates Prowlarr + qBit and reports the file server mode.
You can also hit:

```
https://stream.yourdomain.com/<secret>/check
```

It returns the status of Prowlarr, qBittorrent, and the file server, so a bad
credential or path shows up immediately. Fix anything that says `FAIL` first.

## Install the addon

In Stremio / Nuvio, or as a custom addon in **AIOStreams**, add:

```
https://stream.yourdomain.com/<secret>/manifest.json
```

(Drop `/<secret>` if you left `ADDON_SECRET` blank.) AIOStreams only *consumes*
the addon — all the work runs on your instance.

## Reverse proxy

Point your TLS reverse proxy (Caddy/nginx/Traefik) at the container's `:7700`
and set `ADDON_BASE_URL` to the public URL. Players reject plain HTTP.

## Progressive playback — what you get

Sequential download + the range-serving file server give **fast-start
progressive playback**: it begins after a small buffer, and seeking *backward*
into downloaded parts works. Seeking *forward* into a not-yet-downloaded region
waits for that part to arrive. On a VPS with well-seeded content this is rarely
felt — the file finishes in seconds-to-a-minute, after which seek-anywhere just
works. (True seek-anywhere from byte zero needs a piece-prioritizing engine like
TorrServer, which won't seed for your tracker ratio — so qBit-sequential is the
right trade here.)

## Speed features

- **Torrent reuse / fast path** — if a release is already on the box and past the
  buffer threshold, `/play` redirects **instantly**. Big win for repeat plays.
- **Search caching** — identical Prowlarr queries are cached, which also reduces
  load on your trackers.
- **Keep-warm seeding** — torrents stay seeding (good for ratio / H&R), so
  they're instantly available next time.
- **Bounded disk cache** — already-downloaded torrents are your "instant replay"
  cache, kept fast but capped by `CACHE_MAX_GB` / `CACHE_MAX_AGE_DAYS`, evicting
  the least-recently-played first (like rclone's `vfs-cache-max-size`/`-age`).
  Eviction is **ratio-safe**: a torrent is never deleted until it has met
  `MIN_SEED_HOURS` of seed time, so the cache can't cause a hit-and-run penalty.
- **Per-box concurrency cap** so simultaneous streams don't pin the box.

## Scaling / external file server

The built-in server is fine for personal and small use. If you expect heavy or
many concurrent streams, set `FILE_SERVER_BASE_URL` to put **nginx** in front of
the same directory (see `nginx.example.conf`). The play redirect then targets
nginx instead of the built-in server.

## Media matching

Search results are parsed and filtered so you get the right release, ranked by quality — not just a raw seeder-sorted list. For each release SeedStream:

- **Parses** the release name into resolution, source (REMUX/BluRay/WEB-DL/…), video codec, HDR/Dolby Vision, audio (Atmos/DTS-HD/…), language, release group, and season/episode.
- **Verifies the match** against the request: movies are checked by title **and year** (so *The Mummy* 1999 won't return the 2017 film), and episodes are checked by season + episode — including **season packs** and episode ranges that cover the requested episode. Wrong years, wrong seasons, and wrong episodes are dropped.
- **Ranks by quality**, then health: resolution first, then source/HDR, then seeders. CAM/TS/screener rips are dropped by default.
- **Dedupes** identical releases returned by multiple indexers.
- **Picks the right file**: when you pick a season pack, the requested episode is served — not the largest file in the pack.
- **Labels cleanly**: each stream shows resolution + HDR as the badge and source • codec • audio • group on the detail line, instead of the raw scene name only.

Tune it with `MIN_SEEDERS`, `RESOLUTIONS` (allowlist), `PREFERRED_RESOLUTION` (e.g. cap to `1080p`), and `EXCLUDE_CAM` — see `.env.example`.

**ID matching (automatic).** Many indexers attach an IMDb ID to each result. SeedStream uses the IMDb ID Stremio already provides to (a) drop results whose ID doesn't match and confirm those that do, and (b) issue a best-effort ID-based search alongside the text search for indexers that support it. No configuration needed.

**TMDB (optional) — better foreign/localized matching.** Set `TMDB_API_KEY` to also search and match on a title's alternative names. Without it, a release named under its original-language title (e.g. *Parasite* released as *Gisaengchung*) gets dropped on the title check; with it, SeedStream searches the original title too and accepts releases matching any known AKA. Falls back to Cinemeta when the key is absent or a lookup fails.

**TVDB + AniList (optional) — anime and TV.** Anime release groups number episodes by *absolute* count (e.g. `Attack on Titan - 26`), which doesn't match Stremio's season/episode. Set `TVDB_API_KEY` and SeedStream maps the requested season/episode to its absolute number (the same approach Sonarr uses) — so anime episodes get **verified** instead of shown as "unverified", and season packs serve the correct absolute episode. TVDB also adds series title aliases, which helps regular TV matching too. Set `ANILIST_API_KEY` to also pull anime title synonyms (romaji/native). Both are optional and degrade gracefully.

Remaining anime caveat: when no episode-number source matches (e.g. a release with no parseable number, or a show whose IMDb and TVDB season numbering disagree), the release is still shown as **(unverified)** rather than dropped. The numbering schemes across IMDb/TVDB/release groups aren't perfectly consistent, so this improves anime matching substantially but isn't bulletproof.

## Notes & limitations

- One video file is served per stream — the largest for movies, or the requested
  episode for season packs (see Media matching above).
- `QBIT_SAVE_PATH` must be an absolute path the SeedStream container can read.
- **qBit must be able to reach the download URL.** Private trackers work as long as
  qBit can fetch the magnet/.torrent Prowlarr returns. If Prowlarr's download URLs
  point at its own base URL (e.g. `localhost:9696`), a qBit running in a separate
  container can't reach that — set Prowlarr's URL base to something both containers
  can resolve (the service name, or the host's LAN IP).
- **Respect your trackers' rules.** This downloads to *your* box and seeds there
  like any normal client; it doesn't bypass ratio, H&R, or freeleech rules.

## Roadmap ideas

- Per-cour anime numbering (AniDB-style) for shows where IMDb/TVDB seasons diverge
- Optional debrid fast-path (check cache, fall back to qBit)
- Optional debrid fast-path (check cache, fall back to qBit)

## License

MIT
