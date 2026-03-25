import { useState, useEffect, useCallback, useImperativeHandle, forwardRef, useRef } from "react"
import { Folder, FolderOpen, File, ChevronRight, ChevronDown, FilePlus, FolderPlus, Pencil, Trash2 } from "lucide-react"
import { cn } from "@/lib/utils"
import { listDir, writeFile, mkdir, deleteEntry, renameEntry } from "@/hooks/use-workspace"
import type { FileEntry } from "@/hooks/use-workspace"

export interface FileTreeHandle {
  refresh: () => void
}

interface FileTreeProps {
  selectedPath: string | null
  onSelect: (path: string) => void
  onDeleted?: (path: string) => void
  onRenamed?: (oldPath: string, newPath: string) => void
  hasDirtyChildren?: (path: string) => boolean
}

// Inline input used for new file/folder creation and renaming.
function InlineInput({
  defaultValue,
  onSubmit,
  onCancel,
  depth,
  icon,
}: {
  defaultValue: string
  onSubmit: (value: string) => void
  onCancel: () => void
  depth: number
  icon: React.ReactNode
}) {
  const inputRef = useRef<HTMLInputElement>(null)
  useEffect(() => {
    inputRef.current?.focus()
    inputRef.current?.select()
  }, [])

  return (
    <div
      className="flex items-center gap-1 px-2 py-1"
      style={{ paddingLeft: `${depth * 16 + 8}px` }}
    >
      <span className="w-3.5 shrink-0" />
      {icon}
      <input
        ref={inputRef}
        defaultValue={defaultValue}
        className="flex-1 min-w-0 bg-transparent text-sm outline-none border-b border-accent"
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            const val = e.currentTarget.value.trim()
            if (val) onSubmit(val)
            else onCancel()
          }
          if (e.key === "Escape") onCancel()
        }}
        onBlur={(e) => {
          const val = e.currentTarget.value.trim()
          if (val && val !== defaultValue) onSubmit(val)
          else onCancel()
        }}
      />
    </div>
  )
}

// Simple context menu rendered at mouse position.
function ContextMenu({
  x,
  y,
  items,
  onClose,
}: {
  x: number
  y: number
  items: { label: string; icon: React.ReactNode; onClick: () => void; variant?: "destructive" }[]
  onClose: () => void
}) {
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose()
    }
    document.addEventListener("mousedown", handler)
    return () => document.removeEventListener("mousedown", handler)
  }, [onClose])

  return (
    <div
      ref={ref}
      className="fixed z-50 min-w-40 rounded-lg bg-popover p-1 text-popover-foreground shadow-md ring-1 ring-foreground/10"
      style={{ left: x, top: y }}
    >
      {items.map((item) => (
        <button
          key={item.label}
          onClick={() => {
            item.onClick()
            onClose()
          }}
          className={cn(
            "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-sm cursor-pointer transition-colors",
            item.variant === "destructive"
              ? "text-destructive hover:bg-destructive/10"
              : "hover:bg-accent hover:text-accent-foreground"
          )}
        >
          {item.icon}
          {item.label}
        </button>
      ))}
    </div>
  )
}

interface ContextMenuState {
  x: number
  y: number
  entry: FileEntry | null // null = background (workspace root)
  parentPath: string
}

type InlineAction =
  | { type: "new-file"; parentPath: string; depth: number }
  | { type: "new-folder"; parentPath: string; depth: number }
  | { type: "rename"; entry: FileEntry; depth: number }

const FileTree = forwardRef<FileTreeHandle, FileTreeProps>(function FileTree(
  { selectedPath, onSelect, onDeleted, onRenamed, hasDirtyChildren },
  ref
) {
  const [children, setChildren] = useState<Record<string, FileEntry[]>>({})
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [error, setError] = useState<string | null>(null)
  const [contextMenu, setContextMenu] = useState<ContextMenuState | null>(null)
  const [inlineAction, setInlineAction] = useState<InlineAction | null>(null)

  const loadRoot = useCallback(() => {
    listDir()
      .then((entries) => setChildren((prev) => ({ ...prev, "": entries })))
      .catch((err) => setError(err.message))
  }, [])

  useEffect(() => {
    loadRoot()
  }, [loadRoot])

  const refreshDir = useCallback((dirPath: string) => {
    listDir(dirPath || undefined)
      .then((entries) => setChildren((prev) => ({ ...prev, [dirPath]: entries })))
      .catch(() => {})
  }, [])

  useImperativeHandle(ref, () => ({
    refresh() {
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

  const handleContextMenu = useCallback(
    (e: React.MouseEvent, entry: FileEntry | null, parentPath: string) => {
      e.preventDefault()
      setContextMenu({ x: e.clientX, y: e.clientY, entry, parentPath })
    },
    []
  )

  const handleNewFile = useCallback(async (parentPath: string, name: string) => {
    const fullPath = parentPath ? `${parentPath}/${name}` : name
    try {
      await writeFile(fullPath, "")
      refreshDir(parentPath)
      onSelect(fullPath)
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create file")
    }
    setInlineAction(null)
  }, [refreshDir, onSelect])

  const handleNewFolder = useCallback(async (parentPath: string, name: string) => {
    const fullPath = parentPath ? `${parentPath}/${name}` : name
    try {
      await mkdir(fullPath)
      refreshDir(parentPath)
      // Auto-expand the new folder.
      setExpanded((prev) => new Set(prev).add(fullPath))
      setChildren((prev) => ({ ...prev, [fullPath]: [] }))
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create folder")
    }
    setInlineAction(null)
  }, [refreshDir])

  const handleDelete = useCallback(async (entry: FileEntry, parentPath: string) => {
    const label = entry.type === "dir" ? "folder" : "file"
    const dirtyWarning =
      entry.type === "dir" && hasDirtyChildren?.(entry.path)
        ? "\n\nThis folder contains files with unsaved changes that will be lost."
        : ""
    if (!window.confirm(`Delete ${label} "${entry.name}"?${dirtyWarning}`)) return
    try {
      await deleteEntry(entry.path)
      refreshDir(parentPath)
      onDeleted?.(entry.path)
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to delete")
    }
  }, [refreshDir, onDeleted, hasDirtyChildren])

  const handleRename = useCallback(async (entry: FileEntry, newName: string) => {
    if (newName === entry.name) {
      setInlineAction(null)
      return
    }
    const parentPath = entry.path.includes("/")
      ? entry.path.substring(0, entry.path.lastIndexOf("/"))
      : ""
    const newPath = parentPath ? `${parentPath}/${newName}` : newName
    try {
      await renameEntry(entry.path, newPath)
      refreshDir(parentPath)
      onRenamed?.(entry.path, newPath)
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to rename")
    }
    setInlineAction(null)
  }, [refreshDir, onRenamed])

  const getContextMenuItems = useCallback((entry: FileEntry | null, parentPath: string): { label: string; icon: React.ReactNode; onClick: () => void; variant?: "destructive" }[] => {
    const targetDir = entry?.type === "dir" ? entry.path : parentPath
    const items: { label: string; icon: React.ReactNode; onClick: () => void; variant?: "destructive" }[] = [
      {
        label: "New File",
        icon: <FilePlus className="h-4 w-4" />,
        onClick: () => {
          if (entry?.type === "dir") {
            setExpanded((prev) => new Set(prev).add(entry.path))
            if (!children[entry.path]) {
              listDir(entry.path)
                .then((entries) => setChildren((c) => ({ ...c, [entry.path]: entries })))
                .catch(() => {})
            }
          }
          const depth = targetDir ? targetDir.split("/").length : 0
          setInlineAction({ type: "new-file", parentPath: targetDir, depth })
        },
      },
      {
        label: "New Folder",
        icon: <FolderPlus className="h-4 w-4" />,
        onClick: () => {
          if (entry?.type === "dir") {
            setExpanded((prev) => new Set(prev).add(entry.path))
            if (!children[entry.path]) {
              listDir(entry.path)
                .then((entries) => setChildren((c) => ({ ...c, [entry.path]: entries })))
                .catch(() => {})
            }
          }
          const depth = targetDir ? targetDir.split("/").length : 0
          setInlineAction({ type: "new-folder", parentPath: targetDir, depth })
        },
      },
    ]

    if (entry) {
      items.push({
        label: "Rename",
        icon: <Pencil className="h-4 w-4" />,
        onClick: () => {
          const depth = entry.path.split("/").length - 1
          setInlineAction({ type: "rename", entry, depth })
        },
      })
      items.push({
        label: "Delete",
        icon: <Trash2 className="h-4 w-4" />,
        variant: "destructive" as const,
        onClick: () => handleDelete(entry, parentPath),
      })
    }

    return items
  }, [children, handleDelete])

  const renderEntries = (parentPath: string, depth: number) => {
    const entries = children[parentPath]
    if (!entries) return null

    return entries.map((entry) => {
      const isDir = entry.type === "dir"
      const isExpanded = expanded.has(entry.path)
      const isSelected = entry.path === selectedPath
      const isRenaming =
        inlineAction?.type === "rename" && inlineAction.entry.path === entry.path

      if (isRenaming) {
        return (
          <InlineInput
            key={entry.path}
            defaultValue={entry.name}
            onSubmit={(name) => handleRename(entry, name)}
            onCancel={() => setInlineAction(null)}
            depth={depth}
            icon={
              isDir ? (
                <Folder className="h-4 w-4 shrink-0 text-amber-500" />
              ) : (
                <File className="h-4 w-4 shrink-0" />
              )
            }
          />
        )
      }

      return (
        <div key={entry.path}>
          <button
            data-tree-entry
            onClick={() => {
              if (isDir) {
                toggleDir(entry.path)
              } else {
                onSelect(entry.path)
              }
            }}
            onContextMenu={(e) => handleContextMenu(e, entry, parentPath)}
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
          {isDir && isExpanded && (
            <>
              {/* Show inline input at top of expanded dir if creating inside it */}
              {inlineAction &&
                inlineAction.type !== "rename" &&
                inlineAction.parentPath === entry.path && (
                  <InlineInput
                    defaultValue=""
                    onSubmit={(name) =>
                      inlineAction.type === "new-file"
                        ? handleNewFile(entry.path, name)
                        : handleNewFolder(entry.path, name)
                    }
                    onCancel={() => setInlineAction(null)}
                    depth={depth + 1}
                    icon={
                      inlineAction.type === "new-folder" ? (
                        <Folder className="h-4 w-4 shrink-0 text-amber-500" />
                      ) : (
                        <File className="h-4 w-4 shrink-0" />
                      )
                    }
                  />
                )}
              {renderEntries(entry.path, depth + 1)}
            </>
          )}
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

  if (rootEntries.length === 0 && !inlineAction) {
    return (
      <div
        className="p-4 text-sm italic text-muted-foreground"
        onContextMenu={(e) => handleContextMenu(e, null, "")}
      >
        Workspace is empty
      </div>
    )
  }

  return (
    <>
      <div
        className="flex min-h-full flex-col py-1"
        onContextMenu={(e) => {
          // Fire on any background area (not already handled by an entry).
          if ((e.target as HTMLElement).closest("[data-tree-entry]")) return
          e.preventDefault()
          handleContextMenu(e, null, "")
        }}
      >
        {/* Inline input at root level */}
        {inlineAction &&
          inlineAction.type !== "rename" &&
          inlineAction.parentPath === "" && (
            <InlineInput
              defaultValue=""
              onSubmit={(name) =>
                inlineAction.type === "new-file"
                  ? handleNewFile("", name)
                  : handleNewFolder("", name)
              }
              onCancel={() => setInlineAction(null)}
              depth={0}
              icon={
                inlineAction.type === "new-folder" ? (
                  <Folder className="h-4 w-4 shrink-0 text-amber-500" />
                ) : (
                  <File className="h-4 w-4 shrink-0" />
                )
              }
            />
          )}
        {renderEntries("", 0)}
      </div>
      {contextMenu && (
        <ContextMenu
          x={contextMenu.x}
          y={contextMenu.y}
          items={getContextMenuItems(contextMenu.entry, contextMenu.parentPath)}
          onClose={() => setContextMenu(null)}
        />
      )}
    </>
  )
})

export default FileTree
