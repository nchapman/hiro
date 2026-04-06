import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import { HugeiconsIcon } from "@hugeicons/react"
import type { IconSvgElement } from "@hugeicons/react"
import {
  Message01Icon,
  FolderOpenIcon,
  NoteIcon,
  Calendar03Icon,
  Settings01Icon,
  Logout01Icon,
  TerminalIcon,
  PaintBrush01Icon,
} from "@hugeicons/core-free-icons"
import { useTheme } from "@/hooks/use-theme"
import { cn } from "@/lib/utils"
import { useState } from "react"

export type Activity = "chat" | "files" | "logs" | "schedules" | "settings"

interface ActivityBarProps {
  activity: Activity
  onActivityChange: (activity: Activity) => void
  onLogout: () => void
  pendingNodeCount?: number
}

const activities: { id: Activity; icon: IconSvgElement; label: string }[] = [
  { id: "chat", icon: Message01Icon, label: "Chat" },
  { id: "files", icon: FolderOpenIcon, label: "Files" },
  { id: "logs", icon: NoteIcon, label: "Logs" },
  { id: "schedules", icon: Calendar03Icon, label: "Schedules" },
  { id: "settings", icon: Settings01Icon, label: "Settings" },
]

export default function ActivityBar({
  activity,
  onActivityChange,
  onLogout,
  pendingNodeCount = 0,
}: ActivityBarProps) {
  const { themeId, setThemeId, availableThemes } = useTheme()
  const [themeOpen, setThemeOpen] = useState(false)

  const darkThemes = availableThemes.filter((t) => t.type === "dark")
  const lightThemes = availableThemes.filter((t) => t.type === "light")

  return (
    <aside className="flex h-full w-12 min-w-12 flex-col items-center border-r bg-card py-2">
      {/* Activity icons */}
      <div className="flex flex-col items-center gap-1">
        {activities.map(({ id, icon, label }) => (
          <Tooltip key={id}>
            <TooltipTrigger
              onClick={() => onActivityChange(id)}
              className={cn(
                "relative inline-flex h-10 w-10 items-center justify-center rounded-md cursor-pointer transition-colors",
                activity === id
                  ? "bg-accent text-accent-foreground"
                  : "text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
              )}
            >
              <HugeiconsIcon icon={icon} className="h-5 w-5" />
              {id === "settings" && pendingNodeCount > 0 && (
                <span className="absolute -top-0.5 -right-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-destructive px-1 text-[10px] font-medium text-destructive-foreground">
                  {pendingNodeCount}
                </span>
              )}
            </TooltipTrigger>
            <TooltipContent side="right">
              {id === "settings" && pendingNodeCount > 0
                ? `Settings (${pendingNodeCount} pending)`
                : label}
            </TooltipContent>
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
        <Popover open={themeOpen} onOpenChange={setThemeOpen}>
          <PopoverTrigger
            className="inline-flex h-10 w-10 items-center justify-center rounded-md cursor-pointer transition-colors text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
          >
            <HugeiconsIcon icon={PaintBrush01Icon} className="h-5 w-5" />
          </PopoverTrigger>
          <PopoverContent side="right" align="end" className="w-48 p-1">
            <div className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground px-2 py-1">Dark</div>
            {darkThemes.map((t) => (
              <button
                key={t.id}
                onClick={() => { setThemeId(t.id); setThemeOpen(false) }}
                className={cn(
                  "flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-sm transition-colors",
                  t.id === themeId
                    ? "bg-accent text-accent-foreground"
                    : "hover:bg-accent/50"
                )}
              >
                {t.name}
              </button>
            ))}
            <div className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground px-2 py-1 mt-1">Light</div>
            {lightThemes.map((t) => (
              <button
                key={t.id}
                onClick={() => { setThemeId(t.id); setThemeOpen(false) }}
                className={cn(
                  "flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-sm transition-colors",
                  t.id === themeId
                    ? "bg-accent text-accent-foreground"
                    : "hover:bg-accent/50"
                )}
              >
                {t.name}
              </button>
            ))}
          </PopoverContent>
        </Popover>
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
