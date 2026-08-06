import React, { useCallback, useEffect, useMemo, useState } from 'react'
import { Search, Loader2, Globe } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { apiFetch } from '../api'

// TrackerDefinitionPicker lets a user find their tracker by name and fill in only
// the credentials it asks for, instead of knowing a Torznab URL and API path.
// The credential fields are generated from the definition itself, so each
// tracker asks for exactly what it needs.
export function TrackerDefinitionPicker({ onSelect, onCancel }) {
  const [query, setQuery] = useState('')
  const [entries, setEntries] = useState([])
  const [total, setTotal] = useState(0)
  const [definitionsDir, setDefinitionsDir] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [chosen, setChosen] = useState(null)
  const [values, setValues] = useState({})
  const [urlOverride, setUrlOverride] = useState('')

  const load = useCallback(async (q) => {
    setLoading(true)
    setError('')
    try {
      const res = await apiFetch(`/api/trackers/definitions?q=${encodeURIComponent(q || '')}&limit=200`)
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const data = await res.json()
      setEntries(Array.isArray(data.definitions) ? data.definitions : [])
      setTotal(Number(data.total || 0))
      setDefinitionsDir(data.definitions_dir || '')
    } catch (e) {
      setError(`Could not load the tracker list: ${e.message}`)
      setEntries([])
    } finally {
      setLoading(false)
    }
  }, [])

  // Debounced so typing does not fire a request per keystroke.
  useEffect(() => {
    const t = setTimeout(() => { void load(query) }, 200)
    return () => clearTimeout(t)
  }, [query, load])

  const credentials = useMemo(() => (chosen?.settings || []).filter(Boolean), [chosen])

  // The server decides what is sensitive, since definitions mark passkeys and
  // cookies as plain text even though they must be masked.
  const isSecret = (setting) => setting.secret === true || setting.type === 'password'

  const choose = (entry) => {
    setChosen(entry)
    setUrlOverride('')
    const initial = {}
    for (const s of entry.settings || []) initial[s.name] = ''
    setValues(initial)
  }

  const confirm = () => {
    onSelect?.({
      name: chosen.name,
      definition_id: chosen.id,
      definition_settings: values,
      url: urlOverride.trim(),
    })
  }

  if (chosen) {
    return (
      <div className="space-y-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="text-sm font-medium">{chosen.name}</div>
            <div className="text-xs text-muted-foreground break-all">
              {(chosen.links && chosen.links[0]) || chosen.id}
            </div>
          </div>
          <Button type="button" variant="outline" size="sm" onClick={() => setChosen(null)}>Back</Button>
        </div>

        {chosen.needs_cookie && (
          <div className="rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-xs">
            This tracker challenges logins with a captcha, which SeedStream cannot solve.
            Paste a session cookie from your browser into the cookie field instead of a password.
          </div>
        )}

        <div className="rounded-md border border-border/60 p-3 space-y-3">
          {credentials.length === 0 && (
            <div className="text-sm text-muted-foreground">This tracker needs no credentials.</div>
          )}
          {credentials.map((s) => (
            <div key={s.name} className="flex flex-col gap-1.5">
              <Label className="text-sm font-medium">{s.label || s.name}</Label>
              <Input
                className="h-9"
                type={isSecret(s) ? 'password' : 'text'}
                autoComplete="off"
                value={values[s.name] ?? ''}
                onChange={(e) => setValues((v) => ({ ...v, [s.name]: e.target.value }))}
              />
            </div>
          ))}

          <div className="flex flex-col gap-1.5">
            <Label className="text-sm font-medium flex items-center gap-1.5">
              <Globe className="h-3.5 w-3.5" /> Tracker address (optional)
            </Label>
            <Input
              className="h-9"
              placeholder={(chosen.links && chosen.links[0]) || 'https://tracker.example.com'}
              value={urlOverride}
              onChange={(e) => setUrlOverride(e.target.value)}
              autoComplete="off"
            />
            <div className="text-xs text-muted-foreground">
              Leave blank to use the address above. Set it if the tracker has moved to a new domain.
            </div>
          </div>
        </div>

        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={() => setChosen(null)}>Cancel</Button>
          <Button type="button" onClick={confirm}>Add tracker</Button>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-3">
      <div className="relative">
        <Search className="absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          className="h-9 pl-8"
          placeholder="Search trackers by name"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          autoFocus
        />
      </div>

      {error && <div className="text-xs text-destructive">{error}</div>}

      <div className="max-h-80 overflow-y-auto rounded-md border border-border/60">
        {loading && (
          <div className="flex items-center gap-2 p-3 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" /> Loading…
          </div>
        )}
        {!loading && entries.length === 0 && (
          <div className="p-3 text-sm text-muted-foreground">
            No trackers matched. {total === 0 && definitionsDir
              ? `Add definition files to ${definitionsDir} to populate this list.`
              : ''}
          </div>
        )}
        {!loading && entries.map((e) => (
          <button
            key={e.id}
            type="button"
            onClick={() => choose(e)}
            className="flex w-full flex-col items-start gap-0.5 border-b border-border/40 p-2.5 text-left last:border-b-0 hover:bg-muted/50"
          >
            <span className="text-sm font-medium">{e.name}</span>
            <span className="text-xs text-muted-foreground break-all">
              {(e.links && e.links[0]) || e.id}{e.type ? ` · ${e.type}` : ''}
            </span>
          </button>
        ))}
      </div>

      <div className="flex items-center justify-between gap-2">
        <span className="text-xs text-muted-foreground">
          {total} tracker{total === 1 ? '' : 's'} available
        </span>
        <Button type="button" variant="outline" size="sm" onClick={onCancel}>Cancel</Button>
      </div>
    </div>
  )
}
