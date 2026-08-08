import { useState, useEffect, useRef, useCallback } from 'react'
import Settings from './Settings'
import Login from './components/Login'
import ChangePassword from './components/ChangePassword'
import { SidebarProvider, SidebarInset } from "@/components/ui/sidebar"
import { AppSidebar } from "@/components/AppSidebar"
import { SiteHeader } from "@/components/SiteHeader"
import { DashboardPage } from "@/components/DashboardPage"
import { StatisticsPage } from "@/components/StatisticsPage"
import { LogsPage } from "@/components/LogsPage"
import { CerberusPage } from "@/components/CerberusPage"
import { ProfilePage } from "@/components/ProfilePage"
import StreamManagement from './components/StreamManagement'
import { getApiUrl, UNAUTHORIZED_EVENT } from './api'
import { AlertCircle, Loader2 } from "lucide-react"

import { useAdminRuntime } from './hooks/useAdminRuntime'

function App() {
  const [authChecked, setAuthChecked] = useState(false)
  const [authenticated, setAuthenticated] = useState(false)
  const [isAdmin, setIsAdmin] = useState(false)
  const [currentUser, setCurrentUser] = useState(null)
  const [mustChangePassword, setMustChangePassword] = useState(false)
  const [theme, setTheme] = useState(localStorage.getItem('theme') || 'system')
  const hasLoggedOutRef = useRef(false)
  const [activePage, setActivePage] = useState('dashboard')

  const shouldRunAdminRuntime = authenticated && isAdmin

  const {
    stats,
    config,
    saveStatus,
    clearSaveStatus,
    isSaving,
    isRestarting,
    error,
    wsStatus,
    ws,
    version,
    logs,
    history,
    torrentHistory,
    indexerCaps,
    sendCommand,
  } = useAdminRuntime({
    authenticated: shouldRunAdminRuntime,
    hasLoggedOutRef,
    setAuthenticated,
    setCurrentUser,
    setMustChangePassword,
  })

  const chartData = history.map((h, i) => ({
    time: h.time,
    speed: h.speed,
    torrents: torrentHistory[i]?.torrents ?? 0,
  }))

  useEffect(() => {
    // Dashboard authentication is cookie-only. Remove the pre-separation bearer
    // token so it cannot be reused as an API or addon credential.
    localStorage.removeItem('auth_token')
    fetch('/api/auth/check', {
      credentials: 'include',
    })
      .then(res => res.json().then(data => ({ ok: res.ok, data })))
      .then(({ ok, data }) => {
        if (ok && data.authenticated) {
          hasLoggedOutRef.current = false
          setAuthenticated(true)
          setCurrentUser(data.username)
          setIsAdmin(data.is_admin !== false)
          setMustChangePassword(data.must_change_password || false)
        } else {
          setAuthenticated(false)
        }
      })
      .catch(() => {
        // Server unreachable on startup — fall back to login screen
        setAuthenticated(false)
      })
      .finally(() => {
        setAuthChecked(true)
      })
  }, [])

  const handleLogin = (username, mustChange, isAdminFlag) => {
    hasLoggedOutRef.current = false
    setAuthenticated(true)
    setCurrentUser(username)
    setIsAdmin(isAdminFlag !== false)
    setMustChangePassword(mustChange)
  }

  const clearAuthState = useCallback(() => {
    hasLoggedOutRef.current = true
    setAuthenticated(false)
    setCurrentUser(null)
    setMustChangePassword(false)
    if (ws) {
      ws.close()
    }
    window.ws = null
  }, [ws])

  const handleLogout = useCallback(() => {
    hasLoggedOutRef.current = true
    fetch(getApiUrl('/api/auth/logout'), {
      method: 'POST',
      credentials: 'include',
    }).catch(() => {})
    clearAuthState()
  }, [clearAuthState])

  useEffect(() => {
    const handleUnauthorized = () => {
      if (!authenticated || hasLoggedOutRef.current) return
      clearAuthState()
    }

    window.addEventListener(UNAUTHORIZED_EVENT, handleUnauthorized)
    return () => {
      window.removeEventListener(UNAUTHORIZED_EVENT, handleUnauthorized)
    }
  }, [authenticated, clearAuthState])

  useEffect(() => {
    const root = window.document.documentElement;
    root.classList.remove("light", "dark");

    if (theme === "system") {
      const systemTheme = window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
      root.classList.add(systemTheme);
    } else {
      root.classList.add(theme);
    }
    localStorage.setItem('theme', theme);
  }, [theme]);

  const isSettingsPage = activePage === 'settings'

  if (!authChecked) {
    return (
      <div className="fixed inset-0 z-50 flex flex-col items-center justify-center bg-background/80 backdrop-blur-sm gap-4">
        <Loader2 className="h-12 w-12 text-primary animate-spin" />
        <div className="text-xl font-semibold tracking-tight">Verifying session...</div>
      </div>
    )
  }

  if (!authenticated) {
    return <Login onLogin={handleLogin} version={version} />
  }

  if (mustChangePassword && currentUser) {
    return <ChangePassword username={currentUser} onPasswordChanged={() => {
      setMustChangePassword(false)
    }} requireCurrentPassword={false} />
  }

  if (error && wsStatus === 'disconnected') {
      return (
        <div className="flex flex-col h-screen items-center justify-center gap-4">
            <AlertCircle className="h-12 w-12 text-destructive animate-pulse" />
            <div className="text-xl font-semibold text-destructive">{error}</div>
            <p className="text-muted-foreground">Retrying connection...</p>
        </div>
      )
  }

  if (!stats || isRestarting) return (
    <div className="fixed inset-0 z-50 flex flex-col items-center justify-center bg-background/80 backdrop-blur-sm gap-4">
        <Loader2 className="h-12 w-12 text-primary animate-spin" />
        <div className="text-xl font-semibold tracking-tight">
            {isRestarting ? "Restarting SeedStream..." : "Initializing SeedStream Dashboard..."}
        </div>
        {isRestarting && <p className="text-muted-foreground animate-pulse">Redirecting to home shortly...</p>}
    </div>
  )

  return (
    <SidebarProvider>
      <AppSidebar
        activePage={activePage}
        onNavigate={setActivePage}
        version={version}
        currentUser={currentUser}
        forcePasswordResetEnabled={Boolean(config?.env_overrides?.includes('admin_must_change_password'))}
        onLogout={handleLogout}
        theme={theme}
        onThemeChange={setTheme}
      />
      <SidebarInset className="min-h-0 min-w-0 overflow-x-hidden">
        <SiteHeader activePage={activePage} />
        <div className="flex min-w-0 flex-1 min-h-0 flex-col overflow-x-hidden">
          {activePage === 'dashboard' && (
            <DashboardPage
              stats={stats}
              chartData={chartData}
              sendCommand={sendCommand}
              config={config}
            />
          )}
          {activePage === 'statistics' && (
            <StatisticsPage />
          )}
          {activePage === 'cerberus' && (
            <div className="pt-4 md:pt-5 pb-3 px-4 lg:px-5">
              <CerberusPage />
            </div>
          )}
          {activePage === 'install' && (
            <div className="pt-4 md:pt-5 pb-3 px-4 lg:px-5">
              <StreamManagement
                globalConfig={config}
                movieSearchQueries={config?.movie_search_queries || []}
                seriesSearchQueries={config?.series_search_queries || []}
                initialStreamsByName={config?.streams || {}}
              />
            </div>
          )}
          {activePage === 'logs' && (
            <LogsPage logs={logs} />
          )}
          {activePage === 'profile' && (
            <div className="pt-4 md:pt-5 pb-3 px-4 lg:px-5">
              <ProfilePage
                currentUser={currentUser}
                config={config}
                sendCommand={sendCommand}
                ws={ws}
                onUsernameChanged={setCurrentUser}
              />
            </div>
          )}
          {isSettingsPage && (
            <div className="pt-4 md:pt-5 pb-3 px-4 lg:px-5">
              <Settings
                initialConfig={config}
                sendCommand={sendCommand}
                saveStatus={saveStatus}
                clearSaveStatus={clearSaveStatus}
                isSaving={isSaving}
                indexerCaps={indexerCaps}
                stats={stats}
              />
            </div>
          )}
        </div>
      </SidebarInset>
    </SidebarProvider>
  )
}

export default App
