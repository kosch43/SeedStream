'use strict';
const axios = require('axios');
const fs = require('fs');
const path = require('path');
const { cfg } = require('./config');

function api(params) {
  return axios.get(`${cfg.sabnzbd.url}/api`, {
    params: { output: 'json', apikey: cfg.sabnzbd.apiKey, ...params },
    timeout: 15000,
  }).then(r => r.data);
}

// Send an NZB URL to SABnzbd. Returns the nzo_id assigned by SABnzbd.
async function addNzb(downloadUrl, title) {
  const data = await api({
    mode: 'addurl',
    name: downloadUrl,
    nzbname: title || 'seedstream',
    cat: cfg.sabnzbd.category,
    priority: 1,
  });
  if (!data.status) throw new Error('SABnzbd addurl failed: ' + JSON.stringify(data));
  const ids = data.nzo_ids || [];
  if (!ids.length) throw new Error('SABnzbd returned no nzo_id');
  return ids[0];
}

// Check queue then history. Returns { phase, percentage, storage, mbLeft }.
async function getStatus(nzoId) {
  const q = await api({ mode: 'queue', output: 'json' });
  const slot = ((q.queue && q.queue.slots) || []).find(s => s.nzo_id === nzoId);
  if (slot) {
    return {
      phase: 'downloading',
      percentage: parseFloat(slot.percentage || 0),
      mbLeft: parseFloat(slot.mbleft || 0),
      storage: null,
    };
  }
  const h = await api({ mode: 'history', output: 'json', limit: 200 });
  const item = ((h.history && h.history.slots) || []).find(s => s.nzo_id === nzoId);
  if (item) {
    return {
      phase: item.status === 'Completed' ? 'completed' : item.status.toLowerCase(),
      percentage: 100,
      mbLeft: 0,
      storage: item.storage || null,
    };
  }
  return { phase: 'unknown', percentage: 0, mbLeft: 0, storage: null };
}

// Poll until completed; returns the completed job directory path or null on timeout.
async function waitForComplete(nzoId, timeoutMs) {
  const deadline = Date.now() + (timeoutMs || cfg.nzb.timeoutSec * 1000);
  while (Date.now() < deadline) {
    const s = await getStatus(nzoId);
    if (s.phase === 'completed' && s.storage) return s.storage;
    if (s.phase === 'failed') throw new Error('SABnzbd download failed for ' + nzoId);
    await sleep(4000);
  }
  return null;
}

// Scan history for an already-completed item whose name includes the given title.
// Returns the SABnzbd history slot or null.
async function findCompleted(title) {
  if (!title) return null;
  const h = await api({ mode: 'history', output: 'json', limit: 200 });
  const items = (h.history && h.history.slots) || [];
  const norm = s => (s || '').toLowerCase().replace(/[^a-z0-9]/g, '');
  const target = norm(title).slice(0, 40);
  if (!target) return null;
  return items.find(s => s.status === 'Completed' && norm(s.name).includes(target)) || null;
}

// Recursively find the largest video file under a directory.
function findVideoFile(dir, videoExts) {
  let best = null;
  try {
    const entries = fs.readdirSync(dir, { withFileTypes: true });
    for (const e of entries) {
      const full = path.join(dir, e.name);
      if (e.isDirectory()) {
        const sub = findVideoFile(full, videoExts);
        if (sub && (!best || sub.size > best.size)) best = sub;
      } else if (e.isFile()) {
        const ext = (e.name.split('.').pop() || '').toLowerCase();
        if (videoExts.includes(ext)) {
          try {
            const stat = fs.statSync(full);
            if (!best || stat.size > best.size) best = { path: full, size: stat.size };
          } catch { /* skip unreadable */ }
        }
      }
    }
  } catch { /* dir not yet readable */ }
  return best;
}

async function ping() {
  const data = await api({ mode: 'version' });
  if (!data.version) throw new Error('SABnzbd ping failed');
  return true;
}

function enabled() {
  return !!(cfg.sabnzbd.url && cfg.sabnzbd.apiKey);
}

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

module.exports = { addNzb, getStatus, waitForComplete, findCompleted, findVideoFile, ping, enabled };
