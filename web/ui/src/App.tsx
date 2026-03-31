import { useState, useEffect, useCallback, useRef, lazy, Suspense } from "react"
import { Routes, Route, useNavigate, useLocation, matchPath } from "react-router-dom"
import { TooltipProvider } from "@/components/ui/tooltip"
import { Toaster } from "sonner"
import { ThemeCtx, useThemeProvider } from "@/hooks/use-theme"
import ActivityBar from "@/components/ActivityBar"
import type { Activity } from "@/components/ActivityBar"
import Login from "@/components/Login"
import Setup from "@/components/Setup"
import { cn } from "@/lib/utils"
import { Skeleton } from "@/components/ui/skeleton"

import Sidebar from "@/pages/chat/Sidebar"
import Chat from "@/pages/chat/ChatPage"

const TerminalPage = lazy(() => import("@/pages/terminal/TerminalPage"))
const FilesPage = lazy(() => import("@/pages/files/FilesPage"))
const LogsPage = lazy(() => import("@/pages/logs/LogsPage"))
const SettingsPage = lazy(() => import("@/pages/settings/SettingsPage"))
const SharedFilePage = lazy(() => import("@/pages/shared/SharedFilePage"))

export interface SessionInfo {
  id: string
  name: string
  mode: string
  status: "running" | "stopped"
  description?: string
  model?: string
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

/** Skeleton fallback for the Files section — shows sidebar chrome + empty editor area. */
const filesSkeleton = (
  <div className="flex h-full flex-1 overflow-hidden">
    <aside className="flex h-full w-56 min-w-56 flex-col border-r bg-card">
      <div className="flex h-12 items-center border-b px-4">
        <span className="font-heading text-sm font-medium">Files</span>
      </div>
      <div className="flex flex-col gap-1 py-2 px-2">
        {Array.from({ length: 6 }).map((_, i) => (
          <div key={i} className="flex items-center gap-2 px-2 py-1">
            <Skeleton className="h-3.5 w-3.5 shrink-0 rounded" />
            <Skeleton className="h-3.5 shrink-0 rounded" style={{ width: `${50 + ((i * 37) % 60)}px` }} />
          </div>
        ))}
      </div>
    </aside>
    <main className="flex flex-1 items-center justify-center text-muted-foreground">
      Select a file to view
    </main>
  </div>
)

/** Skeleton fallback for the Logs section — shows toolbar chrome + skeleton rows. */
const logsSkeleton = (
  <div className="flex h-full flex-1 flex-col overflow-hidden">
    <div className="flex h-12 items-center gap-2 border-b px-4">
      <Skeleton className="h-7 w-24 rounded-md" />
      <Skeleton className="h-7 w-32 rounded-md" />
      <Skeleton className="h-7 w-48 rounded-md" />
    </div>
    <div className="flex flex-col font-mono text-[13px]">
      {Array.from({ length: 12 }).map((_, i) => (
        <div key={i} className="flex items-center gap-2 border-b border-border/40 px-4 py-1.5">
          <Skeleton className="h-3.5 w-20 rounded" />
          <Skeleton className="h-3.5 w-10 rounded" />
          <Skeleton className="h-3.5 w-16 rounded" />
          <Skeleton className="h-3.5 rounded" style={{ width: `${120 + ((i * 47) % 200)}px` }} />
        </div>
      ))}
    </div>
  </div>
)

/** Skeleton fallback for the Settings section — shows header + card outlines. */
const settingsSkeleton = (
  <div className="flex h-full flex-1 flex-col">
    <div className="flex h-12 shrink-0 items-center border-b px-4">
      <span className="font-heading text-sm font-medium">Settings</span>
    </div>
    <div className="flex-1 overflow-y-auto">
      <div className="mx-auto flex max-w-2xl flex-col gap-6 p-6">
        {Array.from({ length: 3 }).map((_, i) => (
          <div key={i} className="flex flex-col gap-4 rounded-xl border p-6">
            <Skeleton className="h-5 w-32 rounded" />
            <Skeleton className="h-3.5 w-56 rounded" />
            <Skeleton className="h-9 w-full rounded-md" />
          </div>
        ))}
      </div>
    </div>
  </div>
)

/** All known top-level route prefixes — used for unknown-path redirect. */
const KNOWN_PREFIXES = ["/chat", "/files", "/logs", "/settings", "/terminal", "/shared"]

/**
 * Derives the current activity from the URL pathname.
 * /terminal and /shared are handled before the main layout renders;
 * everything else defaults to chat.
 */
function activityFromPath(pathname: string): Activity {
  if (pathname.startsWith("/files")) return "files"
  if (pathname.startsWith("/logs")) return "logs"
  if (pathname.startsWith("/settings")) return "settings"
  return "chat"
}

/** Parse the chat session ID from the URL, if on a /chat/:sessionId route. */
function sessionIdFromPath(pathname: string): string | undefined {
  const match = matchPath("/chat/:sessionId", pathname)
  return match?.params.sessionId
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

  // Track which sections have been visited so we can lazy-mount them.
  // Once a section is visited, it stays mounted (CSS-hidden) to preserve state.
  const [visited, setVisited] = useState<Set<Activity>>(() => new Set([activity]))
  useEffect(() => {
    setVisited((prev) => {
      if (prev.has(activity)) return prev
      const next = new Set(prev)
      next.add(activity)
      return next
    })
  }, [activity])

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
      const res = await fetch("/api/instances")
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
    const first = sessions.find(
      (s) => (s.mode === "persistent" || s.mode === "coordinator") && s.status === "running",
    )
    if (!first) return
    setSelectedSessionId(first.id)
    hasAutoSelected.current = true
    if (location.pathname === "/chat" || location.pathname === "/") {
      navigate(`/chat/${first.id}`, { replace: true })
    }
  }, [sessions, location.pathname, navigate])

  // Redirect unknown paths to /chat
  useEffect(() => {
    const p = location.pathname
    if (p === "/" || !KNOWN_PREFIXES.some((prefix) => p.startsWith(prefix))) {
      navigate("/chat", { replace: true })
    }
  }, [location.pathname, navigate])

  // Sync URL → selected session when on a /chat/:sessionId route
  const urlSessionId = sessionIdFromPath(location.pathname)
  useEffect(() => {
    if (urlSessionId && urlSessionId !== selectedSessionId) {
      setSelectedSessionId(urlSessionId)
    }
  }, [urlSessionId, selectedSessionId])

  // Redirect if URL session ID doesn't exist
  useEffect(() => {
    if (!urlSessionId || sessions.length === 0) return
    const exists = sessions.some((s) => s.id === urlSessionId)
    if (!exists) {
      navigate("/chat", { replace: true })
    }
  }, [urlSessionId, sessions, navigate])

  const handleSelect = useCallback(
    (id: string) => {
      hasAutoSelected.current = true
      setSelectedSessionId(id)
      navigate(`/chat/${id}`)
    },
    [navigate],
  )

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

  // Terminal opens in a popup window — separate page, not part of the shell.
  if (location.pathname === "/terminal") {
    return (
      <ThemeCtx.Provider value={themeCtx}>
        <Suspense fallback={suspenseFallback}>
          <Routes>
            <Route path="/terminal" element={<TerminalPage />} />
          </Routes>
        </Suspense>
      </ThemeCtx.Provider>
    )
  }

  const effectiveId = urlSessionId ?? selectedSessionId
  const selectedSession = sessions.find((s) => s.id === effectiveId) ?? null

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
              {/* Chat — always mounted (default section) */}
              <div className={cn("flex flex-1 overflow-hidden", activity !== "chat" && "hidden")}>
                <Sidebar
                  sessions={sessions}
                  selectedId={effectiveId}
                  onSelect={handleSelect}
                />
                <main className="flex flex-1 flex-col overflow-hidden">
                  <Chat session={selectedSession} onSessionsChanged={fetchSessions} />
                </main>
              </div>

              {/* Files — mounted on first visit, stays alive */}
              {visited.has("files") && (
                <div className={cn("flex flex-1 overflow-hidden", activity !== "files" && "hidden")}>
                  <Suspense fallback={filesSkeleton}>
                    <FilesPage />
                  </Suspense>
                </div>
              )}

              {/* Logs — mounted on first visit, stays alive */}
              {visited.has("logs") && (
                <div className={cn("flex flex-1 overflow-hidden", activity !== "logs" && "hidden")}>
                  <Suspense fallback={logsSkeleton}>
                    <LogsPage />
                  </Suspense>
                </div>
              )}

              {/* Settings — mounted on first visit, stays alive */}
              {visited.has("settings") && (
                <div className={cn("flex flex-1 overflow-hidden", activity !== "settings" && "hidden")}>
                  <main className="flex flex-1 flex-col overflow-hidden">
                    <Suspense fallback={settingsSkeleton}>
                      <SettingsPage />
                    </Suspense>
                  </main>
                </div>
              )}
            </div>
          </div>
        )}
      </TooltipProvider>
      <Toaster position="bottom-right" richColors closeButton theme={themeCtx.resolved} />
    </ThemeCtx.Provider>
  )
}
