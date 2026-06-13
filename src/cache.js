'use strict';

// Simple in-memory TTL cache. Good enough for a single backend instance.
// For multi-instance, swap for Redis (same get/set shape).
class TTLCache {
  constructor() { this.m = new Map(); }
  get(key) {
    const e = this.m.get(key);
    if (!e) return undefined;
    if (Date.now() > e.exp) { this.m.delete(key); return undefined; }
    return e.val;
  }
  set(key, val, ttlMs) {
    this.m.set(key, { val, exp: Date.now() + ttlMs });
  }
}

// Limits concurrent "add + buffer" operations per box so one user can't
// pin a seedbox by opening 20 streams. Keyed by qBit URL.
class ConcurrencyGuard {
  constructor(max) { this.max = max; this.active = new Map(); }
  tryAcquire(key) {
    const n = this.active.get(key) || 0;
    if (n >= this.max) return false;
    this.active.set(key, n + 1);
    return true;
  }
  release(key) {
    const n = this.active.get(key) || 1;
    if (n <= 1) this.active.delete(key);
    else this.active.set(key, n - 1);
  }
}

module.exports = {
  searchCache: new TTLCache(),
  guard: new ConcurrencyGuard(parseInt(process.env.MAX_CONCURRENT_PER_BOX || '3', 10)),
};
