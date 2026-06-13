'use strict';
const axios = require('axios');
const { cfg } = require('./config');
const { searchCache } = require('./cache');

const client = axios.create({
  baseURL: cfg.prowlarr.url,
  headers: { 'X-Api-Key': cfg.prowlarr.apiKey },
  timeout: 30000,
});

function catsFor(type) { return type === 'series' ? [5000] : [2000]; }

// Generic Prowlarr search used by both torrent and NZB paths.
async function _search(query, type, torznabType, indexerIds, cachePrefix) {
  const cacheKey = `${cachePrefix}|${type}|${torznabType}|${query.toLowerCase()}`;
  const hit = searchCache.get(cacheKey);
  if (hit) return hit;

  const params = new URLSearchParams();
  params.set('query', query);
  params.set('type', torznabType);
  for (const c of catsFor(type)) params.append('categories', String(c));
  for (const id of indexerIds) params.append('indexerIds', id);
  params.set('limit', String(Math.max(cfg.maxResults * 4, 100)));

  let out = [];
  try {
    const { data } = await client.get(`/api/v1/search?${params.toString()}`);
    out = (Array.isArray(data) ? data : [])
      .map(normalize)
      .filter(r => r && (r.magnet || r.downloadUrl))
      .sort((a, b) => (b.seeders || 0) - (a.seeders || 0));
  } catch (e) {
    console.warn(`prowlarr search failed [${torznabType}] "${query}":`, e.message);
    return [];
  }

  searchCache.set(cacheKey, out, cfg.searchCacheTtlSec * 1000);
  return out;
}

// Torrent indexer search.
async function search(query, type, torznabType) {
  return _search(query, type, torznabType || 'search', cfg.prowlarr.indexerIds, 'torrent');
}

// NZB (Usenet) indexer search. Uses NZB_INDEXER_IDS from config.
async function searchNzb(query, type, torznabType) {
  if (!cfg.nzb.indexerIds.length) return [];
  return _search(query, type, torznabType || 'search', cfg.nzb.indexerIds, 'nzb');
}

// ID-based search via Prowlarr's token syntax. Best-effort.
async function searchById(imdbId, type, season, episode) {
  const num = String(imdbId || '').replace(/^tt/i, '');
  if (!num) return [];
  if (type === 'series') {
    let q = `{ImdbId:${num}}`;
    if (season != null) q += ` {Season:${season}}`;
    if (episode != null) q += ` {Episode:${episode}}`;
    return search(q, 'series', 'tvsearch');
  }
  return search(`{ImdbId:${num}}`, 'movie', 'movie');
}

// ID-based search for NZB indexers.
async function searchNzbById(imdbId, type, season, episode) {
  if (!cfg.nzb.indexerIds.length) return [];
  const num = String(imdbId || '').replace(/^tt/i, '');
  if (!num) return [];
  if (type === 'series') {
    let q = `{ImdbId:${num}}`;
    if (season != null) q += ` {Season:${season}}`;
    if (episode != null) q += ` {Episode:${episode}}`;
    return searchNzb(q, 'series', 'tvsearch');
  }
  return searchNzb(`{ImdbId:${num}}`, 'movie', 'movie');
}

function normalize(r) {
  const title = r.title || r.sortTitle;
  if (!title) return null;
  // Detect source type from Prowlarr's protocol field; fall back to presence of seeders.
  const sourceType = r.protocol === 'usenet' ? 'nzb'
    : (r.protocol === 'torrent' || r.seeders != null) ? 'torrent'
      : 'torrent';
  return {
    title,
    size: r.size || 0,
    seeders: r.seeders != null ? r.seeders : 0,
    leechers: r.leechers != null ? r.leechers : 0,
    indexer: r.indexer || 'unknown',
    magnet: r.magnetUrl || (typeof r.guid === 'string' && r.guid.startsWith('magnet:') ? r.guid : null),
    downloadUrl: r.downloadUrl || null,
    infoHash: (r.infoHash || '').toLowerCase() || null,
    sourceType,
    // IDs the indexer attached to the result (0/absent -> null).
    imdbId: r.imdbId ? Number(r.imdbId) : null,
    tmdbId: r.tmdbId ? Number(r.tmdbId) : null,
    tvdbId: r.tvdbId ? Number(r.tvdbId) : null,
  };
}

async function ping() {
  const { data } = await client.get('/api/v1/system/status');
  return !!data;
}

module.exports = { search, searchNzb, searchById, searchNzbById, ping };
