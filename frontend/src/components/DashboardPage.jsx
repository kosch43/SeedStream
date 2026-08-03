import { useMemo, useState } from 'react'
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { Activity, X, MonitorPlay, HardDrive, Magnet } from "lucide-react"
import { cn } from "@/lib/utils"

export function DashboardPage({ stats, sendCommand, config }) {
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

  const enabledTrackers = (config?.indexers || []).filter((idx) => idx.enabled !== false).length
  const enabledClients = (config?.torrent_clients || []).filter((tc) => tc.enabled !== false).length

  const confirmCloseActiveSession = () => {
    if (!activeSessionToClose) return
    sendCommand('close_session', { id: activeSessionToClose.id })
    setActiveSessionToClose(null)
  }

  return (
    <>
      <div className="flex flex-col gap-4 py-4 md:gap-6 md:py-6 px-4 lg:px-6">
        {/* KPI cards */}
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-3">
          <Card>
            <CardHeader>
              <CardDescription>Active Streams</CardDescription>
              <CardTitle className="tabular-nums text-primary">{stats.active_sessions?.length ?? 0}</CardTitle>
              <p className="text-xs text-muted-foreground">streaming</p>
            </CardHeader>
          </Card>
          <Card>
            <CardHeader>
              <div className="flex items-center justify-between gap-2">
                <CardDescription>Torrent Trackers</CardDescription>
                <Magnet className="h-4 w-4 text-muted-foreground" />
              </div>
              <CardTitle className="tabular-nums text-primary">{enabledTrackers}</CardTitle>
              <p className="text-xs text-muted-foreground">enabled</p>
            </CardHeader>
          </Card>
          <Card>
            <CardHeader>
              <div className="flex items-center justify-between gap-2">
                <CardDescription>Torrent Clients</CardDescription>
                <HardDrive className="h-4 w-4 text-muted-foreground" />
              </div>
              <CardTitle className="tabular-nums text-primary">{enabledClients}</CardTitle>
              <p className="text-xs text-muted-foreground">enabled</p>
            </CardHeader>
          </Card>
        </div>

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

        {/* Trackers */}
        <Card className="overflow-hidden">
          <CardHeader>
            <div className="flex items-center gap-2">
              <MonitorPlay className="h-5 w-5 text-primary" />
              <CardTitle className="text-lg font-semibold tracking-tight">Torrent Trackers</CardTitle>
            </div>
            <CardDescription>All configured trackers and their current usage.</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="grid gap-3 grid-cols-1 sm:grid-cols-2 lg:grid-cols-3">
              {displayedTrackers.map((idx) => {
                const apiUsedPct = idx.api_hits_limit > 0 ? ((idx.api_hits_limit - idx.api_hits_remaining) / idx.api_hits_limit) * 100 : 0
                const dlUsedPct = idx.downloads_limit > 0 ? ((idx.downloads_limit - idx.downloads_remaining) / idx.downloads_limit) * 100 : 0
                const barColor = (pct) => pct >= 90 ? 'bg-destructive' : pct >= 75 ? 'bg-chart-4' : 'bg-primary'
                const hasApiLimit = idx.api_hits_limit > 0
                const hasDlLimit = idx.downloads_limit > 0
                const isEnabled = idx.enabled !== false
                const trackerUrl = trackerUrls.get((idx.name || '').trim()) || ''
                return (
                  <Card
                    key={idx.name}
                    className={cn("overflow-hidden h-full", !isEnabled && "opacity-60 grayscale")}
                  >
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
