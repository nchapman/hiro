import { useState, useEffect, useCallback, useImperativeHandle, forwardRef } from "react"
import { Folder, FolderOpen, File, ChevronRight, ChevronDown } from "lucide-react"
import { cn } from "@/lib/utils"
import { listDir } from "@/hooks/use-workspace"
import type { FileEntry } from "@/hooks/use-workspace"

export interface FileTreeHandle {
  refresh: () => void
}

interface FileTreeProps {
  selectedPath: string | null
  onSelect: (path: string) => void
}

const FileTree = forwardRef<FileTreeHandle, FileTreeProps>(function FileTree(
  { selectedPath, onSelect },
  ref
) {
  const [children, setChildren] = useState<Record<string, FileEntry[]>>({})
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [error, setError] = useState<string | null>(null)

  const loadRoot = useCallback(() => {
    listDir()
      .then((entries) => setChildren((prev) => ({ ...prev, "": entries })))
      .catch((err) => setError(err.message))
  }, [])

  // Load root on mount.
  useEffect(() => {
    loadRoot()
  }, [loadRoot])

  useImperativeHandle(ref, () => ({
    refresh() {
      // Reload root + all expanded dirs atomically to avoid flicker.
      const dirs = ["", ...Array.from(expanded)]
      Promise.all(
        dirs.map((dir) =>
          listDir(dir || undefined)
            .then((entries) => [dir, entries] as const)
            .catch(() => [dir, []] as const)
        )
      ).then((results) => {
        setChildren(Object.fromEntries(results))
      })
    },
  }), [expanded])

  const toggleDir = useCallback(
    (path: string) => {
      setExpanded((prev) => {
        const next = new Set(prev)
        if (next.has(path)) {
          next.delete(path)
        } else {
          next.add(path)
          // Fetch children if not cached.
          if (!children[path]) {
            listDir(path)
              .then((entries) =>
                setChildren((c) => ({ ...c, [path]: entries }))
              )
              .catch((err) => setError(err.message))
          }
        }
        return next
      })
    },
    [children]
  )

  const renderEntries = (parentPath: string, depth: number) => {
    const entries = children[parentPath]
    if (!entries) return null

    return entries.map((entry) => {
      const isDir = entry.type === "dir"
      const isExpanded = expanded.has(entry.path)
      const isSelected = entry.path === selectedPath

      return (
        <div key={entry.path}>
          <button
            onClick={() => {
              if (isDir) {
                toggleDir(entry.path)
              } else {
                onSelect(entry.path)
              }
            }}
            className={cn(
              "flex w-full items-center gap-1 rounded-md px-2 py-1 text-sm text-left transition-colors cursor-pointer",
              isSelected
                ? "bg-accent text-accent-foreground"
                : "text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
            )}
            style={{ paddingLeft: `${depth * 16 + 8}px` }}
          >
            {isDir ? (
              <>
                {isExpanded ? (
                  <ChevronDown className="h-3.5 w-3.5 shrink-0" />
                ) : (
                  <ChevronRight className="h-3.5 w-3.5 shrink-0" />
                )}
                {isExpanded ? (
                  <FolderOpen className="h-4 w-4 shrink-0 text-amber-500" />
                ) : (
                  <Folder className="h-4 w-4 shrink-0 text-amber-500" />
                )}
              </>
            ) : (
              <>
                <span className="w-3.5 shrink-0" />
                <File className="h-4 w-4 shrink-0" />
              </>
            )}
            <span className="truncate">{entry.name}</span>
          </button>
          {isDir && isExpanded && renderEntries(entry.path, depth + 1)}
        </div>
      )
    })
  }

  if (error) {
    return (
      <div className="p-4 text-sm text-destructive">
        Error: {error}
      </div>
    )
  }

  const rootEntries = children[""]
  if (!rootEntries) {
    return (
      <div className="p-4 text-sm text-muted-foreground">Loading...</div>
    )
  }

  if (rootEntries.length === 0) {
    return (
      <div className="p-4 text-sm italic text-muted-foreground">
        Workspace is empty
      </div>
    )
  }

  return (
    <div className="flex flex-col py-1">{renderEntries("", 0)}</div>
  )
})

export default FileTree
