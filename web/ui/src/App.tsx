import { useState, useEffect, useCallback, useRef, lazy, Suspense } from "react"
import { Routes, Route, useNavigate, useLocation, matchPath } from "react-router-dom"
import { TooltipProvider } from "@/components/ui/tooltip"
import { Toaster, toast } from "sonner"
import { ThemeCtx, useThemeProvider } from "@/hooks/use-theme"
import ActivityBar from "@/components/ActivityBar"
import type { Activity } from "@/components/ActivityBar"
import Login from "@/components/Login"
import Setup from "@/components/Setup"
import WorkerStatus from "@/components/WorkerStatus"
import ErrorBoundary from "@/components/ErrorBoundary"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { Skeleton } from "@/components/ui/skeleton"

import Sidebar from "@/pages/chat/Sidebar"
import Chat from "@/pages/chat/ChatPage"

const TerminalPage = lazy(() => import("@/pages/terminal/TerminalPage"))
const FilesPage = lazy(() => import("@/pages/files/FilesPage"))
const LogsPage = lazy(() => import("@/pages/logs/LogsPage"))
const SchedulesPage = lazy(() => import("@/pages/schedules/SchedulesPage"))
const SettingsPage = lazy(() => import("@/pages/settings/SettingsPage"))
const SharedFilePage = lazy(() => import("@/pages/shared/SharedFilePage"))

export interface SessionInfo {
  id: string
  name: string
  mode: string
  parent_id?: string
  status: "running" | "stopped"
  description?: string
  model?: string
}

type AppState =
  | { kind: "loading" }
  | { kind: "error" }
  | { kind: "setup" }
  | { kind: "login" }
  | { kind: "worker" }
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

/** Skeleton fallback for the Schedules section — shows header + card outlines. */
const schedulesSkeleton = (
  <div className="flex h-full flex-1 flex-col overflow-hidden">
    <div className="flex h-12 items-center gap-2 border-b px-4">
      <span className="font-heading text-sm font-medium mr-2">Schedules</span>
      <Skeleton className="h-7 w-28 rounded-md" />
      <Skeleton className="h-7 w-24 rounded-md" />
      <Skeleton className="h-7 w-48 rounded-md" />
    </div>
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 p-6">
      {Array.from({ length: 2 }).map((_, i) => (
        <div key={i} className="flex flex-col gap-3 rounded-xl border p-4">
          <Skeleton className="h-4 w-24 rounded" />
          <div className="flex flex-col gap-2">
            {Array.from({ length: 2 }).map((_, j) => (
              <div key={j} className="flex items-center gap-3 rounded-lg border border-border/50 p-3">
                <Skeleton className="h-2 w-2 rounded-full" />
                <Skeleton className="h-4 w-32 rounded" />
                <Skeleton className="h-4 w-16 rounded" />
                <Skeleton className="h-4 w-24 rounded" />
                <div className="flex-1" />
                <Skeleton className="h-4 w-20 rounded" />
              </div>
            ))}
          </div>
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
const KNOWN_PREFIXES = ["/chat", "/files", "/logs", "/schedules", "/settings", "/terminal", "/shared", "/setup", "/login", "/worker"]

/**
 * Derives the current activity from the URL pathname.
 * /terminal and /shared are handled before the main layout renders;
 * everything else defaults to chat.
 */
function activityFromPath(pathname: string): Activity {
  if (pathname.startsWith("/files")) return "files"
  if (pathname.startsWith("/logs")) return "logs"
  if (pathname.startsWith("/schedules")) return "schedules"
  if (pathname.startsWith("/settings")) return "settings"
  return "chat"
}

/** Parse the chat session ID from the URL, matching /chat/:sessionId and sub-routes. */
function sessionIdFromPath(pathname: string): string | undefined {
  const exact = matchPath("/chat/:sessionId", pathname)
  if (exact) return exact.params.sessionId
  const nested = matchPath("/chat/:sessionId/:sub", pathname)
  return nested?.params.sessionId
}

export default function App() {
  const themeCtx = useThemeProvider()
  const [appState, setAppState] = useState<AppState>({ kind: "loading" })
  const [clusterMode, setClusterMode] = useState("")
  const [pendingNodeCount, setPendingNodeCount] = useState(0)
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
      setClusterMode(data.mode || "")
      if (data.needsSetup) {
        setAppState({ kind: "setup" })
      } else if (data.authRequired && !data.authenticated) {
        setAppState({ kind: "login" })
      } else if (data.mode === "worker") {
        setAppState({ kind: "worker" })
      } else {
        setAppState({ kind: "ready" })
      }
    } catch {
      setAppState({ kind: "error" })
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

  // Poll for pending node count when running as leader.
  // Only show a toast when genuinely new nodes appear (not on reconnects).
  const pendingToastId = useRef<string | number | null>(null)
  const knownPendingIds = useRef<Set<string>>(new Set())
  const lastPendingNodeCount = useRef(0)
  useEffect(() => {
    if (appState.kind !== "ready" || clusterMode !== "leader") return
    const fetchPending = async () => {
      try {
        const res = await fetch("/api/cluster/pending")
        if (res.ok) {
          const data: { node_id: string }[] = await res.json()
          const currentIds = new Set(data.map((n) => n.node_id))
          const count = data.length
          setPendingNodeCount(count)

          const newNodes = data.filter((n) => !knownPendingIds.current.has(n.node_id))
          const countChanged = count !== lastPendingNodeCount.current
          knownPendingIds.current = currentIds
          lastPendingNodeCount.current = count

          if (count > 0 && (newNodes.length > 0 || countChanged)) {
            if (pendingToastId.current !== null) {
              toast.dismiss(pendingToastId.current)
            }
            const label = count === 1
              ? "A worker node is waiting for approval"
              : `${count} worker nodes are waiting for approval`
            pendingToastId.current = toast.info(label, {
              duration: Infinity,
              action: {
                label: "Review",
                onClick: () => {
                  toast.dismiss(pendingToastId.current!)
                  pendingToastId.current = null
                  navigate("/settings")
                },
              },
            })
          } else if (count === 0 && pendingToastId.current !== null) {
            toast.dismiss(pendingToastId.current)
            pendingToastId.current = null
          }
        }
      } catch { /* ignore */ }
    }
    fetchPending()
    const interval = setInterval(fetchPending, 5000)
    return () => {
      clearInterval(interval)
      if (pendingToastId.current !== null) {
        toast.dismiss(pendingToastId.current)
        pendingToastId.current = null
      }
      setPendingNodeCount(0)
      knownPendingIds.current = new Set()
      lastPendingNodeCount.current = 0
    }
  }, [appState.kind, clusterMode, navigate])

  // Poll for pending channel senders across all instances.
  // Only dismiss+recreate the toast when new senders appear or the count changes.
  const pendingChannelToastId = useRef<string | number | null>(null)
  const knownPendingSenders = useRef<Set<string>>(new Set())
  const lastPendingCount = useRef(0)
  useEffect(() => {
    if (appState.kind !== "ready") return
    const fetchPendingChannels = async () => {
      try {
        const res = await fetch("/api/channel-access/pending")
        if (res.ok) {
          const data: { count: number; items: { key: string; instance_id: string }[] } = await res.json()
          const currentIds = new Set(data.items.map((s) => `${s.instance_id}:${s.key}`))
          const newSenders = data.items.filter((s) => !knownPendingSenders.current.has(`${s.instance_id}:${s.key}`))
          const countChanged = data.count !== lastPendingCount.current
          knownPendingSenders.current = currentIds
          lastPendingCount.current = data.count

          if (data.count > 0 && (newSenders.length > 0 || countChanged) ) {
            if (pendingChannelToastId.current !== null) {
              toast.dismiss(pendingChannelToastId.current)
            }
            const label = data.count === 1
              ? "A channel sender is waiting for approval"
              : `${data.count} channel senders are waiting for approval`
            pendingChannelToastId.current = toast.info(label, {
              duration: Infinity,
              action: {
                label: "Review",
                onClick: () => {
                  toast.dismiss(pendingChannelToastId.current!)
                  pendingChannelToastId.current = null
                  if (data.items.length > 0) {
                    navigate(`/chat/${data.items[0].instance_id}/config`)
                  }
                },
              },
            })
          } else if (data.count === 0 && pendingChannelToastId.current !== null) {
            toast.dismiss(pendingChannelToastId.current)
            pendingChannelToastId.current = null
          }
        }
      } catch { /* ignore */ }
    }
    fetchPendingChannels()
    const interval = setInterval(fetchPendingChannels, 5000)
    return () => {
      clearInterval(interval)
      if (pendingChannelToastId.current !== null) {
        toast.dismiss(pendingChannelToastId.current)
        pendingChannelToastId.current = null
      }
      knownPendingSenders.current = new Set()
      lastPendingCount.current = 0
    }
  }, [appState.kind, navigate])

  // Auto-select first persistent running session (once)
  useEffect(() => {
    if (hasAutoSelected.current || sessions.length === 0) return
    const first = sessions.find(
      (s) => s.mode === "persistent" && s.status === "running",
    )
    if (!first) return
    setSelectedSessionId(first.id)
    hasAutoSelected.current = true
    if (location.pathname === "/chat" || location.pathname === "/") {
      navigate(`/chat/${first.id}`, { replace: true })
    }
  }, [sessions, location.pathname, navigate])

  // Navigate to the correct path for the current app state.
  useEffect(() => {
    if (appState.kind === "setup" && location.pathname !== "/setup") {
      navigate("/setup", { replace: true })
    } else if (appState.kind === "login" && location.pathname !== "/login") {
      navigate("/login", { replace: true })
    } else if (appState.kind === "worker" && location.pathname !== "/worker") {
      navigate("/worker", { replace: true })
    }
  }, [appState.kind, location.pathname, navigate])

  // Redirect unknown paths to /chat (only when ready)
  useEffect(() => {
    if (appState.kind !== "ready") return
    const p = location.pathname
    if (p === "/" || !KNOWN_PREFIXES.some((prefix) => p.startsWith(prefix))) {
      navigate("/chat", { replace: true })
    }
  }, [location.pathname, navigate, appState.kind])

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
    setClusterMode("")
    setPendingNodeCount(0)
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

        {appState.kind === "error" && (
          <div className="flex h-screen flex-col items-center justify-center gap-3 bg-background text-foreground">
            <p className="text-sm text-muted-foreground">
              Unable to connect to the server.
            </p>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setAppState({ kind: "loading" })
                checkAuth()
              }}
            >
              Retry
            </Button>
          </div>
        )}

        {appState.kind === "setup" && (
          <Setup onComplete={() => checkAuth()} />
        )}

        {appState.kind === "login" && (
          <Login onSuccess={() => checkAuth()} />
        )}

        {appState.kind === "worker" && (
          <WorkerStatus onDisconnect={() => setAppState({ kind: "setup" })} />
        )}

        {appState.kind === "ready" && (
          <div className="flex h-screen overflow-hidden bg-background text-foreground">
            <ActivityBar
              activity={activity}
              onActivityChange={handleActivityChange}
              onLogout={handleLogout}
              pendingNodeCount={pendingNodeCount}
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
                  <ErrorBoundary section="Chat">
                    <Chat session={selectedSession} onSessionsChanged={fetchSessions} />
                  </ErrorBoundary>
                </main>
              </div>

              {/* Files — mounted on first visit, stays alive */}
              {visited.has("files") && (
                <div className={cn("flex flex-1 overflow-hidden", activity !== "files" && "hidden")}>
                  <ErrorBoundary section="Files">
                    <Suspense fallback={filesSkeleton}>
                      <FilesPage />
                    </Suspense>
                  </ErrorBoundary>
                </div>
              )}

              {/* Logs — mounted on first visit, stays alive */}
              {visited.has("logs") && (
                <div className={cn("flex flex-1 overflow-hidden", activity !== "logs" && "hidden")}>
                  <ErrorBoundary section="Logs">
                    <Suspense fallback={logsSkeleton}>
                      <LogsPage />
                    </Suspense>
                  </ErrorBoundary>
                </div>
              )}

              {/* Schedules — mounted on first visit, stays alive */}
              {visited.has("schedules") && (
                <div className={cn("flex flex-1 overflow-hidden", activity !== "schedules" && "hidden")}>
                  <ErrorBoundary section="Schedules">
                    <Suspense fallback={schedulesSkeleton}>
                      <SchedulesPage />
                    </Suspense>
                  </ErrorBoundary>
                </div>
              )}

              {/* Settings — mounted on first visit, stays alive */}
              {visited.has("settings") && (
                <div className={cn("flex flex-1 overflow-hidden", activity !== "settings" && "hidden")}>
                  <main className="flex flex-1 flex-col overflow-hidden">
                    <ErrorBoundary section="Settings">
                      <Suspense fallback={settingsSkeleton}>
                        <SettingsPage />
                      </Suspense>
                    </ErrorBoundary>
                  </main>
                </div>
              )}
            </div>
          </div>
        )}
      </TooltipProvider>
      <Toaster position="top-right" richColors closeButton theme={themeCtx.resolved?.isDark ? "dark" : "light"} />
    </ThemeCtx.Provider>
  )
}
