import { useState, useEffect, useCallback, useImperativeHandle, forwardRef, useRef } from "react"
import { Folder, FolderOpen, File, ChevronRight, ChevronDown, FilePlus, FolderPlus, Pencil, Trash2, TerminalSquare, Upload } from "lucide-react"
import { cn } from "@/lib/utils"
import { listDir, writeFile, mkdir, deleteEntry, renameEntry, uploadFile } from "@/hooks/use-files"
import type { FileEntry } from "@/hooks/use-files"

const MAX_UPLOAD_SIZE = 50 << 20 // 50 MB — matches server maxFileWriteSize
const MAX_UPLOAD_LABEL = `${MAX_UPLOAD_SIZE >> 20} MB`

// Platform-critical paths that cannot be deleted or renamed.
// Must match protectedPaths in internal/api/files.go.
const protectedPaths = new Set(["agents", "sessions", "skills", "workspace", "config.yaml"])

// Pure path helpers for drag-and-drop.
const parentOf = (path: string) =>
  path.includes("/") ? path.substring(0, path.lastIndexOf("/")) : ""

const nameOf = (path: string) =>
  path.includes("/") ? path.substring(path.lastIndexOf("/") + 1) : path

/** Returns true if `path` is equal to or nested under `ancestor`. */
const isAncestorOrSelf = (ancestor: string, path: string) =>
  path === ancestor || path.startsWith(ancestor + "/")

/** Resolve the target directory for a drop — directory entries are targets, files resolve to their parent. */
const resolveDropDir = (entry: FileEntry | null): string =>
  entry === null ? "" : entry.type === "dir" ? entry.path : parentOf(entry.path)

/** Strip path separators and reject `.`/`..` to prevent path manipulation from File.name. */
function sanitizeFilename(name: string): string | null {
  const base = name.split("/").pop()?.split("\\").pop() ?? ""
  if (!base || base === "." || base === "..") return null
  return base
}

export interface FileTreeHandle {
  refresh: () => void
  refreshDir: (dirPath: string) => void
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
        onBlur={() => onCancel()}
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

  // Clamp to viewport after mount.
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const rect = el.getBoundingClientRect()
    if (rect.right > window.innerWidth) el.style.left = `${x - rect.width}px`
    if (rect.bottom > window.innerHeight) el.style.top = `${y - rect.height}px`
  }, [x, y])

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
  entry: FileEntry | null // null = background (root)
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

  // Drag-and-drop state
  const [dropTarget, setDropTarget] = useState<string | null>(null) // dir path being hovered, "" = root
  const dragExpandTimer = useRef<ReturnType<typeof setTimeout>>(null)
  const dragCounter = useRef(0) // track nested dragenter/dragleave on root

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
    refreshDir(dirPath: string) {
      // Only refresh if the directory is currently visible (root or expanded).
      if (dirPath !== "" && !expanded.has(dirPath)) return
      refreshDir(dirPath)
    },
  }), [expanded, refreshDir])

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

  // --- Drag-and-drop helpers ---

  const clearDragState = useCallback(() => {
    setDropTarget(null)
    dragCounter.current = 0
    if (dragExpandTimer.current) {
      clearTimeout(dragExpandTimer.current)
      dragExpandTimer.current = null
    }
  }, [])

  const handleDragStart = useCallback((e: React.DragEvent, entry: FileEntry) => {
    if (protectedPaths.has(entry.path)) {
      e.preventDefault()
      return
    }
    e.dataTransfer.setData("application/x-filetree-path", entry.path)
    e.dataTransfer.effectAllowed = "move"
  }, [])

  const handleEntryDragOver = useCallback((e: React.DragEvent, entry: FileEntry) => {
    // Accept internal moves or external file drops
    const hasInternal = e.dataTransfer.types.includes("application/x-filetree-path")
    const hasFiles = e.dataTransfer.types.includes("Files")
    if (!hasInternal && !hasFiles) return

    e.preventDefault()
    e.stopPropagation()
    e.dataTransfer.dropEffect = hasInternal ? "move" : "copy"

    setDropTarget(resolveDropDir(entry))

    // Clear any pending expand timer from a previously hovered directory
    if (dragExpandTimer.current) {
      clearTimeout(dragExpandTimer.current)
      dragExpandTimer.current = null
    }

    // Auto-expand directories after hovering for 500ms
    if (entry.type === "dir" && !expanded.has(entry.path)) {
      dragExpandTimer.current = setTimeout(() => {
        toggleDir(entry.path)
      }, 500)
    }
  }, [expanded, toggleDir])

  const handleEntryDragLeave = useCallback((e: React.DragEvent) => {
    e.stopPropagation()
    if (dragExpandTimer.current) {
      clearTimeout(dragExpandTimer.current)
      dragExpandTimer.current = null
    }
    // Only clear if we're leaving to outside the tree (handled by root dragleave)
  }, [])

  const handleEntryDrop = useCallback(async (e: React.DragEvent, entry: FileEntry | null) => {
    e.preventDefault()
    e.stopPropagation()
    clearDragState()

    const targetDir = resolveDropDir(entry)

    // Internal move takes priority over file drops
    const srcPath = e.dataTransfer.getData("application/x-filetree-path")
    if (srcPath) {
      const srcName = nameOf(srcPath)
      const srcParent = parentOf(srcPath)
      const newPath = targetDir ? `${targetDir}/${srcName}` : srcName

      // No-op: same location
      if (newPath === srcPath) return
      // Prevent moving folder into itself or descendant
      if (isAncestorOrSelf(srcPath, targetDir)) return

      try {
        await renameEntry(srcPath, newPath)
        refreshDir(srcParent)
        if (srcParent !== targetDir) refreshDir(targetDir)
        onRenamed?.(srcPath, newPath)
      } catch (err) {
        setError(err instanceof Error ? err.message : "Failed to move")
      }
      return
    }

    // External file upload (parallel)
    if (e.dataTransfer.files.length > 0) {
      const files = Array.from(e.dataTransfer.files)
      const results = await Promise.allSettled(
        files.map(async (file) => {
          const safeName = sanitizeFilename(file.name)
          if (!safeName) throw new Error(`${file.name}: invalid filename`)
          if (file.size > MAX_UPLOAD_SIZE) throw new Error(`${safeName}: too large (max ${MAX_UPLOAD_LABEL})`)
          const destPath = targetDir ? `${targetDir}/${safeName}` : safeName
          await uploadFile(destPath, file)
        })
      )
      const errors = results
        .filter((r): r is PromiseRejectedResult => r.status === "rejected")
        .map((r) => r.reason instanceof Error ? r.reason.message : "upload failed")
      if (results.some((r) => r.status === "fulfilled")) refreshDir(targetDir)
      if (errors.length > 0) setError(errors.join("; "))
    }
  }, [clearDragState, refreshDir, onRenamed])

  // Root-level drag handlers for external file drops and visual feedback
  const handleRootDragEnter = useCallback((e: React.DragEvent) => {
    const hasInternal = e.dataTransfer.types.includes("application/x-filetree-path")
    const hasFiles = e.dataTransfer.types.includes("Files")
    if (!hasInternal && !hasFiles) return
    e.preventDefault()
    dragCounter.current++
    // Only set root as drop target if not already hovering a specific entry
    setDropTarget((prev) => prev === null ? "" : prev)
  }, [])

  const handleRootDragOver = useCallback((e: React.DragEvent) => {
    const hasInternal = e.dataTransfer.types.includes("application/x-filetree-path")
    const hasFiles = e.dataTransfer.types.includes("Files")
    if (!hasInternal && !hasFiles) return
    e.preventDefault()
    e.dataTransfer.dropEffect = hasInternal ? "move" : "copy"
    // If the event isn't on a tree entry, target is root
    if (!(e.target as HTMLElement).closest("[data-tree-entry]")) {
      setDropTarget("")
    }
  }, [])

  const handleRootDragLeave = useCallback((_e: React.DragEvent) => {
    dragCounter.current = Math.max(0, dragCounter.current - 1)
    if (dragCounter.current === 0) {
      clearDragState()
    }
  }, [clearDragState])

  const handleRootDrop = useCallback((e: React.DragEvent) => {
    // Only handle if not already captured by an entry
    if ((e.target as HTMLElement).closest("[data-tree-entry]")) return
    handleEntryDrop(e, null)
  }, [handleEntryDrop])

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

    // Open Terminal — for directories and background (root).
    if (!entry || entry.type === "dir") {
      items.push({
        label: "Open Terminal",
        icon: <TerminalSquare className="h-4 w-4" />,
        onClick: () => {
          const dir = entry?.type === "dir" ? entry.path : ""
          const params = dir ? `?dir=${encodeURIComponent(dir)}` : ""
          window.open(`/terminal${params}`, "_blank", "width=960,height=600,noopener,noreferrer")
        },
      })
    }

    if (entry && !protectedPaths.has(entry.path)) {
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

      const entryDropDir = isDir ? entry.path : parentOf(entry.path)
      const isDragTarget = dropTarget === entryDropDir && dropTarget !== null

      return (
        <div key={entry.path}>
          <button
            data-tree-entry
            draggable={!protectedPaths.has(entry.path)}
            onClick={() => {
              if (isDir) {
                toggleDir(entry.path)
              } else {
                onSelect(entry.path)
              }
            }}
            onContextMenu={(e) => handleContextMenu(e, entry, parentPath)}
            onDragStart={(e) => handleDragStart(e, entry)}
            onDragEnd={clearDragState}
            onDragOver={(e) => handleEntryDragOver(e, entry)}
            onDragLeave={handleEntryDragLeave}
            onDrop={(e) => handleEntryDrop(e, entry)}
            className={cn(
              "flex w-full items-center gap-1 rounded-md px-2 py-1 text-sm text-left transition-colors cursor-pointer",
              isDragTarget
                ? "bg-accent/70 ring-1 ring-accent-foreground/20"
                : isSelected
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

  const rootEntries = children[""]
  if (!rootEntries) {
    return (
      <div className="p-4 text-sm text-muted-foreground">Loading...</div>
    )
  }

  const isRootDropTarget = dropTarget === ""

  if (rootEntries.length === 0 && !inlineAction) {
    return (
      <div
        className={cn(
          "flex min-h-full flex-col p-4 text-sm italic text-muted-foreground",
          isRootDropTarget && "bg-accent/30 ring-1 ring-inset ring-accent-foreground/20"
        )}
        onContextMenu={(e) => handleContextMenu(e, null, "")}
        onDragEnter={handleRootDragEnter}
        onDragOver={handleRootDragOver}
        onDragLeave={handleRootDragLeave}
        onDragEnd={clearDragState}
        onDrop={(e) => handleEntryDrop(e, null)}
      >
        {isRootDropTarget ? (
          <span className="flex items-center gap-1.5">
            <Upload className="h-4 w-4" /> Drop files to upload
          </span>
        ) : (
          "No files"
        )}
      </div>
    )
  }

  return (
    <>
      {error && (
        <button
          className="flex w-full items-start gap-1.5 px-3 py-2 text-xs text-destructive bg-destructive/10 cursor-pointer hover:bg-destructive/15"
          onClick={() => setError(null)}
        >
          <span className="flex-1 text-left">{error}</span>
          <span className="shrink-0">&times;</span>
        </button>
      )}
      <div
        className={cn(
          "flex min-h-full flex-col py-1",
          isRootDropTarget && "bg-accent/30"
        )}
        onContextMenu={(e) => {
          // Fire on any background area (not already handled by an entry).
          if ((e.target as HTMLElement).closest("[data-tree-entry]")) return
          e.preventDefault()
          handleContextMenu(e, null, "")
        }}
        onDragEnter={handleRootDragEnter}
        onDragOver={handleRootDragOver}
        onDragLeave={handleRootDragLeave}
        onDragEnd={clearDragState}
        onDrop={handleRootDrop}
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
