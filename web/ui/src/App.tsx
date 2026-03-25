import { useState, useEffect, useCallback, useRef, lazy, Suspense } from "react"
import { Routes, Route, Navigate, useNavigate, useParams, useLocation } from "react-router-dom"
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
const FilesPage = lazy(() => import("@/pages/FilesPage"))
const SharedFilePage = lazy(() => import("@/pages/SharedFilePage"))

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

const suspenseFallback = (
  <div className="flex flex-1 items-center justify-center text-muted-foreground">
    Loading...
  </div>
)

/** Derives the current activity from the URL pathname. */
function activityFromPath(pathname: string): Activity {
  if (pathname.startsWith("/files")) return "files"
  if (pathname.startsWith("/settings")) return "settings"
  return "chat"
}

/** The main chat view, reading sessionId from the URL. */
function ChatRoute({
  sessions,
  selectedSessionId,
  onSelect,
  onSessionsChanged,
}: {
  sessions: SessionInfo[]
  selectedSessionId: string | null
  onSelect: (id: string) => void
  onSessionsChanged: () => void
}) {
  const { sessionId } = useParams()
  const navigate = useNavigate()
  const effectiveId = sessionId ?? selectedSessionId

  // Sync URL param → parent state on mount / param change
  useEffect(() => {
    if (sessionId && sessionId !== selectedSessionId) {
      onSelect(sessionId)
    }
  }, [sessionId, selectedSessionId, onSelect])

  // Redirect to /chat if the URL session ID doesn't exist
  useEffect(() => {
    if (!sessionId || sessions.length === 0) return
    const exists = sessions.some((s) => s.id === sessionId)
    if (!exists) {
      navigate("/chat", { replace: true })
    }
  }, [sessionId, sessions, navigate])

  const handleSelect = useCallback(
    (id: string) => {
      onSelect(id)
      navigate(`/chat/${id}`)
    },
    [onSelect, navigate],
  )

  const selectedSession = sessions.find((s) => s.id === effectiveId) ?? null

  return (
    <>
      <Sidebar
        sessions={sessions}
        selectedId={effectiveId}
        onSelect={handleSelect}
      />
      <main className="flex flex-1 flex-col overflow-hidden">
        <Chat session={selectedSession} onSessionsChanged={onSessionsChanged} />
      </main>
    </>
  )
}

export default function App() {
  const themeCtx = useThemeProvider()
  const [appState, setAppState] = useState<AppState>({ kind: "loading" })
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const [selectedSessionId, setSelectedSessionId] = useState<string | null>(null)
  const hasAutoSelected = useRef(false)
  const navigate = useNavigate()
  const location = useLocation()

  const activity = activityFromPath(location.pathname)

  const handleActivityChange = useCallback(
    (a: Activity) => {
      if (a === "chat") {
        navigate(selectedSessionId ? `/chat/${selectedSessionId}` : "/chat")
      } else {
        navigate(`/${a}`)
      }
    },
    [navigate, selectedSessionId],
  )

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

  // Auto-select first persistent running session (once)
  useEffect(() => {
    if (hasAutoSelected.current || sessions.length === 0) return
    const persistent = sessions.find(
      (s) => s.mode === "persistent" && s.status === "running",
    )
    if (!persistent) return
    setSelectedSessionId(persistent.id)
    hasAutoSelected.current = true
    if (location.pathname === "/chat" || location.pathname === "/") {
      navigate(`/chat/${persistent.id}`, { replace: true })
    }
  }, [sessions, location.pathname, navigate])

  const handleSelect = useCallback((id: string) => {
    hasAutoSelected.current = true
    setSelectedSessionId(id)
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

  // Clear selection if the selected session was deleted.
  useEffect(() => {
    if (selectedSessionId && !sessions.find((s) => s.id === selectedSessionId)) {
      setSelectedSessionId(null)
    }
  }, [sessions, selectedSessionId])

  // Shared file viewer lives outside the auth gate entirely.
  if (location.pathname.startsWith("/shared/")) {
    return (
      <ThemeCtx.Provider value={themeCtx}>
        <Suspense fallback={suspenseFallback}>
          <Routes>
            <Route path="/shared/:token" element={<SharedFilePage />} />
          </Routes>
        </Suspense>
      </ThemeCtx.Provider>
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
              onActivityChange={handleActivityChange}
              onLogout={handleLogout}
            />
            <div className="flex flex-1 overflow-hidden">
              <Routes>
                <Route
                  path="/chat/:sessionId?"
                  element={
                    <ChatRoute
                      sessions={sessions}
                      selectedSessionId={selectedSessionId}
                      onSelect={handleSelect}
                      onSessionsChanged={fetchSessions}
                    />
                  }
                />
                <Route
                  path="/files"
                  element={
                    <Suspense fallback={suspenseFallback}>
                      <FilesPage />
                    </Suspense>
                  }
                />
                <Route
                  path="/settings"
                  element={
                    <main className="flex flex-1 flex-col overflow-hidden">
                      <SettingsPage />
                    </main>
                  }
                />
                <Route
                  path="/terminal"
                  element={
                    <Suspense fallback={suspenseFallback}>
                      <TerminalPage />
                    </Suspense>
                  }
                />
                <Route path="*" element={<Navigate to="/chat" replace />} />
              </Routes>
            </div>
          </div>
        )}
      </TooltipProvider>
    </ThemeCtx.Provider>
  )
}
