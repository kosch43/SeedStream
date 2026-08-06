import React, { forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { Loader2, AlertTriangle, Save, Paintbrush } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Form, FormField, FormItem, FormLabel, FormControl, FormMessage, FormDescription } from "@/components/ui/form"
import { PasswordInput } from "@/components/ui/password-input"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { cn } from "@/lib/utils"

const CARD_FIELDS = {
  admin: ['log_level', 'keep_log_files'],
  memory: ['memory_limit_mb'],
  playback: ['playback_startup_timeout_seconds', 'failover_fast_mode', 'session_ttl_minutes', 'session_post_playback_ttl_minutes'],
  metadata: ['tmdb_api_key', 'tvdb_api_key'],
  cerberus: ['min_seeders', 'cerberus_base_url', 'cerberus_api_key'],
  uploadGuard: ['monthly_upload_cap_gb', 'post_cap_upload_mbps', 'upload_cap_reset_day'],
}

function pickInitialValues(values = {}) {
  const parsedPlaybackStartupTimeout = values.playback_startup_timeout_seconds == null
    ? 5
    : Number(values.playback_startup_timeout_seconds)
  const parsedSessionTtl = values.session_ttl_minutes == null
    ? 30
    : Number(values.session_ttl_minutes)
  const parsedSessionPostPlaybackTtl = values.session_post_playback_ttl_minutes == null
    ? 240
    : Number(values.session_post_playback_ttl_minutes)
  return {
    log_level: values.log_level ?? 'INFO',
    keep_log_files: Number(values.keep_log_files ?? 9) || 9,
    memory_limit_mb: Number(values.memory_limit_mb ?? 512),
    min_seeders: values.min_seeders == null ? 10 : Number(values.min_seeders),
    playback_startup_timeout_seconds: Number.isFinite(parsedPlaybackStartupTimeout) ? parsedPlaybackStartupTimeout : 5,
    session_ttl_minutes: Number.isFinite(parsedSessionTtl) ? parsedSessionTtl : 30,
    session_post_playback_ttl_minutes: Number.isFinite(parsedSessionPostPlaybackTtl) ? parsedSessionPostPlaybackTtl : 240,
    failover_fast_mode: values.failover_fast_mode == null ? true : values.failover_fast_mode === true,
    tmdb_api_key: values.tmdb_api_key ?? '',
    tvdb_api_key: values.tvdb_api_key ?? '',
    cerberus_base_url: values.cerberus_base_url ?? '',
    cerberus_api_key: values.cerberus_api_key ?? '',
    monthly_upload_cap_gb: Number(values.monthly_upload_cap_gb ?? 0) || 0,
    post_cap_upload_mbps: Number(values.post_cap_upload_mbps ?? 10) || 10,
    upload_cap_reset_day: Number(values.upload_cap_reset_day ?? 1) || 1,
  }
}

function EnvOverrideIndicator({ show, message = 'Overwritten by environment variable on restart.' }) {
  if (!show) return null
  return (
    <TooltipProvider delayDuration={100}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button type="button" className="inline-flex items-center text-amber-600 hover:text-amber-700 align-middle" aria-label={message}>
            <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
          </button>
        </TooltipTrigger>
        <TooltipContent side="top" align="start">{message}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

export const AdvancedSettingsSection = forwardRef(function AdvancedSettingsSection({
  initialValues,
  envOverrides,
  isSaving,
  onPersist,
  onClearCache,
  onDirtyChange,
  onProceedTabChange,
}, ref) {
  const defaults = useMemo(() => pickInitialValues(initialValues), [initialValues])
  const [lastSavedValues, setLastSavedValues] = useState(defaults)
  const [savingCard, setSavingCard] = useState('')
  const [clearingCache, setClearingCache] = useState(false)
  const [showClearCacheConfirm, setShowClearCacheConfirm] = useState(false)
  const [showRestartConfirm, setShowRestartConfirm] = useState(false)
  const [showDiscardConfirm, setShowDiscardConfirm] = useState(false)
  const [pendingTabChange, setPendingTabChange] = useState('')
  const [showUnsavedHighlights, setShowUnsavedHighlights] = useState(false)
  const dirtyRef = useRef(false)

  const form = useForm({ defaultValues: defaults })
  const { control, handleSubmit, reset, getValues, formState } = form
  const watchedValues = useWatch({ control })

  useEffect(() => {
    const currentValues = pickInitialValues(watchedValues)
    dirtyRef.current = JSON.stringify(currentValues) !== JSON.stringify(lastSavedValues)
    onDirtyChange?.(dirtyRef.current)
  }, [lastSavedValues, onDirtyChange, watchedValues])

  useEffect(() => {
    reset(defaults)
    setLastSavedValues(defaults)
    dirtyRef.current = false
    onDirtyChange?.(false)
  }, [defaults, onDirtyChange, reset])

  useImperativeHandle(ref, () => ({
    hasUnsavedChanges() {
      return dirtyRef.current
    },
    discardChanges() {
      reset(lastSavedValues)
      dirtyRef.current = false
      onDirtyChange?.(false)
    },
    requestTabChange(nextTab) {
      if (!dirtyRef.current) {
        onProceedTabChange(nextTab)
        return true
      }
      setPendingTabChange(nextTab)
      setShowDiscardConfirm(true)
      return false
    },
  }), [lastSavedValues, onDirtyChange, onProceedTabChange, reset])

  const saveCard = async (cardId) => {
    setSavingCard(cardId)
    try {
      const values = getValues()
      const payload = Object.fromEntries(CARD_FIELDS[cardId].map((key) => [key, values[key]]))
      await onPersist(payload, cardId)
      const nextValues = { ...lastSavedValues, ...payload }
      setLastSavedValues(nextValues)
      reset(nextValues)
      dirtyRef.current = false
      onDirtyChange?.(false)
      setShowUnsavedHighlights(false)
    } finally {
      setSavingCard('')
    }
  }

  const handleCardSave = (cardId) => handleSubmit(async () => {
    if (cardId === 'memory' && formState.dirtyFields?.memory_limit_mb) {
      setShowRestartConfirm(true)
      return
    }
    await saveCard(cardId)
  })()

  const renderSaveButton = (cardId) => (
    <TooltipProvider delayDuration={100}>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="destructive"
            size="icon"
            onClick={() => { void handleCardSave(cardId) }}
            disabled={isSaving || formState.isSubmitting}
            className="h-9 w-9"
          >
            {(isSaving || formState.isSubmitting) && savingCard === cardId
              ? <Loader2 className="h-4 w-4 animate-spin" />
              : <Save className="h-4 w-4" />}
          </Button>
        </TooltipTrigger>
        <TooltipContent>Save</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )

  const fieldClassName = (fieldName, baseClassName = '') => cn(
    baseClassName,
    showUnsavedHighlights && formState.dirtyFields?.[fieldName] && 'border-destructive ring-1 ring-destructive focus-visible:ring-destructive'
  )
  const stackedFieldRowClass = "flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between sm:gap-4"
  const controlMediumClass = "w-full min-w-0 sm:max-w-[10rem]"
  const controlSelectClass = "flex h-9 w-full min-w-0 max-w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 overflow-hidden text-ellipsis whitespace-nowrap sm:max-w-[14rem]"
  const labelClass = "min-w-0 text-sm font-medium"

  const handleClearCacheClick = async () => {
    if (clearingCache) return
    setClearingCache(true)
    try {
      await onClearCache()
    } finally {
      setClearingCache(false)
    }
  }

  return (
    <Form {...form}>
      <form className="space-y-6">
        <div className="grid grid-cols-1 gap-6 2xl:grid-cols-2">
          <div className="space-y-6">
            <Card>
              <CardHeader>
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1 max-w-[26rem] space-y-0.5">
                    <CardTitle>Logs</CardTitle>
                    <CardDescription>Log level and file retention.</CardDescription>
                  </div>
                  <div className="flex items-center gap-2">{renderSaveButton('admin')}</div>
                </div>
              </CardHeader>
              <CardContent>
                <div className="space-y-4">
                  <div className="rounded-md border border-border/60">
                    <FormField control={control} name="log_level" render={({ field }) => (
                      <FormItem className="rounded-none border-0 p-3">
                        <div className={stackedFieldRowClass}>
                          <FormLabel className={cn(labelClass, 'flex items-center gap-1.5 sm:flex-1')}>Log Level <EnvOverrideIndicator show={envOverrides.includes('log_level')} /></FormLabel>
                          <FormControl>
                            <select className={fieldClassName('log_level', controlSelectClass)} {...field}>
                              <option value="DEBUG">DEBUG</option>
                              <option value="INFO">INFO</option>
                              <option value="WARN">WARN</option>
                              <option value="ERROR">ERROR</option>
                            </select>
                          </FormControl>
                        </div>
                        <FormDescription className="mt-3">Controls how verbose SeedStream logging should be.</FormDescription>
                        <FormMessage />
                      </FormItem>
                    )} />
                    <FormField control={control} name="keep_log_files" render={({ field }) => (
                      <FormItem className="relative rounded-none border-0 p-3">
                        <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                        <div className={stackedFieldRowClass}>
                          <FormLabel className={cn(labelClass, 'flex items-center gap-1.5 sm:flex-1')}>Keep log files <EnvOverrideIndicator show={envOverrides.includes('keep_log_files')} /></FormLabel>
                          <FormControl><Input type="number" min={1} max={50} className={fieldClassName('keep_log_files', `h-9 ${controlMediumClass}`)} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; field.onChange(v === '' ? 9 : Math.min(50, Math.max(1, Number(v) || 9))) }} /></FormControl>
                        </div>
                        <FormDescription className="mt-3">Number of log files to keep. Oldest rotated logs are purged on restart.</FormDescription>
                        <FormMessage />
                      </FormItem>
                    )} />
                  </div>

                </div>
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1 max-w-[30rem] space-y-0.5">
                    <CardTitle>Playback</CardTitle>
                    <CardDescription>Startup behavior before the first playable response is sent.</CardDescription>
                  </div>
                  <div className="shrink-0">{renderSaveButton('playback')}</div>
                </div>
              </CardHeader>
              <CardContent>
                <div className="rounded-md border border-border/60">
                  <FormField control={control} name="playback_startup_timeout_seconds" render={({ field }) => (
                    <FormItem className="rounded-none border-0 p-3">
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'sm:flex-1')}>Playback startup timeout (s)</FormLabel>
                        <FormControl><Input type="number" min={1} max={60} className={fieldClassName('playback_startup_timeout_seconds', `h-9 ${controlMediumClass}`)} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; const next = Number(v); field.onChange(v === '' ? 5 : Math.min(60, Math.max(1, Number.isNaN(next) ? 5 : next))) }} /></FormControl>
                      </div>
                      <FormDescription className="mt-3">How long SeedStream waits for the initial playback probe/open before failing over to the next release. Higher values reduce false startup timeouts but delay failover.</FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                  <FormField control={control} name="failover_fast_mode" render={({ field }) => (
                    <FormItem className="relative rounded-none border-0 p-3">
                      <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                      <div className={stackedFieldRowClass}>
                        <div className="sm:flex-1">
                          <FormLabel className={labelClass}>Failover fast mode</FormLabel>
                        </div>
                        <FormControl>
                          <Switch
                            checked={field.value === true}
                            onCheckedChange={field.onChange}
                            className={showUnsavedHighlights && formState.dirtyFields?.failover_fast_mode ? 'ring-2 ring-destructive ring-offset-2 ring-offset-background' : ''}
                          />
                        </FormControl>
                      </div>
                      <FormDescription className="mt-3">
                        When enabled, failover prioritizes faster startup and may skip deeper checks.
                        When disabled, failover spends longer exhausting all options before moving on.
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                  <FormField control={control} name="session_ttl_minutes" render={({ field }) => (
                    <FormItem className="relative rounded-none border-0 p-3">
                      <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'sm:flex-1')}>Session inactive TTL (m)</FormLabel>
                        <FormControl>
                          <Input
                            type="number"
                            min={1}
                            max={1440}
                            className={fieldClassName('session_ttl_minutes', `h-9 ${controlMediumClass}`)}
                            {...field}
                            value={field.value ?? ''}
                            onChange={e => {
                              const v = e.target.value;
                              const next = Number(v);
                              field.onChange(v === '' ? 30 : Math.min(1440, Math.max(1, Number.isNaN(next) ? 30 : next)))
                            }}
                          />
                        </FormControl>
                      </div>
                      <FormDescription className="mt-3">
                        How long inactive or deferred catalog sessions stay in memory before being evicted.
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                  <FormField control={control} name="session_post_playback_ttl_minutes" render={({ field }) => (
                    <FormItem className="relative rounded-none border-0 p-3">
                      <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'sm:flex-1')}>Paused playback TTL (m)</FormLabel>
                        <FormControl>
                          <Input
                            type="number"
                            min={1}
                            max={1440}
                            className={fieldClassName('session_post_playback_ttl_minutes', `h-9 ${controlMediumClass}`)}
                            {...field}
                            value={field.value ?? ''}
                            onChange={e => {
                              const v = e.target.value;
                              const next = Number(v);
                              field.onChange(v === '' ? 240 : Math.min(1440, Math.max(1, Number.isNaN(next) ? 240 : next)))
                            }}
                          />
                        </FormControl>
                      </div>
                      <FormDescription className="mt-3">
                        How long a session stays in memory after active playback ends (e.g. when paused). Keeping this long prevents needing to reload the catalog to resume.
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                </div>
              </CardContent>
            </Card>
          </div>

          <div className="space-y-6">
            <Card>
              <CardHeader>
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1 max-w-[30rem] space-y-0.5">
                    <CardTitle>Memory & Cache</CardTitle>
                    <CardDescription>Runtime memory limits and search cache maintenance.</CardDescription>
                  </div>
                  <div className="shrink-0">{renderSaveButton('memory')}</div>
                </div>
              </CardHeader>
              <CardContent className="space-y-3">
                <div className="rounded-md border border-border/60">
                  <FormField control={control} name="memory_limit_mb" render={({ field }) => (
                    <FormItem className="rounded-none border-0 p-3">
                      <div className={stackedFieldRowClass}>
                        <FormLabel className={cn(labelClass, 'sm:flex-1')}>Memory limit (MB)</FormLabel>
                        <FormControl><Input type="number" min={0} className={fieldClassName('memory_limit_mb', `h-9 ${controlMediumClass}`)} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; field.onChange(v === '' ? 0 : Number(v) || 0) }} /></FormControl>
                      </div>
                      <FormDescription className="mt-3">Soft limit on total process memory (0 = no limit). Segment cache uses 80% of this. Restart required.</FormDescription>
                      <FormMessage />
                    </FormItem>
                  )} />
                </div>
                <div className="rounded-md border border-border/60 p-3">
                  <div className="flex items-center justify-between gap-4">
                    <div className="space-y-0.5">
                      <div className="text-sm font-medium">Clear cache</div>
                    </div>
                    <Button type="button" variant="destructive" onClick={() => setShowClearCacheConfirm(true)} disabled={clearingCache} className="h-9 shrink-0 gap-2">
                      {clearingCache ? <Loader2 className="h-4 w-4 animate-spin" /> : <Paintbrush className="h-4 w-4" />}
                      Clear
                    </Button>
                  </div>
                  <div className="mt-3 text-sm text-muted-foreground">Clears the in-memory playlist and raw search caches immediately.</div>
                </div>
              </CardContent>
            </Card>
          </div>
        </div>

        <Card>
          <CardHeader>
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0 flex-1 max-w-[34rem] space-y-0.5">
                <CardTitle>Metadata APIs</CardTitle>
                <CardDescription>Optional API keys for metadata enrichment during search and matching. Built-in defaults are available, but using your own keys is recommended.</CardDescription>
              </div>
              <div className="shrink-0">{renderSaveButton('metadata')}</div>
            </div>
          </CardHeader>
          <CardContent>
            <div className="rounded-md border border-border/60">
              <FormField control={control} name="tmdb_api_key" render={({ field }) => (
                <FormItem className="rounded-none border-0 p-3">
                  <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                    <FormLabel className="min-w-0 text-sm font-medium xl:flex-1 flex items-center gap-1.5">TMDB Read Access Token <EnvOverrideIndicator show={envOverrides.includes('tmdb_api_key')} /></FormLabel>
                    <FormControl><div className="w-full xl:max-w-3xl"><PasswordInput className={fieldClassName('tmdb_api_key', 'h-9 w-full font-mono text-xs')} {...field} value={field.value || ''} /></div></FormControl>
                  </div>
                  <FormDescription className="mt-3">Used for localized titles, year enrichment, and text-based movie/show name resolution. Without it, text-search metadata is limited and some requests fall back to ID-only behavior.</FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
              <FormField control={control} name="tvdb_api_key" render={({ field }) => (
                <FormItem className="relative rounded-none border-0 p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                    <FormLabel className="min-w-0 text-sm font-medium xl:flex-1 flex items-center gap-1.5">TVDB API Key <EnvOverrideIndicator show={envOverrides.includes('tvdb_api_key')} /></FormLabel>
                    <FormControl><div className="w-full xl:max-w-3xl"><PasswordInput className={fieldClassName('tvdb_api_key', 'h-9 w-full font-mono text-xs')} {...field} value={field.value || ''} /></div></FormControl>
                  </div>
                  <FormDescription className="mt-3">Used primarily for series metadata ID resolution. When available, SeedStream can resolve TVDB IDs directly before falling back to TMDB-based lookup.</FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0 flex-1 max-w-[34rem] space-y-0.5">
                <CardTitle>Cerberus (community torrent health)</CardTitle>
                <CardDescription>
                  The torrent watchdog always runs locally — tracking stalled torrents and avoiding known-bad ones.
                  Optionally point it at a central Cerberus server to share failure reports and pull community blocklist
                  data. Leave blank for local-only mode.
                </CardDescription>
              </div>
              <div className="shrink-0">{renderSaveButton('cerberus')}</div>
            </div>
          </CardHeader>
          <CardContent>
            <div className="rounded-md border border-border/60">
              <FormField control={control} name="min_seeders" render={({ field }) => (
                <FormItem className="rounded-none border-0 p-3">
                  <div className={stackedFieldRowClass}>
                    <FormLabel className={cn(labelClass, 'sm:flex-1')}>Minimum seeders to play</FormLabel>
                    <FormControl><Input type="number" min={0} className={fieldClassName('min_seeders', `h-9 ${controlMediumClass}`)} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; field.onChange(v === '' ? 10 : Math.max(0, Number(v) || 0)) }} /></FormControl>
                  </div>
                  <FormDescription className="mt-3">
                    A torrent needs at least this many seeders before SeedStream will offer or play it — a thinner swarm downloads slower than playback consumes it, which is what causes stalling and buffering. Enforced in three places: under-seeded releases are hidden from the stream list, refused at play time so failover picks a healthier one, and the watchdog acts on an under-seeded torrent at half the usual stall threshold. Only applies to trackers that report a seeder count, and it will never empty your stream list. Default 10; set to 0 to disable.
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
              <FormField control={control} name="cerberus_base_url" render={({ field }) => (
                <FormItem className="relative rounded-none border-0 p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                    <FormLabel className="min-w-0 text-sm font-medium xl:flex-1 flex items-center gap-1.5">Central server URL <EnvOverrideIndicator show={envOverrides.includes('cerberus_base_url')} /></FormLabel>
                    <FormControl><div className="w-full xl:max-w-3xl"><Input className={fieldClassName('cerberus_base_url', 'h-9 w-full font-mono text-xs')} {...field} value={field.value || ''} placeholder="https://cerberus.example.com" autoComplete="off" /></div></FormControl>
                  </div>
                  <FormDescription className="mt-3">Base URL of a central Cerberus server. When set, failures are reported upstream and its community blocklist is merged with your local one. Requires a restart to take effect.</FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
              <FormField control={control} name="cerberus_api_key" render={({ field }) => (
                <FormItem className="relative rounded-none border-0 p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:gap-4">
                    <FormLabel className="min-w-0 text-sm font-medium xl:flex-1 flex items-center gap-1.5">API key <EnvOverrideIndicator show={envOverrides.includes('cerberus_api_key')} /></FormLabel>
                    <FormControl><div className="w-full xl:max-w-3xl"><PasswordInput className={fieldClassName('cerberus_api_key', 'h-9 w-full font-mono text-xs')} {...field} value={field.value || ''} /></div></FormControl>
                  </div>
                  <FormDescription className="mt-3">Optional bearer token sent with every request to the central server.</FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0 flex-1 max-w-[34rem] space-y-0.5">
                <CardTitle>Monthly upload guard</CardTitle>
                <CardDescription>
                  Many seedboxes throttle upload speed after a monthly traffic allowance is used up. Enter your allowance
                  and the watchdog tracks how much has been uploaded this month (BitTorrent seeding plus streaming). Once the
                  cap is reached, SeedStream serves only titles whose bitrate fits the throttled speed — with a larger
                  pre-buffer — and holds heavier titles with a disclaimer until the allowance resets. The total is an
                  estimate; it cannot read your provider's meter. Set the cap to 0 to disable.
                </CardDescription>
              </div>
              <div className="shrink-0">{renderSaveButton('uploadGuard')}</div>
            </div>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="rounded-md border border-border/60">
              <FormField control={control} name="monthly_upload_cap_gb" render={({ field }) => (
                <FormItem className="rounded-none border-0 p-3">
                  <div className={stackedFieldRowClass}>
                    <FormLabel className={cn(labelClass, 'sm:flex-1')}>Monthly upload allowance (GB)</FormLabel>
                    <FormControl><Input type="number" min={0} step="any" className={fieldClassName('monthly_upload_cap_gb', `h-9 ${controlMediumClass}`)} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; field.onChange(v === '' ? 0 : Number(v) || 0) }} /></FormControl>
                  </div>
                  <FormDescription className="mt-3">Your seedbox's monthly upload cap in gigabytes (decimal — a 2 TB cap is 2000). 0 disables the guard entirely.</FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
              <FormField control={control} name="post_cap_upload_mbps" render={({ field }) => (
                <FormItem className="relative rounded-none border-0 p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className={stackedFieldRowClass}>
                    <FormLabel className={cn(labelClass, 'sm:flex-1')}>Throttled upload speed (Mbps)</FormLabel>
                    <FormControl><Input type="number" min={0} step="any" className={fieldClassName('post_cap_upload_mbps', `h-9 ${controlMediumClass}`)} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; field.onChange(v === '' ? 0 : Number(v) || 0) }} /></FormControl>
                  </div>
                  <FormDescription className="mt-3">The upload speed your provider throttles to after the cap is hit (e.g. 10). Titles needing more than this are held until reset. Default 10.</FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
              <FormField control={control} name="upload_cap_reset_day" render={({ field }) => (
                <FormItem className="relative rounded-none border-0 p-3">
                  <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                  <div className={stackedFieldRowClass}>
                    <FormLabel className={cn(labelClass, 'sm:flex-1')}>Allowance reset day</FormLabel>
                    <FormControl><Input type="number" min={1} max={28} className={fieldClassName('upload_cap_reset_day', `h-9 ${controlMediumClass}`)} {...field} value={field.value ?? ''} onChange={e => { const v = e.target.value; field.onChange(v === '' ? 1 : Math.min(28, Math.max(1, Number(v) || 1))) }} /></FormControl>
                  </div>
                  <FormDescription className="mt-3">Day of the month (1–28) your provider's traffic counter resets. The monthly total zeroes on this day.</FormDescription>
                  <FormMessage />
                </FormItem>
              )} />
            </div>
          </CardContent>
        </Card>

        <ConfirmDialog
          open={showRestartConfirm}
          onOpenChange={setShowRestartConfirm}
          title="Restart required"
          description="Changing the memory limit requires a SeedStream restart. Do you want to save this change now?"
          confirmLabel="Save"
          confirmVariant="destructive"
          onConfirm={() => {
            setShowRestartConfirm(false)
            setShowUnsavedHighlights(false)
            void saveCard('memory')
          }}
        />
        <ConfirmDialog
          open={showClearCacheConfirm}
          onOpenChange={setShowClearCacheConfirm}
          title="Clear search cache?"
          description="This clears the in-memory playlist and raw search caches immediately."
          confirmLabel="Clear"
          onConfirm={() => {
            setShowClearCacheConfirm(false)
            void handleClearCacheClick()
          }}
        />
        <ConfirmDialog
          open={showDiscardConfirm}
          onOpenChange={(nextOpen) => {
            setShowDiscardConfirm(nextOpen)
            if (!nextOpen) {
              setPendingTabChange('')
              if (dirtyRef.current) setShowUnsavedHighlights(true)
            }
          }}
          title="Discard advanced changes?"
          description="Your unsaved changes in the Advanced tab will be lost."
          confirmLabel="Discard"
          onConfirm={() => {
            const nextTab = pendingTabChange
            setShowDiscardConfirm(false)
            setPendingTabChange('')
            reset(lastSavedValues)
            dirtyRef.current = false
            onDirtyChange?.(false)
            setShowUnsavedHighlights(false)
            if (nextTab) onProceedTabChange(nextTab)
          }}
        />
      </form>
    </Form>
  )
})
