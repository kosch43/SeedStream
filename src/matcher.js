'use strict';
const { filenameParse } = require('@ctrl/video-filename-parser');
const { cfg } = require('./config');

// ---- normalization helpers --------------------------------------------
function norm(s) {
  return (s || '')
    .toLowerCase()
    .normalize('NFKD').replace(/[\u0300-\u036f]/g, '')
    .replace(/&/g, ' and ')
    .replace(/[^a-z0-9]+/g, ' ')
    .trim();
}

function humanSize(b) {
  if (!b) return '?';
  const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0, n = b;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(1)} ${u[i]}`;
}

function resLower(p) { return (p.resolution || '').toLowerCase(); }

// ---- source / quality reads -------------------------------------------
const CAM_SOURCES = ['CAM', 'TS', 'TELESYNC', 'TELECINE', 'SCREENER', 'DVDSCR', 'WORKPRINT', 'HDCAM', 'HDTS'];

function isCam(p, raw) {
  const srcs = (p.sources || []).map(s => String(s).toUpperCase());
  if (srcs.some(s => CAM_SOURCES.includes(s))) return true;
  return /\b(hdcam|cam[\s.\-]?rip|telesync|telecine|hdts|\.ts\.|dvdscr|screener|workprint)\b/i.test(raw);
}

function sourceLabel(p) {
  if (p.modifier && String(p.modifier).toUpperCase() === 'REMUX') return 'REMUX';
  const s = (p.sources || [])[0];
  if (!s) return '';
  const map = {
    BLURAY: 'BluRay', WEBDL: 'WEB-DL', WEBRIP: 'WEBRip', HDTV: 'HDTV',
    DVD: 'DVD', CAM: 'CAM', TS: 'TS', TELESYNC: 'TS', SCREENER: 'SCR',
    DVDSCR: 'SCR', PDTV: 'TV', SDTV: 'TV',
  };
  const key = String(s).toUpperCase();
  return map[key] || (key.charAt(0) + key.slice(1).toLowerCase());
}

function hdrLabel(p) {
  const e = p.edition || {};
  if (e.dolbyVision) return 'DV';
  if (e.hdr) return 'HDR';
  return '';
}

function audioLabel(p, raw) {
  let codec = p.audioCodec || '';
  codec = codec.replace(/dolby\s*truehd/i, 'TrueHD').replace(/dolby\s*digital\s*plus/i, 'DD+')
    .replace(/dolby\s*digital/i, 'DD');
  const atmos = /\batmos\b/i.test(raw);
  const parts = [];
  if (atmos) parts.push('Atmos');
  if (codec && !/atmos/i.test(codec)) parts.push(codec);
  if (p.audioChannels) parts.push(p.audioChannels);
  return parts.join(' ');
}

const BOGUS_GROUPS = new Set(['DL', 'AUDIO', 'HEVC', 'AVC', 'AAC', 'DDP', 'DD', 'AC3', 'EAC3',
  'REMUX', 'PROPER', 'REPACK', 'INTERNAL', 'WEB', 'BLURAY', 'HDR', 'DV', 'X264', 'X265']);

function cleanGroup(g) {
  if (!g) return '';
  if (BOGUS_GROUPS.has(String(g).toUpperCase())) return '';
  if (/^(x?26[45]|h\.?26[45]|\d+p|10bit)$/i.test(g)) return '';
  return g;
}

function langLabel(p) {
  const langs = p.languages || [];
  if (langs.length > 1) return 'Multi';
  if (langs.length === 1 && langs[0] !== 'English') return langs[0];
  return '';
}

// ---- scoring ----------------------------------------------------------
const RES_SCORE = { '2160p': 4000, '1080p': 3000, '720p': 2000, '576p': 1200, '480p': 1000, '360p': 800 };
const SRC_SCORE = { REMUX: 700, BluRay: 500, 'WEB-DL': 420, WEBRip: 360, HDTV: 220, DVD: 160, TV: 140 };

function scoreOf(p, raw, opts) {
  let s = (RES_SCORE[resLower(p)] || 500) + (SRC_SCORE[sourceLabel(p)] || 0);
  const e = p.edition || {};
  if (e.dolbyVision) s += 180; else if (e.hdr) s += 120;
  if ((p.revision || {}).version > 1) s += 50; // proper/repack
  if (opts.preferredResolution && resLower(p) === opts.preferredResolution) s += 10000;
  return s;
}

// ---- relevance matching -----------------------------------------------
// Returns null (drop) or { confidence: 'high'|'med'|'low', pack: bool }.
// `release` is the normalized release ({ title, imdbId, ... }); `req` carries
// { type, titles[], year, season, episode, imdbId }.
function numImdb(x) {
  if (x == null) return null;
  const n = parseInt(String(x).replace(/^tt/i, ''), 10);
  return Number.isFinite(n) ? n : null;
}

function titleMatches(req, raw) {
  const relNorm = norm(raw);
  const titles = (req.titles && req.titles.length) ? req.titles : (req.title ? [req.title] : []);
  if (!titles.length) return true;
  return titles.some(t => {
    const tokens = norm(t).split(' ').filter(x => x.length > 1 || /\d/.test(x));
    if (!tokens.length) return false;
    const hits = tokens.filter(x => relNorm.includes(x)).length;
    const need = tokens.length <= 2 ? tokens.length : Math.ceil(tokens.length * 0.7);
    return hits >= need;
  });
}

function matchesRequest(p, release, req) {
  const raw = release.title || '';

  // ---- ID gate: strongest, most reliable signal when present ----
  const reqId = numImdb(req.imdbId);
  const relId = numImdb(release.imdbId);
  let idMatched = null;
  if (reqId && relId) idMatched = (reqId === relId);
  if (idMatched === false) return null; // result is definitively a different title

  // ---- title (fuzzy, against any known/AKA title); skip if ID already matched ----
  if (!idMatched && !titleMatches(req, raw)) return null;

  if (req.type === 'movie') {
    if (idMatched) return { confidence: 'high', pack: false };
    if (req.year && p.year) {
      if (Math.abs(parseInt(p.year, 10) - req.year) > 1) return null; // wrong year
      return { confidence: 'high', pack: false };
    }
    return { confidence: p.year ? 'high' : 'med', pack: false };
  }

  // series
  const ss = req.season, ee = req.episode;
  const seasons = p.seasons || [];
  const eps = p.episodeNumbers || [];
  if (p.fullSeason && seasons.includes(ss)) return { confidence: 'high', pack: true };
  if (seasons.includes(ss) && eps.includes(ee)) return { confidence: 'high', pack: false };
  if (seasons.includes(ss) && eps.length >= 2) {
    const lo = Math.min(...eps), hi = Math.max(...eps);
    if (ee >= lo && ee <= hi) return { confidence: 'high', pack: true };
  }
  if (seasons.length === 0) {
    // No season parsed (anime absolute numbering, or odd naming). If TVDB gave us
    // the absolute episode number, verify against it; otherwise keep low-confidence
    // "unverified" rather than dropping — better than showing anime users nothing.
    const abs = req.absoluteEpisode;
    if (abs != null && eps.length) {
      if (eps.includes(abs)) return { confidence: 'high', pack: eps.length > 1 };
      if (eps.length >= 2) {
        const lo = Math.min(...eps), hi = Math.max(...eps);
        if (abs >= lo && abs <= hi) return { confidence: 'high', pack: true };
      }
    }
    return { confidence: 'low', pack: eps.length > 1 };
  }
  return null; // a season was parsed but it isn't the requested one -> drop
}

const CONF_RANK = { high: 3, med: 2, low: 1 };

// ---- main: filter, dedupe, rank ---------------------------------------
function rankAndFilter(releases, req, opts) {
  const seen = new Set();
  const scored = [];
  for (const r of releases) {
    const p = filenameParse(r.title || '', req.type === 'series');
    const m = matchesRequest(p, r, req);
    if (!m) continue;
    if (opts.excludeCam && isCam(p, r.title || '')) continue;
    if (opts.resolutions && opts.resolutions.length && !opts.resolutions.includes(resLower(p))) continue;
    // Seeder floor only applies to torrents; NZBs don't have seeders.
    if (r.sourceType !== 'nzb' && (r.seeders || 0) < (opts.minSeeders || 0)) continue;

    const key = r.infoHash || `${norm(r.title)}|${r.size || 0}`;
    if (seen.has(key)) continue;
    seen.add(key);

    scored.push({ raw: r, parsed: p, conf: CONF_RANK[m.confidence], pack: !!m.pack, score: scoreOf(p, r.title || '', opts) });
  }
  // Torrents rank first (seeders as final tiebreak), then NZBs (quality order).
  scored.sort((a, b) => {
    const aIsNzb = a.raw.sourceType === 'nzb' ? 1 : 0;
    const bIsNzb = b.raw.sourceType === 'nzb' ? 1 : 0;
    return b.conf - a.conf || aIsNzb - bIsNzb || b.score - a.score || (b.raw.seeders || 0) - (a.raw.seeders || 0);
  });
  return scored.slice(0, opts.maxResults);
}

// ---- file selection within a torrent ----------------------------------
function isVideo(name) {
  return cfg.videoExts.includes((name.split('.').pop() || '').toLowerCase());
}

function videoFiles(files) {
  return files
    .map((f, i) => Object.assign({ _idx: f.index != null ? f.index : i }, f))
    .filter(f => isVideo(f.name || ''));
}

// Pick the right file to play. For a series request with multiple video files
// (a season/episode pack), find the file matching the requested S/E rather than
// blindly taking the largest. Falls back to the largest video file.
function stripExt(name) { return (name || '').replace(/\.[a-z0-9]{2,4}$/i, ''); }

function pickFile(files, req) {
  const vids = videoFiles(files);
  if (!vids.length) return null;
  const largest = vids.slice().sort((a, b) => b.size - a.size)[0];

  if (req && req.type === 'series' && req.season != null && req.episode != null && vids.length > 1) {
    const wantAbs = req.absoluteEpisode != null ? req.absoluteEpisode
      : (req.abs != null ? req.abs : null);
    // exact season + episode
    for (const f of vids) {
      const p = filenameParse(stripExt(f.name), true);
      if ((p.seasons || []).includes(req.season) && (p.episodeNumbers || []).includes(req.episode)) return f;
    }
    // absolute episode number, no season parsed (anime packs)
    for (const f of vids) {
      const p = filenameParse(stripExt(f.name), true);
      if ((p.seasons || []).length === 0) {
        const eps = p.episodeNumbers || [];
        if (wantAbs != null && eps.includes(wantAbs)) return f;
        if (eps.includes(req.episode)) return f;
      }
    }
    // couldn't identify the episode in the pack -> largest (best-effort)
  }
  return largest;
}


function buildLabel(item) {
  const p = item.parsed, r = item.raw, raw = r.title || '';
  const isNzb = r.sourceType === 'nzb';
  const res = (p.resolution || '').toLowerCase();
  const hdr = hdrLabel(p);
  const nameTag = [res, hdr].filter(Boolean).join(' ');
  const prefix = isNzb ? 'SeedStream NZB' : 'SeedStream';
  const name = nameTag ? `${prefix} ${nameTag}` : prefix;

  const meta = [sourceLabel(p), p.videoCodec, audioLabel(p, raw), langLabel(p), cleanGroup(p.group) && `[${cleanGroup(p.group)}]`]
    .filter(Boolean).join(' • ');
  const tags = `${item.pack ? ' [PACK]' : ''}${item.conf <= 1 ? ' (unverified)' : ''}`;
  const seedLine = isNzb
    ? `NZB via ${r.indexer}  💾 ${humanSize(r.size)}`
    : `👤 ${r.seeders || 0}  💾 ${humanSize(r.size)}`;
  const title = `${raw}${tags}\n${meta}\n${seedLine}`;
  return { name, title };
}

module.exports = { rankAndFilter, buildLabel, pickFile, humanSize, _internals: { matchesRequest, scoreOf, isCam, sourceLabel } };
