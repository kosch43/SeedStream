import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from "@/components/ui/chart"
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"
import { apiFetch } from "@/api"

// ── Utilities ────────────────────────────────────────────────────────────────

function toNumber(v, fallback = 0) {
  const n = Number(v)
  return Number.isFinite(n) ? n : fallback
}

function pick(obj, snake, pascal, fallback = undefined) {
  if (!obj || typeof obj !== 'object') return fallback
  if (obj[snake] != null) return obj[snake]
  if (obj[pascal] != null) return obj[pascal]
  return fallback
}

function parseDateValue(raw) {
  if (!raw) return null
  const d = new Date(`${raw}T00:00:00`)
  return Number.isNaN(d.getTime()) ? null : d
}

function formatDownloadedBytes(bytes) {
  const mb = toNumber(bytes) / (1024 * 1024)
  return mb >= 1024 ? `${(mb / 1024).toFixed(2)} GB` : `${mb.toFixed(1)} MB`
}

function fmt1(v) { return toNumber(v).toFixed(1) }
function fmtPct(v) { return `${fmt1(v)}%` }

function fmtDate(date) {
  const y = date.getFullYear()
  const m = String(date.getMonth() + 1).padStart(2, '0')
  const d = String(date.getDate()).padStart(2, '0')
  return `${y}-${m}-${d}`
}

function defaultRange() {
  const end = new Date()
  const start = new Date(end)
  start.setDate(end.getDate() - 30)
  return { from: fmtDate(start), to: fmtDate(end) }
}

function rangeFromPreset(preset) {
  if (preset === 'all') return { from: '', to: '' }
  const days = { '7d': 7, '30d': 30, '90d': 90 }[preset] || 30
  const end = new Date()
  const start = new Date(end)
  start.setDate(end.getDate() - days)
  return { from: fmtDate(start), to: fmtDate(end) }
}

function normalizeIndexerRow(item) {
  const name = String(pick(item, 'indexer_name', 'IndexerName', '') || '').trim()
  if (!name) return null
  return {
    name,
    searches:         toNumber(pick(item, 'searches', 'Searches')),
    searchSharePct:   toNumber(pick(item, 'search_share_pct', 'SearchSharePct')),
    avgResponseMs:    toNumber(pick(item, 'avg_response_ms', 'AvgResponseMS')),
    avgResults:       toNumber(pick(item, 'avg_results', 'AvgResults')),
    successRatePct:   toNumber(pick(item, 'success_rate_pct', 'SuccessRatePct')),
    downloads:        toNumber(pick(item, 'downloads', 'Downloads')),
    downloadSharePct: toNumber(pick(item, 'download_share_pct', 'DownloadSharePct')),
    uniqueDownloads:  toNumber(pick(item, 'unique_downloads', 'UniqueDownloads')),
    uniquenessScore:  toNumber(pick(item, 'avg_uniqueness_score', 'AvgUniquenessScore')),
  }
}

function normalizeProviderRow(item) {
  const name = String(pick(item, 'provider_name', 'ProviderName', '') || '').trim()
  if (!name) return null
  return {
    name,
    host:            String(pick(item, 'host', 'Host', '')),
    articles:        toNumber(pick(item, 'articles', 'Articles')),
    available:       toNumber(pick(item, 'available', 'Available')),
    missing:         toNumber(pick(item, 'missing', 'Missing')),
    successRatePct:  toNumber(pick(item, 'success_rate_pct', 'SuccessRatePct')),
    downloadedBytes: toNumber(pick(item, 'downloaded_bytes', 'DownloadedBytes')),
    dataSharePct:    toNumber(pick(item, 'data_share_pct', 'DataSharePct')),
  }
}

function windowAvgResponseMs(rows) {
  let tw = 0, ts = 0
  for (const r of rows) {
    if (r.avgResponseMs > 0 && r.searches > 0) { tw += r.avgResponseMs * r.searches; ts += r.searches }
  }
  return ts === 0 ? 0 : tw / ts
}

const WEEKDAY_LABELS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']

function buildHourData(arr) {
  return Array.from({ length: 24 }, (_, h) => ({ label: String(h).padStart(2, '0'), count: toNumber(arr[h]) }))
}
function buildWeekdayData(arr) {
  return Array.from({ length: 7 }, (_, w) => ({ label: WEEKDAY_LABELS[w], count: toNumber(arr[w]) }))
}

// ── Shared UI components ─────────────────────────────────────────────────────

function HelpIcon({ text }) {
  return (
    <TooltipProvider delayDuration={200}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            className="ml-1 inline-flex h-[14px] w-[14px] items-center justify-center rounded-full border border-muted-foreground/40 text-[9px] font-normal text-muted-foreground leading-none hover:border-foreground hover:text-foreground transition-colors align-middle"
            tabIndex={-1}
          >
            ?
          </button>
        </TooltipTrigger>
        <TooltipContent side="top" className="max-w-xs text-xs leading-relaxed">
          {text}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

function Th({ label, help, right = false }) {
  return (
    <th className={`px-3 py-2 font-medium text-xs text-muted-foreground whitespace-nowrap ${right ? 'text-right' : 'text-left'}`}>
      {label}
      {help && <HelpIcon text={help} />}
    </th>
  )
}

function Td({ children, right = false, className = '' }) {
  return (
    <td className={`px-3 py-[7px] text-sm ${right ? 'text-right tabular-nums' : ''} ${className}`}>
      {children}
    </td>
  )
}

function EmptyRow({ cols, message = 'No data for this window.' }) {
  return (
    <tr>
      <td colSpan={cols} className="px-3 py-6 text-center text-sm text-muted-foreground">
        {message}
      </td>
    </tr>
  )
}

function StatsCard({ title, children }) {
  return (
    <Card>
      <CardHeader className="pb-0 pt-4 px-4">
        <CardTitle className="text-sm font-semibold">{title}</CardTitle>
      </CardHeader>
      <CardContent className="px-0 pt-2 pb-0">
        {children}
      </CardContent>
    </Card>
  )
}

function SimpleTable({ children }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse">
        {children}
      </table>
    </div>
  )
}

function HBarChart({ data, unit = '' }) {
  if (data.length === 0) return null
  const height = Math.max(140, data.length * 34)
  const config = { value: { label: 'Value', color: 'hsl(var(--primary))' } }
  return (
    <div className="px-4 pt-2 pb-1">
      <ChartContainer config={config} className="w-full" style={{ height: `${height}px` }}>
        <BarChart data={data} layout="vertical" margin={{ top: 2, right: 12, left: 4, bottom: 2 }}>
          <CartesianGrid horizontal={false} />
          <XAxis type="number" tick={{ fontSize: 11 }} unit={unit} />
          <YAxis type="category" dataKey="name" width={140} tick={{ fontSize: 11 }} />
          <ChartTooltip content={<ChartTooltipContent />} />
          <Bar dataKey="value" fill="var(--color-value)" radius={3} name="value" />
        </BarChart>
      </ChartContainer>
    </div>
  )
}

function TimeBarChart({ data }) {
  const empty = data.every((d) => d.count === 0)
  if (empty) {
    return <div className="px-4 py-6 text-center text-sm text-muted-foreground">No data for this window.</div>
  }
  const config = { count: { label: 'Count', color: 'hsl(var(--primary))' } }
  return (
    <div className="px-4 pt-2 pb-3">
      <ChartContainer config={config} className="w-full" style={{ height: '150px' }}>
        <BarChart data={data} margin={{ top: 4, right: 4, left: -20, bottom: 4 }}>
          <CartesianGrid vertical={false} />
          <XAxis dataKey="label" tick={{ fontSize: 10 }} />
          <YAxis tick={{ fontSize: 10 }} allowDecimals={false} />
          <ChartTooltip content={<ChartTooltipContent />} />
          <Bar dataKey="count" fill="var(--color-count)" radius={2} name="count" />
        </BarChart>
      </ChartContainer>
    </div>
  )
}

// ── Section toggle label map ─────────────────────────────────────────────────

const SECTION_LABELS = {
  avgResponseTimes:  'Avg. response times',
  apiAccesses:       'Indexer API accesses',
  nzbDownloads:      'NZB downloads per indexer',
  indexerScores:     'Indexer scores',
  searchesByHour:    'Searches per hour of day',
  searchesByWeekday: 'Searches per day of week',
  downloadsByHour:   'NZB downloads per hour of day',
  downloadsByWeekday:'NZB downloads per day of week',
}

// ── Main component ────────────────────────────────────────────────────────────

export function StatisticsPage() {
  const [indexerStats, setIndexerStats] = useState([])
  const [providerStats, setProviderStats] = useState([])
  const [timeDist, setTimeDist] = useState(null)
  const [preset, setPreset] = useState('30d')
  const [customRange, setCustomRange] = useState(defaultRange())
  const [activeRange, setActiveRange] = useState(defaultRange())
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
    if (!background) { setLoading(true); setLoadError('') }
    try {
      const qs = new URLSearchParams()
      if (from) qs.set('from', from)
      if (to) qs.set('to', to)
      const q = qs.toString()
      const [idxData, provData] = await Promise.all([
        apiFetch(`/api/stats/indexers${q ? `?${q}` : ''}`),
        apiFetch(`/api/stats/providers${q ? `?${q}` : ''}`),
      ])
      setIndexerStats((Array.isArray(idxData?.indexers) ? idxData.indexers : []).map(normalizeIndexerRow).filter(Boolean))
      setProviderStats((Array.isArray(provData?.providers) ? provData.providers : []).map(normalizeProviderRow).filter(Boolean))
      setTimeDist(idxData?.time_distribution ?? null)
    } catch (err) {
      if (!background) {
        setLoadError(err?.message || 'Failed to load statistics.')
        setIndexerStats([]); setProviderStats([]); setTimeDist(null)
      }
    } finally {
      if (!background) setLoading(false)
      inFlightRef.current = false
    }
  }, [])

  useEffect(() => {
    if (preset === 'custom') return
    const r = rangeFromPreset(preset)
    setActiveRange(r)
    void loadStats(r.from, r.to)
  }, [loadStats, preset])

  useEffect(() => {
    let tid = null; let cancelled = false
    const poll = async () => {
      if (cancelled) return
      if (!document.hidden) await loadStats(activeRange.from, activeRange.to, { background: true })
      if (!cancelled) tid = window.setTimeout(poll, 7000)
    }
    const onVisibility = () => {
      if (document.hidden || cancelled) return
      if (tid != null) { clearTimeout(tid); tid = null }
      void poll()
    }
    tid = window.setTimeout(poll, 7000)
    document.addEventListener('visibilitychange', onVisibility)
    return () => { cancelled = true; document.removeEventListener('visibilitychange', onVisibility); if (tid) clearTimeout(tid) }
  }, [activeRange.from, activeRange.to, loadStats])

  const rangeError = useMemo(() => {
    if (preset !== 'custom') return ''
    const f = parseDateValue(customRange.from), t = parseDateValue(customRange.to)
    if (!f || !t) return 'Select both dates.'
    const today = new Date(); today.setHours(0, 0, 0, 0)
    if (t < f) return 'To date must be on or after From date.'
    if (f > today || t > today) return 'Future dates not allowed.'
    return ''
  }, [customRange.from, customRange.to, preset])

  const winAvgMs = useMemo(() => windowAvgResponseMs(indexerStats), [indexerStats])

  const daysInWindow = useMemo(() => {
    if (!activeRange.from || !activeRange.to) return null
    const f = parseDateValue(activeRange.from), t = parseDateValue(activeRange.to)
    if (!f || !t) return null
    return Math.max(1, Math.round((t - f) / 86400000))
  }, [activeRange])

  const rangeLabel = useMemo(() => {
    return `${activeRange.from || 'Beginning'} – ${activeRange.to || 'Now'}`
  }, [activeRange])

  // ── Derived data per section ────────────────────────────────────────────────

  // Section 1: Avg Response Times — sorted fastest first
  const respSorted = useMemo(() =>
    [...indexerStats].filter(r => r.searches > 0).sort((a, b) => a.avgResponseMs - b.avgResponseMs),
    [indexerStats])

  const respChartData = useMemo(() =>
    respSorted.filter(r => r.avgResponseMs > 0).map(r => ({ name: r.name, value: Math.round(r.avgResponseMs) })),
    [respSorted])

  // Section 2: API Accesses — sorted by searches desc
  const apiSorted = useMemo(() =>
    [...indexerStats].sort((a, b) => b.searches - a.searches),
    [indexerStats])

  const apiChartData = useMemo(() =>
    apiSorted.map(r => ({ name: r.name, value: r.searches })),
    [apiSorted])

  // Section 3: NZB Downloads — sorted by downloads desc
  const dlSorted = useMemo(() =>
    [...indexerStats].sort((a, b) => b.downloads - a.downloads),
    [indexerStats])

  const dlChartData = useMemo(() =>
    dlSorted.map(r => ({ name: r.name, value: r.downloads })),
    [dlSorted])

  // Section 4: Indexer Scores — sorted by score desc
  const scoreSorted = useMemo(() =>
    [...indexerStats].sort((a, b) => b.uniquenessScore - a.uniquenessScore),
    [indexerStats])

  // Time distribution
  const searchesByHour     = useMemo(() => buildHourData(timeDist?.searches_by_hour ?? new Array(24).fill(0)), [timeDist])
  const searchesByWeekday  = useMemo(() => buildWeekdayData(timeDist?.searches_by_weekday ?? new Array(7).fill(0)), [timeDist])
  const downloadsByHour    = useMemo(() => buildHourData(timeDist?.downloads_by_hour ?? new Array(24).fill(0)), [timeDist])
  const downloadsByWeekday = useMemo(() => buildWeekdayData(timeDist?.downloads_by_weekday ?? new Array(7).fill(0)), [timeDist])

  // Provider
  const provSorted = useMemo(() => [...providerStats].sort((a, b) => b.downloadedBytes - a.downloadedBytes), [providerStats])
  const provChartData = useMemo(() => provSorted.map(r => ({ name: r.name, value: r.dataSharePct })), [provSorted])

  // ── Render ──────────────────────────────────────────────────────────────────

  return (
    <div className="flex flex-col gap-4 py-4 md:gap-5 md:py-6 px-4 lg:px-6">

      {/* Date Range */}
      <Card>
        <CardContent className="pt-4 pb-4">
          <div className="flex flex-wrap items-center gap-2">
            <ToggleGroup
              type="single" value={preset}
              onValueChange={(v) => { if (!v) return; setPreset(v) }}
              variant="outline" size="sm" className="justify-start"
            >
              <ToggleGroupItem value="7d">7D</ToggleGroupItem>
              <ToggleGroupItem value="30d">30D</ToggleGroupItem>
              <ToggleGroupItem value="90d">90D</ToggleGroupItem>
              <ToggleGroupItem value="all">All</ToggleGroupItem>
              <ToggleGroupItem value="custom">Custom</ToggleGroupItem>
            </ToggleGroup>
            <span className="text-xs text-muted-foreground">
              {loading ? 'Loading…' : rangeLabel}
            </span>
          </div>

          {preset === 'custom' && (
            <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:items-end">
              <div>
                <div className="mb-1 text-xs text-muted-foreground">From</div>
                <Input type="date" value={customRange.from} className="h-8 sm:w-40 text-sm"
                  onChange={(e) => setCustomRange(p => ({ ...p, from: e.target.value }))} />
              </div>
              <div>
                <div className="mb-1 text-xs text-muted-foreground">To</div>
                <Input type="date" value={customRange.to} className="h-8 sm:w-40 text-sm"
                  onChange={(e) => setCustomRange(p => ({ ...p, to: e.target.value }))} />
              </div>
              <Button size="sm" variant="outline" disabled={loading || !!rangeError}
                onClick={() => { if (!rangeError) { setActiveRange(customRange); void loadStats(customRange.from, customRange.to) } }}>
                Apply
              </Button>
            </div>
          )}
          {preset === 'custom' && rangeError && (
            <div className="mt-1 text-xs text-destructive">{rangeError}</div>
          )}
        </CardContent>
      </Card>

      {loadError && (
        <div className="text-sm text-destructive px-1">{loadError}</div>
      )}

      {/* Section toggles */}
      <Card>
        <CardContent className="pt-3 pb-3">
          <div className="flex flex-wrap gap-x-5 gap-y-2">
            {Object.entries(SECTION_LABELS).map(([key, label]) => (
              <label key={key} htmlFor={`s-${key}`} className="flex items-center gap-1.5 cursor-pointer select-none">
                <Checkbox id={`s-${key}`} checked={sections[key]} onCheckedChange={() => toggleSection(key)} />
                <span className="text-xs">{label}</span>
              </label>
            ))}
          </div>
        </CardContent>
      </Card>

      {/* ── Avg. response times ── */}
      {sections.avgResponseTimes && (
        <StatsCard title="Avg. response times (in ms)">
          <HBarChart data={respChartData} unit=" ms" />
          <SimpleTable>
            <thead className="border-b border-border">
              <tr>
                <Th label="Indexer" />
                <Th label="Avg. response time (ms)" right
                  help="Average response time in milliseconds for successful API searches. Lower is better." />
                <Th label="Delta" right
                  help="Difference from the weighted average response time across all indexers in this window. Green = faster than average, red = slower." />
              </tr>
            </thead>
            <tbody>
              {respSorted.length === 0 && <EmptyRow cols={3} />}
              {respSorted.map((row) => {
                let deltaText = '—', deltaClass = 'text-muted-foreground'
                if (row.avgResponseMs > 0 && winAvgMs > 0) {
                  const d = row.avgResponseMs - winAvgMs, abs = Math.round(Math.abs(d))
                  deltaText = abs === 0 ? '0 ms' : `${d > 0 ? '+' : '−'}${abs} ms`
                  deltaClass = d > 0 ? 'text-red-500' : d < 0 ? 'text-green-500' : 'text-muted-foreground'
                }
                return (
                  <tr key={row.name} className="border-b border-border/40 hover:bg-muted/30 transition-colors">
                    <Td>{row.name}</Td>
                    <Td right>{row.avgResponseMs > 0 ? `${Math.round(row.avgResponseMs)} ms` : '—'}</Td>
                    <Td right className={deltaClass}>{deltaText}</Td>
                  </tr>
                )
              })}
            </tbody>
          </SimpleTable>
        </StatsCard>
      )}

      {/* ── Indexer API accesses ── */}
      {sections.apiAccesses && (
        <StatsCard title="Indexer API accesses">
          <HBarChart data={apiChartData} />
          <SimpleTable>
            <thead className="border-b border-border">
              <tr>
                <Th label="Indexer" />
                <Th label="Avg. per day" right
                  help="Average number of API search calls made to this indexer per day in the selected window." />
                <Th label="% successful" right
                  help="Percentage of API calls that returned a successful response (HTTP 200 with results)." />
                <Th label="% failed" right
                  help="Percentage of API calls that returned an error or timed out." />
              </tr>
            </thead>
            <tbody>
              {apiSorted.length === 0 && <EmptyRow cols={4} />}
              {apiSorted.map((row) => {
                const avgPerDay = daysInWindow && row.searches > 0
                  ? (row.searches / daysInWindow).toFixed(1) : '—'
                return (
                  <tr key={row.name} className="border-b border-border/40 hover:bg-muted/30 transition-colors">
                    <Td>{row.name}</Td>
                    <Td right>{avgPerDay}</Td>
                    <Td right>{fmtPct(row.successRatePct)}</Td>
                    <Td right>{fmtPct(100 - row.successRatePct)}</Td>
                  </tr>
                )
              })}
            </tbody>
          </SimpleTable>
        </StatsCard>
      )}

      {/* ── NZB downloads per indexer ── */}
      {sections.nzbDownloads && (
        <StatsCard title="NZB downloads per indexer">
          <HBarChart data={dlChartData} />
          <SimpleTable>
            <thead className="border-b border-border">
              <tr>
                <Th label="Indexer" />
                <Th label="Total" right
                  help="Total number of NZBs downloaded from this indexer in the selected window." />
                <Th label="% of all enabled" right
                  help="This indexer's share of all NZB downloads across all enabled indexers in the window." />
              </tr>
            </thead>
            <tbody>
              {dlSorted.length === 0 && <EmptyRow cols={3} />}
              {dlSorted.map((row) => (
                <tr key={row.name} className="border-b border-border/40 hover:bg-muted/30 transition-colors">
                  <Td>{row.name}</Td>
                  <Td right>{row.downloads}</Td>
                  <Td right>{fmtPct(row.downloadSharePct)}</Td>
                </tr>
              ))}
            </tbody>
          </SimpleTable>
        </StatsCard>
      )}

      {/* ── Indexer scores ── */}
      {sections.indexerScores && (
        <StatsCard title="Indexer scores">
          <SimpleTable>
            <thead className="border-b border-border">
              <tr>
                <Th label="Indexer" />
                <Th label="Avg. score" right
                  help="The results uniqueness score determines how unique a downloaded result is to the indexer. A high score means the indexer often returned results which were either only downloadable from that indexer or 'could've been' downloaded from it. Formula: 100 × indexers searched / indexers returning the same result." />
                <Th label="# of dl searches" right
                  help="Number of times this indexer's results were selected for a download." />
                <Th label="Unique downloads" right
                  help="Number of downloads where only this indexer returned the result — i.e. it was the sole source." />
              </tr>
            </thead>
            <tbody>
              {scoreSorted.length === 0 && <EmptyRow cols={4} />}
              {scoreSorted.map((row) => (
                <tr key={row.name} className="border-b border-border/40 hover:bg-muted/30 transition-colors">
                  <Td>{row.name}</Td>
                  <Td right>{row.uniquenessScore > 0 ? Math.round(row.uniquenessScore) : '—'}</Td>
                  <Td right>{row.downloads}</Td>
                  <Td right>{row.uniqueDownloads}</Td>
                </tr>
              ))}
            </tbody>
          </SimpleTable>
          <p className="px-3 py-2 text-[11px] text-muted-foreground border-t border-border/40">
            Don&apos;t read too much into these stats. Which indexer is picked for a download depends on its score and some more or less random values.
          </p>
        </StatsCard>
      )}

      {/* ── Time distribution ── */}
      {sections.searchesByHour && (
        <StatsCard title="Searches per hour of day">
          <TimeBarChart data={searchesByHour} />
        </StatsCard>
      )}

      {sections.searchesByWeekday && (
        <StatsCard title="Searches per day of week">
          <TimeBarChart data={searchesByWeekday} />
        </StatsCard>
      )}

      {sections.downloadsByHour && (
        <StatsCard title="NZB downloads per hour of day">
          <TimeBarChart data={downloadsByHour} />
        </StatsCard>
      )}

      {sections.downloadsByWeekday && (
        <StatsCard title="NZB downloads per day of week">
          <TimeBarChart data={downloadsByWeekday} />
        </StatsCard>
      )}

      {/* ── Provider statistics ── */}
      <StatsCard title="Provider statistics">
        {provChartData.length > 0 && (
          <div className="px-4 pt-2 pb-1">
            <ChartContainer
              config={{ value: { label: 'Data share %', color: 'hsl(var(--primary))' } }}
              className="w-full"
              style={{ height: `${Math.max(140, provChartData.length * 34)}px` }}
            >
              <BarChart data={provChartData} layout="vertical" margin={{ top: 2, right: 12, left: 4, bottom: 2 }}>
                <CartesianGrid horizontal={false} />
                <XAxis type="number" tick={{ fontSize: 11 }} unit="%" />
                <YAxis type="category" dataKey="name" width={140} tick={{ fontSize: 11 }} />
                <ChartTooltip content={<ChartTooltipContent />} />
                <Bar dataKey="value" fill="hsl(var(--primary))" radius={3} name="value" />
              </BarChart>
            </ChartContainer>
          </div>
        )}
        <SimpleTable>
          <thead className="border-b border-border">
            <tr>
              <Th label="Provider" />
              <Th label="Articles" right help="Total article checks (available + missing) in this window." />
              <Th label="Available" right help="Articles confirmed present on the server." />
              <Th label="Missing" right help="Articles that were not found on the server (404)." />
              <Th label="Success" right help="Percentage of article checks that returned an available result." />
              <Th label="Downloaded" right help="Total data downloaded from this provider in the window." />
              <Th label="Data share" right help="This provider's share of all downloaded bytes across all providers." />
            </tr>
          </thead>
          <tbody>
            {provSorted.length === 0 && <EmptyRow cols={7} />}
            {provSorted.map((row) => (
              <tr key={row.name} className="border-b border-border/40 hover:bg-muted/30 transition-colors">
                <Td>
                  <div>{row.name}</div>
                  {row.host && <div className="text-xs text-muted-foreground">{row.host}</div>}
                </Td>
                <Td right>{row.articles}</Td>
                <Td right>{row.available}</Td>
                <Td right>{row.missing}</Td>
                <Td right>{fmtPct(row.successRatePct)}</Td>
                <Td right>{formatDownloadedBytes(row.downloadedBytes)}</Td>
                <Td right>{fmtPct(row.dataSharePct)}</Td>
              </tr>
            ))}
          </tbody>
        </SimpleTable>
      </StatsCard>

    </div>
  )
}
