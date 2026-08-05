# Changelog

## [1.1.0](https://github.com/kosch43/SeedStream/compare/v1.0.0...v1.1.0) (2026-08-05)


### Features

* built-in Torznab tracker manager — no Prowlarr required ([1c0952f](https://github.com/kosch43/SeedStream/commit/1c0952f7649ef6e8ecf3878a46b5bdf30e47414a))
* make torrent buffer bytes and prepare timeout configurable ([8528cab](https://github.com/kosch43/SeedStream/commit/8528cab23bbcfe70d3fe32c55cff7ba69938ab06))
* per-stream Prowlarr — each member's searches use their own indexer ([88f3c12](https://github.com/kosch43/SeedStream/commit/88f3c125eeef6a8526edce0da57db8cb3c0263e0))
* per-stream qBittorrent client — each stream uses its own seedbox ([12a01c7](https://github.com/kosch43/SeedStream/commit/12a01c7ea1657db91e01cd36445c39bf0b076ce5))
* per-user member logins — members configure their own Prowlarr and qBittorrent ([8ae0bc9](https://github.com/kosch43/SeedStream/commit/8ae0bc9fb4f5454a64fa08da31f630fde64f9ec1))
* rebuild Usenet statistics on NZBHydra2-style event model ([ad90003](https://github.com/kosch43/SeedStream/commit/ad9000330a85424246124291436b827756193e2f))
* separate Torznab tracker stats from Usenet indexer stats ([3ee8003](https://github.com/kosch43/SeedStream/commit/3ee800394e59ccdebca10f8d97d1b86d1594361d))


### Bug Fixes

* address 4 GPT-identified bugs in auth, migration, and password hashing ([373bff0](https://github.com/kosch43/SeedStream/commit/373bff0bd9a24d6be2f5f2e738fd8f539a586127))
* AuthenticateToken only accepts admin token ([b1569b9](https://github.com/kosch43/SeedStream/commit/b1569b9f191e4b3478defa75656b4966a9d8f257))
* **cerberus:** normalize base32/hex info_hash mismatches ([187cd73](https://github.com/kosch43/SeedStream/commit/187cd73c4cc4f6e2eb4edc4090d15262f32e5f2f))
* prevent cross-protocol dedup collapsing NZB and torrent results ([8bda79f](https://github.com/kosch43/SeedStream/commit/8bda79fd7150ffab62b0d5a8f882042e72fe76fc))
* resolve 10 bugs found in full-service review ([7ff5862](https://github.com/kosch43/SeedStream/commit/7ff5862ec34f2b8a07c216a692ecf898de39b45d))
* unique_downloads must require success=1 in download aggregation ([305941c](https://github.com/kosch43/SeedStream/commit/305941cbb1a0a7d1f739daab4c4598cdc6b48693))
* use passkey terminology for trackers, drop dead username/password ([57075f0](https://github.com/kosch43/SeedStream/commit/57075f013efd69e294853a7c4099fe7395aba614))

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
