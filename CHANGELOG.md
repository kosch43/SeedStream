# Changelog

## 1.0.0 (2026-06-13)

First release of SeedStream by Kosch — a self-hosted Stremio addon that streams
torrents from your own trackers (GPL-3.0).

### Features

* **torrents:** stream torrents via a seedbox qBittorrent — sequential download
  for instant playback while the torrent keeps seeding for private-tracker
  ratio (SeedStream never seeds or discards data itself)
* **torrents:** Torznab/Prowlarr torrent trackers configured in the Torrent
  Trackers UI
* **settings:** new Torrent Clients tab to register seedbox qBittorrent
  instances (URL, credentials, category, save path)
* **install:** one-command self-hosted setup via `docker compose up -d --build`
