# Changelog

## 1.0.0 (2026-06-13)

First release of SeedStream by Kosch — a self-hosted Stremio addon that streams
both Usenet (NZB) and torrents from your own indexers, built on the open-source
StreamNZB project (GPL-3.0).

### Features

* **torrents:** stream torrents via a seedbox qBittorrent — sequential download
  for instant playback while the torrent keeps seeding for private-tracker
  ratio (SeedStream never seeds or discards data itself)
* **torrents:** Torznab/Prowlarr torrent indexers work through the existing
  Indexers UI; results are auto-detected and labelled as torrents
* **settings:** new Torrent Clients tab to register seedbox qBittorrent
  instances (URL, credentials, category, save path)
* **usenet:** on-the-fly NZB streaming, NNTP proxy, and stream management
* **install:** one-command self-hosted setup via `docker compose up -d --build`
