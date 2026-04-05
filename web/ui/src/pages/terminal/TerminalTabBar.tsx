import { useEffect, useState } from "react"
import { HugeiconsIcon } from "@hugeicons/react"
import { Cancel01Icon, Add01Icon, ComputerIcon } from "@hugeicons/core-free-icons"
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from "@/components/ui/dropdown-menu"
import { cn } from "@/lib/utils"

export interface TerminalTab {
  id: string
  nodeId: string
  nodeName: string
}

interface TerminalNode {
  id: string
  name: string
  status: string
  is_home: boolean
}

interface TerminalTabBarProps {
  tabs: TerminalTab[]
  activeTabId: string | null
  onSwitch: (tabId: string) => void
  onClose: (tabId: string) => void
  onCreate: (nodeId: string) => void
}

export default function TerminalTabBar({
  tabs,
  activeTabId,
  onSwitch,
  onClose,
  onCreate,
}: TerminalTabBarProps) {
  const [nodes, setNodes] = useState<TerminalNode[]>([])

  // Fetch available nodes for the "+" dropdown.
  useEffect(() => {
    const fetchNodes = async () => {
      try {
        const res = await fetch("/api/terminal/nodes")
        if (res.ok) {
          setNodes(await res.json())
        }
      } catch {
        // Fallback to just local.
        setNodes([{ id: "home", name: "local", status: "online", is_home: true }])
      }
    }
    fetchNodes()
    const interval = setInterval(fetchNodes, 30000)
    return () => clearInterval(interval)
  }, [])

  const hasMultipleNodes = nodes.length > 1

  return (
    <div className="flex h-9 min-h-9 items-center bg-sidebar border-b border-border px-1 gap-0.5 select-none">
      {tabs.map((tab) => (
        <button
          key={tab.id}
          onClick={() => onSwitch(tab.id)}
          className={cn(
            "group flex items-center gap-1.5 px-3 h-7 rounded text-xs font-medium transition-colors max-w-48 min-w-0",
            tab.id === activeTabId
              ? "bg-background text-foreground"
              : "text-muted-foreground hover:text-foreground hover:bg-accent",
          )}
        >
          <HugeiconsIcon icon={ComputerIcon} className="h-3 w-3 shrink-0 opacity-60" />
          <span className="truncate">{tab.nodeName}</span>
          <span
            role="button"
            tabIndex={-1}
            onClick={(e) => {
              e.stopPropagation()
              onClose(tab.id)
            }}
            className={cn(
              "ml-auto shrink-0 rounded p-0.5 transition-colors",
              "opacity-0 group-hover:opacity-100 hover:bg-accent",
            )}
          >
            <HugeiconsIcon icon={Cancel01Icon} className="h-2.5 w-2.5" />
          </span>
        </button>
      ))}

      {/* New terminal button */}
      {hasMultipleNodes ? (
        <DropdownMenu>
          <DropdownMenuTrigger
            className="inline-flex h-7 w-7 items-center justify-center rounded text-muted-foreground hover:text-foreground hover:bg-accent transition-colors cursor-pointer"
          >
            <HugeiconsIcon icon={Add01Icon} className="h-3.5 w-3.5" />
          </DropdownMenuTrigger>
          <DropdownMenuContent side="bottom" align="start">
            {nodes.filter((n) => n.status === "online").map((node) => (
              <DropdownMenuItem key={node.id} onClick={() => onCreate(node.id)}>
                <HugeiconsIcon icon={ComputerIcon} className="h-3.5 w-3.5 mr-2 opacity-60" />
                {node.name}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      ) : (
        <button
          onClick={() => onCreate("home")}
          className="inline-flex h-7 w-7 items-center justify-center rounded text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
        >
          <HugeiconsIcon icon={Add01Icon} className="h-3.5 w-3.5" />
        </button>
      )}
    </div>
  )
}
