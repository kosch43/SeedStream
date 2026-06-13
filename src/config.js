'use strict';
require('dotenv').config();

function clean(v) { return (v || '').replace(/\/$/, ''); }

const cfg = {
  port: parseInt(process.env.PORT || '7700', 10),

  // Public HTTPS URL of THIS addon (no trailing slash). Stremio reaches it here.
  addonBaseUrl: clean(process.env.ADDON_BASE_URL),

  // Optional secret path prefix gating the WHOLE addon. If set, the addon lives
  // at /<secret>/manifest.json and every route requires that prefix. This keeps
  // randoms who find your URL from using your box. Leave blank to disable.
  addonSecret: (process.env.ADDON_SECRET || '').trim(),

  prowlarr: {
    url: clean(process.env.PROWLARR_URL),
    apiKey: process.env.PROWLARR_API_KEY || '',
    // Prowlarr indexer IDs for TORRENT searches (comma-separated).
    indexerIds: (process.env.PROWLARR_INDEXER_IDS || '')
      .split(',').map(s => s.trim()).filter(Boolean),
  },

  // Optional TMDB API key. If set, enables alternative-title (AKA) search and
  // matching for foreign/localized releases. If absent, falls back to Cinemeta.
  tmdb: {
    apiKey: (process.env.TMDB_API_KEY || '').trim(),
  },

  // Optional TVDB v4 API key (+ subscriber PIN for user-supported keys). Enables
  // absolute-episode mapping for anime and extra title aliases for TV/anime.
  tvdb: {
    apiKey: (process.env.TVDB_API_KEY || '').trim(),
    pin: (process.env.TVDB_PIN || '').trim(),
  },

  // Optional AniList token. AniList reads work without it, so this key acts as the
  // opt-in for anime title-synonym enrichment (romaji/native) and raises rate limits.
  anilist: {
    apiKey: (process.env.ANILIST_API_KEY || '').trim(),
  },

  // ---- Torrent downloader (qBittorrent) ----
  qbit: {
    url: clean(process.env.QBIT_URL),
    username: process.env.QBIT_USERNAME || '',
    password: process.env.QBIT_PASSWORD || '',
    category: process.env.QBIT_CATEGORY || 'seedstream',
    savePath: process.env.QBIT_SAVE_PATH || '',
  },

  // nginx (or seedbox personal web server) exposing QBIT_SAVE_PATH with ranges.
  fileServerBaseUrl: clean(process.env.FILE_SERVER_BASE_URL),

  // ---- Usenet downloader (SABnzbd) ----
  sabnzbd: {
    url: clean(process.env.SABNZBD_URL),
    apiKey: process.env.SABNZBD_API_KEY || '',
    // Absolute path SABnzbd writes completed downloads to (same path seen inside
    // this container). Must contain the category subfolder if SABNZBD_CATEGORY is set.
    savePath: process.env.SABNZBD_COMPLETE_PATH || '',
    category: process.env.SABNZBD_CATEGORY || 'seedstream',
  },

  // ---- Usenet search (NZB via Prowlarr) ----
  nzb: {
    // Prowlarr indexer IDs for USENET (NZB) searches (comma-separated).
    // Set this to enable NZB results alongside torrents. Leave blank to disable.
    indexerIds: (process.env.NZB_INDEXER_IDS || '')
      .split(',').map(s => s.trim()).filter(Boolean),
    // How long to wait for SABnzbd to finish before returning 504. Usenet is
    // usually fast (seconds for small files, a few minutes for large ones).
    timeoutSec: parseInt(process.env.NZB_TIMEOUT_SEC || '600', 10),
  },

  bufferMb: parseInt(process.env.BUFFER_MB || '32', 10),
  bufferTimeoutSec: parseInt(process.env.BUFFER_TIMEOUT_SEC || '90', 10),
  maxResults: parseInt(process.env.MAX_RESULTS || '30', 10),
  searchCacheTtlSec: parseInt(process.env.SEARCH_CACHE_TTL_SEC || '600', 10),
  maxConcurrentPerBox: parseInt(process.env.MAX_CONCURRENT_PER_BOX || '3', 10),

  // ---- matching / filtering ----
  minSeeders: parseInt(process.env.MIN_SEEDERS || '0', 10),
  // Allowlist of resolutions to keep, e.g. "2160p,1080p". Empty = keep all.
  resolutions: (process.env.RESOLUTIONS || '')
    .split(',').map(s => s.trim().toLowerCase()).filter(Boolean),
  // Boost a resolution to the top of results, e.g. "1080p". Empty = quality order.
  preferredResolution: (process.env.PREFERRED_RESOLUTION || '').trim().toLowerCase(),
  // Drop cam/TS/screener releases (default true).
  excludeCam: (process.env.EXCLUDE_CAM || 'true').toLowerCase() !== 'false',

  videoExts: ['mkv', 'mp4', 'avi', 'm4v', 'mov', 'wmv', 'flv', 'webm', 'ts', 'm2ts'],

  // ---- disk cache / retention ----
  cache: {
    maxGb: parseFloat(process.env.CACHE_MAX_GB || '0'),
    maxAgeDays: parseFloat(process.env.CACHE_MAX_AGE_DAYS || '0'),
    minSeedHours: parseFloat(process.env.MIN_SEED_HOURS || '72'),
    sweepMinutes: parseInt(process.env.CACHE_SWEEP_MINUTES || '15', 10),
  },
};

// Returns a list of missing required settings (empty = all good).
function validate() {
  const missing = [];
  const torrentOk = cfg.qbit.url && cfg.qbit.username && cfg.qbit.password && cfg.qbit.savePath;
  const nzbOk = cfg.sabnzbd.url && cfg.sabnzbd.apiKey && cfg.sabnzbd.savePath && cfg.nzb.indexerIds.length;

  const checks = {
    ADDON_BASE_URL: cfg.addonBaseUrl,
    PROWLARR_URL: cfg.prowlarr.url,
    PROWLARR_API_KEY: cfg.prowlarr.apiKey,
  };
  for (const [k, v] of Object.entries(checks)) if (!v) missing.push(k);

  if (!torrentOk && !nzbOk) {
    // Need at least one download backend fully configured.
    if (!cfg.qbit.url) missing.push('QBIT_URL (or configure SABnzbd for NZB-only mode)');
    if (!cfg.qbit.username) missing.push('QBIT_USERNAME');
    if (!cfg.qbit.password) missing.push('QBIT_PASSWORD');
    if (!cfg.qbit.savePath) missing.push('QBIT_SAVE_PATH');
  }
  return missing;
}

module.exports = { cfg, validate };
