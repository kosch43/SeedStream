export function normalizeAvailNZBMode(mode) {
  return String(mode || 'on').trim().toLowerCase() === 'off' ? 'off' : 'on'
}

export function isAvailNZBEnabled(mode) {
  return normalizeAvailNZBMode(mode) === 'on'
}
