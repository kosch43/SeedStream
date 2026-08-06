import { useCallback, useEffect, useState } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Loader2, RefreshCw, ShieldCheck, ShieldAlert, ShieldX, Shield } from "lucide-react"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/api"

// Risk presentation. Order matters: the API already sorts most-urgent first, so
// anything needing action is at the top of the table.
const RISK = {
  critical: { label: 'Act now', cls: 'text-destructive font-semibold', Icon: ShieldX },
  warning: { label: 'At risk', cls: 'text-amber-600 font-medium', Icon: ShieldAlert },
  watch: { label: 'Watch', cls: 'text-amber-500', Icon: Shield },
  ok: { label: 'On track', cls: 'text-muted-foreground', Icon: Shield },
  met: { label: 'Met', cls: 'text-emerald-600', Icon: ShieldCheck },
  unknown: { label: 'No rules', cls: 'text-muted-foreground/70', Icon: Shield },
}

function riskOf(r) { return RISK[r] || RISK.unknown }

function hours(v) {
  if (v == null || !Number.isFinite(v)) return '—'
  if (Math.abs(v) >= 48) return `${(v / 24).toFixed(1)}d`
  return `${v.toFixed(1)}h`
}

function StatTile({ label, value, tone }) {
  return (
    <div className="rounded-md border border-border/60 p-3">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className={cn('mt-1 text-2xl font-semibold tabular-nums', tone)}>{value}</div>
    </div>
  )
}

export function CerberusPage() {
  const [data, setData] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      // apiFetch returns the decoded body and throws on a non-2xx status, so
      // there is no Response to inspect here.
      setData(await apiFetch('/api/cerberus/status'))
    } catch (e) {
      setError(String(e.message || e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
    // Obligations move on the scale of hours; a slow refresh is plenty.
    const t = setInterval(() => { void load() }, 60_000)
    return () => clearInterval(t)
  }, [load])

  const s = data?.summary || {}
  const torrents = data?.torrents || []
  const blocklist = data?.blocklist || []

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold">Cerberus</h1>
          <p className="text-sm text-muted-foreground">
            Torrent health and private-tracker obligations. Cerberus never deletes anything.
          </p>
        </div>
        <Button type="button" variant="outline" onClick={() => { void load() }} disabled={loading} className="gap-2">
          {loading ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
          Refresh
        </Button>
      </div>

      {error && (
        <div className="rounded-md border border-destructive/50 bg-destructive/10 p-3 text-sm text-destructive">
          Could not load Cerberus status: {error}
        </div>
      )}

      {data && !data.enabled && (
        <div className="rounded-md border border-border/60 p-4 text-sm text-muted-foreground">
          Cerberus is not active. It needs a torrent client configured.
        </div>
      )}

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        <StatTile label="Tracked" value={s.tracked ?? 0} />
        <StatTile label="Act now" value={s.critical ?? 0} tone={s.critical ? 'text-destructive' : undefined} />
        <StatTile label="At risk" value={s.warning ?? 0} tone={s.warning ? 'text-amber-600' : undefined} />
        <StatTile label="Watch" value={s.watch ?? 0} />
        <StatTile label="Met" value={s.met ?? 0} tone={s.met ? 'text-emerald-600' : undefined} />
        <StatTile label="Blocklisted" value={s.blocked ?? 0} />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Hit-and-run standing</CardTitle>
          <CardDescription>
            Seed time and ratio are qBittorrent's own accounting, not the tracker's. Set a tracker's
            seed-time, ratio and window under Settings → Advanced → Cerberus for it to appear here with a deadline.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {torrents.length === 0 ? (
            <div className="py-6 text-center text-sm text-muted-foreground">
              {loading ? 'Loading…' : 'No tracked torrents yet. They appear here once played through SeedStream.'}
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-xs uppercase text-muted-foreground">
                    <th className="py-2 pr-3 font-medium">Status</th>
                    <th className="py-2 pr-3 font-medium">Torrent</th>
                    <th className="py-2 pr-3 font-medium">Tracker</th>
                    <th className="py-2 pr-3 font-medium text-right">Seeded</th>
                    <th className="py-2 pr-3 font-medium text-right">Ratio</th>
                    <th className="py-2 pr-3 font-medium text-right">Deadline in</th>
                  </tr>
                </thead>
                <tbody>
                  {torrents.map((t) => {
                    const r = riskOf(t.risk)
                    const Icon = r.Icon
                    return (
                      <tr key={t.hash} className="border-b border-border/40 align-top">
                        <td className={cn('py-2 pr-3 whitespace-nowrap', r.cls)}>
                          <span className="inline-flex items-center gap-1.5">
                            <Icon className="h-4 w-4 shrink-0" />{r.label}
                          </span>
                        </td>
                        <td className="py-2 pr-3">
                          <div className="max-w-[28rem] truncate" title={t.name}>{t.name}</div>
                          <div className="text-xs text-muted-foreground">{t.detail}</div>
                        </td>
                        <td className="py-2 pr-3 whitespace-nowrap">{t.indexer_name || '—'}</td>
                        <td className="py-2 pr-3 text-right tabular-nums whitespace-nowrap">
                          {hours(t.seeding_hours)}
                          {t.required_hours > 0 && (
                            <span className="text-muted-foreground"> / {hours(t.required_hours)}</span>
                          )}
                        </td>
                        <td className="py-2 pr-3 text-right tabular-nums whitespace-nowrap">
                          {Number(t.ratio ?? 0).toFixed(2)}
                          {t.required_ratio > 0 && (
                            <span className="text-muted-foreground"> / {Number(t.required_ratio).toFixed(2)}</span>
                          )}
                        </td>
                        <td className="py-2 pr-3 text-right tabular-nums whitespace-nowrap">
                          {t.window_known ? hours(t.hours_remaining) : <span className="text-muted-foreground">not set</span>}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Health blocklist</CardTitle>
          <CardDescription>
            Torrents the watchdog gave up on; they are kept out of your stream list. Entries expire so a
            swarm that has since recovered can be tried again.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {blocklist.length === 0 ? (
            <div className="py-6 text-center text-sm text-muted-foreground">Nothing blocklisted.</div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-xs uppercase text-muted-foreground">
                    <th className="py-2 pr-3 font-medium">Info hash</th>
                    <th className="py-2 pr-3 font-medium">Reason</th>
                    <th className="py-2 pr-3 font-medium text-right">Failures</th>
                    <th className="py-2 pr-3 font-medium">Last failure</th>
                  </tr>
                </thead>
                <tbody>
                  {blocklist.map((b) => (
                    <tr key={b.info_hash} className="border-b border-border/40">
                      <td className="py-2 pr-3 font-mono text-xs">{String(b.info_hash).slice(0, 12)}…</td>
                      <td className="py-2 pr-3">{b.reason || '—'}</td>
                      <td className="py-2 pr-3 text-right tabular-nums">{b.failure_count}</td>
                      <td className="py-2 pr-3 whitespace-nowrap">
                        {b.last_failure_at ? new Date(b.last_failure_at).toLocaleString() : '—'}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
