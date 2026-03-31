import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { HugeiconsIcon } from "@hugeicons/react"
import type { IconSvgElement } from "@hugeicons/react"
import {
  Message01Icon,
  FolderOpenIcon,
  NoteIcon,
  Settings01Icon,
  Sun01Icon,
  Moon01Icon,
  ComputerIcon,
  Logout01Icon,
  TerminalIcon,
} from "@hugeicons/core-free-icons"
import { useTheme } from "@/hooks/use-theme"
import { cn } from "@/lib/utils"

export type Activity = "chat" | "files" | "logs" | "settings"

interface ActivityBarProps {
  activity: Activity
  onActivityChange: (activity: Activity) => void
  onLogout: () => void
}

const themeIcons = {
  light: Sun01Icon,
  dark: Moon01Icon,
  system: ComputerIcon,
} as const

const themeOrder = ["system", "light", "dark"] as const

const activities: { id: Activity; icon: IconSvgElement; label: string }[] = [
  { id: "chat", icon: Message01Icon, label: "Chat" },
  { id: "files", icon: FolderOpenIcon, label: "Files" },
  { id: "logs", icon: NoteIcon, label: "Logs" },
  { id: "settings", icon: Settings01Icon, label: "Settings" },
]

export default function ActivityBar({
  activity,
  onActivityChange,
  onLogout,
}: ActivityBarProps) {
  const { theme, setTheme } = useTheme()
  const themeIcon = themeIcons[theme]

  const cycleTheme = () => {
    const idx = themeOrder.indexOf(theme)
    setTheme(themeOrder[(idx + 1) % themeOrder.length])
  }

  return (
    <aside className="flex h-full w-12 min-w-12 flex-col items-center border-r bg-card py-2">
      {/* Activity icons */}
      <div className="flex flex-col items-center gap-1">
        {activities.map(({ id, icon, label }) => (
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
              <HugeiconsIcon icon={icon} className="h-5 w-5" />
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
            <HugeiconsIcon icon={TerminalIcon} className="h-5 w-5" />
          </TooltipTrigger>
          <TooltipContent side="right">Terminal</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger
            onClick={cycleTheme}
            className="inline-flex h-10 w-10 items-center justify-center rounded-md cursor-pointer transition-colors text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
          >
            <HugeiconsIcon icon={themeIcon} className="h-5 w-5" />
          </TooltipTrigger>
          <TooltipContent side="right">Theme: {theme}</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger
            onClick={onLogout}
            className="inline-flex h-10 w-10 items-center justify-center rounded-md cursor-pointer transition-colors text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
          >
            <HugeiconsIcon icon={Logout01Icon} className="h-5 w-5" />
          </TooltipTrigger>
          <TooltipContent side="right">Log out</TooltipContent>
        </Tooltip>
      </div>
    </aside>
  )
}
