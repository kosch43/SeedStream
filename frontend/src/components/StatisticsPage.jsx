import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from "@/components/ui/chart"
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"
import { Database, Gauge } from "lucide-react"
import { apiFetch } from "@/api"

function toNumber(value, fallback = 0) {
  const n = Number(value)
  return Number.isFinite(n) ? n : fallback
}

function pick(obj, snakeKey, pascalKey, fallback = undefined) {
  if (!obj || typeof obj !== 'object') return fallback
  if (obj[snakeKey] != null) return obj[snakeKey]
  if (obj[pascalKey] != null) return obj[pascalKey]
  return fallback
}

function parseDateValue(raw) {
  if (!raw) return null
  const d = new Date(`${raw}T00:00:00`)
  if (Number.isNaN(d.getTime())) return null
  return d
}

function formatDownloadedBytes(bytes) {
  const n = toNumber(bytes)
  const mb = n / (1024 * 1024)
  if (mb >= 1024) return `${(mb / 1024).toFixed(2)} GB`
  return `${mb.toFixed(1)} MB`
}

function formatPct(value) {
  return `${toNumber(value).toFixed(1)}%`
}

function formatDateInput(date) {
  const y = date.getFullYear()
  const m = String(date.getMonth() + 1).padStart(2, '0')
  const d = String(date.getDate()).padStart(2, '0')
  return `${y}-${m}-${d}`
}

function defaultDateRange() {
  const end = new Date()
  const start = new Date(end)
  start.setDate(end.getDate() - 30)
  return {
    from: formatDateInput(start),
    to: formatDateInput(end),
  }
}

function rangeFromPreset(preset) {
  if (preset === 'all') return { from: '', to: '' }
  const days = {
    '7d': 7,
    '30d': 30,
    '90d': 90,
  }[preset] || 30
  const end = new Date()
  const start = new Date(end)
  start.setDate(end.getDate() - days)
  return {
    from: formatDateInput(start),
    to: formatDateInput(end),
  }
}

// User-selectable bar-chart metric for the indexer section. No fabricated
// metrics: every option maps to a measured/aggregated value from the backend.
const indexerMetricOptions = {
  uniqueness: { label: 'Uniqueness score', key: 'uniquenessScore' },
  searches: { label: 'Searches', key: 'searches' },
  searchShare: { label: 'Search share %', key: 'searchSharePct' },
  response: { label: 'Avg response (ms)', key: 'avgResponseMs' },
  results: { label: 'Avg results', key: 'avgResults' },
  success: { label: 'Success %', key: 'successRatePct' },
  downloads: { label: 'Downloads', key: 'downloads' },
  downloadShare: { label: 'Download share %', key: 'downloadSharePct' },
  uniqueDownloads: { label: 'Unique downloads', key: 'uniqueDownloads' },
}

function normalizeIndexerRow(item) {
  const name = String(pick(item, 'indexer_name', 'IndexerName', '') || '').trim()
  if (!name) return null
  return {
    name,
    searches: toNumber(pick(item, 'searches', 'Searches')),
    searchSharePct: toNumber(pick(item, 'search_share_pct', 'SearchSharePct')),
    avgResponseMs: toNumber(pick(item, 'avg_response_ms', 'AvgResponseMS')),
    avgResults: toNumber(pick(item, 'avg_results', 'AvgResults')),
    successRatePct: toNumber(pick(item, 'success_rate_pct', 'SuccessRatePct')),
    downloads: toNumber(pick(item, 'downloads', 'Downloads')),
    downloadSharePct: toNumber(pick(item, 'download_share_pct', 'DownloadSharePct')),
    uniqueDownloads: toNumber(pick(item, 'unique_downloads', 'UniqueDownloads')),
    uniquenessScore: toNumber(pick(item, 'avg_uniqueness_score', 'AvgUniquenessScore')),
  }
}

function normalizeProviderRow(item) {
  const name = String(pick(item, 'provider_name', 'ProviderName', '') || '').trim()
  if (!name) return null
  return {
    name,
    host: String(pick(item, 'host', 'Host', '')),
    articles: toNumber(pick(item, 'articles', 'Articles')),
    available: toNumber(pick(item, 'available', 'Available')),
    missing: toNumber(pick(item, 'missing', 'Missing')),
    successRatePct: toNumber(pick(item, 'success_rate_pct', 'SuccessRatePct')),
    downloadedBytes: toNumber(pick(item, 'downloaded_bytes', 'DownloadedBytes')),
    dataSharePct: toNumber(pick(item, 'data_share_pct', 'DataSharePct')),
  }
}

const providerChartConfig = {
  data: {
    label: "Data share %",
    color: "hsl(var(--primary))",
  },
}

export function StatisticsPage() {
  const [indexerStats, setIndexerStats] = useState([])
  const [providerStats, setProviderStats] = useState([])
  const [preset, setPreset] = useState('30d')
  const [customRange, setCustomRange] = useState(defaultDateRange())
  const [activeRange, setActiveRange] = useState(defaultDateRange())
  const [indexerMetric, setIndexerMetric] = useState('uniqueness')
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState('')
  const inFlightRef = useRef(false)

  const loadStats = useCallback(async (from, to, { background = false } = {}) => {
    if (inFlightRef.current) return
    inFlightRef.current = true
    if (!background) {
      setLoading(true)
      setLoadError('')
    }
    try {
      const query = new URLSearchParams()
      if (from) query.set('from', from)
      if (to) query.set('to', to)
      const qs = query.toString()
      const [idxData, provData] = await Promise.all([
        apiFetch(`/api/stats/indexers${qs ? `?${qs}` : ''}`),
        apiFetch(`/api/stats/providers${qs ? `?${qs}` : ''}`),
      ])
      const indexers = (Array.isArray(idxData?.indexers) ? idxData.indexers : [])
        .map(normalizeIndexerRow)
        .filter(Boolean)
      const providers = (Array.isArray(provData?.providers) ? provData.providers : [])
        .map(normalizeProviderRow)
        .filter(Boolean)
      setIndexerStats(indexers)
      setProviderStats(providers)
    } catch (error) {
      if (!background) {
        setLoadError(error?.message || 'Failed to load statistics.')
        setIndexerStats([])
        setProviderStats([])
      }
    } finally {
      if (!background) {
        setLoading(false)
      }
      inFlightRef.current = false
    }
  }, [])

  useEffect(() => {
    if (preset === 'custom') return
    const nextRange = rangeFromPreset(preset)
    setActiveRange(nextRange)
    void loadStats(nextRange.from, nextRange.to)
  }, [loadStats, preset])

  useEffect(() => {
    let timeoutId = null
    let cancelled = false
    const pollDelayMs = 7000

    const poll = async () => {
      if (cancelled) return
      if (document.hidden) {
        timeoutId = window.setTimeout(poll, pollDelayMs)
        return
      }
      await loadStats(activeRange.from, activeRange.to, { background: true })
      if (cancelled) return
      timeoutId = window.setTimeout(poll, pollDelayMs)
    }

    const handleVisibilityChange = () => {
      if (document.hidden || cancelled) return
      if (timeoutId != null) {
        window.clearTimeout(timeoutId)
        timeoutId = null
      }
      void poll()
    }

    timeoutId = window.setTimeout(poll, pollDelayMs)
    document.addEventListener('visibilitychange', handleVisibilityChange)

    return () => {
      cancelled = true
      document.removeEventListener('visibilitychange', handleVisibilityChange)
      if (timeoutId != null) window.clearTimeout(timeoutId)
    }
  }, [activeRange.from, activeRange.to, loadStats])

  const customRangeValidation = useMemo(() => {
    if (preset !== 'custom') return ''
    const fromDate = parseDateValue(customRange.from)
    const toDate = parseDateValue(customRange.to)
    if (!fromDate || !toDate) return 'Select both dates.'
    const today = new Date()
    today.setHours(0, 0, 0, 0)
    if (toDate < fromDate) return 'To date must be on or after From date.'
    if (fromDate > today || toDate > today) return 'Future dates are not allowed.'
    return ''
  }, [customRange.from, customRange.to, preset])

  const indexerMetricKey = indexerMetricOptions[indexerMetric]?.key || 'uniquenessScore'

  const indexerRows = useMemo(() => {
    const rows = [...indexerStats]
    return rows.sort((a, b) => {
      const aMetric = toNumber(a[indexerMetricKey])
      const bMetric = toNumber(b[indexerMetricKey])
      if (aMetric === bMetric) return a.name.localeCompare(b.name)
      return bMetric - aMetric
    })
  }, [indexerStats, indexerMetricKey])

  const providerRows = useMemo(() => {
    return [...providerStats].sort((a, b) => b.downloadedBytes - a.downloadedBytes)
  }, [providerStats])

  const indexerChartData = useMemo(() => indexerRows.map((row) => ({
    name: row.name,
    value: toNumber(row[indexerMetricKey]),
  })), [indexerMetricKey, indexerRows])

  const providerChartData = useMemo(() => providerRows.map((row) => ({
    name: row.name,
    data: row.dataSharePct,
  })), [providerRows])

  const indexerChartConfig = useMemo(() => ({
    value: {
      label: indexerMetricOptions[indexerMetric]?.label || 'Metric',
      color: "hsl(var(--primary))",
    },
  }), [indexerMetric])

  const rangeLabel = useMemo(() => {
    const from = activeRange?.from || 'Beginning'
    const to = activeRange?.to || 'Now'
    return `${from} - ${to}`
  }, [activeRange])

  const indexerChartHeight = Math.max(220, indexerChartData.length * 42)
  const providerChartHeight = Math.max(220, providerChartData.length * 42)

  return (
    <div className="flex flex-col gap-4 py-4 md:gap-6 md:py-6 px-4 lg:px-6">
      <Card>
        <CardHeader>
          <CardTitle>Date Range</CardTitle>
          <CardDescription>Use quick presets or choose a custom range. Statistics are aggregated from individual events over the selected window.</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <ToggleGroup
              type="single"
              value={preset}
              onValueChange={(value) => {
                if (!value) return
                setPreset(value)
              }}
              variant="outline"
              size="sm"
              className="justify-start"
            >
              <ToggleGroupItem value="7d">7D</ToggleGroupItem>
              <ToggleGroupItem value="30d">30D</ToggleGroupItem>
              <ToggleGroupItem value="90d">90D</ToggleGroupItem>
              <ToggleGroupItem value="all">All</ToggleGroupItem>
              <ToggleGroupItem value="custom">Custom</ToggleGroupItem>
            </ToggleGroup>
            <div className="text-xs text-muted-foreground">{loading ? 'Loading...' : `Showing: ${rangeLabel}`}</div>
          </div>

          {preset === 'custom' && (
            <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
              <div className="w-full sm:w-auto">
                <div className="mb-1 text-xs text-muted-foreground">From</div>
                <Input
                  type="date"
                  value={customRange.from}
                  onChange={(event) => setCustomRange((prev) => ({ ...prev, from: event.target.value }))}
                  className="h-9 sm:w-44"
                />
              </div>
              <div className="w-full sm:w-auto">
                <div className="mb-1 text-xs text-muted-foreground">To</div>
                <Input
                  type="date"
                  value={customRange.to}
                  onChange={(event) => setCustomRange((prev) => ({ ...prev, to: event.target.value }))}
                  className="h-9 sm:w-44"
                />
              </div>
              <Button
                type="button"
                variant="outline"
                className="h-9 sm:min-w-28"
                disabled={loading || Boolean(customRangeValidation)}
                onClick={() => {
                  if (customRangeValidation) return
                  setActiveRange(customRange)
                  void loadStats(customRange.from, customRange.to)
                }}
              >
                Apply Custom
              </Button>
            </div>
          )}
          {preset === 'custom' && customRangeValidation && (
            <div className="text-xs text-destructive">{customRangeValidation}</div>
          )}
        </CardContent>
      </Card>

      {loadError && (
        <Card>
          <CardContent className="pt-6 text-sm text-destructive">{loadError}</CardContent>
        </Card>
      )}

      <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
        <Card className="overflow-hidden">
          <CardHeader>
            <div className="flex items-center gap-2">
              <Gauge className="h-5 w-5 text-primary" />
              <CardTitle>Indexer statistics</CardTitle>
            </div>
            <CardDescription>Searches, response times, success rate, downloads, and uniqueness score aggregated over the window.</CardDescription>
            <div className="pt-2">
              <ToggleGroup
                type="single"
                value={indexerMetric}
                onValueChange={(value) => {
                  if (!value) return
                  setIndexerMetric(value)
                }}
                variant="outline"
                size="sm"
                className="flex-wrap justify-start"
              >
                {Object.entries(indexerMetricOptions).map(([key, opt]) => (
                  <ToggleGroupItem key={key} value={key}>{opt.label}</ToggleGroupItem>
                ))}
              </ToggleGroup>
            </div>
          </CardHeader>
          <CardContent className="space-y-4">
            <ChartContainer config={indexerChartConfig} className="w-full" style={{ height: `${indexerChartHeight}px` }}>
              <BarChart data={indexerChartData} layout="vertical" margin={{ top: 8, right: 12, left: 12, bottom: 8 }}>
                <CartesianGrid horizontal={false} />
                <XAxis type="number" tick={{ fontSize: 11 }} />
                <YAxis type="category" dataKey="name" width={160} tick={{ fontSize: 11 }} />
                <ChartTooltip content={<ChartTooltipContent />} />
                <Bar dataKey="value" fill="var(--color-value)" radius={4} name="value" />
              </BarChart>
            </ChartContainer>

            <div className="overflow-x-auto rounded-md border border-border/60">
              <table className="w-full text-sm">
                <thead className="bg-muted/40 text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2 text-left font-medium">Indexer</th>
                    <th className="px-3 py-2 text-right font-medium">Searches</th>
                    <th className="px-3 py-2 text-right font-medium">Search share</th>
                    <th className="px-3 py-2 text-right font-medium">Avg response</th>
                    <th className="px-3 py-2 text-right font-medium">Avg results</th>
                    <th className="px-3 py-2 text-right font-medium">Success</th>
                    <th className="px-3 py-2 text-right font-medium">Downloads</th>
                    <th className="px-3 py-2 text-right font-medium">Download share</th>
                    <th className="px-3 py-2 text-right font-medium">Unique downloads</th>
                    <th className="px-3 py-2 text-right font-medium">Uniqueness score</th>
                  </tr>
                </thead>
                <tbody>
                  {indexerRows.map((row) => (
                    <tr key={row.name} className="border-t border-border/50">
                      <td className="px-3 py-2"><span className="truncate">{row.name}</span></td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.searches}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{formatPct(row.searchSharePct)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.avgResponseMs > 0 ? `${row.avgResponseMs.toFixed(0)} ms` : 'N/A'}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.avgResults.toFixed(1)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{formatPct(row.successRatePct)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.downloads}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{formatPct(row.downloadSharePct)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.uniqueDownloads}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.uniquenessScore.toFixed(0)}</td>
                    </tr>
                  ))}
                  {indexerRows.length === 0 && (
                    <tr>
                      <td colSpan={10} className="px-3 py-6 text-center text-muted-foreground">No indexer statistics in this window.</td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </CardContent>
        </Card>

        <Card className="overflow-hidden">
          <CardHeader>
            <div className="flex items-center gap-2">
              <Database className="h-5 w-5 text-primary" />
              <CardTitle>Provider statistics</CardTitle>
            </div>
            <CardDescription>Article availability and downloaded volume aggregated per provider over the window.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <ChartContainer config={providerChartConfig} className="w-full" style={{ height: `${providerChartHeight}px` }}>
              <BarChart data={providerChartData} layout="vertical" margin={{ top: 8, right: 12, left: 12, bottom: 8 }}>
                <CartesianGrid horizontal={false} />
                <XAxis type="number" tick={{ fontSize: 11 }} />
                <YAxis type="category" dataKey="name" width={160} tick={{ fontSize: 11 }} />
                <ChartTooltip content={<ChartTooltipContent />} />
                <Bar dataKey="data" fill="var(--color-data)" radius={4} name="data" />
              </BarChart>
            </ChartContainer>

            <div className="overflow-x-auto rounded-md border border-border/60">
              <table className="w-full text-sm">
                <thead className="bg-muted/40 text-muted-foreground">
                  <tr>
                    <th className="px-3 py-2 text-left font-medium">Provider</th>
                    <th className="px-3 py-2 text-right font-medium">Articles</th>
                    <th className="px-3 py-2 text-right font-medium">Available</th>
                    <th className="px-3 py-2 text-right font-medium">Missing</th>
                    <th className="px-3 py-2 text-right font-medium">Success</th>
                    <th className="px-3 py-2 text-right font-medium">Downloaded</th>
                    <th className="px-3 py-2 text-right font-medium">Data share</th>
                  </tr>
                </thead>
                <tbody>
                  {providerRows.map((row) => (
                    <tr key={row.name} className="border-t border-border/50">
                      <td className="px-3 py-2">
                        <div className="flex items-center gap-2"><span className="truncate">{row.name}</span></div>
                        {row.host && <div className="text-xs text-muted-foreground truncate">{row.host}</div>}
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.articles}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.available}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.missing}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{formatPct(row.successRatePct)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{formatDownloadedBytes(row.downloadedBytes)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{formatPct(row.dataSharePct)}</td>
                    </tr>
                  ))}
                  {providerRows.length === 0 && (
                    <tr>
                      <td colSpan={7} className="px-3 py-6 text-center text-muted-foreground">No provider statistics in this window.</td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </CardContent>
        </Card>
      </div>

    </div>
  )
}
