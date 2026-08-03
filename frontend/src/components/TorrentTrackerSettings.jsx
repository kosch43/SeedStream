import React, { useEffect, useRef, useState } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, focusDialogCloseButton } from "@/components/ui/dialog"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { Download, Plus, Settings, Trash2 } from "lucide-react"

// Presets provide starting-point URLs; credentials are always entered by the user.
const TRACKER_PRESETS = [
  { name: 'Jackett (all indexers)', url: 'http://localhost:9117', api_path: '/api/v2.0/indexers/all/results/torznab', timeout_seconds: 10 },
  { name: 'Prowlarr', url: 'http://localhost:9696', api_path: '/1/api', timeout_seconds: 10 },
  { name: 'Generic Torznab', url: '', api_path: '/api', timeout_seconds: 10 },
]

function normalizeName(value) {
  return (value || '').trim().toLowerCase()
}

function normalizeTrackerDraft(draft) {
  const v = draft || {}
  return {
    name: (v.name || '').trim(),
    url: (v.url || '').trim(),
    api_path: v.api_path || '/api',
    // passkey: the user-specific token a private tracker issues for its Torznab/RSS
    // feed. It is sent as the ?apikey= parameter on every Torznab request, which is
    // how the tracker ties the search back to your account. Stored as api_key in
    // IndexerConfig so the shared Torznab client picks it up automatically.
    api_key: v.api_key || '',
    type: 'torznab',
    api_hits_day: Number(v.api_hits_day || 0),
    rate_limit_rps: Number(v.rate_limit_rps || 0),
    timeout_seconds: Number(v.timeout_seconds || 0),
    enabled: v.enabled !== false,
    proxy_url: (v.proxy_url || '').trim(),
    hnr_min_seed_hours: Number(v.hnr_min_seed_hours || 0),
    hnr_min_ratio: Number(v.hnr_min_ratio || 0),
    hnr_mode: v.hnr_mode || 'any',
  }
}

function emptyTrackerDraft() {
  return normalizeTrackerDraft({})
}

function normalizeTrackerIdentity(draft) {
  const next = normalizeTrackerDraft(draft)
  return `torznab::${normalizeName(next.url)}::${normalizeName(next.api_path)}::${normalizeName(next.api_key)}`
}

function formatLimitValue(value) {
  return value > 0 ? String(value) : '∞'
}

function summarizeTracker(tracker) {
  const parts = []
  if (tracker.url) parts.push(tracker.url)
  parts.push(tracker.api_key ? 'Passkey: set' : 'Passkey: none')
  if (tracker.timeout_seconds > 0) parts.push(`Timeout: ${tracker.timeout_seconds}s`)
  else parts.push('Timeout: 10s default')
  parts.push(`Hits/day: ${formatLimitValue(tracker.api_hits_day)}`)
  parts.push(`RPS: ${formatLimitValue(tracker.rate_limit_rps)}`)
  if (tracker.proxy_url) parts.push('Proxy: override')
  // H&R summary
  const seedH = Number(tracker.hnr_min_seed_hours || 0)
  const ratio = Number(tracker.hnr_min_ratio || 0)
  if (seedH > 0 || ratio > 0) {
    const mode = tracker.hnr_mode === 'all' ? 'both' : 'either'
    const parts2 = []
    if (seedH > 0) parts2.push(`${seedH}h seed`)
    if (ratio > 0) parts2.push(`${ratio} ratio`)
    parts.push(`H&R: ${parts2.join(' + ')} (${mode})`)
  } else {
    parts.push('H&R: not configured')
  }
  return parts
}

function firstFieldErrorMessage(fieldErrors, fallback) {
  const first = Object.values(fieldErrors || {}).find(Boolean)
  return first || fallback
}

function TrackerDialog({ open, onOpenChange, initialValue, onSave, onClearStatus, title, description, saveLabel, existingNames = [], existingTrackers = [], editing = false }) {
  const [draft, setDraft] = useState(() => normalizeTrackerDraft(initialValue))
  const [wasOpen, setWasOpen] = useState(open)
  const [saveError, setSaveError] = useState('')
  const [fieldErrors, setFieldErrors] = useState({})
  const [saving, setSaving] = useState(false)
  const [showDiscardConfirm, setShowDiscardConfirm] = useState(false)
  const [presetTooltipOpen, setPresetTooltipOpen] = useState(false)
  const [presetMenuOpen, setPresetMenuOpen] = useState(false)
  const nameInputRef = useRef(null)

  useEffect(() => {
    if (open && !wasOpen) {
      setDraft(normalizeTrackerDraft(initialValue))
      setSaveError('')
      setFieldErrors({})
    }
    setWasOpen(open)
  }, [open, wasOpen, initialValue])

  const normalizedInitial = JSON.stringify(normalizeTrackerDraft(initialValue))
  const normalizedCurrent = JSON.stringify(normalizeTrackerDraft(draft))
  const isDirty = normalizedInitial !== normalizedCurrent
  const duplicateName = existingNames.some((name) => normalizeName(name) === normalizeName(draft.name))
  const duplicateTracker = existingTrackers.find((t) => normalizeTrackerIdentity(t) === normalizeTrackerIdentity(draft))
  const selectedPresetName = TRACKER_PRESETS.find((p) =>
    p.url === draft.url && p.api_path === draft.api_path
  )?.name || ''

  const requestClose = () => {
    if (saving) return
    if (isDirty) { setShowDiscardConfirm(true); return }
    onClearStatus?.()
    onOpenChange(false)
  }

  const update = (key, value) => setDraft((current) => ({ ...current, [key]: value }))
  const fieldClass = (key) => fieldErrors[key] ? 'border-destructive focus-visible:ring-destructive' : ''
  const rowClass = 'flex flex-col gap-3 min-[360px]:flex-row min-[360px]:items-center min-[360px]:gap-4'
  const labelClass = 'min-w-0 min-[360px]:flex-1'
  const controlBaseClass = 'w-full min-[360px]:ml-auto min-[360px]:shrink-0'
  const controlWideClass = `${controlBaseClass} min-[360px]:w-[14rem]`
  const controlNameClass = `${controlBaseClass} flex items-center gap-2 min-[360px]:w-[16.5rem]`
  const controlNarrowClass = `${controlBaseClass} min-[360px]:w-[9rem]`

  const handleSave = async () => {
    const nextFieldErrors = {}
    if (!draft.name?.trim()) nextFieldErrors.name = 'Tracker name is required'
    if (!draft.url?.trim()) nextFieldErrors.url = 'URL is required'
    if (duplicateName) nextFieldErrors.name = 'A tracker with this name already exists'
    if (duplicateTracker) nextFieldErrors.url = `An identical tracker already exists: "${duplicateTracker.name}".`
    if (Object.keys(nextFieldErrors).length > 0) {
      setFieldErrors(nextFieldErrors)
      setSaveError(firstFieldErrorMessage(nextFieldErrors, 'Please review the highlighted fields.'))
      return
    }
    setSaving(true)
    setSaveError('')
    setFieldErrors({})
    try {
      await onSave(normalizeTrackerDraft(draft))
      onOpenChange(false)
    } catch (error) {
      const nextErrors = {}
      Object.entries(error?.fieldErrors || {}).forEach(([path, message]) => {
        if (path.includes('.name')) nextErrors.name = message
        else if (path.includes('.url')) nextErrors.url = message
        else if (path.includes('.api_path')) nextErrors.api_path = message
        else if (path.includes('.api_key')) nextErrors.api_key = message
        else if (path.includes('.timeout_seconds')) nextErrors.timeout_seconds = message
        else if (path.includes('.api_hits_day')) nextErrors.api_hits_day = message
        else if (path.includes('.rate_limit_rps')) nextErrors.rate_limit_rps = message
        else if (path.includes('.proxy_url')) nextErrors.proxy_url = message
      })
      setFieldErrors(nextErrors)
      setSaveError(firstFieldErrorMessage(nextErrors, error?.message || 'Save failed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => {
      if (nextOpen) { onOpenChange(true); return }
      requestClose()
    }}>
      <DialogContent className="flex max-h-[85vh] max-w-3xl flex-col overflow-hidden" onOpenAutoFocus={focusDialogCloseButton}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>

        <div className="min-h-0 flex-1 overflow-y-auto pr-1">
          <div className="space-y-4">

            {/* Name */}
            <div className="rounded-md border border-border/60 p-3">
              <div className={rowClass}>
                <div className={labelClass}>
                  <Label className="text-sm font-medium">Name</Label>
                </div>
                <div className={controlNameClass}>
                  <Input
                    ref={nameInputRef}
                    className={`h-9 ${fieldClass('name')}`}
                    value={draft.name}
                    onChange={(e) => update('name', e.target.value)}
                    placeholder="e.g. TorrentLeech"
                  />
                  {!editing && (
                    <TooltipProvider delayDuration={100}>
                      <Tooltip open={presetTooltipOpen && !presetMenuOpen} onOpenChange={setPresetTooltipOpen}>
                        <DropdownMenu onOpenChange={(nextOpen) => {
                          setPresetMenuOpen(nextOpen)
                          if (nextOpen) setPresetTooltipOpen(false)
                        }}>
                          <TooltipTrigger asChild>
                            <DropdownMenuTrigger asChild>
                              <Button type="button" variant={selectedPresetName ? 'secondary' : 'outline'} size="icon" className="h-9 w-9 shrink-0" aria-label={selectedPresetName ? `Load preset (${selectedPresetName})` : 'Load preset'}>
                                <Download className="h-4 w-4" />
                              </Button>
                            </DropdownMenuTrigger>
                          </TooltipTrigger>
                          <DropdownMenuContent align="end" className="max-h-80 w-64 overflow-y-auto" onCloseAutoFocus={(e) => e.preventDefault()}>
                            {TRACKER_PRESETS.map((preset) => (
                              <DropdownMenuItem key={preset.name} onClick={() => {
                                setPresetTooltipOpen(false)
                                setSaveError('')
                                setFieldErrors({})
                                setDraft((current) => ({
                                  ...current,
                                  name: preset.name,
                                  url: preset.url,
                                  api_path: preset.api_path,
                                  timeout_seconds: Number(preset.timeout_seconds || 0),
                                }))
                                requestAnimationFrame(() => {
                                  nameInputRef.current?.focus()
                                  nameInputRef.current?.select?.()
                                })
                              }}>
                                {preset.name}
                              </DropdownMenuItem>
                            ))}
                          </DropdownMenuContent>
                        </DropdownMenu>
                        <TooltipContent>{selectedPresetName ? `Load preset (${selectedPresetName})` : 'Load preset'}</TooltipContent>
                      </Tooltip>
                    </TooltipProvider>
                  )}
                </div>
              </div>
            </div>

            {/* URL / API Path */}
            <div className="rounded-md border border-border/60">
              <div className="p-3">
                <div className={rowClass}>
                  <div className={labelClass}>
                    <Label className="text-sm font-medium">URL</Label>
                    <p className="mt-1 text-xs text-muted-foreground">Base URL of the tracker or aggregator</p>
                  </div>
                  <div className={controlWideClass}>
                    <Input className={`h-9 ${fieldClass('url')}`} value={draft.url} onChange={(e) => update('url', e.target.value)} placeholder="https://tracker.example.com" autoComplete="off" />
                  </div>
                </div>
              </div>
              <div className="relative p-3">
                <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                <div className={rowClass}>
                  <div className={labelClass}>
                    <Label className="text-sm font-medium">Torznab Path</Label>
                    <p className="mt-1 text-xs text-muted-foreground">API path for the Torznab endpoint</p>
                  </div>
                  <div className={controlWideClass}>
                    <Input className={`h-9 ${fieldClass('api_path')}`} value={draft.api_path} onChange={(e) => update('api_path', e.target.value)} placeholder="/api" autoComplete="off" />
                  </div>
                </div>
              </div>
            </div>

            {/* Passkey — the credential a private tracker issues for its Torznab feed */}
            <div className="rounded-md border border-border/60 p-3">
              <div className={rowClass}>
                <div className={labelClass}>
                  <Label className="text-sm font-medium">Passkey</Label>
                  <p className="mt-1 text-xs text-muted-foreground">
                    The token your tracker issues for its Torznab/RSS feed (sometimes called RSS key or API key on the tracker's profile page). Sent as <code>?apikey=</code> on every request. Leave blank for public trackers.
                  </p>
                </div>
                <div className={controlWideClass}>
                  <Input className={`h-9 ${fieldClass('api_key')}`} type="password" value={draft.api_key} onChange={(e) => update('api_key', e.target.value)} autoComplete="off" />
                </div>
              </div>
            </div>

            {/* Proxy */}
            <div className="rounded-md border border-border/60 p-3">
              <div className={rowClass}>
                <div className={labelClass}>
                  <Label className="text-sm font-medium">HTTP(S) Proxy</Label>
                  <p className="mt-1 text-xs text-muted-foreground">Optional per-tracker proxy override</p>
                </div>
                <div className={controlWideClass}>
                  <Input className={`h-9 ${fieldClass('proxy_url')}`} value={draft.proxy_url} onChange={(e) => update('proxy_url', e.target.value)} placeholder="http://proxy:8888" autoComplete="off" />
                </div>
              </div>
            </div>

            {/* Rate limits */}
            <div className="rounded-md border border-border/60">
              <div className="p-3">
                <div className={rowClass}>
                  <div className={labelClass}>
                    <Label className="text-sm font-medium">Timeout (seconds)</Label>
                  </div>
                  <div className={controlNarrowClass}>
                    <Input className={`h-9 ${fieldClass('timeout_seconds')}`} type="number" min={0} value={draft.timeout_seconds === 0 ? '' : draft.timeout_seconds} onChange={(e) => update('timeout_seconds', e.target.value === '' ? 0 : Number(e.target.value))} placeholder="10" />
                  </div>
                </div>
              </div>
              <div className="relative p-3">
                <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                <div className={rowClass}>
                  <div className={labelClass}>
                    <Label className="text-sm font-medium">Requests/Second</Label>
                  </div>
                  <div className={controlNarrowClass}>
                    <Input className={`h-9 ${fieldClass('rate_limit_rps')}`} type="number" min={0} value={draft.rate_limit_rps === 0 ? '' : draft.rate_limit_rps} onChange={(e) => update('rate_limit_rps', e.target.value === '' ? 0 : Number(e.target.value))} placeholder="∞" />
                  </div>
                </div>
              </div>
              <div className="relative p-3">
                <div className="absolute left-3 right-3 top-0 border-t border-border/60" />
                <div className={rowClass}>
                  <div className={labelClass}>
                    <Label className="text-sm font-medium">Hits/Day</Label>
                  </div>
                  <div className={controlNarrowClass}>
                    <Input className={`h-9 ${fieldClass('api_hits_day')}`} type="number" min={0} value={draft.api_hits_day === 0 ? '' : draft.api_hits_day} onChange={(e) => update('api_hits_day', e.target.value === '' ? 0 : Number(e.target.value))} placeholder="∞" />
                  </div>
                </div>
              </div>
            </div>

            {/* H&R rules */}
            <div className="rounded-md border border-amber-500/40 bg-amber-50/40 dark:bg-amber-950/20">
              <div className="flex items-center gap-2 border-b border-amber-500/30 px-3 py-2">
                <span className="text-sm font-semibold text-amber-700 dark:text-amber-400">Hit &amp; Run Rules</span>
                <span className="rounded-full bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/50 dark:text-amber-400">Private tracker</span>
              </div>
              <p className="px-3 pt-2 text-xs text-muted-foreground">
                Set your tracker's seeding obligations. SeedStream will enforce these before allowing any torrent removal, protecting your account from H&amp;R flags.
                Check your tracker's rules page for the exact requirements.
              </p>
              <div className="p-3 pt-2">
                <div className={rowClass}>
                  <div className={labelClass}>
                    <Label className="text-sm font-medium">Min. seed time (hours)</Label>
                    <p className="mt-0.5 text-xs text-muted-foreground">e.g. 72 for 3 days, 168 for 1 week</p>
                  </div>
                  <div className={controlNarrowClass}>
                    <Input
                      className="h-9"
                      type="number"
                      min={0}
                      step={1}
                      value={draft.hnr_min_seed_hours === 0 ? '' : draft.hnr_min_seed_hours}
                      onChange={(e) => update('hnr_min_seed_hours', e.target.value === '' ? 0 : Number(e.target.value))}
                      placeholder="0 = none"
                    />
                  </div>
                </div>
              </div>
              <div className="relative p-3 pt-0">
                <div className="absolute left-3 right-3 top-0 border-t border-amber-500/20" />
                <div className={rowClass}>
                  <div className={labelClass}>
                    <Label className="text-sm font-medium">Min. ratio</Label>
                    <p className="mt-0.5 text-xs text-muted-foreground">e.g. 1.0 means upload = download</p>
                  </div>
                  <div className={controlNarrowClass}>
                    <Input
                      className="h-9"
                      type="number"
                      min={0}
                      step={0.1}
                      value={draft.hnr_min_ratio === 0 ? '' : draft.hnr_min_ratio}
                      onChange={(e) => update('hnr_min_ratio', e.target.value === '' ? 0 : Number(e.target.value))}
                      placeholder="0 = none"
                    />
                  </div>
                </div>
              </div>
              <div className="relative p-3 pt-0">
                <div className="absolute left-3 right-3 top-0 border-t border-amber-500/20" />
                <div className={rowClass}>
                  <div className={labelClass}>
                    <Label className="text-sm font-medium">Satisfy mode</Label>
                    <p className="mt-0.5 text-xs text-muted-foreground">
                      <strong>Either</strong>: seed time <em>or</em> ratio clears the obligation<br />
                      <strong>Both</strong>: seed time <em>and</em> ratio must both be met
                    </p>
                  </div>
                  <div className={controlNarrowClass}>
                    <select
                      className="h-9 w-full rounded-md border border-input bg-background px-3 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                      value={draft.hnr_mode || 'any'}
                      onChange={(e) => update('hnr_mode', e.target.value)}
                    >
                      <option value="any">Either (OR)</option>
                      <option value="all">Both (AND)</option>
                    </select>
                  </div>
                </div>
              </div>
            </div>

          </div>
        </div>

        <DialogFooter className="flex items-center justify-between gap-3">
          <div className="min-h-9 flex-1">
            {saveError && (
              <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                {saveError}
              </div>
            )}
          </div>
          <div className="flex flex-row items-center justify-end gap-2">
            <Button type="button" variant="outline" onClick={requestClose}>Cancel</Button>
            <Button type="button" variant="destructive" onClick={handleSave} disabled={saving}>{saveLabel}</Button>
          </div>
        </DialogFooter>
      </DialogContent>
      <ConfirmDialog
        open={showDiscardConfirm}
        onOpenChange={setShowDiscardConfirm}
        title="Discard changes?"
        description="Your unsaved tracker changes will be lost."
        confirmLabel="Discard"
        onConfirm={() => {
          setShowDiscardConfirm(false)
          onClearStatus?.()
          onOpenChange(false)
        }}
      />
    </Dialog>
  )
}

export function TorrentTrackerSettings({ fields = [], append, update, replace, onPersist, onClearStatus, onStatus }) {
  const trackers = fields
  const [editingIndex, setEditingIndex] = useState(null)
  const [showAddDialog, setShowAddDialog] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState(null)
  const [deleteBlockedName, setDeleteBlockedName] = useState('')

  useEffect(() => () => { onClearStatus?.() }, [onClearStatus])

  const handleCreate = async (draft) => {
    const nextTrackers = [...trackers.map((t) => normalizeTrackerDraft(t)), normalizeTrackerDraft(draft)]
    await onPersist?.(nextTrackers)
    append(normalizeTrackerDraft(draft))
    setDeleteBlockedName('')
    onStatus?.({ type: 'success', message: `Tracker "${draft.name || draft.url}" added successfully.` })
  }

  const handleSave = async (index, draft) => {
    const nextTrackers = [...trackers]
    nextTrackers[index] = normalizeTrackerDraft(draft)
    await onPersist?.(nextTrackers)
    update(index, normalizeTrackerDraft(draft))
    setDeleteBlockedName('')
    onStatus?.({ type: 'success', message: `Tracker "${draft.name || draft.url}" saved successfully.` })
  }

  const handleDelete = async (index) => {
    const tracker = trackers[index]
    if (!tracker) return
    setDeleteBlockedName('')
    const nextTrackers = trackers.filter((_, i) => i !== index).map((t) => normalizeTrackerDraft(t))
    try {
      await onPersist?.(nextTrackers)
      replace(nextTrackers)
      onStatus?.({ type: 'success', message: `Tracker "${tracker.name || tracker.url}" deleted successfully.` })
    } catch (error) {
      onStatus?.({ type: 'error', message: error?.message || `Failed to delete tracker "${tracker.name || tracker.url}".` })
    }
  }

  const handleToggleEnabled = async (index, enabled) => {
    const current = trackers[index]
    if (!current) return
    const nextTrackers = [...trackers]
    nextTrackers[index] = { ...normalizeTrackerDraft(current), enabled }
    await onPersist?.(nextTrackers)
    replace(nextTrackers.map((t) => normalizeTrackerDraft(t)))
    setDeleteBlockedName('')
    onStatus?.({ type: 'success', message: `Tracker "${current.name || current.url}" ${enabled ? 'enabled' : 'disabled'} successfully.` })
  }

  return (
    <TooltipProvider delayDuration={100}>
      <div className="space-y-4">
        <Card>
          <CardHeader>
            <div className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-3">
              <div className="min-w-0 space-y-0.5">
                <CardTitle>Torrent Trackers</CardTitle>
                <CardDescription>
                  Add torrent trackers directly — no Prowlarr required. Enter the passkey from your tracker's profile page; public trackers need none.
                </CardDescription>
              </div>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button type="button" variant="destructive" size="icon" className="h-9 w-9 shrink-0" onClick={() => setShowAddDialog(true)} aria-label="Add tracker">
                    <Plus className="h-4 w-4" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Add Tracker</TooltipContent>
              </Tooltip>
            </div>
          </CardHeader>
          <CardContent className="space-y-3">
            {trackers.length === 0 ? (
              <p className="text-sm text-muted-foreground">No torrent trackers configured yet.</p>
            ) : (
              trackers.map((tracker, index) => {
                const normalized = normalizeTrackerDraft(tracker)
                const summary = summarizeTracker(normalized)
                return (
                  <Card
                    key={`${normalized.name || normalized.url || 'tracker'}-${index}`}
                    className={deleteBlockedName && deleteBlockedName === (normalized.name || normalized.url || '') ? 'border-destructive/60 ring-1 ring-destructive/30' : ''}
                  >
                    <CardContent className="pt-6">
                      <div className="min-w-0 flex-1 space-y-3">
                        <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                          <div className="flex items-center gap-2 self-end sm:order-2">
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <div className="inline-flex h-9 w-20 items-center justify-center rounded-md border border-border/60 bg-muted/30 px-2">
                                  <Switch
                                    checked={normalized.enabled !== false}
                                    onCheckedChange={(checked) => handleToggleEnabled(index, checked === true)}
                                    aria-label={normalized.enabled !== false ? `Disable ${normalized.name || 'tracker'}` : `Enable ${normalized.name || 'tracker'}`}
                                    className="h-6 w-12 data-[state=checked]:bg-green-500 data-[state=unchecked]:bg-muted-foreground/30"
                                    thumbClassName="flex h-5 w-5 items-center justify-center data-[state=checked]:translate-x-6 data-[state=unchecked]:translate-x-0"
                                  />
                                </div>
                              </TooltipTrigger>
                              <TooltipContent>{normalized.enabled !== false ? 'Disable tracker' : 'Enable tracker'}</TooltipContent>
                            </Tooltip>
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Button type="button" variant="outline" size="icon" className="h-9 w-9" aria-label={`Edit ${normalized.name || 'tracker'}`} onClick={() => {
                                  setDeleteBlockedName('')
                                  onClearStatus?.()
                                  setEditingIndex(index)
                                }}>
                                  <Settings className="h-4 w-4" />
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>Edit tracker</TooltipContent>
                            </Tooltip>
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Button type="button" variant="destructive" size="icon" className="h-9 w-9" aria-label={`Delete ${normalized.name || 'tracker'}`} onClick={() => setDeleteTarget({ index, name: normalized.name || normalized.url })}>
                                  <Trash2 className="h-4 w-4" />
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>Delete tracker</TooltipContent>
                            </Tooltip>
                          </div>
                          <div className="min-w-0 font-semibold sm:order-1">{normalized.name || normalized.url || `Tracker ${index + 1}`}</div>
                        </div>
                        <div className="flex flex-wrap gap-2 text-xs text-muted-foreground">
                          <span className="rounded-full border border-border px-2 py-1">Torznab</span>
                          {summary.map((part) => (
                            <span key={part} className="rounded-full border border-border px-2 py-1">{part}</span>
                          ))}
                        </div>
                      </div>
                    </CardContent>

                    <TrackerDialog
                      open={editingIndex === index}
                      onOpenChange={(nextOpen) => {
                        if (!nextOpen) setDeleteBlockedName('')
                        setEditingIndex(nextOpen ? index : null)
                      }}
                      initialValue={normalized}
                      existingNames={trackers.filter((_, i) => i !== index).map((t) => t?.name || '')}
                      existingTrackers={trackers.filter((_, i) => i !== index)}
                      onSave={(draft) => handleSave(index, draft)}
                      onClearStatus={onClearStatus}
                      title={normalized.name || normalized.url || 'Edit Tracker'}
                      description="Edit torrent tracker settings."
                      saveLabel="Save"
                      editing
                    />
                  </Card>
                )
              })
            )}
          </CardContent>
        </Card>

        <TrackerDialog
          open={showAddDialog}
          onOpenChange={(nextOpen) => setShowAddDialog(nextOpen)}
          initialValue={emptyTrackerDraft()}
          existingNames={trackers.map((t) => t?.name || '')}
          existingTrackers={trackers}
          onSave={handleCreate}
          onClearStatus={onClearStatus}
          title="Add Torrent Tracker"
          description="Add a torrent tracker with a Torznab feed. Enter the passkey from your tracker's profile page (leave blank for public trackers)."
          saveLabel="Save"
          editing={false}
        />
        <ConfirmDialog
          open={Boolean(deleteTarget)}
          onOpenChange={(nextOpen) => { if (!nextOpen) setDeleteTarget(null) }}
          title="Delete tracker?"
          description={deleteTarget ? `Are you sure you want to delete tracker "${deleteTarget.name}"?` : ''}
          confirmLabel="Delete"
          onConfirm={() => {
            const target = deleteTarget
            setDeleteTarget(null)
            if (target) void handleDelete(target.index)
          }}
        />
      </div>
    </TooltipProvider>
  )
}
