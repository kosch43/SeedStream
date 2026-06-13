'use strict';
const express = require('express');
const axios = require('axios');
const fs = require('fs');
const { cfg, validate } = require('./config');
const prowlarr = require('./prowlarr');
const qbit = require('./qbit');
const matcher = require('./matcher');
const tmdb = require('./tmdb');
const tvdb = require('./tvdb');
const anilist = require('./anilist');
const cacheManager = require('./cache_manager');
const { guard } = require('./cache');

const app = express();
app.use((req, res, next) => {
  res.setHeader('Access-Control-Allow-Origin', '*');
  res.setHeader('Access-Control-Allow-Headers', '*');
  next();
});

// Optional secret path prefix gating the whole addon.
app.use((req, res, next) => {
  if (!cfg.addonSecret) return next();
  const p = `/${cfg.addonSecret}`;
  if (req.url === p || req.url.startsWith(p + '/')) {
    req.url = req.url.slice(p.length) || '/';
    return next();
  }
  return res.status(403).send('forbidden');
});

const manifest = {
  id: 'community.seedstream',
  version: '0.8.0',
  name: 'SeedStream',
  description: 'Private-tracker streaming via your own Prowlarr + qBittorrent. Built-in file server, sequential progressive playback, torrent reuse. Runs on a single VPS.',
  resources: ['stream'],
  types: ['movie', 'series'],
  catalogs: [],
  idPrefixes: ['tt'],
  behaviorHints: { configurable: false, configurationRequired: false },
};

function secretSeg() { return cfg.addonSecret ? `/${cfg.addonSecret}` : ''; }

function humanSize(b) {
  if (!b) return '?';
  const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0, n = b;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(1)} ${u[i]}`;
}

async function metaInfo(type, imdbId) {
  try {
    const { data } = await axios.get(
      `https://v3-cinemeta.strem.io/meta/${type}/${imdbId}.json`, { timeout: 8000 });
    const meta = data && data.meta;
    if (!meta || !meta.name) return null;
    const ystr = String(meta.year || meta.releaseInfo || '');
    const ymatch = ystr.match(/\d{4}/);
    return { name: meta.name, year: ymatch ? parseInt(ymatch[0], 10) : null };
  } catch { return null; }
}

app.get('/manifest.json', (req, res) => res.json(manifest));

// ---- Stream ----
app.get('/stream/:type/:id.json', async (req, res) => {
  try {
    const { type } = req.params;
    const [imdbId, seasonStr, episodeStr] = req.params.id.split(':');
    const season = seasonStr != null ? parseInt(seasonStr, 10) : null;
    const episode = episodeStr != null ? parseInt(episodeStr, 10) : null;

    // Metadata: Cinemeta (always, free) enriched with optional providers.
    //  TMDB    -> alternative titles (AKAs) for foreign/localized releases
    //  TVDB    -> absolute-episode mapping for anime + series aliases
    //  AniList -> anime title synonyms (romaji/native)
    const cm = await metaInfo(type, imdbId);
    const tm = tmdb.enabled() ? await tmdb.lookup(type, imdbId) : null;

    const primaryName = (tm && tm.title) || (cm && cm.name);
    if (!primaryName) return res.json({ streams: [] });
    const year = (tm && tm.year) || (cm && cm.year) || null;

    // TVDB (series only): absolute episode + aliases. tvdb_id from TMDB is a free hint.
    const tv = (type === 'series')
      ? await tvdb.lookup(type, imdbId, tm && tm.tvdbId, season, episode)
      : null;
    const absoluteEpisode = tv ? tv.absoluteEpisode : null;

    // AniList (series only): merge synonyms only if they actually correspond to this
    // title (guards against a fuzzy match to a different anime for non-anime shows).
    let aniTitles = [];
    let aniRomaji = null;
    if (type === 'series') {
      const ani = await anilist.lookupByTitle(primaryName);
      if (ani && titleOverlap(primaryName, [ani.english, ani.romaji])) {
        aniTitles = ani.titles || [];
        aniRomaji = ani.romaji || null;
      }
    }

    // Titles the matcher will accept (all known names), deduped.
    const matchTitles = uniq([
      ...(tm ? tm.titles : []),
      ...(tv ? tv.titles : []),
      ...aniTitles,
      cm && cm.name,
    ].filter(Boolean));
    // Titles to actually search with: primary + original-language + romaji (bounded).
    const searchTitles = uniq([primaryName, tm && tm.originalTitle, aniRomaji].filter(Boolean)).slice(0, 3);

    // Build the query set (text + best-effort ID + absolute for anime), parallel, merge.
    const queries = [];
    if (type === 'series' && season != null && episode != null) {
      const ss = String(season).padStart(2, '0');
      const ee = String(episode).padStart(2, '0');
      for (const t of searchTitles) queries.push(prowlarr.search(`${t} S${ss}E${ee}`, type));
      queries.push(prowlarr.search(`${searchTitles[0]} S${ss}`, type)); // season pack
      queries.push(prowlarr.searchById(imdbId, type, season, episode));
      if (absoluteEpisode != null) {
        queries.push(prowlarr.search(`${searchTitles[0]} ${absoluteEpisode}`, type)); // anime absolute
      }
    } else {
      for (const t of searchTitles) queries.push(prowlarr.search(year ? `${t} ${year}` : t, type));
      queries.push(prowlarr.searchById(imdbId, type));
    }
    const releases = (await Promise.all(queries)).flat();

    const request = { type, titles: matchTitles, year, season, episode, imdbId, absoluteEpisode };
    const ranked = matcher.rankAndFilter(releases, request, {
      excludeCam: cfg.excludeCam,
      resolutions: cfg.resolutions,
      minSeeders: cfg.minSeeders,
      preferredResolution: cfg.preferredResolution,
      maxResults: cfg.maxResults,
    });

    const streams = ranked.map((item) => {
      const r = item.raw;
      const payload = Buffer.from(JSON.stringify({
        magnet: r.magnet, downloadUrl: r.downloadUrl, infoHash: r.infoHash,
        type, season, episode, abs: absoluteEpisode,
      })).toString('base64url');
      const { name, title } = matcher.buildLabel(item);
      return {
        name,
        title,
        url: `${cfg.addonBaseUrl}${secretSeg()}/play/${payload}`,
        behaviorHints: { notWebReady: false, bingeGroup: `seedstream-${imdbId}` },
      };
    });
    res.json({ streams });
  } catch (e) {
    console.error('stream error:', e.message);
    res.json({ streams: [] });
  }
});

function uniq(arr) {
  const seen = new Set(); const out = [];
  for (const x of arr) { const k = String(x).toLowerCase().trim(); if (!seen.has(k)) { seen.add(k); out.push(x); } }
  return out;
}

// True if `name` shares most of its significant tokens with any candidate title.
// Used to confirm an AniList fuzzy match really corresponds to the requested show.
function titleOverlap(name, candidates) {
  const norm = s => (s || '').toLowerCase().normalize('NFKD').replace(/[\u0300-\u036f]/g, '')
    .replace(/[^a-z0-9]+/g, ' ').trim();
  const tok = norm(name).split(' ').filter(t => t.length > 1);
  if (!tok.length) return false;
  return (candidates || []).filter(Boolean).some(c => {
    const cn = norm(c);
    const hits = tok.filter(t => cn.includes(t)).length;
    return hits >= Math.ceil(tok.length * 0.6);
  });
}

// ---- Play / redirect (with reuse fast-path) ----
app.get('/play/:payload', async (req, res) => {
  const boxKey = cfg.qbit.url;
  let acquired = false;
  try {
    const ref = JSON.parse(Buffer.from(req.params.payload, 'base64url').toString());

    const existing = await qbit.findExisting(ref.infoHash);
    if (existing) {
      const files = await qbit.getFiles(existing.hash);
      const vid = matcher.pickFile(files, ref);
      if (vid) {
        const done = (vid.progress || 0) * (vid.size || 0);
        if (done >= cfg.bufferMb * 1024 * 1024 || vid.progress >= 0.999) {
          cacheManager.recordPlay(existing.hash);
          return res.redirect(302, fileUrl(vid.name));
        }
      }
    }

    if (!guard.tryAcquire(boxKey)) {
      return res.status(429).send('too many concurrent streams on this box; try again shortly');
    }
    acquired = true;

    const hash = existing ? existing.hash : await qbit.addTorrent(ref);

    let files = [];
    for (let i = 0; i < 20; i++) {
      files = await qbit.getFiles(hash);
      if (files.length) break;
      await new Promise(r => setTimeout(r, 750));
    }
    const vid = matcher.pickFile(files, ref);
    if (!vid) return res.status(404).send('no video file in torrent');

    const ready = await qbit.waitForBuffer(hash, vid._idx);
    if (!ready) {
      // Buffer didn't fill in time. Don't redirect to an underfilled file —
      // tell the player to retry; by then more has downloaded and the reuse
      // fast-path above will serve it immediately.
      return res.status(504).send('still buffering, try again in a moment');
    }
    cacheManager.recordPlay(hash);
    return res.redirect(302, fileUrl(vid.name));
  } catch (e) {
    console.error('play error:', e.message);
    return res.status(500).send('play error: ' + e.message);
  } finally {
    if (acquired) guard.release(boxKey);
  }
});

// Build the playback URL: external file server if configured, else our own.
function fileUrl(name) {
  const encoded = name.split('/').map(encodeURIComponent).join('/');
  if (cfg.fileServerBaseUrl) return `${cfg.fileServerBaseUrl}/${encoded}`;
  return `${cfg.addonBaseUrl}${secretSeg()}/files/${encoded}`;
}

// ---- Built-in file server (single-VPS default) ----
// Serves files from QBIT_SAVE_PATH with HTTP range support. res.sendFile handles
// Range headers + Accept-Ranges, and the `root` option blocks path traversal.
app.get('/files/*', (req, res) => {
  if (cfg.fileServerBaseUrl) return res.status(404).send('external file server configured');
  let rel;
  try { rel = decodeURIComponent(req.params[0] || ''); }
  catch { return res.status(400).end(); }
  if (!rel || rel.includes('\0')) return res.status(400).end();
  res.sendFile(rel, {
    root: cfg.qbit.savePath,
    acceptRanges: true,
    dotfiles: 'deny',
    cacheControl: false,
  }, (err) => {
    if (err && !res.headersSent) res.status(err.status === 416 ? 416 : 404).end();
  });
});

// ---- Config check ----
app.get('/check', async (req, res) => {
  const out = { prowlarr: null, qbit: null, fileServer: null };
  try { await prowlarr.ping(); out.prowlarr = 'ok'; }
  catch (e) { out.prowlarr = 'FAIL: ' + e.message; }
  try { await qbit.ping(); out.qbit = 'ok'; }
  catch (e) { out.qbit = 'FAIL: ' + e.message; }
  if (cfg.fileServerBaseUrl) {
    try {
      const r = await axios.head(cfg.fileServerBaseUrl, { timeout: 8000, validateStatus: () => true });
      out.fileServer = `external, reachable (HTTP ${r.status})`;
    } catch (e) { out.fileServer = 'FAIL: ' + e.message; }
  } else {
    try {
      await fs.promises.access(cfg.qbit.savePath, fs.constants.R_OK);
      out.fileServer = `internal, serving ${cfg.qbit.savePath}`;
    } catch (e) {
      out.fileServer = `FAIL: cannot read QBIT_SAVE_PATH (${cfg.qbit.savePath}): ${e.message}`;
    }
  }
  res.json(out);
});

app.get('/health', (req, res) => res.json({ ok: true }));

app.listen(cfg.port, async () => {
  console.log(`\nSeedStream v${manifest.version} on :${cfg.port}`);
  const missing = validate();
  if (missing.length) {
    console.warn('  [!] Missing required config: ' + missing.join(', '));
    console.warn('      Fill these in .env, then restart.');
  } else {
    console.log(`  Manifest: ${cfg.addonBaseUrl}${secretSeg()}/manifest.json`);
    console.log(`  File server: ${cfg.fileServerBaseUrl ? 'external (' + cfg.fileServerBaseUrl + ')' : 'built-in, serving ' + cfg.qbit.savePath}`);
    console.log('  Validating services...');
    try { await prowlarr.ping(); console.log('    [ok] Prowlarr'); }
    catch (e) { console.warn('    [FAIL] Prowlarr: ' + e.message); }
    try { await qbit.ping(); console.log('    [ok] qBittorrent'); }
    catch (e) { console.warn('    [FAIL] qBittorrent: ' + e.message); }
    if (cacheManager.enabled()) {
      const caps = [cfg.cache.maxGb > 0 && `${cfg.cache.maxGb}GB`, cfg.cache.maxAgeDays > 0 && `${cfg.cache.maxAgeDays}d`]
        .filter(Boolean).join(' / ');
      console.log(`  Cache: bounded (${caps}), protecting ${cfg.cache.minSeedHours}h seed time`);
      cacheManager.start();
    } else {
      console.log('  Cache: unbounded (set CACHE_MAX_GB / CACHE_MAX_AGE_DAYS to enable eviction)');
    }
  }
  console.log('');
});
