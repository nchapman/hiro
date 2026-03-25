import { useState, useEffect, useCallback, useRef, useMemo } from "react"
import CodeMirror from "@uiw/react-codemirror"
import { javascript } from "@codemirror/lang-javascript"
import { python } from "@codemirror/lang-python"
import { go } from "@codemirror/lang-go"
import { markdown } from "@codemirror/lang-markdown"
import { json } from "@codemirror/lang-json"
import { html } from "@codemirror/lang-html"
import { css } from "@codemirror/lang-css"
import { yaml } from "@codemirror/lang-yaml"
import { keymap } from "@codemirror/view"
import { Button } from "@/components/ui/button"
import { Tooltip, TooltipTrigger, TooltipContent } from "@/components/ui/tooltip"
import { Save, Share2, Check, Loader2, X } from "lucide-react"
import { useTheme } from "@/hooks/use-theme"
import { readFile, writeFile, shareFile } from "@/hooks/use-files"
import type { Extension } from "@codemirror/state"

// Extensions the browser can render inline (image/video/audio/pdf).
const previewableExtensions: Record<string, "image" | "video" | "audio" | "pdf"> = {
  png: "image", jpg: "image", jpeg: "image", gif: "image",
  bmp: "image", ico: "image", webp: "image", svg: "image",
  mp4: "video", webm: "video", ogg: "video",
  mp3: "audio", wav: "audio", flac: "audio",
  pdf: "pdf",
}

const binaryExtensions = new Set([
  // Images
  "png", "jpg", "jpeg", "gif", "bmp", "ico", "webp", "svg", "tiff", "tif",
  // Audio/Video
  "mp3", "mp4", "wav", "avi", "mov", "mkv", "flac", "ogg", "webm",
  // Archives
  "zip", "tar", "gz", "bz2", "xz", "7z", "rar", "zst",
  // Compiled/Binary
  "exe", "dll", "so", "dylib", "o", "a", "class", "pyc", "pyo",
  "wasm", "bin", "dat",
  // Documents
  "pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx",
  // Databases
  "db", "sqlite", "sqlite3",
  // Fonts
  "ttf", "otf", "woff", "woff2", "eot",
])

function getFileExt(path: string): string {
  return path.split("/").pop()?.split(".").pop()?.toLowerCase() ?? ""
}

function isBinaryPath(path: string): boolean {
  return binaryExtensions.has(getFileExt(path))
}

function getPreviewType(path: string): "image" | "video" | "audio" | "pdf" | null {
  return previewableExtensions[getFileExt(path)] ?? null
}

function isBinaryContent(content: string): boolean {
  const sample = content.slice(0, 8192)
  return sample.includes("\0")
}

function getLanguage(path: string): Extension | null {
  const ext = path.split(".").pop()?.toLowerCase()
  switch (ext) {
    case "js":
    case "mjs":
    case "cjs":
      return javascript()
    case "ts":
    case "mts":
    case "cts":
      return javascript({ typescript: true })
    case "jsx":
      return javascript({ jsx: true })
    case "tsx":
      return javascript({ jsx: true, typescript: true })
    case "py":
      return python()
    case "go":
      return go()
    case "md":
      return markdown()
    case "json":
      return json()
    case "html":
    case "htm":
      return html()
    case "css":
      return css()
    case "yml":
    case "yaml":
      return yaml()
    default:
      return null
  }
}

interface FileEditorProps {
  path: string
  onSaved?: () => void
  onDirtyChange?: (dirty: boolean) => void
}

export default function FileEditor({ path, onSaved, onDirtyChange }: FileEditorProps) {
  const { resolved: theme } = useTheme()
  const [content, setContent] = useState<string>("")
  const [savedContent, setSavedContent] = useState<string>("")
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [binary, setBinary] = useState(false)
  const saveRef = useRef<() => void>(() => {})

  const [shareState, setShareState] = useState<"idle" | "sharing" | "copied" | "error">("idle")

  const dirty = content !== savedContent

  // Notify parent of dirty state changes. Use a ref to avoid re-firing
  // when the callback identity changes (inline arrow in parent).
  const dirtyChangeRef = useRef(onDirtyChange)
  dirtyChangeRef.current = onDirtyChange
  const prevDirtyRef = useRef(dirty)
  useEffect(() => {
    if (prevDirtyRef.current !== dirty) {
      prevDirtyRef.current = dirty
      dirtyChangeRef.current?.(dirty)
    }
  }, [dirty])

  const handleSave = useCallback(async () => {
    if (!dirty) return
    setSaving(true)
    setError(null)
    try {
      await writeFile(path, content)
      setSavedContent(content)
      onSaved?.()
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save")
    } finally {
      setSaving(false)
    }
  }, [path, content, dirty, onSaved])

  const handleShare = useCallback(async () => {
    setShareState("sharing")
    try {
      const token = await shareFile(path)
      const url = `${window.location.origin}/shared/${token}`
      await navigator.clipboard.writeText(url)
      setShareState("copied")
      setTimeout(() => setShareState("idle"), 2000)
    } catch {
      setShareState("error")
      setTimeout(() => setShareState("idle"), 2000)
    }
  }, [path])

  // Keep saveRef current so the keymap closure always calls the latest save.
  saveRef.current = handleSave

  // Load file content when path changes.
  useEffect(() => {
    setBinary(false)
    if (isBinaryPath(path)) {
      setBinary(true)
      setLoading(false)
      return
    }
    setLoading(true)
    setError(null)
    readFile(path)
      .then((text) => {
        if (isBinaryContent(text)) {
          setBinary(true)
          return
        }
        setContent(text)
        setSavedContent(text)
      })
      .catch((err) => setError(err.message))
      .finally(() => setLoading(false))
  }, [path])

  const extensions = useMemo(() => {
    const exts: Extension[] = []
    const lang = getLanguage(path)
    if (lang) exts.push(lang)
    exts.push(
      keymap.of([
        {
          key: "Mod-s",
          run: () => {
            saveRef.current()
            return true
          },
        },
      ])
    )
    return exts
  }, [path])

  const shareButton = (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant="ghost"
            size="icon"
            onClick={handleShare}
            disabled={shareState === "sharing"}
            className="h-7 w-7"
          >
            {shareState === "copied" ? (
              <Check className="h-3.5 w-3.5 text-emerald-500" />
            ) : shareState === "error" ? (
              <X className="h-3.5 w-3.5 text-destructive" />
            ) : shareState === "sharing" ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Share2 className="h-3.5 w-3.5" />
            )}
          </Button>
        }
      />
      <TooltipContent>
        {shareState === "copied" ? "Link copied!" : shareState === "error" ? "Failed to share" : "Share file"}
      </TooltipContent>
    </Tooltip>
  )

  if (loading) {
    return (
      <div className="flex flex-1 items-center justify-center text-muted-foreground">
        Loading...
      </div>
    )
  }

  if (error && !content) {
    return (
      <div className="flex flex-1 items-center justify-center text-destructive">
        {error}
      </div>
    )
  }

  if (binary) {
    const previewType = getPreviewType(path)
    const fileUrl = `/api/files/file?path=${encodeURIComponent(path)}`

    const binaryHeader = (
      <div className="flex items-center gap-2 border-b px-4 py-2 text-sm">
        <span className="truncate font-mono text-muted-foreground">{path}</span>
        <div className="flex-1" />
        {shareButton}
      </div>
    )

    if (previewType === "image") {
      return (
        <div className="flex flex-1 flex-col overflow-hidden">
          {binaryHeader}
          <div className="flex flex-1 items-center justify-center overflow-auto p-4 bg-[repeating-conic-gradient(var(--color-muted)_0%_25%,transparent_0%_50%)_50%/16px_16px]">
            <img src={fileUrl} alt={path} className="max-w-full max-h-full object-contain" />
          </div>
        </div>
      )
    }

    if (previewType === "video") {
      return (
        <div className="flex flex-1 flex-col overflow-hidden">
          {binaryHeader}
          <div className="flex flex-1 items-center justify-center p-4">
            <video src={fileUrl} controls className="max-w-full max-h-full" />
          </div>
        </div>
      )
    }

    if (previewType === "audio") {
      return (
        <div className="flex flex-1 flex-col overflow-hidden">
          {binaryHeader}
          <div className="flex flex-1 items-center justify-center p-4">
            <audio src={fileUrl} controls />
          </div>
        </div>
      )
    }

    if (previewType === "pdf") {
      return (
        <div className="flex flex-1 flex-col overflow-hidden">
          {binaryHeader}
          <iframe src={fileUrl} className="flex-1 w-full border-0" title={path} />
        </div>
      )
    }

    return (
      <div className="flex flex-1 flex-col overflow-hidden">
        {binaryHeader}
        <div className="flex flex-1 flex-col items-center justify-center gap-2 text-muted-foreground">
          <span className="text-4xl">Binary file</span>
          <span className="text-sm">{path}</span>
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-1 flex-col overflow-hidden">
      {/* Header bar */}
      <div className="flex items-center gap-2 border-b px-4 py-2 text-sm">
        <span className="truncate font-mono text-muted-foreground">
          {path}
        </span>
        {dirty && (
          <span className="shrink-0 text-xs text-amber-500">Unsaved</span>
        )}
        <div className="flex-1" />
        {error && (
          <span className="shrink-0 text-xs text-destructive">{error}</span>
        )}
        {shareButton}
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant="ghost"
                size="icon"
                onClick={handleSave}
                disabled={!dirty || saving}
                className="h-7 w-7"
              >
                {saving ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <Save className="h-3.5 w-3.5" />
                )}
              </Button>
            }
          />
          <TooltipContent>Save</TooltipContent>
        </Tooltip>
      </div>

      {/* Editor */}
      <div className="flex-1 overflow-hidden">
        <CodeMirror
          value={content}
          onChange={setContent}
          extensions={extensions}
          theme={theme === "dark" ? "dark" : "light"}
          height="100%"
          className="h-full"
          basicSetup={{
            lineNumbers: true,
            foldGutter: true,
            highlightActiveLine: true,
            bracketMatching: true,
            closeBrackets: true,
            autocompletion: true,
            indentOnInput: true,
          }}
        />
      </div>
    </div>
  )
}
