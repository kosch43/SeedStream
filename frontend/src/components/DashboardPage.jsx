import { useMemo, useState } from 'react'
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from "@/components/ui/chart"
import { ComposedChart, Area, XAxis, YAxis } from "recharts"
import { Activity, HardDrive, X, MonitorPlay } from "lucide-react"
import { cn } from "@/lib/utils"

const chartConfig = {
  speed: {
    label: "Download",
    color: "hsl(var(--primary))",
  },
  torrents: {
    label: "Active torrents",
    color: "hsl(var(--primary))",
  },
}

function formatDownloadedMb(mb) {
  const n = Number(mb) || 0
  if (n >= 1000) return { value: (n / 1000).toFixed(2), unit: 'GB' }
  return { value: n.toFixed(1), unit: 'MB' }
}

// formatClock renders seconds as h:mm:ss, or m:ss under an hour.
function formatClock(totalSeconds) {
  const s = Math.max(0, Math.floor(Number(totalSeconds) || 0))
  const hours = Math.floor(s / 3600)
  const minutes = Math.floor((s % 3600) / 60)
  const seconds = s % 60
  const pad = (n) => String(n).padStart(2, '0')
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(seconds)}` : `${minutes}:${pad(seconds)}`
}

// formatRunway describes how long the stream could keep playing if the download
// stopped now. Rounded to the unit a viewer would care about — the difference
// between 4 and 6 seconds matters, the difference between 11 and 13 minutes
// does not.
function formatRunway(seconds) {
  const s = Math.max(0, Math.floor(Number(seconds) || 0))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  return `${(s / 3600).toFixed(1)}h`
}

// StreamPosition shows where the viewer is in the title and how much downloaded
// video sits in front of them.
//
// The runway is the number that predicts a stall: overall download progress can
// climb steadily while the run ahead of the playhead does not move, and that
// gap is exactly what a viewer experiences as a freeze. Showing it means a
// struggling stream is visible here before it is visible on their screen.
function StreamPosition({ position }) {
  const hasTimeline = position.runtime_seconds > 0
  const percent = Math.min(100, Math.max(0, Number(position.percent) || 0))
  const runway = Number(position.runway_seconds) || 0
  // Under ten seconds the player is within moments of catching the download.
  const low = hasTimeline && position.runway_bytes > 0 && runway < 10
  const stalled = position.runway_bytes <= 0

  return (
    <div className="mt-2 space-y-1">
      <div className="h-1 w-full overflow-hidden rounded-full bg-muted">
        <div className="h-full rounded-full bg-primary transition-all" style={{ width: `${percent}%` }} />
      </div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground tabular-nums">
        <span>
          {hasTimeline
            ? `${formatClock(position.position_seconds)} / ${formatClock(position.runtime_seconds)}`
            : `${percent.toFixed(1)}%`}
        </span>
        <span aria-hidden="true">·</span>
        {stalled ? (
          <span className="text-destructive">waiting for data</span>
        ) : (
          <span className={cn(low && "text-destructive")}>
            {hasTimeline ? `${formatRunway(runway)} downloaded ahead` : 'buffered ahead'}
          </span>
        )}
      </div>
    </div>
  )
}

export function DashboardPage({ stats, chartData, sendCommand, config }) {
  const [activeSessionToClose, setActiveSessionToClose] = useState(null)

  const trackerUrls = useMemo(() => {
    const urls = new Map()
    ;(config?.indexers || []).forEach((idx) => {
      const name = (idx?.name || '').trim()
      if (!name) return
      urls.set(name, idx?.url || '')
    })
    return urls
  }, [config])

  const displayedClients = useMemo(() => {
    const liveByName = new Map(
      (stats?.torrent_clients || []).map((c) => [String(c.name || '').trim(), c])
    )
    return (config?.torrent_clients || []).map((tc) => {
      const name = (tc.name || '').trim() || 'qBittorrent'
      const live = liveByName.get(name)
      return {
        name,
        url: tc.url || '',
        category: tc.category || 'seedstream',
        savePath: tc.save_path || '',
        remotePath: tc.remote_path || '',
        enabled: tc.enabled !== false,
        // Per-client figures. Absent until the first stats tick arrives, which
        // is why these are null rather than 0 — an unknown value and a genuinely
        // idle client should not look the same.
        // Unknown until the first tick, and unknown again if the client could
        // not be reached — in neither case is zero a truthful answer.
        seeds: live && live.reachable !== false ? live.seeds : null,
        peers: live && live.reachable !== false ? live.peers : null,
        activeTorrents: live && live.reachable !== false ? live.active_torrents : null,
        totalTorrents: live && live.reachable !== false ? live.total_torrents : null,
        reachable: live ? live.reachable !== false : null,
      }
    })
  }, [config, stats])

  const displayedTrackers = useMemo(() => {
    const statMap = new Map((stats?.indexers || []).map((indexer) => [String(indexer.name || '').trim(), indexer]))
    const rows = []

    ;(config?.indexers || []).forEach((indexer) => {
      const name = String(indexer.name || '').trim()
      const stat = statMap.get(name)
      rows.push({
        name: stat?.name || name || 'Tracker',
        api_hits_used: stat?.api_hits_used ?? 0,
        api_hits_limit: stat?.api_hits_limit ?? Number(indexer.api_hits_day || 0),
        api_hits_remaining: stat?.api_hits_remaining ?? Number(indexer.api_hits_day || 0),
        downloads_used: stat?.downloads_used ?? 0,
        downloads_limit: stat?.downloads_limit ?? Number(indexer.downloads_day || 0),
        downloads_remaining: stat?.downloads_remaining ?? Number(indexer.downloads_day || 0),
        enabled: indexer.enabled !== false,
      })
      statMap.delete(name)
    })

    statMap.forEach((indexer) => {
      rows.push({ ...indexer, enabled: true })
    })

    return rows
  }, [config, stats])

  const confirmCloseActiveSession = () => {
    if (!activeSessionToClose) return
    sendCommand('close_session', { id: activeSessionToClose.id })
    setActiveSessionToClose(null)
  }

  return (
    <>
      <div className="flex flex-col gap-4 py-4 md:gap-6 md:py-6 px-4 lg:px-6">
        {/* KPI cards */}
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-5">
          <Card>
            <CardHeader>
              <CardDescription>Active Streams</CardDescription>
              <CardTitle className="tabular-nums text-primary">{stats.active_sessions?.length ?? 0}</CardTitle>
              <p className="text-xs text-muted-foreground">streaming</p>
            </CardHeader>
          </Card>
          <Card>
            <CardHeader>
              <CardDescription>Download Speed</CardDescription>
              <CardTitle className="flex items-baseline gap-1.5 tabular-nums">
                <span className="text-primary">{(stats.download_speed_mbps ?? 0).toFixed(1)}</span>
                <span className="text-sm font-normal text-muted-foreground">Mbps</span>
              </CardTitle>
            </CardHeader>
          </Card>
          <Card>
            <CardHeader>
              <CardDescription>Upload Speed</CardDescription>
              <CardTitle className="flex items-baseline gap-1.5 tabular-nums">
                <span className="text-primary">{(stats.upload_speed_mbps ?? 0).toFixed(1)}</span>
                <span className="text-sm font-normal text-muted-foreground">Mbps</span>
              </CardTitle>
              <p className="text-xs text-muted-foreground">seeding</p>
            </CardHeader>
          </Card>
          <Card>
            <CardHeader>
              <CardDescription>Active Torrents</CardDescription>
              <CardTitle className="flex items-baseline gap-1.5 tabular-nums">
                <span className="text-primary">{stats.active_torrents ?? 0}</span>
                <span className="text-sm font-normal text-muted-foreground">/ {stats.total_torrents ?? 0}</span>
              </CardTitle>
            </CardHeader>
          </Card>
          <Card>
            <CardHeader>
              <CardDescription>Uploaded Total</CardDescription>
              <CardTitle className="flex items-baseline gap-1.5 tabular-nums">
                {(() => {
                  const { value, unit } = formatDownloadedMb(stats.uploaded_mb)
                  return <><span className="text-primary">{value}</span><span className="text-sm font-normal text-muted-foreground">{unit}</span></>
                })()}
              </CardTitle>
              <p className="text-xs text-muted-foreground">ratio contribution</p>
            </CardHeader>
          </Card>
        </div>

        {/* Seedbox activity chart */}
        <Card className="overflow-hidden">
          <CardHeader>
            <CardTitle>Seedbox activity</CardTitle>
            <CardDescription>Download speed (Mbps) and active torrents over time</CardDescription>
          </CardHeader>
          <CardContent className="p-0">
            <ChartContainer config={chartConfig} className="h-[200px] w-full">
              <ComposedChart data={chartData} margin={{ top: 8, right: 8, bottom: 8, left: 32 }}>
                <defs>
                  <linearGradient id="chartSpeed" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0%" stopColor="hsl(var(--primary))" stopOpacity={0.4} />
                    <stop offset="100%" stopColor="hsl(var(--primary))" stopOpacity={0} />
                  </linearGradient>
                  <linearGradient id="chartTorrents" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0%" stopColor="hsl(var(--primary))" stopOpacity={0.25} />
                    <stop offset="100%" stopColor="hsl(var(--primary))" stopOpacity={0} />
                  </linearGradient>
                </defs>
                <XAxis dataKey="time" tick={{ fontSize: 10 }} />
                <YAxis yAxisId="left" tick={{ fontSize: 10 }} width={28} />
                <YAxis yAxisId="right" orientation="right" tick={{ fontSize: 10 }} allowDecimals={false} width={28} />
                <ChartTooltip content={<ChartTooltipContent />} />
                <Area
                  yAxisId="left"
                  type="monotone"
                  dataKey="speed"
                  stroke="hsl(var(--primary))"
                  strokeWidth={2}
                  fill="url(#chartSpeed)"
                  dot={false}
                  isAnimationActive={false}
                  name="speed"
                />
                <Area
                  yAxisId="right"
                  type="monotone"
                  dataKey="torrents"
                  stroke="hsl(var(--primary))"
                  strokeWidth={2}
                  strokeOpacity={0.7}
                  fill="url(#chartTorrents)"
                  dot={false}
                  isAnimationActive={false}
                  name="torrents"
                />
              </ComposedChart>
            </ChartContainer>
          </CardContent>
        </Card>

        {/* Active sessions */}
        {stats.active_sessions?.length > 0 && (
          <Card className="overflow-hidden">
            <CardHeader>
              <div className="flex items-center gap-2">
                <Activity className="h-5 w-5 text-primary" />
                <CardTitle className="text-lg font-semibold tracking-tight">Active streams</CardTitle>
              </div>
              <CardDescription>Streams that are currently being played.</CardDescription>
            </CardHeader>
            <CardContent>
              <div className="space-y-2">
                {stats.active_sessions.map(sess => (
                  <Card key={sess.id} className="group relative min-w-0 pr-10">
                    <CardContent className="p-3">
                      <div
                        className="min-w-0 pr-2 text-sm font-medium leading-snug whitespace-normal break-words [overflow-wrap:anywhere] md:truncate md:whitespace-nowrap"
                        title={sess.title}
                      >
                        {sess.title}
                      </div>
                      <div className="text-xs text-muted-foreground truncate min-w-0">{sess.clients.join(', ')}</div>
                      {sess.position && <StreamPosition position={sess.position} />}
                    </CardContent>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="absolute right-2 top-1/2 -translate-y-1/2 h-7 w-7 text-destructive hover:text-destructive hover:bg-destructive/10"
                      onClick={() => setActiveSessionToClose(sess)}
                      title="End stream"
                      aria-label={`End stream ${sess.title}`}
                    >
                      <X className="h-4 w-4" />
                    </Button>
                  </Card>
                ))}
              </div>
            </CardContent>
          </Card>
        )}

        {/* Torrent clients & Trackers */}
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
          <Card className="overflow-hidden">
            <CardHeader>
              <div className="flex items-center gap-2">
                <HardDrive className="h-5 w-5 text-primary" />
                <CardTitle className="text-lg font-semibold tracking-tight">Torrent Clients</CardTitle>
              </div>
              <CardDescription>Seedbox clients that download and keep seeding.</CardDescription>
            </CardHeader>
            <CardContent>
              <div className="grid gap-3 grid-cols-1 sm:grid-cols-2">
                {displayedClients.map((c) => (
                  <Card key={c.name} className={cn("min-h-[170px]", !c.enabled && "opacity-60 grayscale")}>
                    <CardHeader className="p-3 pb-1">
                      <div className="flex items-center gap-2">
                        <CardTitle className="text-base font-semibold truncate leading-tight" title={c.name}>{c.name}</CardTitle>
                        <TooltipProvider delayDuration={100}>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Badge variant="outline" className="ml-auto h-5 min-w-5 rounded-full px-1.5">
                                <span className={cn(
                                  "h-1.5 w-1.5 rounded-full",
                                  !c.enabled ? "bg-destructive" : c.reachable === false ? "bg-chart-4" : "bg-green-600"
                                )} />
                              </Badge>
                            </TooltipTrigger>
                            <TooltipContent>
                              {!c.enabled ? 'Disabled' : c.reachable === false ? 'Unreachable' : 'Connected'}
                            </TooltipContent>
                          </Tooltip>
                        </TooltipProvider>
                      </div>
                      <p className="text-[10px] text-muted-foreground truncate" title={c.url}>{c.url}</p>
                    </CardHeader>
                    <CardContent className="p-3 pt-0">
                      <div className="flex items-center justify-between mt-2">
                        <div className="flex flex-col">
                          <span className="text-[10px] uppercase text-muted-foreground font-medium">Seeds</span>
                          <span className="text-lg font-bold tabular-nums text-primary">{c.seeds ?? '—'}</span>
                        </div>
                        <div className="flex flex-col text-right">
                          <span className="text-[10px] uppercase text-muted-foreground font-medium">Peers</span>
                          <span className="text-lg font-bold tabular-nums text-primary">{c.peers ?? '—'}</span>
                        </div>
                      </div>
                      <div className="mt-3 flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
                        <span className="truncate" title={c.savePath}>{c.savePath || 'no save path'}</span>
                        <TooltipProvider delayDuration={100}>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Badge variant="outline" className="h-4 px-1.5 text-[10px] shrink-0">{c.category}</Badge>
                            </TooltipTrigger>
                            <TooltipContent>Category</TooltipContent>
                          </Tooltip>
                        </TooltipProvider>
                      </div>
                      {c.remotePath && (
                        <p className="mt-1 text-[10px] text-muted-foreground truncate" title={`${c.remotePath} → ${c.savePath}`}>
                          {c.remotePath} → {c.savePath}
                        </p>
                      )}
                    </CardContent>
                  </Card>
                ))}
                {displayedClients.length === 0 && (
                  <div className="col-span-full py-8 text-center rounded-lg border border-dashed text-muted-foreground text-sm">
                    No torrent clients configured.
                  </div>
                )}
              </div>
            </CardContent>
          </Card>

          <Card className="overflow-hidden">
            <CardHeader>
              <div className="flex items-center gap-2">
                <MonitorPlay className="h-5 w-5 text-primary" />
                <CardTitle className="text-lg font-semibold tracking-tight">Torrent Trackers</CardTitle>
              </div>
              <CardDescription>All configured trackers and their current usage.</CardDescription>
            </CardHeader>
            <CardContent>
              <div className="grid gap-3 grid-cols-1 sm:grid-cols-2">
                {displayedTrackers.map((idx) => {
                  const apiUsedPct = idx.api_hits_limit > 0 ? ((idx.api_hits_limit - idx.api_hits_remaining) / idx.api_hits_limit) * 100 : 0
                  const dlUsedPct = idx.downloads_limit > 0 ? ((idx.downloads_limit - idx.downloads_remaining) / idx.downloads_limit) * 100 : 0
                  const barColor = (pct) => pct >= 90 ? 'bg-destructive' : pct >= 75 ? 'bg-chart-4' : 'bg-primary'
                  const hasApiLimit = idx.api_hits_limit > 0
                  const hasDlLimit = idx.downloads_limit > 0
                  const isEnabled = idx.enabled !== false
                  const trackerUrl = trackerUrls.get((idx.name || '').trim()) || ''
                  return (
                    <Card key={idx.name} className={cn("overflow-hidden h-full", !isEnabled && "opacity-60 grayscale")}>
                      <CardHeader className="p-4 pb-2">
                        <div className="flex items-center gap-2">
                          <CardTitle className="text-base font-semibold truncate leading-tight" title={idx.name}>{idx.name}</CardTitle>
                          <TooltipProvider delayDuration={100}>
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Badge variant="outline" className="ml-auto h-5 min-w-5 rounded-full px-1.5">
                                  <span className={cn("h-1.5 w-1.5 rounded-full", isEnabled ? "bg-green-600" : "bg-destructive")} />
                                </Badge>
                              </TooltipTrigger>
                              <TooltipContent>{isEnabled ? 'Active' : 'Inactive'}</TooltipContent>
                            </Tooltip>
                          </TooltipProvider>
                        </div>
                        <p className="text-[10px] text-muted-foreground truncate" title={trackerUrl}>{trackerUrl}</p>
                      </CardHeader>
                      <CardContent className="p-4 pt-0">
                        <div className="grid grid-cols-2 gap-4">
                          <div className="space-y-1.5">
                            <p className="text-[11px] font-medium text-muted-foreground uppercase tracking-wider">API hits</p>
                            <p className="text-lg font-bold tabular-nums text-primary">{idx.api_hits_used}</p>
                            {hasApiLimit && (
                              <div className="w-full bg-muted h-2 rounded-full overflow-hidden mt-1">
                                <div className={cn("h-full transition-all duration-500 rounded-full", barColor(apiUsedPct))} style={{ width: `${apiUsedPct}%` }} />
                              </div>
                            )}
                            <p className="text-[11px] text-muted-foreground">
                              {hasApiLimit ? `of ${idx.api_hits_limit} today` : 'Unlimited'}
                            </p>
                          </div>
                          <div className="space-y-1.5">
                            <p className="text-[11px] font-medium text-muted-foreground uppercase tracking-wider">Grabs</p>
                            <p className="text-lg font-bold tabular-nums text-primary">{idx.downloads_used}</p>
                            {hasDlLimit && (
                              <div className="w-full bg-muted h-2 rounded-full overflow-hidden mt-1">
                                <div className={cn("h-full transition-all duration-500 rounded-full", barColor(dlUsedPct))} style={{ width: `${dlUsedPct}%` }} />
                              </div>
                            )}
                            <p className="text-[11px] text-muted-foreground">
                              {hasDlLimit ? `of ${idx.downloads_limit} today` : 'Unlimited'}
                            </p>
                          </div>
                        </div>
                      </CardContent>
                    </Card>
                  )
                })}
                {displayedTrackers.length === 0 && (
                  <div className="col-span-full py-8 text-center rounded-lg border border-dashed text-muted-foreground text-sm">
                    No torrent trackers configured.
                  </div>
                )}
              </div>
            </CardContent>
          </Card>
        </div>
      </div>

      <Dialog open={Boolean(activeSessionToClose)} onOpenChange={(open) => { if (!open) setActiveSessionToClose(null) }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>End active stream?</DialogTitle>
            <DialogDescription className="break-words [overflow-wrap:anywhere]">
              {activeSessionToClose
                ? `This will stop playback for "${activeSessionToClose.title}".`
                : 'This will stop playback for the selected stream.'}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter className="flex-row flex-wrap items-center justify-center gap-2 sm:justify-center sm:space-x-0">
            <Button type="button" variant="outline" className="min-w-28" onClick={() => setActiveSessionToClose(null)}>
              Cancel
            </Button>
            <Button type="button" variant="destructive" className="min-w-28" onClick={confirmCloseActiveSession}>
              End stream
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
