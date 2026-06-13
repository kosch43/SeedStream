'use strict';
const axios = require('axios');
const { cfg } = require('./config');
const { searchCache } = require('./cache');

const URL = 'https://graphql.anilist.co';
const QUERY = `query($search:String){Media(search:$search,type:ANIME){id title{romaji english native} synonyms}}`;

// AniList reads need no auth; we use the key as the opt-in for anime enrichment.
function enabled() { return !!cfg.anilist.apiKey; }

// ---- pure parser (testable) ----
function parseMedia(media) {
  if (!media) return null;
  const t = media.title || {};
  const titles = [];
  const seen = new Set();
  for (const x of [t.romaji, t.english, t.native, ...(media.synonyms || [])]) {
    const k = (x || '').toLowerCase().trim();
    if (x && x.trim().length >= 3 && !seen.has(k)) { seen.add(k); titles.push(x); }
  }
  return { anilistId: media.id, titles, romaji: t.romaji || null, english: t.english || null };
}

async function lookupByTitle(title) {
  if (!enabled() || !title) return null;
  const cacheKey = `anilist|${title.toLowerCase()}`;
  const cached = searchCache.get(cacheKey);
  if (cached !== undefined) return cached;
  try {
    const headers = { 'Content-Type': 'application/json', Accept: 'application/json' };
    if (cfg.anilist.apiKey) headers.Authorization = `Bearer ${cfg.anilist.apiKey}`;
    const { data } = await axios.post(URL, { query: QUERY, variables: { search: title } },
      { headers, timeout: 8000 });
    const media = data && data.data && data.data.Media;
    const info = parseMedia(media);
    searchCache.set(cacheKey, info, cfg.searchCacheTtlSec * 1000);
    return info;
  } catch (e) {
    console.warn('anilist lookup failed:', e.message);
    searchCache.set(cacheKey, null, 60 * 1000);
    return null;
  }
}

module.exports = { enabled, lookupByTitle, parseMedia };
