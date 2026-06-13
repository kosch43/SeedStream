'use strict';
const axios = require('axios');
const { cfg } = require('./config');

let cookie = null, cookieAt = 0;
const http = axios.create({ baseURL: cfg.qbit.url, timeout: 30000 });

async function login() {
  if (cookie && Date.now() - cookieAt < 25 * 60 * 1000) return cookie;
  const body = new URLSearchParams({ username: cfg.qbit.username, password: cfg.qbit.password });
  const res = await http.post('/api/v2/auth/login', body.toString(), {
    headers: { 'Content-Type': 'application/x-www-form-urlencoded', Referer: cfg.qbit.url },
  });
  const sc = res.headers['set-cookie'];
  if (!sc || !sc.length) throw new Error('qBittorrent login failed (check creds / WebUI host whitelist)');
  cookie = sc[0].split(';')[0];
  cookieAt = Date.now();
  return cookie;
}

async function api(method, path, data, extraHeaders) {
  const c = await login();
  const headers = Object.assign({ Cookie: c, Referer: cfg.qbit.url }, extraHeaders || {});
  return (await http.request({ method, url: path, data, headers })).data;
}

async function getTorrent(hash) {
  const data = await api('get', `/api/v2/torrents/info?hashes=${hash}`);
  return Array.isArray(data) && data.length ? data[0] : null;
}

async function listCategory() {
  const data = await api('get', `/api/v2/torrents/info?category=${encodeURIComponent(cfg.qbit.category)}`);
  return Array.isArray(data) ? data : [];
}

async function findExisting(infoHash) {
  if (!infoHash) return null;
  return await getTorrent(infoHash);
}

async function addTorrent(ref) {
  const form = new URLSearchParams();
  form.set('category', cfg.qbit.category);
  form.set('savepath', cfg.qbit.savePath);
  form.set('sequentialDownload', 'true');
  form.set('firstLastPiecePrio', 'true');
  form.set('autoTMM', 'false');
  if (ref.magnet) form.set('urls', ref.magnet);
  else if (ref.downloadUrl) form.set('urls', ref.downloadUrl);
  else throw new Error('No magnet or downloadUrl to add');

  await api('post', '/api/v2/torrents/add', form.toString(),
    { 'Content-Type': 'application/x-www-form-urlencoded' });

  let hash = ref.infoHash || null;
  if (!hash) hash = await resolveHashByCategory();
  return hash;
}

async function resolveHashByCategory() {
  for (let i = 0; i < 10; i++) {
    const list = await listCategory();
    if (list.length) {
      list.sort((a, b) => b.added_on - a.added_on);
      return list[0].hash.toLowerCase();
    }
    await sleep(500);
  }
  throw new Error('Could not resolve torrent hash after add');
}

async function getFiles(hash) {
  const data = await api('get', `/api/v2/torrents/files?hash=${hash}`);
  return Array.isArray(data) ? data : [];
}

function pickVideoFile(files) {
  return files
    .map((f, i) => Object.assign({ _idx: (f.index != null ? f.index : i) }, f))
    .filter(f => cfg.videoExts.includes((f.name.split('.').pop() || '').toLowerCase()))
    .sort((a, b) => b.size - a.size)[0] || null;
}

// True once the VIDEO FILE's head has buffered ~bufferMb, or timeout.
// We check the specific file's progress (head-first under sequential download),
// NOT the torrent's global progress, which spans every file in the torrent.
async function waitForBuffer(hash, fileIdx) {
  const deadline = Date.now() + cfg.bufferTimeoutSec * 1000;
  const target = cfg.bufferMb * 1024 * 1024;
  while (Date.now() < deadline) {
    const files = await getFiles(hash);
    const f = files.find((x, i) => (x.index != null ? x.index : i) === fileIdx);
    if (f) {
      const done = (f.progress || 0) * (f.size || 0);
      if (done >= target || f.progress >= 0.999) return true;
    }
    await sleep(1500);
  }
  return false;
}

async function ping() { await login(); return true; }
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

// Delete torrents (and optionally their files) — used by cache eviction.
async function deleteTorrents(hashes, deleteFiles) {
  if (!hashes || !hashes.length) return;
  const form = new URLSearchParams();
  form.set('hashes', hashes.join('|'));
  form.set('deleteFiles', deleteFiles ? 'true' : 'false');
  await api('post', '/api/v2/torrents/delete', form.toString(),
    { 'Content-Type': 'application/x-www-form-urlencoded' });
}

module.exports = {
  addTorrent, getTorrent, getFiles, pickVideoFile, waitForBuffer,
  listCategory, findExisting, ping, deleteTorrents,
};
