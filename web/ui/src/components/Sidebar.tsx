import { Separator } from "@/components/ui/separator"
import { ScrollArea } from "@/components/ui/scroll-area"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { Settings, Sun, Moon, Monitor, LogOut, TerminalSquare } from "lucide-react"
import { useTheme } from "@/hooks/use-theme"
import { cn } from "@/lib/utils"
import type { SessionInfo } from "@/App"

interface SidebarProps {
  sessions: SessionInfo[]
  selectedId: string | null
  onSelect: (id: string) => void
  view: "chat" | "settings"
  onViewChange: (view: "chat" | "settings") => void
  onLogout: () => void
}

const themeIcons = {
  light: Sun,
  dark: Moon,
  system: Monitor,
} as const

const themeOrder = ["system", "light", "dark"] as const

export default function Sidebar({
  sessions,
  selectedId,
  onSelect,
  view,
  onViewChange,
  onLogout,
}: SidebarProps) {
  const { theme, setTheme } = useTheme()
  const ThemeIcon = themeIcons[theme]

  const cycleTheme = () => {
    const idx = themeOrder.indexOf(theme)
    setTheme(themeOrder[(idx + 1) % themeOrder.length])
  }

  return (
    <aside className="flex h-full w-56 min-w-56 flex-col border-r bg-card">
      <div className="px-4 py-4">
        <button
          onClick={() => onViewChange("chat")}
          className="text-xl font-bold tracking-tight text-primary cursor-pointer"
        >
          hive
        </button>
      </div>
      <Separator />
      <div className="px-4 pt-4 pb-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
        Sessions
      </div>
      <ScrollArea className="flex-1 px-2">
        {sessions.length === 0 ? (
          <p className="px-2 py-2 text-sm italic text-muted-foreground">
            No sessions
          </p>
        ) : (
          <div className="flex flex-col gap-0.5">
            {sessions.map((session) => (
              <Tooltip key={session.id}>
                <TooltipTrigger
                  onClick={() => {
                    onSelect(session.id)
                    onViewChange("chat")
                  }}
                  className={cn(
                    "flex w-full items-center gap-2 rounded-md px-3 py-2 text-sm text-left transition-colors cursor-pointer",
                    session.id === selectedId && view === "chat"
                      ? "bg-accent font-semibold text-accent-foreground"
                      : "text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
                  )}
                >
                  <span
                    className={cn(
                      "h-1.5 w-1.5 shrink-0 rounded-full",
                      session.status === "stopped"
                        ? "bg-gray-400"
                        : session.mode === "ephemeral"
                          ? "bg-violet-500"
                          : "bg-green-500"
                    )}
                  />
                  <span className="truncate">{session.name}</span>
                </TooltipTrigger>
                <TooltipContent side="right">
                  {session.description || session.name}
                </TooltipContent>
              </Tooltip>
            ))}
          </div>
        )}
      </ScrollArea>
      <Separator />
      <div className="flex items-center gap-1 p-2">
        <Tooltip>
          <TooltipTrigger
            onClick={() => onViewChange("settings")}
            className={cn(
              "inline-flex h-8 w-8 items-center justify-center rounded-md text-sm cursor-pointer transition-colors",
              view === "settings"
                ? "bg-secondary text-secondary-foreground"
                : "hover:bg-accent hover:text-accent-foreground"
            )}
          >
            <Settings className="h-4 w-4" />
          </TooltipTrigger>
          <TooltipContent side="right">Settings</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger
            onClick={() => window.open("/terminal", "_blank", "width=960,height=600,noopener,noreferrer")}
            className="inline-flex h-8 w-8 items-center justify-center rounded-md text-sm cursor-pointer transition-colors hover:bg-accent hover:text-accent-foreground"
          >
            <TerminalSquare className="h-4 w-4" />
          </TooltipTrigger>
          <TooltipContent side="right">Terminal</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger
            onClick={cycleTheme}
            className="inline-flex h-8 w-8 items-center justify-center rounded-md text-sm cursor-pointer transition-colors hover:bg-accent hover:text-accent-foreground"
          >
            <ThemeIcon className="h-4 w-4" />
          </TooltipTrigger>
          <TooltipContent side="right">
            Theme: {theme}
          </TooltipContent>
        </Tooltip>
        <div className="flex-1" />
        <Tooltip>
          <TooltipTrigger
            onClick={onLogout}
            className="inline-flex h-8 w-8 items-center justify-center rounded-md text-sm cursor-pointer transition-colors hover:bg-accent hover:text-accent-foreground"
          >
            <LogOut className="h-4 w-4" />
          </TooltipTrigger>
          <TooltipContent side="right">Log out</TooltipContent>
        </Tooltip>
      </div>
    </aside>
  )
}
