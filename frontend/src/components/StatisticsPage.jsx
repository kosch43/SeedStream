import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { Checkbox } from "@/components/ui/checkbox"
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
  const days = { '7d': 7, '30d': 30, '90d': 90 }[preset] || 30
  const end = new Date()
  const start = new Date(end)
  start.setDate(end.getDate() - days)
  return { from: formatDateInput(start), to: formatDateInput(end) }
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

const timeDistChartConfig = {
  count: { label: "Count", color: "hsl(var(--primary))" },
}

const hBarChartConfig = {
  value: { label: "Value", color: "hsl(var(--primary))" },
}

function allZero(arr) { return arr.every((v) => v === 0) }

function buildHourChartData(arr) {
  return Array.from({ length: 24 }, (_, h) => ({
    label: String(h).padStart(2, '0'),
    count: toNumber(arr[h]),
  }))
}

const WEEKDAY_LABELS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
function buildWeekdayChartData(arr) {
  return Array.from({ length: 7 }, (_, w) => ({
    label: WEEKDAY_LABELS[w],
    count: toNumber(arr[w]),
  }))
}

function EmptyState({ message = 'No data for this window.' }) {
  return (
    <div className="flex items-center justify-center py-8 text-sm text-muted-foreground">
      {message}
    </div>
  )
}

function SectionHeader({ id, label, checked, onToggle }) {
  return (
    <label
      htmlFor={id}
      className="flex items-center gap-2 cursor-pointer select-none"
    >
      <Checkbox
        id={id}
        checked={checked}
        onCheckedChange={onToggle}
      />
      <span className="text-sm font-medium">{label}</span>
    </label>
  )
}

function HorizontalBarSection({ data, dataKey = 'value', unit = '' }) {
  if (data.length === 0) return null
  const height = Math.max(160, data.length * 36)
  return (
    <ChartContainer config={hBarChartConfig} className="w-full" style={{ height: `${height}px` }}>
      <BarChart data={data} layout="vertical" margin={{ top: 4, right: 16, left: 8, bottom: 4 }}>
        <CartesianGrid horizontal={false} />
        <XAxis type="number" tick={{ fontSize: 11 }} unit={unit} />
        <YAxis type="category" dataKey="name" width={150} tick={{ fontSize: 11 }} />
        <ChartTooltip content={<ChartTooltipContent />} />
        <Bar dataKey={dataKey} fill="var(--color-value)" radius={3} name={dataKey} />
      </BarChart>
    </ChartContainer>
  )
}

function TimeDistBarChart({ data }) {
  const empty = data.every((d) => d.count === 0)
  if (empty) return <EmptyState />
  return (
    <ChartContainer config={timeDistChartConfig} className="w-full" style={{ height: '160px' }}>
      <BarChart data={data} margin={{ top: 4, right: 4, left: -20, bottom: 4 }}>
        <CartesianGrid vertical={false} />
        <XAxis dataKey="label" tick={{ fontSize: 10 }} />
        <YAxis tick={{ fontSize: 10 }} allowDecimals={false} />
        <ChartTooltip content={<ChartTooltipContent />} />
        <Bar dataKey="count" fill="var(--color-count)" radius={2} name="count" />
      </BarChart>
    </ChartContainer>
  )
}

function StatsTable({ headers, rows, emptyMessage = 'No data for this window.' }) {
  if (rows.length === 0) return <EmptyState message={emptyMessage} />
  return (
    <div className="overflow-x-auto rounded-md border border-border/60">
      <table className="w-full text-sm">
        <thead className="bg-muted/40 text-muted-foreground">
          <tr>
            {headers.map((h) => (
              <th
                key={h.label}
                className={`px-3 py-2 font-medium whitespace-nowrap ${h.right ? 'text-right' : 'text-left'}`}
              >
                {h.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, i) => (
            <tr key={i} className="border-t border-border/50">
              {row.cells.map((cell, j) => (
                <td
                  key={j}
                  className={`px-3 py-2 ${headers[j]?.right ? 'text-right tabular-nums' : ''} ${cell.className || ''}`}
                >
                  {cell.value}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

const SECTION_LABELS = {
  avgResponseTimes: 'Avg. response times',
  apiAccesses: 'Indexer API accesses',
  nzbDownloads: 'NZB downloads per indexer',
  indexerScores: 'Indexer scores',
  searchesByHour: 'Searches per hour of day',
  searchesByWeekday: 'Searches per day of week',
  downloadsByHour: 'NZB downloads per hour of day',
  downloadsByWeekday: 'NZB downloads per day of week',
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
  const [sections, setSections] = useState(
    Object.fromEntries(Object.keys(SECTION_LABELS).map((k) => [k, true]))
  )
  const inFlightRef = useRef(false)

  function toggleSection(key) {
    setSections((prev) => ({ ...prev, [key]: !prev[key] }))
  }

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
      if (!background) setLoading(false)
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
      if (timeoutId != null) { window.clearTimeout(timeoutId); timeoutId = null }
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

  const windowAvgResponseMs = useMemo(() => computeWindowAvgResponseMs(indexerStats), [indexerStats])

  const daysInWindow = useMemo(() => {
    if (!activeRange.from || !activeRange.to) return null
    const from = parseDateValue(activeRange.from)
    const to = parseDateValue(activeRange.to)
    if (!from || !to) return null
    const diff = (to - from) / (1000 * 60 * 60 * 24)
    return Math.max(1, Math.round(diff))
  }, [activeRange])

  const rangeLabel = useMemo(() => {
    const from = activeRange?.from || 'Beginning'
    const to = activeRange?.to || 'Now'
    return `${from} – ${to}`
  }, [activeRange])

  // Section 1: Average Response Times — sorted by response time asc (fastest first)
  const responseTimeData = useMemo(() => {
    return [...indexerStats]
      .filter((r) => r.avgResponseMs > 0)
      .sort((a, b) => a.avgResponseMs - b.avgResponseMs)
      .map((r) => ({ name: r.name, value: Math.round(r.avgResponseMs) }))
  }, [indexerStats])

  const responseTimeRows = useMemo(() => {
    return [...indexerStats]
      .filter((r) => r.searches > 0)
      .sort((a, b) => a.avgResponseMs - b.avgResponseMs)
      .map((row) => {
        let deltaText = '—'
        let deltaClass = 'text-muted-foreground'
        if (row.avgResponseMs > 0 && windowAvgResponseMs > 0) {
          const delta = row.avgResponseMs - windowAvgResponseMs
          const abs = Math.round(Math.abs(delta))
          if (abs > 0) {
            deltaText = `${delta > 0 ? '+' : '−'}${abs} ms`
            deltaClass = delta > 0 ? 'text-red-500' : 'text-green-500'
          } else {
            deltaText = '0 ms'
          }
        }
        return {
          cells: [
            { value: row.name },
            { value: row.avgResponseMs > 0 ? `${Math.round(row.avgResponseMs)} ms` : 'N/A' },
            { value: deltaText, className: deltaClass },
          ],
        }
      })
  }, [indexerStats, windowAvgResponseMs])

  // Section 2: Indexer API Accesses — sorted by searches desc
  const apiAccessData = useMemo(() => {
    return [...indexerStats]
      .sort((a, b) => b.searches - a.searches)
      .map((r) => ({ name: r.name, value: r.searches }))
  }, [indexerStats])

  const apiAccessRows = useMemo(() => {
    return [...indexerStats]
      .sort((a, b) => b.searches - a.searches)
      .map((row) => {
        const avgPerDay = daysInWindow && row.searches > 0
          ? (row.searches / daysInWindow).toFixed(1)
          : '—'
        const failPct = row.successRatePct > 0
          ? formatPct(100 - row.successRatePct)
          : '0.0%'
        return {
          cells: [
            { value: row.name },
            { value: avgPerDay },
            { value: formatPct(row.successRatePct) },
            { value: failPct },
          ],
        }
      })
  }, [indexerStats, daysInWindow])

  // Section 3: NZB Downloads per Indexer — sorted by downloads desc
  const nzbDownloadData = useMemo(() => {
    return [...indexerStats]
      .sort((a, b) => b.downloads - a.downloads)
      .map((r) => ({ name: r.name, value: r.downloads }))
  }, [indexerStats])

  const nzbDownloadRows = useMemo(() => {
    return [...indexerStats]
      .sort((a, b) => b.downloads - a.downloads)
      .map((row) => ({
        cells: [
          { value: row.name },
          { value: row.downloads },
          { value: formatPct(row.downloadSharePct) },
        ],
      }))
  }, [indexerStats])

  // Section 4: Indexer Scores — sorted by score desc
  const indexerScoreRows = useMemo(() => {
    return [...indexerStats]
      .sort((a, b) => b.uniquenessScore - a.uniquenessScore)
      .map((row) => ({
        cells: [
          { value: row.name },
          { value: row.uniquenessScore > 0 ? row.uniquenessScore.toFixed(0) : '—' },
          { value: row.downloads },
          { value: row.uniqueDownloads },
        ],
      }))
  }, [indexerStats])

  const searchesByHour = useMemo(() =>
    buildHourChartData(timeDist?.searches_by_hour ?? new Array(24).fill(0)), [timeDist])
  const searchesByWeekday = useMemo(() =>
    buildWeekdayChartData(timeDist?.searches_by_weekday ?? new Array(7).fill(0)), [timeDist])
  const downloadsByHour = useMemo(() =>
    buildHourChartData(timeDist?.downloads_by_hour ?? new Array(24).fill(0)), [timeDist])
  const downloadsByWeekday = useMemo(() =>
    buildWeekdayChartData(timeDist?.downloads_by_weekday ?? new Array(7).fill(0)), [timeDist])

  const providerRows = useMemo(() =>
    [...providerStats].sort((a, b) => b.downloadedBytes - a.downloadedBytes), [providerStats])
  const providerChartData = useMemo(() =>
    providerRows.map((row) => ({ name: row.name, value: row.dataSharePct })), [providerRows])
  const providerChartHeight = Math.max(160, providerChartData.length * 36)

  return (
    <div className="flex flex-col gap-4 py-4 md:gap-6 md:py-6 px-4 lg:px-6">
      {/* Date range picker */}
      <Card>
        <CardHeader>
          <CardTitle>Date Range</CardTitle>
          <CardDescription>Statistics are aggregated from individual events over the selected window.</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <ToggleGroup
              type="single"
              value={preset}
              onValueChange={(value) => { if (!value) return; setPreset(value) }}
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
            <div className="text-xs text-muted-foreground">
              {loading ? 'Loading…' : `Showing: ${rangeLabel}`}
            </div>
          </div>

          {preset === 'custom' && (
            <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
              <div className="w-full sm:w-auto">
                <div className="mb-1 text-xs text-muted-foreground">From</div>
                <Input
                  type="date"
                  value={customRange.from}
                  onChange={(e) => setCustomRange((prev) => ({ ...prev, from: e.target.value }))}
                  className="h-9 sm:w-44"
                />
              </div>
              <div className="w-full sm:w-auto">
                <div className="mb-1 text-xs text-muted-foreground">To</div>
                <Input
                  type="date"
                  value={customRange.to}
                  onChange={(e) => setCustomRange((prev) => ({ ...prev, to: e.target.value }))}
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
                Apply
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

      {/* Section toggles — NZBHydra2 style checkbox list */}
      <Card>
        <CardContent className="pt-4">
          <div className="flex flex-wrap gap-x-6 gap-y-3">
            {Object.entries(SECTION_LABELS).map(([key, label]) => (
              <SectionHeader
                key={key}
                id={`section-toggle-${key}`}
                label={label}
                checked={sections[key]}
                onToggle={() => toggleSection(key)}
              />
            ))}
          </div>
        </CardContent>
      </Card>

      {/* ── Section 1: Average Response Times ── */}
      {sections.avgResponseTimes && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">Avg. response times (in ms)</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <HorizontalBarSection data={responseTimeData} unit=" ms" />
            <StatsTable
              headers={[
                { label: 'Indexer' },
                { label: 'Avg. response time (ms)', right: true },
                { label: 'Delta', right: true },
              ]}
              rows={responseTimeRows}
            />
          </CardContent>
        </Card>
      )}

      {/* ── Section 2: Indexer API Accesses ── */}
      {sections.apiAccesses && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">Indexer API accesses</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <HorizontalBarSection data={apiAccessData} />
            <StatsTable
              headers={[
                { label: 'Indexer' },
                { label: 'Avg. per day', right: true },
                { label: '% successful', right: true },
                { label: '% failed', right: true },
              ]}
              rows={apiAccessRows}
            />
          </CardContent>
        </Card>
      )}

      {/* ── Section 3: NZB Downloads per Indexer ── */}
      {sections.nzbDownloads && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">NZB downloads per indexer</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <HorizontalBarSection data={nzbDownloadData} />
            <StatsTable
              headers={[
                { label: 'Indexer' },
                { label: 'Total', right: true },
                { label: '% of all enabled', right: true },
              ]}
              rows={nzbDownloadRows}
            />
          </CardContent>
        </Card>
      )}

      {/* ── Section 4: Indexer Scores ── */}
      {sections.indexerScores && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">Indexer scores</CardTitle>
            <CardDescription className="text-xs">
              Don&apos;t read too much into these stats. The score is based on how often an indexer uniquely provides a result that was downloaded: 100 × indexers searched / indexers with this result.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <StatsTable
              headers={[
                { label: 'Indexer' },
                { label: 'Avg. score', right: true },
                { label: '# of dl searches', right: true },
                { label: 'Unique downloads', right: true },
              ]}
              rows={indexerScoreRows}
            />
          </CardContent>
        </Card>
      )}

      {/* ── Time distribution sections ── */}
      {sections.searchesByHour && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">Searches per hour of day</CardTitle>
          </CardHeader>
          <CardContent>
            <TimeDistBarChart data={searchesByHour} />
          </CardContent>
        </Card>
      )}

      {sections.searchesByWeekday && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">Searches per day of week</CardTitle>
          </CardHeader>
          <CardContent>
            <TimeDistBarChart data={searchesByWeekday} />
          </CardContent>
        </Card>
      )}

      {sections.downloadsByHour && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">NZB downloads per hour of day</CardTitle>
          </CardHeader>
          <CardContent>
            <TimeDistBarChart data={downloadsByHour} />
          </CardContent>
        </Card>
      )}

      {sections.downloadsByWeekday && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">NZB downloads per day of week</CardTitle>
          </CardHeader>
          <CardContent>
            <TimeDistBarChart data={downloadsByWeekday} />
          </CardContent>
        </Card>
      )}

      {/* ── Provider statistics ── */}
      <Card className="overflow-hidden">
        <CardHeader>
          <div className="flex items-center gap-2">
            <Database className="h-5 w-5 text-primary" />
            <CardTitle>Provider statistics</CardTitle>
          </div>
          <CardDescription>Article availability and downloaded volume aggregated per provider over the window.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <ChartContainer config={hBarChartConfig} className="w-full" style={{ height: `${providerChartHeight}px` }}>
            <BarChart data={providerChartData} layout="vertical" margin={{ top: 4, right: 16, left: 8, bottom: 4 }}>
              <CartesianGrid horizontal={false} />
              <XAxis type="number" tick={{ fontSize: 11 }} unit="%" />
              <YAxis type="category" dataKey="name" width={150} tick={{ fontSize: 11 }} />
              <ChartTooltip content={<ChartTooltipContent />} />
              <Bar dataKey="value" fill="var(--color-value)" radius={3} name="value" />
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
                      <div className="truncate">{row.name}</div>
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
                    <td colSpan={7} className="px-3 py-6 text-center text-muted-foreground">
                      No provider statistics in this window.
                    </td>
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
