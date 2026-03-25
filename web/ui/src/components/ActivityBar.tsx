import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import {
  MessageSquare,
  FolderOpen,
  Settings,
  Sun,
  Moon,
  Monitor,
  LogOut,
  TerminalSquare,
} from "lucide-react"
import { useTheme } from "@/hooks/use-theme"
import { cn } from "@/lib/utils"

export type Activity = "chat" | "workspace" | "settings"

interface ActivityBarProps {
  activity: Activity
  onActivityChange: (activity: Activity) => void
  onLogout: () => void
}

const themeIcons = {
  light: Sun,
  dark: Moon,
  system: Monitor,
} as const

const themeOrder = ["system", "light", "dark"] as const

const activities: { id: Activity; icon: typeof MessageSquare; label: string }[] = [
  { id: "chat", icon: MessageSquare, label: "Chat" },
  { id: "workspace", icon: FolderOpen, label: "Workspace" },
  { id: "settings", icon: Settings, label: "Settings" },
]

export default function ActivityBar({
  activity,
  onActivityChange,
  onLogout,
}: ActivityBarProps) {
  const { theme, setTheme } = useTheme()
  const ThemeIcon = themeIcons[theme]

  const cycleTheme = () => {
    const idx = themeOrder.indexOf(theme)
    setTheme(themeOrder[(idx + 1) % themeOrder.length])
  }

  return (
    <aside className="flex h-full w-12 min-w-12 flex-col items-center border-r bg-card py-2">
      {/* Activity icons */}
      <div className="flex flex-col items-center gap-1">
        {activities.map(({ id, icon: Icon, label }) => (
          <Tooltip key={id}>
            <TooltipTrigger
              onClick={() => onActivityChange(id)}
              className={cn(
                "inline-flex h-10 w-10 items-center justify-center rounded-md cursor-pointer transition-colors",
                activity === id
                  ? "bg-accent text-accent-foreground"
                  : "text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
              )}
            >
              <Icon className="h-5 w-5" />
            </TooltipTrigger>
            <TooltipContent side="right">{label}</TooltipContent>
          </Tooltip>
        ))}
      </div>

      <div className="flex-1" />

      {/* Bottom actions */}
      <div className="flex flex-col items-center gap-1">
        <Tooltip>
          <TooltipTrigger
            onClick={() =>
              window.open(
                "/terminal",
                "_blank",
                "width=960,height=600,noopener,noreferrer"
              )
            }
            className="inline-flex h-10 w-10 items-center justify-center rounded-md cursor-pointer transition-colors text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
          >
            <TerminalSquare className="h-5 w-5" />
          </TooltipTrigger>
          <TooltipContent side="right">Terminal</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger
            onClick={cycleTheme}
            className="inline-flex h-10 w-10 items-center justify-center rounded-md cursor-pointer transition-colors text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
          >
            <ThemeIcon className="h-5 w-5" />
          </TooltipTrigger>
          <TooltipContent side="right">Theme: {theme}</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger
            onClick={onLogout}
            className="inline-flex h-10 w-10 items-center justify-center rounded-md cursor-pointer transition-colors text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
          >
            <LogOut className="h-5 w-5" />
          </TooltipTrigger>
          <TooltipContent side="right">Log out</TooltipContent>
        </Tooltip>
      </div>
    </aside>
  )
}
