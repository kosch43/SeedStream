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

// Compute weighted average response time across all indexers that have data.
function computeWindowAvgResponseMs(rows) {
  let totalWeightedMs = 0
  let totalSearches = 0
  for (const row of rows) {
    if (row.avgResponseMs > 0 && row.searches > 0) {
      totalWeightedMs += row.avgResponseMs * row.searches
      totalSearches += row.searches
    }
  }
  if (totalSearches === 0) return 0
  return totalWeightedMs / totalSearches
}

// All indexer table columns (used for sorting and rendering).
const INDEXER_COLUMNS = [
  { key: 'name',             label: 'Indexer',           align: 'left'  },
  { key: 'searches',         label: 'Searches',          align: 'right' },
  { key: 'searchSharePct',   label: 'Search share %',    align: 'right' },
  { key: 'avgResponseMs',    label: 'Avg response (ms)', align: 'right' },
  { key: 'delta',            label: 'Delta',             align: 'right', noSort: true },
  { key: 'avgResults',       label: 'Avg results',       align: 'right' },
  { key: 'successRatePct',   label: 'Success %',         align: 'right' },
  { key: 'downloads',        label: 'Downloads',         align: 'right' },
  { key: 'downloadSharePct', label: 'Download share %',  align: 'right' },
  { key: 'uniqueDownloads',  label: 'Unique downloads',  align: 'right' },
  { key: 'uniquenessScore',  label: 'Uniqueness score',  align: 'right' },
]

const providerChartConfig = {
  data: {
    label: "Data share %",
    color: "hsl(var(--primary))",
  },
}

const indexerDownloadChartConfig = {
  value: {
    label: "Download share %",
    color: "hsl(var(--primary))",
  },
}

const timeDistChartConfig = {
  count: {
    label: "Count",
    color: "hsl(var(--primary))",
  },
}

// Check whether all values in an array are zero.
function allZero(arr) {
  return arr.every((v) => v === 0)
}

// Build hour-of-day chart data (0..23).
function buildHourChartData(arr) {
  return Array.from({ length: 24 }, (_, h) => ({
    label: String(h).padStart(2, '0'),
    count: toNumber(arr[h]),
  }))
}

// Build weekday chart data (0=Sun..6=Sat).
const WEEKDAY_LABELS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
function buildWeekdayChartData(arr) {
  return Array.from({ length: 7 }, (_, w) => ({
    label: WEEKDAY_LABELS[w],
    count: toNumber(arr[w]),
  }))
}

function SortIcon({ direction }) {
  if (!direction) return <span className="ml-1 opacity-30 text-xs">↕</span>
  return <span className="ml-1 text-xs">{direction === 'asc' ? '↑' : '↓'}</span>
}

function TimeDistChart({ title, data }) {
  const empty = data.every((d) => d.count === 0)
  return (
    <Card className="overflow-hidden">
      <CardHeader className="pb-2">
        <CardTitle className="text-sm font-medium">{title}</CardTitle>
      </CardHeader>
      <CardContent>
        {empty ? (
          <div className="flex items-center justify-center h-32 text-sm text-muted-foreground">
            No data for this window.
          </div>
        ) : (
          <ChartContainer config={timeDistChartConfig} className="w-full" style={{ height: '160px' }}>
            <BarChart data={data} margin={{ top: 4, right: 4, left: -20, bottom: 4 }}>
              <CartesianGrid vertical={false} />
              <XAxis dataKey="label" tick={{ fontSize: 10 }} />
              <YAxis tick={{ fontSize: 10 }} allowDecimals={false} />
              <ChartTooltip content={<ChartTooltipContent />} />
              <Bar dataKey="count" fill="var(--color-count)" radius={2} name="count" />
            </BarChart>
          </ChartContainer>
        )}
      </CardContent>
    </Card>
  )
}

export function StatisticsPage() {
  const [indexerStats, setIndexerStats] = useState([])
  const [providerStats, setProviderStats] = useState([])
  const [timeDist, setTimeDist] = useState(null)
  const [preset, setPreset] = useState('30d')
  const [customRange, setCustomRange] = useState(defaultDateRange())
  const [activeRange, setActiveRange] = useState(defaultDateRange())
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState('')
  // Indexer table sort: default searches desc
  const [sortKey, setSortKey] = useState('searches')
  const [sortDir, setSortDir] = useState('desc')
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
      setTimeDist(idxData?.time_distribution ?? null)
    } catch (error) {
      if (!background) {
        setLoadError(error?.message || 'Failed to load statistics.')
        setIndexerStats([])
        setProviderStats([])
        setTimeDist(null)
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

  // Weighted mean response time across the window.
  const windowAvgResponseMs = useMemo(() => computeWindowAvgResponseMs(indexerStats), [indexerStats])

  // Sorted indexer rows (column click toggles asc/desc; default searches desc).
  const indexerRows = useMemo(() => {
    const rows = [...indexerStats]
    rows.sort((a, b) => {
      let aVal, bVal
      if (sortKey === 'name') {
        aVal = a.name
        bVal = b.name
        const cmp = aVal.localeCompare(bVal)
        return sortDir === 'asc' ? cmp : -cmp
      }
      aVal = toNumber(a[sortKey])
      bVal = toNumber(b[sortKey])
      if (aVal === bVal) return a.name.localeCompare(b.name)
      return sortDir === 'asc' ? aVal - bVal : bVal - aVal
    })
    return rows
  }, [indexerStats, sortKey, sortDir])

  // Summary totals row for indexer table.
  const indexerTotals = useMemo(() => {
    if (indexerStats.length === 0) return null
    const totalSearches = indexerStats.reduce((s, r) => s + r.searches, 0)
    const totalDownloads = indexerStats.reduce((s, r) => s + r.downloads, 0)
    const totalUniqueDownloads = indexerStats.reduce((s, r) => s + r.uniqueDownloads, 0)
    // Weighted avg response (only rows with data)
    const avgResp = windowAvgResponseMs
    // Weighted avg success rate
    let totalSuccessWeight = 0
    let totalSuccessSearches = 0
    for (const r of indexerStats) {
      if (r.searches > 0) {
        totalSuccessWeight += r.successRatePct * r.searches
        totalSuccessSearches += r.searches
      }
    }
    const avgSuccess = totalSuccessSearches > 0 ? totalSuccessWeight / totalSuccessSearches : 0
    return { totalSearches, totalDownloads, totalUniqueDownloads, avgResp, avgSuccess }
  }, [indexerStats, windowAvgResponseMs])

  // Download share chart (sorted by share desc).
  const indexerDownloadChartData = useMemo(() => {
    return [...indexerStats]
      .sort((a, b) => b.downloadSharePct - a.downloadSharePct)
      .map((row) => ({ name: row.name, value: row.downloadSharePct }))
  }, [indexerStats])

  const providerRows = useMemo(() => {
    return [...providerStats].sort((a, b) => b.downloadedBytes - a.downloadedBytes)
  }, [providerStats])

  const providerChartData = useMemo(() => providerRows.map((row) => ({
    name: row.name,
    data: row.dataSharePct,
  })), [providerRows])

  const rangeLabel = useMemo(() => {
    const from = activeRange?.from || 'Beginning'
    const to = activeRange?.to || 'Now'
    return `${from} - ${to}`
  }, [activeRange])

  // Time distribution chart data
  const searchesByHour = useMemo(() =>
    buildHourChartData(timeDist?.searches_by_hour ?? new Array(24).fill(0)),
    [timeDist])
  const searchesByWeekday = useMemo(() =>
    buildWeekdayChartData(timeDist?.searches_by_weekday ?? new Array(7).fill(0)),
    [timeDist])
  const downloadsByHour = useMemo(() =>
    buildHourChartData(timeDist?.downloads_by_hour ?? new Array(24).fill(0)),
    [timeDist])
  const downloadsByWeekday = useMemo(() =>
    buildWeekdayChartData(timeDist?.downloads_by_weekday ?? new Array(7).fill(0)),
    [timeDist])

  const indexerDownloadChartHeight = Math.max(220, indexerDownloadChartData.length * 42)
  const providerChartHeight = Math.max(220, providerChartData.length * 42)

  function handleSortClick(key) {
    setSortKey((prev) => {
      if (prev === key) {
        setSortDir((d) => d === 'asc' ? 'desc' : 'asc')
        return key
      }
      setSortDir('desc')
      return key
    })
  }

  function formatDelta(row) {
    if (row.avgResponseMs <= 0 || row.searches === 0 || windowAvgResponseMs === 0) return null
    const delta = row.avgResponseMs - windowAvgResponseMs
    const abs = Math.round(Math.abs(delta))
    if (abs === 0) return { text: '0 ms', color: 'text-muted-foreground' }
    const sign = delta > 0 ? '+' : '−'
    const color = delta > 0 ? 'text-red-500' : 'text-green-500'
    return { text: `${sign}${abs} ms`, color }
  }

  return (
    <div className="flex flex-col gap-4 py-4 md:gap-6 md:py-6 px-4 lg:px-6">
      {/* Date range picker */}
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

      {/* Indexer section — full width */}
      <Card className="overflow-hidden">
        <CardHeader>
          <div className="flex items-center gap-2">
            <Gauge className="h-5 w-5 text-primary" />
            <CardTitle>Indexer statistics</CardTitle>
          </div>
          <CardDescription>Searches, response times, success rate, downloads, and uniqueness score aggregated over the window.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {/* Download share bar chart — always visible, no chip selector */}
          <ChartContainer config={indexerDownloadChartConfig} className="w-full" style={{ height: `${indexerDownloadChartHeight}px` }}>
            <BarChart data={indexerDownloadChartData} layout="vertical" margin={{ top: 8, right: 12, left: 12, bottom: 8 }}>
              <CartesianGrid horizontal={false} />
              <XAxis type="number" tick={{ fontSize: 11 }} unit="%" />
              <YAxis type="category" dataKey="name" width={160} tick={{ fontSize: 11 }} />
              <ChartTooltip content={<ChartTooltipContent />} />
              <Bar dataKey="value" fill="var(--color-value)" radius={4} name="value" />
            </BarChart>
          </ChartContainer>

          {/* Full-width sortable table */}
          <div className="overflow-x-auto rounded-md border border-border/60">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-muted-foreground">
                <tr>
                  {INDEXER_COLUMNS.map((col) => (
                    <th
                      key={col.key}
                      className={`px-3 py-2 font-medium whitespace-nowrap ${col.align === 'right' ? 'text-right' : 'text-left'} ${col.noSort ? '' : 'cursor-pointer select-none hover:text-foreground'}`}
                      onClick={col.noSort ? undefined : () => handleSortClick(col.key)}
                    >
                      {col.label}
                      {!col.noSort && <SortIcon direction={sortKey === col.key ? sortDir : null} />}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {indexerRows.map((row) => {
                  const delta = formatDelta(row)
                  return (
                    <tr key={row.name} className="border-t border-border/50">
                      <td className="px-3 py-2 whitespace-nowrap">{row.name}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.searches}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{formatPct(row.searchSharePct)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.avgResponseMs > 0 ? `${row.avgResponseMs.toFixed(0)} ms` : 'N/A'}</td>
                      <td className={`px-3 py-2 text-right tabular-nums ${delta ? delta.color : 'text-muted-foreground'}`}>
                        {delta ? delta.text : '—'}
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.avgResults.toFixed(1)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{formatPct(row.successRatePct)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.downloads}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{formatPct(row.downloadSharePct)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.uniqueDownloads}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.uniquenessScore.toFixed(0)}</td>
                    </tr>
                  )
                })}
                {/* Totals row */}
                {indexerTotals && (
                  <tr className="border-t-2 border-border bg-muted/30 font-medium">
                    <td className="px-3 py-2">Total / Avg</td>
                    <td className="px-3 py-2 text-right tabular-nums">{indexerTotals.totalSearches}</td>
                    <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">&mdash;</td>
                    <td className="px-3 py-2 text-right tabular-nums">
                      {indexerTotals.avgResp > 0 ? `${indexerTotals.avgResp.toFixed(0)} ms` : 'N/A'}
                    </td>
                    <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">&mdash;</td>
                    <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">&mdash;</td>
                    <td className="px-3 py-2 text-right tabular-nums">{formatPct(indexerTotals.avgSuccess)}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{indexerTotals.totalDownloads}</td>
                    <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">&mdash;</td>
                    <td className="px-3 py-2 text-right tabular-nums">{indexerTotals.totalUniqueDownloads}</td>
                    <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">&mdash;</td>
                  </tr>
                )}
                {indexerRows.length === 0 && (
                  <tr>
                    <td colSpan={INDEXER_COLUMNS.length} className="px-3 py-6 text-center text-muted-foreground">No indexer statistics in this window.</td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </CardContent>
      </Card>

      {/* Time distribution charts — 2×2 grid */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <TimeDistChart title="Searches by hour of day" data={searchesByHour} />
        <TimeDistChart title="Searches by day of week" data={searchesByWeekday} />
        <TimeDistChart title="Downloads by hour of day" data={downloadsByHour} />
        <TimeDistChart title="Downloads by day of week" data={downloadsByWeekday} />
      </div>

      {/* Provider section — full width */}
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
  )
}
