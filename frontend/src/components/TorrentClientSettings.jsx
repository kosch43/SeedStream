import React, { useEffect, useState } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { ConfirmDialog } from "@/components/ConfirmDialog"
import { HardDrive, Plus, Settings, Trash2 } from "lucide-react"

const CACHE_CLEARED_SUFFIX = ' Search cache cleared.'

function normalizeDraft(draft) {
  const v = draft || {}
  return {
    name: (v.name || '').trim(),
    type: 'qbittorrent',
    url: (v.url || '').trim(),
    username: v.username || '',
    password: v.password || '',
    category: (v.category || '').trim(),
    save_path: (v.save_path || '').trim(),
    enabled: v.enabled !== false,
  }
}

function emptyDraft() {
  return normalizeDraft({ category: 'seedstream' })
}

function summarize(client) {
  const parts = []
  if (client.url) parts.push(client.url)
  parts.push(`Category: ${client.category || 'seedstream'}`)
  if (client.save_path) parts.push(`Path: ${client.save_path}`)
  return parts
}

function TorrentClientDialog({ open, onOpenChange, initialValue, onSave, title, description, saveLabel, existingNames = [], editing = false }) {
  const [draft, setDraft] = useState(() => normalizeDraft(initialValue))
  const [error, setError] = useState('')

  useEffect(() => {
    if (open) {
      setDraft(normalizeDraft(initialValue))
      setError('')
    }
  }, [open, initialValue])

  const update = (patch) => setDraft((d) => ({ ...d, ...patch }))

  const handleSave = () => {
    const next = normalizeDraft(draft)
    if (!next.name) { setError('Name is required.'); return }
    if (!next.url) { setError('WebUI URL is required (e.g. http://seedbox:8080).'); return }
    const nameKey = next.name.toLowerCase()
    if (existingNames.some((n) => n.toLowerCase() === nameKey)) {
      setError('A torrent client with this name already exists.')
      return
    }
    onSave(next)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        <div className="space-y-4 py-2">
          <div className="flex flex-col gap-2">
            <Label htmlFor="tc-name">Name</Label>
            <Input id="tc-name" value={draft.name} placeholder="My Seedbox"
              onChange={(e) => update({ name: e.target.value })} autoComplete="off" />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="tc-url">qBittorrent WebUI URL</Label>
            <Input id="tc-url" value={draft.url} placeholder="http://seedbox:8080"
              onChange={(e) => update({ url: e.target.value })} autoComplete="off" />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="flex flex-col gap-2">
              <Label htmlFor="tc-user">Username</Label>
              <Input id="tc-user" value={draft.username} placeholder="admin"
                onChange={(e) => update({ username: e.target.value })} autoComplete="off" />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="tc-pass">Password</Label>
              <Input id="tc-pass" type="password" value={draft.password}
                placeholder={editing ? 'unchanged' : ''}
                onChange={(e) => update({ password: e.target.value })} autoComplete="new-password" />
            </div>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="tc-cat">Category</Label>
            <Input id="tc-cat" value={draft.category} placeholder="seedstream"
              onChange={(e) => update({ category: e.target.value })} autoComplete="off" />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="tc-path">Save path (absolute, readable by SeedStream)</Label>
            <Input id="tc-path" value={draft.save_path} placeholder="/downloads/seedstream"
              onChange={(e) => update({ save_path: e.target.value })} autoComplete="off" />
            <p className="text-xs text-muted-foreground">
              Where qBittorrent writes downloads. SeedStream reads files from here to stream them,
              so this path must be mounted and readable by SeedStream. Leave blank to use the client's default.
            </p>
          </div>
          <div className="flex items-center justify-between">
            <Label htmlFor="tc-enabled">Enabled</Label>
            <Switch id="tc-enabled" checked={draft.enabled}
              onCheckedChange={(v) => update({ enabled: v })} />
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
        </div>
        <DialogFooter>
          <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button type="button" onClick={handleSave}>{saveLabel || 'Save'}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function TorrentClientSettings({ fields, append, update, remove, replace, onPersist, onStatus, onClearStatus }) {
  const [addOpen, setAddOpen] = useState(false)
  const [editIndex, setEditIndex] = useState(-1)
  const [deleteIndex, setDeleteIndex] = useState(-1)

  const clients = Array.isArray(fields) ? fields : []

  const persist = async (nextClients) => {
    onClearStatus?.()
    try {
      await onPersist(nextClients)
      onStatus?.({ type: 'success', message: `Torrent clients saved.${CACHE_CLEARED_SUFFIX}` })
    } catch (err) {
      onStatus?.({ type: 'error', message: err?.message || 'Failed to save torrent clients.' })
    }
  }

  const handleAdd = (draft) => {
    setAddOpen(false)
    const next = [...clients.map(stripRHF), draft]
    replace(next)
    persist(next)
  }

  const handleEdit = (draft) => {
    const idx = editIndex
    setEditIndex(-1)
    const next = clients.map(stripRHF)
    // Preserve existing password if left blank when editing.
    if (!draft.password && next[idx]?.password) draft.password = next[idx].password
    next[idx] = draft
    replace(next)
    persist(next)
  }

  const handleDelete = () => {
    const idx = deleteIndex
    setDeleteIndex(-1)
    const next = clients.map(stripRHF).filter((_, i) => i !== idx)
    replace(next)
    persist(next)
  }

  const existingNames = clients.map((c) => c.name).filter(Boolean)

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0 flex-1 space-y-0.5">
              <CardTitle>Torrent Clients</CardTitle>
              <CardDescription>
                qBittorrent instances (on seedboxes) that receive torrent picks. Torrents download
                sequentially for instant playback and keep seeding for private-tracker ratio —
                SeedStream never seeds itself. Add a Torznab/Prowlarr indexer under Indexers to get torrent results.
              </CardDescription>
            </div>
            <Button type="button" size="sm" onClick={() => setAddOpen(true)} className="shrink-0">
              <Plus className="h-4 w-4 mr-1" /> Add
            </Button>
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          {clients.length === 0 && (
            <p className="text-sm text-muted-foreground">
              No torrent clients configured. Add a qBittorrent client to enable torrent streaming.
            </p>
          )}
          {clients.map((client, index) => (
            <div key={client.id || `${client.name}-${index}`}
              className="flex items-center justify-between gap-3 rounded-lg border border-border p-3">
              <div className="flex items-center gap-3 min-w-0">
                <HardDrive className="h-5 w-5 shrink-0 text-muted-foreground" />
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-medium truncate">{client.name || 'Unnamed'}</span>
                    {client.enabled === false && (
                      <span className="text-xs rounded bg-muted px-1.5 py-0.5 text-muted-foreground">disabled</span>
                    )}
                  </div>
                  <p className="text-xs text-muted-foreground truncate">{summarize(client).join('  •  ')}</p>
                </div>
              </div>
              <div className="flex items-center gap-1 shrink-0">
                <Button type="button" variant="ghost" size="icon" className="h-8 w-8"
                  onClick={() => setEditIndex(index)} aria-label="Edit">
                  <Settings className="h-4 w-4" />
                </Button>
                <Button type="button" variant="ghost" size="icon" className="h-8 w-8 text-destructive"
                  onClick={() => setDeleteIndex(index)} aria-label="Delete">
                  <Trash2 className="h-4 w-4" />
                </Button>
              </div>
            </div>
          ))}
        </CardContent>
      </Card>

      <TorrentClientDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        initialValue={emptyDraft()}
        onSave={handleAdd}
        title="Add torrent client"
        description="Connect a qBittorrent WebUI running on a seedbox."
        saveLabel="Add"
        existingNames={existingNames}
      />

      <TorrentClientDialog
        open={editIndex >= 0}
        onOpenChange={(v) => { if (!v) setEditIndex(-1) }}
        initialValue={editIndex >= 0 ? stripRHF(clients[editIndex]) : emptyDraft()}
        onSave={handleEdit}
        title="Edit torrent client"
        saveLabel="Save"
        editing
        existingNames={existingNames.filter((_, i) => i !== editIndex)}
      />

      <ConfirmDialog
        open={deleteIndex >= 0}
        onOpenChange={(v) => { if (!v) setDeleteIndex(-1) }}
        title="Remove torrent client?"
        description={deleteIndex >= 0 ? `"${clients[deleteIndex]?.name || 'this client'}" will no longer receive torrents.` : ''}
        confirmLabel="Remove"
        onConfirm={handleDelete}
      />
    </div>
  )
}

// stripRHF drops react-hook-form's injected `id` field so persisted config is clean.
function stripRHF(client) {
  if (!client) return client
  const { id: _id, ...rest } = client
  return normalizeDraft(rest)
}

export default TorrentClientSettings
