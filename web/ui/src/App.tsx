import { useState, useEffect, useCallback, useRef } from "react"
import { TooltipProvider } from "@/components/ui/tooltip"
import { ThemeCtx, useThemeProvider } from "@/hooks/use-theme"
import Sidebar from "@/components/Sidebar"
import Chat from "@/components/Chat"
import Login from "@/components/Login"
import Setup from "@/components/Setup"
import SettingsPage from "@/components/Settings"

export interface AgentInfo {
  id: string
  name: string
  mode: string
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
  const [agents, setAgents] = useState<AgentInfo[]>([])
  const [selectedAgentId, setSelectedAgentId] = useState<string | null>(null)
  const [view, setView] = useState<"chat" | "settings">("chat")
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

  const fetchAgents = useCallback(async () => {
    try {
      const res = await fetch("/api/agents")
      if (res.ok) {
        const data: AgentInfo[] = await res.json()
        setAgents(data)
        if (!hasAutoSelected.current && data.length > 0) {
          const persistent = data.find((a) => a.mode === "persistent")
          if (persistent) {
            setSelectedAgentId(persistent.id)
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
    fetchAgents()
    const interval = setInterval(fetchAgents, 10000)
    return () => clearInterval(interval)
  }, [fetchAgents, appState.kind])

  const handleSelect = useCallback((id: string) => {
    hasAutoSelected.current = true
    setSelectedAgentId(id)
  }, [])

  const handleLogout = useCallback(async () => {
    try {
      await fetch("/api/auth/logout", { method: "POST" })
    } catch {
      /* best-effort */
    }
    setAppState({ kind: "login" })
    setAgents([])
    setSelectedAgentId(null)
    hasAutoSelected.current = false
  }, [])

  const selectedAgent = agents.find((a) => a.id === selectedAgentId) ?? null

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
            <Sidebar
              agents={agents}
              selectedId={selectedAgentId}
              onSelect={handleSelect}
              view={view}
              onViewChange={setView}
              onLogout={handleLogout}
            />
            <main className="flex flex-1 flex-col overflow-hidden">
              {view === "chat" && <Chat agent={selectedAgent} />}
              {view === "settings" && <SettingsPage />}
            </main>
          </div>
        )}
      </TooltipProvider>
    </ThemeCtx.Provider>
  )
}
