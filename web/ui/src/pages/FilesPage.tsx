import { useState, useCallback, useRef } from "react"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Separator } from "@/components/ui/separator"
import { X, Circle } from "lucide-react"
import { cn } from "@/lib/utils"
import FileTree from "@/components/FileTree"
import type { FileTreeHandle } from "@/components/FileTree"
import FileEditor from "@/components/FileEditor"
import { useFileWatch } from "@/hooks/use-file-watch"
import type { FileChangeEvent } from "@/hooks/use-file-watch"

interface OpenTab {
  path: string
  dirty: boolean
  /** Bumped to trigger a re-fetch in the editor when the file changes on disk. */
  reloadKey: number
  /** Set when an external change is detected while the editor has unsaved work. */
  externalChange: "modified" | "removed" | null
}

export default function FilesPage() {
  const [tabs, setTabs] = useState<OpenTab[]>([])
  const [activeTab, setActiveTab] = useState<string | null>(null)
  const treeRef = useRef<FileTreeHandle>(null)

  const openFile = useCallback((path: string) => {
    setTabs((prev) => {
      if (prev.some((t) => t.path === path)) return prev
      return [...prev, { path, dirty: false, reloadKey: 0, externalChange: null }]
    })
    setActiveTab(path)
  }, [])

  const closeTab = useCallback((path: string) => {
    // Read dirty state and confirm before entering the state updater
    // to avoid window.confirm inside React's updater (strict mode issue).
    const tab = tabs.find((t) => t.path === path)
    if (tab?.dirty) {
      if (!window.confirm(`"${path.split("/").pop()}" has unsaved changes. Discard?`)) return
    }
    setTabs((prev) => {
      const idx = prev.findIndex((t) => t.path === path)
      const next = prev.filter((t) => t.path !== path)
      setActiveTab((current) => {
        if (current !== path) return current
        const adj = prev[idx + 1] ?? prev[idx - 1]
        return adj?.path ?? null
      })
      return next
    })
  }, [tabs])

  const markDirty = useCallback((path: string, dirty: boolean) => {
    setTabs((prev) =>
      prev.map((t) => (t.path === path ? { ...t, dirty } : t))
    )
  }, [])

  // No explicit tree refresh on save — the SSE watcher detects the
  // file write and triggers a targeted refreshDir automatically.
  const handleSaved = useCallback(() => {}, [])

  // Check if a path (or any child of it) has unsaved edits in open tabs.
  const hasDirtyChildren = useCallback((path: string) => {
    return tabs.some(
      (t) => t.dirty && (t.path === path || t.path.startsWith(path + "/"))
    )
  }, [tabs])

  const handleDeleted = useCallback((path: string) => {
    setTabs((prev) => prev.filter((t) => t.path !== path && !t.path.startsWith(path + "/")))
    setActiveTab((prev) => {
      if (!prev || prev === path || prev.startsWith(path + "/")) return null
      return prev
    })
  }, [])

  const handleRenamed = useCallback((oldPath: string, newPath: string) => {
    setTabs((prev) =>
      prev.map((t) => {
        if (t.path === oldPath) return { ...t, path: newPath }
        if (t.path.startsWith(oldPath + "/"))
          return { ...t, path: newPath + t.path.slice(oldPath.length) }
        return t
      })
    )
    setActiveTab((prev) => {
      if (prev === oldPath) return newPath
      if (prev?.startsWith(oldPath + "/")) return newPath + prev.slice(oldPath.length)
      return prev
    })
  }, [])

  // Handle external file changes detected via SSE.
  const handleExternalChange = useCallback((path: string, op: FileChangeEvent["op"]) => {
    setTabs((prev) =>
      prev.map((t) => {
        if (t.path !== path) return t
        if (op === "remove" || op === "rename") {
          return { ...t, externalChange: "removed" }
        }
        // op === "write" — file modified externally.
        if (t.dirty) {
          // User has unsaved changes — show notification, don't auto-reload.
          return { ...t, externalChange: "modified" }
        }
        // File is clean — auto-reload silently by bumping reloadKey.
        return { ...t, reloadKey: t.reloadKey + 1, externalChange: null }
      })
    )
  }, [])

  // Handle user clicking "Reload" on the conflict notification.
  const handleReloadRequested = useCallback((path: string) => {
    setTabs((prev) =>
      prev.map((t) =>
        t.path === path
          ? { ...t, reloadKey: t.reloadKey + 1, externalChange: null, dirty: false }
          : t
      )
    )
  }, [])

  // Handle user clicking "Keep mine" on the conflict notification.
  const handleDismissChange = useCallback((path: string) => {
    setTabs((prev) =>
      prev.map((t) =>
        t.path === path ? { ...t, externalChange: null } : t
      )
    )
  }, [])

  // Subscribe to file change events from the server.
  useFileWatch((events) => {
    // Refresh affected directories in the tree.
    const dirs = new Set<string>()
    for (const ev of events) {
      const parent = ev.path.includes("/")
        ? ev.path.substring(0, ev.path.lastIndexOf("/"))
        : ""
      dirs.add(parent)
    }
    for (const dir of dirs) {
      treeRef.current?.refreshDir(dir)
    }

    // Notify open editors of external changes.
    for (const ev of events) {
      if (ev.op === "write" || ev.op === "remove" || ev.op === "rename") {
        handleExternalChange(ev.path, ev.op)
      }
    }
  })

  return (
    <div className="flex h-full flex-1 overflow-hidden">
      {/* File tree sidebar */}
      <aside className="flex h-full w-60 min-w-60 flex-col border-r bg-card">
        <div className="flex items-center px-4 py-4">
          <span className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            Files
          </span>
        </div>
        <Separator />
        <ScrollArea className="flex-1">
          <FileTree
            ref={treeRef}
            selectedPath={activeTab}
            onSelect={openFile}
            onDeleted={handleDeleted}
            onRenamed={handleRenamed}
            hasDirtyChildren={hasDirtyChildren}
          />
        </ScrollArea>
      </aside>

      {/* Editor area */}
      <main className="flex flex-1 flex-col overflow-hidden">
        {/* Tab bar */}
        {tabs.length > 0 && (
          <div className="flex border-b bg-card overflow-x-auto shrink-0">
            {tabs.map((tab) => {
              const name = tab.path.split("/").pop() ?? tab.path
              const isActive = tab.path === activeTab
              return (
                <div
                  key={tab.path}
                  className={cn(
                    "group flex items-center border-r text-sm cursor-pointer shrink-0 transition-colors",
                    isActive
                      ? "bg-background text-foreground"
                      : "text-muted-foreground hover:bg-accent/30"
                  )}
                  onClick={() => setActiveTab(tab.path)}
                >
                  {/* Left gap — dirty indicator */}
                  <div className="flex w-7 items-center justify-center shrink-0">
                    {tab.dirty && (
                      <Circle className="h-2.5 w-2.5 fill-amber-500 text-amber-500" />
                    )}
                  </div>
                  <span className="truncate max-w-40 py-1.5">{name}</span>
                  {/* Right gap — close button on hover */}
                  <div className="flex w-7 items-center justify-center shrink-0">
                    <button
                      onClick={(e) => {
                        e.stopPropagation()
                        closeTab(tab.path)
                      }}
                      className="inline-flex h-5 w-5 items-center justify-center rounded-sm opacity-0 group-hover:opacity-100 hover:bg-accent transition-opacity"
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </div>
                </div>
              )
            })}
          </div>
        )}

        {/* Editors — all open tabs stay mounted, only active one is visible */}
        {tabs.map((tab) => (
          <div
            key={tab.path}
            className={cn(
              "flex-1 overflow-hidden",
              tab.path === activeTab ? "flex flex-col" : "hidden"
            )}
          >
            <FileEditor
              path={tab.path}
              reloadKey={tab.reloadKey}
              externalChange={tab.externalChange}
              onSaved={handleSaved}
              onDirtyChange={(dirty) => markDirty(tab.path, dirty)}
              onReloadRequested={() => handleReloadRequested(tab.path)}
              onDismissChange={() => handleDismissChange(tab.path)}
            />
          </div>
        ))}

        {/* Empty state */}
        {tabs.length === 0 && (
          <div className="flex flex-1 items-center justify-center text-muted-foreground">
            Select a file to view
          </div>
        )}
      </main>
    </div>
  )
}
