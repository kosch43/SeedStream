import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { apiFetch } from '../api'

const NETWORK_TAB_FIELDS = [
  'addon_port',
  'addon_base_url',
  'tls_enabled',
  'tls_cert_file',
  'tls_key_file',
  'tls_auto_domain',
  'tls_auto_email',
  'indexer_query_header',
  'indexer_grab_header',
]

const ADVANCED_TAB_FIELDS = [
  'log_level',
  'keep_log_files',
  'playback_startup_timeout_seconds',
  'failover_fast_mode',
  'session_ttl_minutes',
  'session_post_playback_ttl_minutes',
  'memory_limit_mb',
  'monthly_upload_cap_gb',
  'post_cap_upload_mbps',
  'upload_cap_reset_day',
  'cerberus_base_url',
  'cerberus_api_key',
  'tmdb_api_key',
  'tvdb_api_key',
]

export function fieldToTab(fieldName) {
  if (fieldName.startsWith('indexers')) {
    const searchQueryFields = [
      'movie_categories',
      'tv_categories',
      'extra_search_terms',
      'disable_id_search',
      'disable_string_search',
      'search_result_limit',
      'search_title_language',
      'include_year',
      'series_search_scope',
    ]
    if (searchQueryFields.some((suffix) => fieldName.endsWith(`.${suffix}`))) return 'search_query'
    return 'torrent_trackers'
  }
  if (fieldName.startsWith('movie_search_queries') || fieldName.startsWith('series_search_queries')) return 'search_query'
  if (NETWORK_TAB_FIELDS.includes(fieldName)) return 'network'
  if (ADVANCED_TAB_FIELDS.includes(fieldName)) return 'advanced'
  return null
}

export function isConfigTab(tabId) {
  return tabId === 'network' || tabId === 'advanced'
}

function pickConfigSlice(values, keys) {
  return keys.reduce((acc, key) => {
    acc[key] = values?.[key]
    return acc
  }, {})
}

function buildNamedValidationSummary(errors, prefix, items, fallbackLabel) {
  if (!errors) return ''
  const summaries = []
  const seen = new Set()

  Object.entries(errors).forEach(([path, message]) => {
    const match = path.match(new RegExp(`^${prefix}\\.(\\d+)\\.`))
    if (!match) return
    const index = Number(match[1])
    const item = Array.isArray(items) ? items[index] : null
    const label = item?.name || item?.host || item?.url || `${fallbackLabel} ${index + 1}`
    const summary = `${label}: ${message}`
    if (seen.has(summary)) return
    seen.add(summary)
    summaries.push(summary)
  })

  if (summaries.length === 0) return ''
  if (summaries.length === 1) return summaries[0]
  const visible = summaries.slice(0, 2)
  const remaining = summaries.length - visible.length
  return remaining > 0 ? `${visible.join(' | ')} | +${remaining} more` : visible.join(' | ')
}

function summarizeConfigErrors(errors, sourceTab, values) {
  if (!errors) return ''
  if (sourceTab === 'torrent_trackers') {
    return buildNamedValidationSummary(errors, 'indexers', values?.torznab_trackers, 'Tracker')
  }
  if (sourceTab === 'search_query') {
    const movieSummary = buildNamedValidationSummary(errors, 'movie_search_queries', values?.movie_search_queries, 'Movie Query')
    const seriesSummary = buildNamedValidationSummary(errors, 'series_search_queries', values?.series_search_queries, 'Show Query')
    if (movieSummary && seriesSummary) return `${movieSummary} | ${seriesSummary}`
    return movieSummary || seriesSummary
  }
  return ''
}

export function useSettingsState({
  activeTab,
  clearSaveStatus,
  configSnapshot,
  getValues,
  saveStatus,
  sendCommand,
  setActiveTab,
  setConfigSnapshot,
  setError,
}) {
  const [visibleFooterStatus, setVisibleFooterStatus] = useState(null)
  const [footerStatusVisible, setFooterStatusVisible] = useState(false)
  const [lastSettingsSaveCard, setLastSettingsSaveCard] = useState('')
  const [lastConfigSaveSource, setLastConfigSaveSource] = useState('network')
  const footerTimeoutRef = useRef(null)
  const footerHideTimeoutRef = useRef(null)

  const networkInitialValues = useMemo(
    () => pickConfigSlice(configSnapshot, NETWORK_TAB_FIELDS),
    [configSnapshot]
  )

  const advancedInitialValues = useMemo(
    () => pickConfigSlice(configSnapshot, ADVANCED_TAB_FIELDS),
    [configSnapshot]
  )

  const showFooterStatus = useCallback((status, onHide) => {
    if (footerTimeoutRef.current) {
      window.clearTimeout(footerTimeoutRef.current)
      footerTimeoutRef.current = null
    }
    if (footerHideTimeoutRef.current) {
      window.clearTimeout(footerHideTimeoutRef.current)
      footerHideTimeoutRef.current = null
    }

    if (!status?.message) {
      setFooterStatusVisible(false)
      setVisibleFooterStatus(null)
      return
    }

    setVisibleFooterStatus(status)
    requestAnimationFrame(() => setFooterStatusVisible(true))
    if (status.type === 'error') return

    footerTimeoutRef.current = window.setTimeout(() => {
      setFooterStatusVisible(false)
      footerTimeoutRef.current = null
      footerHideTimeoutRef.current = window.setTimeout(() => {
        setVisibleFooterStatus(null)
        footerHideTimeoutRef.current = null
        onHide?.()
      }, 180)
    }, 2600)
  }, [])

  const clearTransientStatus = useCallback(() => {
    showFooterStatus(null)
    clearSaveStatus?.()
  }, [clearSaveStatus, showFooterStatus])

  useEffect(() => () => {
    if (footerTimeoutRef.current) {
      window.clearTimeout(footerTimeoutRef.current)
    }
    if (footerHideTimeoutRef.current) {
      window.clearTimeout(footerHideTimeoutRef.current)
    }
  }, [])

  const tabsWithErrors = useMemo(() => {
    if (saveStatus.type !== 'error') return new Set()
    const errs = saveStatus.errors
    if (!errs) return new Set()
    const tabs = new Set()
    Object.keys(errs).forEach((key) => tabs.add(fieldToTab(key)))
    return tabs
  }, [saveStatus.errors, saveStatus.type])

  useEffect(() => {
    if (!isConfigTab(lastConfigSaveSource)) return
    if (tabsWithErrors.size > 0 && !tabsWithErrors.has(activeTab)) {
      const firstErrorTabOrder = ['network', 'torrent_trackers', 'torrent_clients', 'search_query', 'advanced']
      const firstErrorTab = firstErrorTabOrder.find((tabId) => tabsWithErrors.has(tabId))
      if (firstErrorTab) setActiveTab(firstErrorTab)
    }
  }, [activeTab, lastConfigSaveSource, setActiveTab, tabsWithErrors])

  const errorCount = saveStatus.errors ? Object.keys(saveStatus.errors).length : 0
  const settingsCardTitles = {
    network: 'Network',
    advanced: 'Advanced',
    addon: 'Addon',
    https: 'HTTPS',
    useragent: 'User-Agent',
    admin: 'Logs',
    memory: 'Memory & Cache',
    playback: 'Playback',
    cerberus: 'Cerberus',
    metadata: 'Metadata APIs',
  }
  const successSuffix = saveStatus.type === 'success' && typeof saveStatus.msg === 'string' && saveStatus.msg.includes('Search cache cleared')
    ? ' Search cache cleared.'
    : ''
  const saveFooterMsg = saveStatus.type === 'error' && errorCount > 0
    ? `Validation failed — ${errorCount} field${errorCount > 1 ? 's' : ''} need${errorCount === 1 ? 's' : ''} attention`
    : saveStatus.type === 'success' && lastSettingsSaveCard
      ? `${settingsCardTitles[lastSettingsSaveCard] || 'Settings'} saved.${successSuffix}`
      : saveStatus.type === 'normal' && lastConfigSaveSource
        ? `Saving ${settingsCardTitles[lastConfigSaveSource] || 'Settings'}...`
        : saveStatus.msg

  useEffect(() => {
    if (activeTab !== 'advanced' && activeTab !== 'network') return
    if (lastConfigSaveSource !== activeTab) {
      setVisibleFooterStatus(null)
      return
    }
    if (!saveFooterMsg) return
    showFooterStatus({ type: saveStatus.type, message: saveFooterMsg }, () => {
      clearSaveStatus?.()
      setLastSettingsSaveCard('')
    })
  }, [activeTab, clearSaveStatus, lastConfigSaveSource, saveFooterMsg, saveStatus.type, showFooterStatus])

  const submitSettings = useCallback(async (overrides = null, sourceTab = activeTab) => {
    try {
      const trimData = (obj) => {
        if (typeof obj !== 'object' || obj === null) return obj
        if (Array.isArray(obj)) {
          return obj.map((item) => trimData(item))
        }
        const nextObj = {}
        for (const key in obj) {
          if (typeof obj[key] === 'string') {
            nextObj[key] = obj[key].trim()
          } else if (typeof obj[key] === 'object') {
            nextObj[key] = trimData(obj[key])
          } else {
            nextObj[key] = obj[key]
          }
        }
        return nextObj
      }

      const baseValues = {
        ...configSnapshot,
        indexers: getValues('torznab_trackers'),
        torrent_clients: getValues('torrent_clients'),
        movie_search_queries: getValues('movie_search_queries'),
        series_search_queries: getValues('series_search_queries'),
      }
      const nextValues = overrides ? { ...baseValues, ...overrides } : baseValues
      const trimmedFullData = trimData(nextValues)

      if (typeof trimmedFullData.memory_limit_mb !== 'number') {
        trimmedFullData.memory_limit_mb = Number(trimmedFullData.memory_limit_mb) || 0
      }
      const keepLog = Number(trimmedFullData.keep_log_files)
      trimmedFullData.keep_log_files = Math.min(50, Math.max(1, Number.isNaN(keepLog) ? 9 : keepLog))
      const playbackStartupTimeout = Number(trimmedFullData.playback_startup_timeout_seconds)
      trimmedFullData.playback_startup_timeout_seconds = Math.min(60, Math.max(1, Number.isNaN(playbackStartupTimeout) ? 5 : playbackStartupTimeout))
      const sessionTtl = Number(trimmedFullData.session_ttl_minutes)
      trimmedFullData.session_ttl_minutes = Math.min(1440, Math.max(1, Number.isNaN(sessionTtl) ? 30 : sessionTtl))
      const postPlaybackTtl = Number(trimmedFullData.session_post_playback_ttl_minutes)
      trimmedFullData.session_post_playback_ttl_minutes = Math.min(1440, Math.max(1, Number.isNaN(postPlaybackTtl) ? 240 : postPlaybackTtl))
      const monthlyUploadCap = Number(trimmedFullData.monthly_upload_cap_gb)
      trimmedFullData.monthly_upload_cap_gb = Math.max(0, Number.isNaN(monthlyUploadCap) ? 0 : monthlyUploadCap)
      const postCapMbps = Number(trimmedFullData.post_cap_upload_mbps)
      trimmedFullData.post_cap_upload_mbps = Math.max(0, Number.isNaN(postCapMbps) ? 0 : postCapMbps)
      const resetDay = Number(trimmedFullData.upload_cap_reset_day)
      trimmedFullData.upload_cap_reset_day = Math.min(28, Math.max(1, Number.isNaN(resetDay) ? 1 : resetDay))
      if (trimmedFullData.failover_fast_mode == null) {
        trimmedFullData.failover_fast_mode = true
      } else {
        trimmedFullData.failover_fast_mode = trimmedFullData.failover_fast_mode === true
      }

      const payload = overrides
        ? Object.keys(overrides).reduce((acc, key) => {
            acc[key] = trimmedFullData[key]
            return acc
          }, {})
        : trimmedFullData

      setLastConfigSaveSource(sourceTab)
      await sendCommand('save_config', payload)

      const nextInitialValues = overrides
        ? {
            ...configSnapshot,
            ...Object.keys(overrides).reduce((acc, key) => {
              acc[key] = trimmedFullData[key]
              return acc
            }, {}),
          }
        : trimmedFullData

      setConfigSnapshot(nextInitialValues)
      return true
    } catch (error) {
      const summary = summarizeConfigErrors(error?.fieldErrors, sourceTab, {
        torznab_trackers: getValues('torznab_trackers'),
        movie_search_queries: getValues('movie_search_queries'),
        series_search_queries: getValues('series_search_queries'),
      })
      let errorMessage = error?.message || 'Failed to save configuration.'
      if (summary) {
        errorMessage = summary
      } else if (error?.fieldErrors) {
        const firstFieldError = Object.values(error.fieldErrors).find((message) => typeof message === 'string' && message.trim() !== '')
        if (firstFieldError) {
          errorMessage = firstFieldError
        }
      }
      console.error('Error saving configuration:', errorMessage, error)
      if (sourceTab !== 'network' && sourceTab !== 'advanced') {
        showFooterStatus({ type: 'error', message: errorMessage })
      }
      setError('root', { message: `Failed to save configuration: ${errorMessage}` })
      throw error
    }
  }, [activeTab, configSnapshot, getValues, sendCommand, setConfigSnapshot, setError, showFooterStatus])

  const handleNetworkPersist = useCallback((payload, cardId = 'network') => {
    setLastSettingsSaveCard(cardId)
    return submitSettings(payload, 'network')
  }, [submitSettings])

  const handleAdvancedPersist = useCallback((payload, cardId = 'advanced') => {
    setLastSettingsSaveCard(cardId)
    return submitSettings(payload, 'advanced')
  }, [submitSettings])

  const handleClearCache = useCallback(async () => {
    showFooterStatus({ type: 'normal', message: 'Clearing search cache...' })
    try {
      const data = await apiFetch('/api/cache/clear', { method: 'POST' })
      showFooterStatus({ type: 'success', message: data?.message || 'Search cache cleared.' })
    } catch (error) {
      showFooterStatus({ type: 'error', message: error?.message || 'Failed to clear search cache.' })
    }
  }, [showFooterStatus])

  return {
    advancedInitialValues,
    clearTransientStatus,
    footerStatusVisible,
    handleAdvancedPersist,
    handleClearCache,
    handleNetworkPersist,
    networkInitialValues,
    showFooterStatus,
    submitSettings,
    tabsWithErrors,
    visibleFooterStatus,
  }
}
