'use strict';
const { cfg } = require('./config');
const qbit = require('./qbit');

// hash -> last time WE served it (ms). Survives via qBit timestamps on restart.
const lastPlayed = Object.create(null);
function recordPlay(hash) { if (hash) lastPlayed[hash] = Date.now(); }

function enabled() { return cfg.cache.maxGb > 0 || cfg.cache.maxAgeDays > 0; }

// ---- pure decision function (testable without qBit) ----
// torrents: qBit info objects ({hash,size,progress,seeding_time,last_activity,completion_on}).
// opts: { maxBytes, maxAgeMs, minSeedSec }. Returns hashes to evict.
function selectEvictions(torrents, opts, now, played) {
  const RECENT_MS = 10 * 60 * 1000; // protect anything likely being watched right now
  const meta = (torrents || []).map(t => ({
    hash: t.hash,
    size: t.size || 0,
    done: (t.progress || 0) >= 0.999,
    seeding: t.seeding_time || 0, // seconds actually seeded (matches tracker seed-time rules)
    used: Math.max((played && played[t.hash]) || 0, (t.last_activity || 0) * 1000, (t.completion_on || 0) * 1000),
  }));

  const isProtected = (t) =>
    !t.done ||                                            // still downloading
    (opts.minSeedSec > 0 && t.seeding < opts.minSeedSec) || // hasn't met seed time -> ratio/H&R safety
    (now - t.used) < RECENT_MS;                           // likely currently streaming

  // Coldest (least-recently-used) first.
  const candidates = meta.filter(t => !isProtected(t)).sort((a, b) => a.used - b.used);
  const evict = new Set();

  // Age cap: drop anything not touched within maxAge.
  if (opts.maxAgeMs > 0) {
    for (const c of candidates) if (now - c.used > opts.maxAgeMs) evict.add(c.hash);
  }
  // Size cap: evict coldest until total fits.
  if (opts.maxBytes > 0) {
    const total = meta.reduce((s, t) => s + t.size, 0);
    let freed = meta.filter(t => evict.has(t.hash)).reduce((s, t) => s + t.size, 0);
    for (const c of candidates) {
      if (total - freed <= opts.maxBytes) break;
      if (!evict.has(c.hash)) { evict.add(c.hash); freed += c.size; }
    }
  }
  return [...evict];
}

async function runSweep() {
  if (!enabled()) return;
  try {
    const list = await qbit.listCategory();
    const opts = {
      maxBytes: cfg.cache.maxGb > 0 ? cfg.cache.maxGb * 1e9 : 0,
      maxAgeMs: cfg.cache.maxAgeDays > 0 ? cfg.cache.maxAgeDays * 86400000 : 0,
      minSeedSec: cfg.cache.minSeedHours > 0 ? cfg.cache.minSeedHours * 3600 : 0,
    };
    const hashes = selectEvictions(list, opts, Date.now(), lastPlayed);
    if (hashes.length) {
      await qbit.deleteTorrents(hashes, true);
      for (const h of hashes) delete lastPlayed[h];
      console.log(`[cache] evicted ${hashes.length} least-recently-played torrent(s)`);
    }
  } catch (e) {
    console.warn('[cache] sweep failed:', e.message);
  }
}

function start() {
  if (!enabled()) return;
  const ms = Math.max(cfg.cache.sweepMinutes, 1) * 60 * 1000;
  setInterval(runSweep, ms);
  setTimeout(runSweep, 30 * 1000); // first pass shortly after startup
}

module.exports = { recordPlay, selectEvictions, runSweep, start, enabled };
