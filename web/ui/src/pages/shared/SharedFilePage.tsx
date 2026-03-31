import { useState, useEffect, useMemo, useCallback } from "react"
import { useParams } from "react-router-dom"
import { HugeiconsIcon } from "@hugeicons/react"
import { Copy01Icon, Tick02Icon } from "@hugeicons/core-free-icons"
import { Button } from "@/components/ui/button"
import { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider } from "@/components/ui/tooltip"
import { Markdown } from "@/components/prompt-kit/markdown"
import { getFileExt, getPreviewType } from "@/lib/file-utils"

const langMap: Record<string, string> = {
  js: "javascript", mjs: "javascript", cjs: "javascript",
  ts: "typescript", mts: "typescript", cts: "typescript",
  jsx: "jsx", tsx: "tsx",
  py: "python", go: "go", rs: "rust", rb: "ruby",
  sh: "bash", bash: "bash", zsh: "bash",
  md: "markdown", json: "json",
  html: "html", htm: "html", css: "css", scss: "scss",
  yaml: "yaml", yml: "yaml", toml: "toml",
  sql: "sql", graphql: "graphql",
  dockerfile: "dockerfile", makefile: "makefile",
  c: "c", cpp: "cpp", h: "c", hpp: "cpp",
  java: "java", kt: "kotlin", swift: "swift",
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

interface SharedFileData {
  name: string
  size: number
  content?: string
}

export default function SharedFilePage() {
  const { token } = useParams<{ token: string }>()
  const [file, setFile] = useState<SharedFileData | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!token) return
    setLoading(true)
    setError(null)
    fetch(`/api/shared/${token}`)
      .then((res) => {
        if (res.status === 410) throw new Error("This file has been moved or deleted")
        if (res.status === 404) throw new Error("File not found or link expired")
        if (!res.ok) throw new Error("Failed to load file")
        return res.json()
      })
      .then((data) => setFile(data))
      .catch((err) => setError(err.message))
      .finally(() => setLoading(false))
  }, [token])

  // For markdown files, strip YAML frontmatter and render it in a code fence,
  // then render the body as markdown. For other text files, wrap everything
  // in a code fence with the detected language.
  const markdownContent = useMemo(() => {
    if (!file?.content) return ""
    const ext = getFileExt(file.name)
    if (ext === "md") {
      const fm = file.content.match(/^---\r?\n([\s\S]*?)\r?\n---\r?\n([\s\S]*)$/)
      if (fm) {
        return "```yaml\n" + fm[1] + "\n```\n\n" + fm[2]
      }
      return file.content
    }
    const lang = langMap[ext] ?? ""
    return "```" + lang + "\n" + file.content + "\n```"
  }, [file])

  if (loading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background text-muted-foreground">
        <div className="animate-pulse text-sm">Loading...</div>
      </div>
    )
  }

  if (error || !file) {
    return (
      <div className="flex min-h-screen flex-col items-center justify-center gap-3 bg-background text-foreground">
        <div className="text-4xl">404</div>
        <div className="text-muted-foreground">{error ?? "File not found"}</div>
      </div>
    )
  }

  const rawUrl = `/api/shared/${token}/raw`
  const previewType = getPreviewType(file.name)

  // Binary preview (image/video/audio/pdf)
  if (!file.content && previewType) {
    return (
      <div className="flex min-h-screen flex-col bg-background text-foreground">
        <Header name={file.name} size={file.size} />
        {previewType === "image" && (
          <div className="flex flex-1 items-center justify-center p-8 bg-[repeating-conic-gradient(var(--color-muted)_0%_25%,transparent_0%_50%)_50%/16px_16px]">
            <img src={rawUrl} alt={file.name} className="max-w-full max-h-[80vh] rounded-lg shadow-lg object-contain" />
          </div>
        )}
        {previewType === "video" && (
          <div className="flex flex-1 items-center justify-center p-8">
            <video src={rawUrl} controls className="max-w-full max-h-[80vh] rounded-lg shadow-lg" />
          </div>
        )}
        {previewType === "audio" && (
          <div className="flex flex-1 items-center justify-center p-8">
            <audio src={rawUrl} controls />
          </div>
        )}
        {previewType === "pdf" && (
          <iframe src={rawUrl} className="flex-1 w-full border-0" title={file.name} />
        )}
      </div>
    )
  }

  // Binary file without preview
  if (!file.content) {
    return (
      <div className="flex min-h-screen flex-col bg-background text-foreground">
        <Header name={file.name} size={file.size} />
        <div className="flex flex-1 flex-col items-center justify-center gap-3 text-muted-foreground">
          <div className="text-4xl">Binary file</div>
          <a href={rawUrl} download={file.name} className="text-sm text-primary underline underline-offset-4 hover:text-primary/80">
            Download {file.name} ({formatSize(file.size)})
          </a>
        </div>
      </div>
    )
  }

  // Text file — render via Markdown (code files get wrapped in a fence)
  return (
    <div className="flex min-h-screen flex-col bg-background text-foreground">
      <Header name={file.name} size={file.size} content={file.content} />
      <div className="mx-auto w-full max-w-4xl flex-1 px-6 py-8">
        <Markdown className="prose prose-sm dark:prose-invert max-w-none prose-pre:my-2 prose-code:before:content-none prose-code:after:content-none">
          {markdownContent}
        </Markdown>
      </div>
    </div>
  )
}

function Header({ name, size, content }: { name: string; size: number; content?: string }) {
  const [copied, setCopied] = useState(false)

  const handleCopy = useCallback(async () => {
    if (!content) return
    await navigator.clipboard.writeText(content)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }, [content])

  return (
    <TooltipProvider>
      <header className="sticky top-0 z-10 border-b bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/60">
        <div className="mx-auto flex max-w-4xl items-center gap-3 px-6 py-3">
          <span className="text-sm font-medium">{name}</span>
          <span className="text-xs text-muted-foreground">{formatSize(size)}</span>
          <div className="flex-1" />
          {content && (
            <Tooltip>
              <TooltipTrigger
                render={
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={handleCopy}
                    className="h-7 w-7"
                  >
                    {copied ? (
                      <HugeiconsIcon icon={Tick02Icon} className="h-3.5 w-3.5 text-emerald-500" />
                    ) : (
                      <HugeiconsIcon icon={Copy01Icon} className="h-3.5 w-3.5" />
                    )}
                  </Button>
                }
              />
              <TooltipContent>{copied ? "Copied!" : "Copy contents"}</TooltipContent>
            </Tooltip>
          )}
          <span className="text-xs text-muted-foreground">
            Shared via Hive
          </span>
        </div>
      </header>
    </TooltipProvider>
  )
}

