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
import { Save } from "lucide-react"
import { useTheme } from "@/hooks/use-theme"
import { readFile, writeFile } from "@/hooks/use-workspace"
import type { Extension } from "@codemirror/state"

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
}

export default function FileEditor({ path, onSaved }: FileEditorProps) {
  const { resolved: theme } = useTheme()
  const [content, setContent] = useState<string>("")
  const [savedContent, setSavedContent] = useState<string>("")
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const saveRef = useRef<() => void>(() => {})

  const dirty = content !== savedContent

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

  // Keep saveRef current so the keymap closure always calls the latest save.
  saveRef.current = handleSave

  // Load file content when path changes.
  useEffect(() => {
    setLoading(true)
    setError(null)
    readFile(path)
      .then((text) => {
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
        <Button
          variant="ghost"
          size="sm"
          onClick={handleSave}
          disabled={!dirty || saving}
          className="h-7 gap-1.5"
        >
          <Save className="h-3.5 w-3.5" />
          {saving ? "Saving..." : "Save"}
        </Button>
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
