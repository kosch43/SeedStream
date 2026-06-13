'use strict';
const axios = require('axios');
const { cfg } = require('./config');
const { searchCache } = require('./cache');

const BASE = 'https://api4.thetvdb.com/v4';
let token = null, tokenAt = 0;

function enabled() { return !!cfg.tvdb.apiKey; }

async function getToken() {
  if (token && Date.now() - tokenAt < 20 * 24 * 3600 * 1000) return token; // ~20 days
  const body = { apikey: cfg.tvdb.apiKey };
  if (cfg.tvdb.pin) body.pin = cfg.tvdb.pin;
  const { data } = await axios.post(`${BASE}/login`, body, { timeout: 8000 });
  token = data && data.data && data.data.token;
  tokenAt = Date.now();
  if (!token) throw new Error('TVDB login returned no token');
  return token;
}

async function api(path, params) {
  const t = await getToken();
  const { data } = await axios.get(`${BASE}${path}`, {
    headers: { Authorization: `Bearer ${t}` },
    params: params || {},
    timeout: 10000,
  });
  return data && data.data;
}

// IMDb id -> TVDB series id (uses a hint from TMDB if we already have one).
async function resolveSeriesId(imdbId, tvdbHint) {
  if (tvdbHint) return tvdbHint;
  const res = await api(`/search/remoteid/${encodeURIComponent(imdbId)}`);
  if (!Array.isArray(res)) return null;
  for (const r of res) {
    if (r.series && r.series.id) return r.series.id;
  }
  return null;
}

// ---- pure parsers (testable) ----
// Build a "season-episode" -> absoluteNumber map from an episodes array.
function parseEpisodes(episodes) {
  const absBySE = {};
  for (const e of (episodes || [])) {
    const s = e.seasonNumber != null ? e.seasonNumber : e.airedSeason;
    const n = e.number != null ? e.number : e.airedEpisodeNumber;
    if (s != null && n != null && e.absoluteNumber != null) {
      absBySE[`${Number(s)}-${Number(n)}`] = e.absoluteNumber;
    }
  }
  return absBySE;
}

function parseTitles(series) {
  const out = [];
  const seen = new Set();
  const push = (t) => {
    const v = typeof t === 'string' ? t : (t && t.name);
    const k = (v || '').toLowerCase().trim();
    if (v && v.trim().length >= 3 && !seen.has(k)) { seen.add(k); out.push(v); }
  };
  if (series) {
    push(series.name);
    for (const a of (series.aliases || [])) push(a);
  }
  return out;
}

async function getEpisodeMap(seriesId) {
  const absBySE = {};
  for (let page = 0; page < 6; page++) {
    const data = await api(`/series/${seriesId}/episodes/default`, { page });
    const eps = data && data.episodes ? data.episodes : (Array.isArray(data) ? data : []);
    if (!eps.length) break;
    Object.assign(absBySE, parseEpisodes(eps));
    if (eps.length < 500) break; // last page
  }
  return absBySE;
}

// Returns { tvdbId, titles[], absoluteEpisode|null } for a series request, else null.
async function lookup(type, imdbId, tvdbHint, season, episode) {
  if (!enabled() || type !== 'series') return null;
  const cacheKey = `tvdb|${imdbId}|${tvdbHint || ''}`;
  let entry = searchCache.get(cacheKey);
  if (entry === undefined) {
    entry = null;
    try {
      const seriesId = await resolveSeriesId(imdbId, tvdbHint);
      if (seriesId) {
        const series = await api(`/series/${seriesId}`);
        const titles = parseTitles(series);
        const absBySE = await getEpisodeMap(seriesId);
        entry = { tvdbId: seriesId, titles, absBySE };
      }
    } catch (e) {
      console.warn('tvdb lookup failed:', e.message);
    }
    searchCache.set(cacheKey, entry, (entry ? cfg.searchCacheTtlSec : 60) * 1000);
  }
  if (!entry) return null;
  const abs = (season != null && episode != null) ? entry.absBySE[`${season}-${episode}`] : null;
  return { tvdbId: entry.tvdbId, titles: entry.titles, absoluteEpisode: abs != null ? abs : null };
}

module.exports = { enabled, lookup, parseEpisodes, parseTitles };
