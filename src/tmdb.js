'use strict';
const axios = require('axios');
const { cfg } = require('./config');
const { searchCache } = require('./cache');

const BASE = 'https://api.themoviedb.org/3';

function enabled() { return !!cfg.tmdb.apiKey; }

async function get(path, params) {
  const { data } = await axios.get(`${BASE}${path}`, {
    params: Object.assign({ api_key: cfg.tmdb.apiKey }, params || {}),
    timeout: 8000,
  });
  return data;
}

// Pure: turn a TMDB detail response into our normalized shape (testable).
function parseDetail(d, isTv) {
  const title = isTv ? d.name : d.title;
  const original = isTv ? d.original_name : d.original_title;
  const dateStr = isTv ? d.first_air_date : d.release_date;
  const year = dateStr ? parseInt(String(dateStr).slice(0, 4), 10) : null;

  const altBlock = d.alternative_titles || {};
  const altList = isTv ? (altBlock.results || []) : (altBlock.titles || []);
  const akas = altList.map(t => t && t.title).filter(Boolean);

  // Unique, ordered title set: primary, original, then AKAs. Skip very short
  // generic AKAs (< 3 chars) to avoid noisy false matches.
  const titles = [];
  const seen = new Set();
  for (const t of [title, original, ...akas]) {
    const k = (t || '').toLowerCase().trim();
    if (t && t.trim().length >= 3 && !seen.has(k)) { seen.add(k); titles.push(t); }
  }

  const ext = d.external_ids || {};
  return {
    tmdbId: d.id,
    title,
    originalTitle: original && original !== title ? original : null,
    year,
    titles,
    tvdbId: ext.tvdb_id || null,
  };
}

// Resolve an IMDb id to TMDB info with alternative titles. null on failure.
async function lookup(type, imdbId) {
  if (!enabled()) return null;
  const cacheKey = `tmdb|${type}|${imdbId}`;
  const cached = searchCache.get(cacheKey);
  if (cached !== undefined) return cached;
  try {
    const isTv = type === 'series';
    const find = await get(`/find/${imdbId}`, { external_source: 'imdb_id' });
    const arr = isTv ? find.tv_results : find.movie_results;
    const base = arr && arr[0];
    if (!base) { searchCache.set(cacheKey, null, cfg.searchCacheTtlSec * 1000); return null; }
    const detail = await get(`/${isTv ? 'tv' : 'movie'}/${base.id}`,
      { append_to_response: 'alternative_titles,external_ids' });
    const info = parseDetail(detail, isTv);
    searchCache.set(cacheKey, info, cfg.searchCacheTtlSec * 1000);
    return info;
  } catch (e) {
    console.warn('tmdb lookup failed:', e.message);
    searchCache.set(cacheKey, null, 60 * 1000); // brief negative cache
    return null;
  }
}

module.exports = { enabled, lookup, parseDetail };
