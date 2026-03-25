import { useState, useCallback, useRef } from "react"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Separator } from "@/components/ui/separator"
import { RefreshCw } from "lucide-react"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import FileTree from "@/components/FileTree"
import type { FileTreeHandle } from "@/components/FileTree"
import FileEditor from "@/components/FileEditor"

export default function WorkspacePage() {
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const treeRef = useRef<FileTreeHandle>(null)

  const handleSelect = useCallback((path: string) => {
    setSelectedPath(path)
  }, [])

  const handleSaved = useCallback(() => {
    treeRef.current?.refresh()
  }, [])

  return (
    <div className="flex h-full flex-1 overflow-hidden">
      {/* File tree sidebar */}
      <aside className="flex h-full w-60 min-w-60 flex-col border-r bg-card">
        <div className="flex items-center justify-between px-4 py-4">
          <span className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            Workspace
          </span>
          <Tooltip>
            <TooltipTrigger
              onClick={() => treeRef.current?.refresh()}
              className="inline-flex h-6 w-6 items-center justify-center rounded-md cursor-pointer transition-colors text-muted-foreground hover:text-accent-foreground"
            >
              <RefreshCw className="h-3.5 w-3.5" />
            </TooltipTrigger>
            <TooltipContent side="right">Refresh</TooltipContent>
          </Tooltip>
        </div>
        <Separator />
        <ScrollArea className="flex-1">
          <FileTree
            ref={treeRef}
            selectedPath={selectedPath}
            onSelect={handleSelect}
          />
        </ScrollArea>
      </aside>

      {/* Editor area */}
      <main className="flex flex-1 flex-col overflow-hidden">
        {selectedPath ? (
          <FileEditor path={selectedPath} onSaved={handleSaved} />
        ) : (
          <div className="flex flex-1 items-center justify-center text-muted-foreground">
            Select a file to view
          </div>
        )}
      </main>
    </div>
  )
}
