import { useState, useEffect } from 'react'
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { AlertCircle, Check, Copy, Loader2, LogOut } from "lucide-react"

export default function MemberSetupPage({ currentUser, authToken, onLogout, mustChangePassword, onPasswordChanged }) {
  const [loading, setLoading] = useState(true)
  const [fetchError, setFetchError] = useState('')

  const [prowlarrURL, setProwlarrURL] = useState('')
  const [prowlarrAPIKey, setProwlarrAPIKey] = useState('')
  const [qbitURL, setQbitURL] = useState('')
  const [qbitUsername, setQbitUsername] = useState('')
  const [qbitPassword, setQbitPassword] = useState('')
  const [qbitCategory, setQbitCategory] = useState('')
  const [qbitSavePath, setQbitSavePath] = useState('')

  const [manifestURL, setManifestURL] = useState('')

  const [prowlarrSaving, setProwlarrSaving] = useState(false)
  const [prowlarrSaveStatus, setProwlarrSaveStatus] = useState(null)

  const [qbitSaving, setQbitSaving] = useState(false)
  const [qbitSaveStatus, setQbitSaveStatus] = useState(null)

  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [passwordSaving, setPasswordSaving] = useState(false)
  const [passwordSaveStatus, setPasswordSaveStatus] = useState(null)

  const [copied, setCopied] = useState(false)

  const authHeaders = () => {
    const h = { 'Content-Type': 'application/json' }
    if (authToken) h['Authorization'] = `Bearer ${authToken}`
    return h
  }

  useEffect(() => {
    setLoading(true)
    setFetchError('')
    fetch('/api/member/setup', {
      credentials: 'include',
      headers: authToken ? { Authorization: `Bearer ${authToken}` } : undefined,
    })
      .then(res => {
        if (!res.ok) throw new Error('Failed to load setup')
        return res.json()
      })
      .then(data => {
        setProwlarrURL(data.prowlarr_url || '')
        const tc = data.torrent_client
        if (tc) {
          setQbitURL(tc.url || '')
          setQbitUsername(tc.username || '')
          setQbitCategory(tc.category || '')
          setQbitSavePath(tc.save_path || '')
        }
        const token = data.token || authToken || ''
        if (token) {
          setManifestURL(`${window.location.origin}/${token}/manifest.json`)
        }
      })
      .catch(err => setFetchError(err.message || 'Failed to load setup'))
      .finally(() => setLoading(false))
  }, [authToken])

  const saveProwlarr = async () => {
    setProwlarrSaving(true)
    setProwlarrSaveStatus(null)
    try {
      const res = await fetch('/api/member/setup', {
        method: 'PUT',
        credentials: 'include',
        headers: authHeaders(),
        body: JSON.stringify({
          prowlarr_url: prowlarrURL,
          prowlarr_api_key: prowlarrAPIKey,
        }),
      })
      const data = await res.json()
      if (data.success) {
        setProwlarrSaveStatus({ type: 'success', msg: 'Prowlarr settings saved.' })
        setProwlarrAPIKey('')
      } else {
        setProwlarrSaveStatus({ type: 'error', msg: data.error || 'Failed to save' })
      }
    } catch (err) {
      setProwlarrSaveStatus({ type: 'error', msg: err.message || 'Failed to save' })
    } finally {
      setProwlarrSaving(false)
    }
  }

  const saveQbit = async () => {
    setQbitSaving(true)
    setQbitSaveStatus(null)
    try {
      const torrentClient = qbitURL ? {
        type: 'qbittorrent',
        url: qbitURL,
        username: qbitUsername,
        password: qbitPassword,
        category: qbitCategory,
        save_path: qbitSavePath,
      } : null
      const res = await fetch('/api/member/setup', {
        method: 'PUT',
        credentials: 'include',
        headers: authHeaders(),
        body: JSON.stringify({ torrent_client: torrentClient }),
      })
      const data = await res.json()
      if (data.success) {
        setQbitSaveStatus({ type: 'success', msg: 'qBittorrent settings saved.' })
        setQbitPassword('')
      } else {
        setQbitSaveStatus({ type: 'error', msg: data.error || 'Failed to save' })
      }
    } catch (err) {
      setQbitSaveStatus({ type: 'error', msg: err.message || 'Failed to save' })
    } finally {
      setQbitSaving(false)
    }
  }

  const savePassword = async () => {
    setPasswordSaveStatus(null)
    if (newPassword.length < 6) {
      setPasswordSaveStatus({ type: 'error', msg: 'Password must be at least 6 characters' })
      return
    }
    if (newPassword !== confirmPassword) {
      setPasswordSaveStatus({ type: 'error', msg: 'Passwords do not match' })
      return
    }
    setPasswordSaving(true)
    try {
      const res = await fetch('/api/member/change-password', {
        method: 'POST',
        credentials: 'include',
        headers: authHeaders(),
        body: JSON.stringify({ password: newPassword }),
      })
      const data = await res.json()
      if (data.success) {
        setPasswordSaveStatus({ type: 'success', msg: 'Password updated successfully.' })
        setNewPassword('')
        setConfirmPassword('')
        onPasswordChanged?.()
      } else {
        setPasswordSaveStatus({ type: 'error', msg: data.error || 'Failed to update password' })
      }
    } catch (err) {
      setPasswordSaveStatus({ type: 'error', msg: err.message || 'Failed to update password' })
    } finally {
      setPasswordSaving(false)
    }
  }

  const copyManifestURL = () => {
    if (!manifestURL) return
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(manifestURL).then(() => {
        setCopied(true)
        setTimeout(() => setCopied(false), 2000)
      })
    } else {
      const textarea = document.createElement('textarea')
      textarea.value = manifestURL
      textarea.style.position = 'fixed'
      textarea.style.opacity = '0'
      document.body.appendChild(textarea)
      textarea.select()
      try { document.execCommand('copy') } catch { void 0 }
      document.body.removeChild(textarea)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }
  }

  if (loading) {
    return (
      <div className="fixed inset-0 z-50 flex flex-col items-center justify-center bg-background/80 backdrop-blur-sm gap-4">
        <Loader2 className="h-12 w-12 text-primary animate-spin" />
        <div className="text-xl font-semibold tracking-tight">Loading...</div>
      </div>
    )
  }

  return (
    <div className="min-h-screen bg-background">
      <header className="border-b border-border px-4 py-3 flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className="font-bold text-lg">SeedStream</span>
          {currentUser && (
            <span className="text-sm text-muted-foreground">/ {currentUser}</span>
          )}
        </div>
        <Button variant="outline" size="sm" onClick={onLogout}>
          <LogOut className="h-4 w-4 mr-1" />
          Logout
        </Button>
      </header>

      <div className="max-w-2xl mx-auto px-4 py-8 space-y-6">
        {fetchError && (
          <div className="flex items-center gap-2 p-3 text-sm text-destructive bg-destructive/10 rounded-md">
            <AlertCircle className="h-4 w-4" />
            <span>{fetchError}</span>
          </div>
        )}

        {mustChangePassword && (
          <div className="flex items-center gap-2 p-3 text-sm text-amber-700 dark:text-amber-400 bg-amber-50 dark:bg-amber-950/30 border border-amber-200 dark:border-amber-800 rounded-md">
            <AlertCircle className="h-4 w-4 shrink-0" />
            <span>You must set a password before using SeedStream. Please update your password below.</span>
          </div>
        )}

        {manifestURL && (
          <Card>
            <CardHeader>
              <CardTitle>Your Manifest URL</CardTitle>
              <CardDescription>Add this URL to your Stremio app to access your stream.</CardDescription>
            </CardHeader>
            <CardContent>
              <div className="flex items-center gap-2">
                <Input value={manifestURL} readOnly className="font-mono text-xs" />
                <Button variant="outline" size="icon" onClick={copyManifestURL} aria-label="Copy manifest URL">
                  {copied ? <Check className="h-4 w-4 text-emerald-500" /> : <Copy className="h-4 w-4" />}
                </Button>
              </div>
            </CardContent>
          </Card>
        )}

        <Card>
          <CardHeader>
            <CardTitle>Prowlarr</CardTitle>
            <CardDescription>Configure your Prowlarr indexer manager connection.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="prowlarr-url">Prowlarr URL</Label>
              <Input
                id="prowlarr-url"
                type="url"
                placeholder="http://localhost:9696"
                value={prowlarrURL}
                onChange={e => setProwlarrURL(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="prowlarr-key">API Key</Label>
              <Input
                id="prowlarr-key"
                type="password"
                placeholder="Leave blank to keep existing key"
                value={prowlarrAPIKey}
                onChange={e => setProwlarrAPIKey(e.target.value)}
              />
            </div>
            {prowlarrSaveStatus && (
              <div className={`flex items-center gap-2 p-3 text-sm rounded-md ${prowlarrSaveStatus.type === 'error' ? 'text-destructive bg-destructive/10' : 'text-emerald-700 dark:text-emerald-400 bg-emerald-50 dark:bg-emerald-950/30'}`}>
                {prowlarrSaveStatus.type === 'error' ? <AlertCircle className="h-4 w-4 shrink-0" /> : <Check className="h-4 w-4 shrink-0" />}
                <span>{prowlarrSaveStatus.msg}</span>
              </div>
            )}
            <Button onClick={saveProwlarr} disabled={prowlarrSaving}>
              {prowlarrSaving ? <><Loader2 className="mr-2 h-4 w-4 animate-spin" /> Saving...</> : 'Save Prowlarr'}
            </Button>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>qBittorrent</CardTitle>
            <CardDescription>Configure your qBittorrent download client.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="qbit-url">qBittorrent URL</Label>
              <Input
                id="qbit-url"
                type="url"
                placeholder="http://localhost:8080"
                value={qbitURL}
                onChange={e => setQbitURL(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="qbit-username">Username</Label>
              <Input
                id="qbit-username"
                type="text"
                placeholder="admin"
                value={qbitUsername}
                onChange={e => setQbitUsername(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="qbit-password">Password</Label>
              <Input
                id="qbit-password"
                type="password"
                placeholder="Leave blank to keep existing password"
                value={qbitPassword}
                onChange={e => setQbitPassword(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="qbit-category">Category</Label>
              <Input
                id="qbit-category"
                type="text"
                placeholder="seedstream"
                value={qbitCategory}
                onChange={e => setQbitCategory(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="qbit-save-path">Save Path</Label>
              <Input
                id="qbit-save-path"
                type="text"
                placeholder="/downloads"
                value={qbitSavePath}
                onChange={e => setQbitSavePath(e.target.value)}
              />
            </div>
            {qbitSaveStatus && (
              <div className={`flex items-center gap-2 p-3 text-sm rounded-md ${qbitSaveStatus.type === 'error' ? 'text-destructive bg-destructive/10' : 'text-emerald-700 dark:text-emerald-400 bg-emerald-50 dark:bg-emerald-950/30'}`}>
                {qbitSaveStatus.type === 'error' ? <AlertCircle className="h-4 w-4 shrink-0" /> : <Check className="h-4 w-4 shrink-0" />}
                <span>{qbitSaveStatus.msg}</span>
              </div>
            )}
            <Button onClick={saveQbit} disabled={qbitSaving}>
              {qbitSaving ? <><Loader2 className="mr-2 h-4 w-4 animate-spin" /> Saving...</> : 'Save qBittorrent'}
            </Button>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Change Password</CardTitle>
            <CardDescription>Set or update your login password.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="new-password">New Password</Label>
              <Input
                id="new-password"
                type="password"
                placeholder="At least 6 characters"
                value={newPassword}
                onChange={e => setNewPassword(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="confirm-password">Confirm Password</Label>
              <Input
                id="confirm-password"
                type="password"
                placeholder="Repeat your new password"
                value={confirmPassword}
                onChange={e => setConfirmPassword(e.target.value)}
              />
            </div>
            {passwordSaveStatus && (
              <div className={`flex items-center gap-2 p-3 text-sm rounded-md ${passwordSaveStatus.type === 'error' ? 'text-destructive bg-destructive/10' : 'text-emerald-700 dark:text-emerald-400 bg-emerald-50 dark:bg-emerald-950/30'}`}>
                {passwordSaveStatus.type === 'error' ? <AlertCircle className="h-4 w-4 shrink-0" /> : <Check className="h-4 w-4 shrink-0" />}
                <span>{passwordSaveStatus.msg}</span>
              </div>
            )}
            <Button onClick={savePassword} disabled={passwordSaving}>
              {passwordSaving ? <><Loader2 className="mr-2 h-4 w-4 animate-spin" /> Saving...</> : 'Update Password'}
            </Button>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
