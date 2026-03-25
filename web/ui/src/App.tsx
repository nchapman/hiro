import { useState, useEffect, useCallback, useRef, lazy, Suspense } from "react"
import { TooltipProvider } from "@/components/ui/tooltip"
import { ThemeCtx, useThemeProvider } from "@/hooks/use-theme"
import ActivityBar from "@/components/ActivityBar"
import type { Activity } from "@/components/ActivityBar"
import Sidebar from "@/components/Sidebar"
import Chat from "@/components/Chat"
import Login from "@/components/Login"
import Setup from "@/components/Setup"
import SettingsPage from "@/components/Settings"

const TerminalPage = lazy(() => import("@/pages/TerminalPage"))
const WorkspacePage = lazy(() => import("@/pages/WorkspacePage"))

export interface SessionInfo {
  id: string
  name: string
  mode: string
  status: "running" | "stopped"
  description?: string
}

type AppState =
  | { kind: "loading" }
  | { kind: "setup" }
  | { kind: "login" }
  | { kind: "ready" }

export default function App() {
  const themeCtx = useThemeProvider()
  const [appState, setAppState] = useState<AppState>({ kind: "loading" })
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const [selectedSessionId, setSelectedSessionId] = useState<string | null>(null)
  const [activity, setActivity] = useState<Activity>("chat")
  const hasAutoSelected = useRef(false)

  const checkAuth = useCallback(async () => {
    try {
      const res = await fetch("/api/auth/status")
      if (!res.ok) {
        setAppState({ kind: "login" })
        return
      }
      const data = await res.json()
      if (data.needsSetup) {
        setAppState({ kind: "setup" })
      } else if (data.authRequired && !data.authenticated) {
        setAppState({ kind: "login" })
      } else {
        setAppState({ kind: "ready" })
      }
    } catch {
      // API not available yet — show loading
      setAppState({ kind: "loading" })
    }
  }, [])

  useEffect(() => {
    checkAuth()
  }, [checkAuth])

  const fetchSessions = useCallback(async () => {
    try {
      const res = await fetch("/api/sessions")
      if (res.ok) {
        const data: SessionInfo[] = await res.json()
        setSessions(data)
        if (!hasAutoSelected.current && data.length > 0) {
          const persistent = data.find(
            (s) => s.mode === "persistent" && s.status === "running"
          )
          if (persistent) {
            setSelectedSessionId(persistent.id)
            hasAutoSelected.current = true
          }
        }
      }
    } catch {
      /* API unavailable */
    }
  }, [])

  useEffect(() => {
    if (appState.kind !== "ready") return
    fetchSessions()
    const interval = setInterval(fetchSessions, 10000)
    return () => clearInterval(interval)
  }, [fetchSessions, appState.kind])

  const handleSelect = useCallback((id: string) => {
    hasAutoSelected.current = true
    setSelectedSessionId(id)
    setActivity("chat")
  }, [])

  const handleLogout = useCallback(async () => {
    try {
      await fetch("/api/auth/logout", { method: "POST" })
    } catch {
      /* best-effort */
    }
    setAppState({ kind: "login" })
    setSessions([])
    setSelectedSessionId(null)
    hasAutoSelected.current = false
  }, [])

  const selectedSession = sessions.find((s) => s.id === selectedSessionId) ?? null

  // Clear selection if the selected session was deleted.
  useEffect(() => {
    if (selectedSessionId && !sessions.find((s) => s.id === selectedSessionId)) {
      setSelectedSessionId(null)
    }
  }, [sessions, selectedSessionId])

  // Standalone terminal page — rendered in its own browser tab.
  if (window.location.pathname === "/terminal") {
    return (
      <Suspense>
        <TerminalPage />
      </Suspense>
    )
  }

  return (
    <ThemeCtx.Provider value={themeCtx}>
      <TooltipProvider>
        {appState.kind === "loading" && (
          <div className="flex h-screen items-center justify-center bg-background text-muted-foreground">
            Loading...
          </div>
        )}

        {appState.kind === "setup" && (
          <Setup onComplete={() => setAppState({ kind: "ready" })} />
        )}

        {appState.kind === "login" && (
          <Login onSuccess={() => setAppState({ kind: "ready" })} />
        )}

        {appState.kind === "ready" && (
          <div className="flex h-screen overflow-hidden bg-background text-foreground">
            <ActivityBar
              activity={activity}
              onActivityChange={setActivity}
              onLogout={handleLogout}
            />
            <div className="flex flex-1 overflow-hidden">
              {activity === "chat" && (
                <>
                  <Sidebar
                    sessions={sessions}
                    selectedId={selectedSessionId}
                    onSelect={handleSelect}
                  />
                  <main className="flex flex-1 flex-col overflow-hidden">
                    <Chat
                      session={selectedSession}
                      onSessionsChanged={fetchSessions}
                    />
                  </main>
                </>
              )}
              {activity === "workspace" && (
                <Suspense>
                  <WorkspacePage />
                </Suspense>
              )}
              {activity === "settings" && (
                <main className="flex flex-1 flex-col overflow-hidden">
                  <SettingsPage />
                </main>
              )}
            </div>
          </div>
        )}
      </TooltipProvider>
    </ThemeCtx.Provider>
  )
}
