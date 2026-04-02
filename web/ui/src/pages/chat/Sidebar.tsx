import { ScrollArea } from "@/components/ui/scroll-area"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import { statusDotColor } from "@/lib/session-utils"
import type { SessionInfo } from "@/App"

interface SidebarProps {
  sessions: SessionInfo[]
  selectedId: string | null
  onSelect: (id: string) => void
}

export default function Sidebar({
  sessions,
  selectedId,
  onSelect,
}: SidebarProps) {
  return (
    <aside className="flex h-full w-56 min-w-56 flex-col border-r bg-card">
      <div className="flex h-12 items-center border-b px-4">
        <span className="font-heading text-sm font-medium">
          Chat
        </span>
      </div>
      <div className="px-4 pt-3 pb-1.5 text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
        Agents
      </div>
      <ScrollArea className="flex-1 px-2">
        {sessions.length === 0 ? (
          <p className="px-2 py-2 text-sm italic text-muted-foreground">
            No agents
          </p>
        ) : (
          <div className="flex flex-col gap-0.5">
            {sessions.map((session) => (
              <Tooltip key={session.id}>
                <TooltipTrigger
                  onClick={() => onSelect(session.id)}
                  className={cn(
                    "flex w-full items-center gap-2 rounded-md px-3 py-2 text-sm text-left transition-colors cursor-pointer",
                    session.id === selectedId
                      ? "bg-accent font-semibold text-accent-foreground"
                      : "text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
                  )}
                >
                  <span
                    className={cn(
                      "h-1.5 w-1.5 shrink-0 rounded-full",
                      statusDotColor(session)
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
    </aside>
  )
}
