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

async function search(query, type, torznabType) {
  torznabType = torznabType || 'search';
  const cacheKey = `${type}|${torznabType}|${query.toLowerCase()}`;
  const hit = searchCache.get(cacheKey);
  if (hit) return hit;

  const params = new URLSearchParams();
  params.set('query', query);
  params.set('type', torznabType);
  for (const c of catsFor(type)) params.append('categories', String(c));
  for (const id of cfg.prowlarr.indexerIds) params.append('indexerIds', id);
  // Pull a generous pool; the matcher does the real filtering/ranking/slicing.
  params.set('limit', String(Math.max(cfg.maxResults * 4, 100)));

  let out = [];
  try {
    const { data } = await client.get(`/api/v1/search?${params.toString()}`);
    out = (Array.isArray(data) ? data : [])
      .map(normalize)
      .filter(r => r && (r.magnet || r.downloadUrl))
      .sort((a, b) => (b.seeders || 0) - (a.seeders || 0));
  } catch (e) {
    // One failing query (e.g. an indexer that rejects ID syntax) shouldn't kill
    // the others — return empty and let the merged result set carry on.
    console.warn(`prowlarr search failed [${torznabType}] "${query}":`, e.message);
    return [];
  }

  searchCache.set(cacheKey, out, cfg.searchCacheTtlSec * 1000);
  return out;
}

// ID-based search via Prowlarr's token syntax, e.g. "{ImdbId:1375666} {Season:1}".
// Best-effort: many indexers ignore IDs, so this is merged with text search.
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

function normalize(r) {
  const title = r.title || r.sortTitle;
  if (!title) return null;
  return {
    title,
    size: r.size || 0,
    seeders: r.seeders != null ? r.seeders : 0,
    leechers: r.leechers != null ? r.leechers : 0,
    indexer: r.indexer || 'unknown',
    magnet: r.magnetUrl || (typeof r.guid === 'string' && r.guid.startsWith('magnet:') ? r.guid : null),
    downloadUrl: r.downloadUrl || null,
    infoHash: (r.infoHash || '').toLowerCase() || null,
    // IDs the indexer attached to the result (0/absent -> null).
    imdbId: r.imdbId ? Number(r.imdbId) : null,
    tmdbId: r.tmdbId ? Number(r.tmdbId) : null,
    tvdbId: r.tvdbId ? Number(r.tvdbId) : null,
  };
}

// Lightweight reachability check for /check.
async function ping() {
  const { data } = await client.get('/api/v1/system/status');
  return !!data;
}

module.exports = { search, searchById, ping };
