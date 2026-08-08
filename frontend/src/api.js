export function getApiUrl(path) {
  return path
}

export const UNAUTHORIZED_EVENT = 'seedstream:unauthorized'

export function notifyUnauthorized(detail = {}) {
  if (typeof window === 'undefined') return
  window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT, { detail }))
}

export async function apiFetch(path, options = {}) {
  const url = getApiUrl(path)
  const headers = new Headers(options.headers || {})
  const res = await fetch(url, { credentials: 'include', ...options, headers })
  let data = null
  const contentType = res.headers.get('content-type')
  if (contentType && contentType.includes('application/json')) {
    try {
      data = await res.json()
    } catch {
      data = null
    }
  }
  if (!res.ok) {
    if (res.status === 401) {
      notifyUnauthorized({ path, status: res.status })
    }
    // statusText is unusable as a fallback: HTTP/2 removed reason phrases, so it
    // is always empty behind a proxy that speaks it. Fall back to the code.
    const err = new Error(
      (data && (data.error || data.message)) || res.statusText || `HTTP ${res.status}`
    )
    if (data && data.errors) err.fieldErrors = data.errors
    err.status = res.status
    throw err
  }
  return data
}
