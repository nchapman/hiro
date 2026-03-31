import { useState, useRef, useEffect, useLayoutEffect, useCallback, memo } from "react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from "@/components/ui/collapsible"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  IconArrowUp,
  IconTool,
  IconChevronRight,
  IconDots,
  IconSquare,
  IconPlayerPlay,
  IconTrash,
  IconPaperclip,
  IconX,
  IconFileText,
} from "@tabler/icons-react"
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { Skeleton } from "@/components/ui/skeleton"
import { useWebSocket } from "@/hooks/use-websocket"
import type { ChatWireMessage, ChatAttachment, UsageInfo } from "@/hooks/use-websocket"
import type { SessionInfo } from "@/App"
import {
  ChatContainerRoot,
  ChatContainerContent,
  type ChatContainerHandle,
} from "@/components/prompt-kit/chat-container"
import { ScrollButton } from "@/components/prompt-kit/scroll-button"
import { Markdown } from "@/components/prompt-kit/markdown"
import { Loader } from "@/components/prompt-kit/loader"
import {
  PromptInput,
  PromptInputTextarea,
} from "@/components/prompt-kit/prompt-input"
import type { ModelInfo, ToolCall, Message, MessageAttachment, PendingAttachment, HistoryMessage } from "@/lib/chat-types"
import { mergeHistoryMessages } from "@/lib/chat-parser"
import { statusDotColor } from "@/lib/session-utils"
import ModelSelector from "@/pages/chat/ModelSelector"
import TokenCounter from "@/pages/chat/TokenCounter"

const MAX_ATTACHMENT_SIZE = 5 * 1024 * 1024 // 5 MB
const MAX_ATTACHMENTS = 10

function isImageType(type: string): boolean {
  return ["image/jpeg", "image/png", "image/gif", "image/webp"].includes(type)
}

function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const result = reader.result as string
      const base64 = result.split(",")[1] || ""
      resolve(base64)
    }
    reader.onerror = reject
    reader.readAsDataURL(file)
  })
}

async function processFiles(files: FileList | File[]): Promise<PendingAttachment[]> {
  const result: PendingAttachment[] = []
  for (const file of Array.from(files)) {
    if (file.size > MAX_ATTACHMENT_SIZE) continue
    const dataBase64 = await fileToBase64(file)
    const att: PendingAttachment = {
      id: crypto.randomUUID(),
      file,
      dataBase64,
      mediaType: file.type || "application/octet-stream",
    }
    if (isImageType(file.type)) {
      att.preview = `data:${file.type};base64,${dataBase64}`
    }
    result.push(att)
  }
  return result
}

// --- Tool call UI ---

const ToolCallBlock = memo(function ToolCallBlock({ toolCall }: { toolCall: ToolCall }) {
  const hasDetails = toolCall.input || toolCall.output

  return (
    <Collapsible className="rounded-lg border">
      <CollapsibleTrigger
        disabled={!hasDetails}
        className={cn(
          "flex w-full items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-muted-foreground transition-colors",
          hasDetails && "cursor-pointer hover:bg-muted/50"
        )}
      >
        <IconTool className="h-3 w-3 shrink-0" />
        {toolCall.status || toolCall.name}
        <span className="flex-1" />
        {hasDetails && (
          <IconChevronRight className="h-3 w-3 shrink-0 transition-transform [[data-panel-open]_&]:rotate-90" />
        )}
      </CollapsibleTrigger>

      {hasDetails && (
        <CollapsibleContent>
          {toolCall.input && (
            <div className="border-t bg-muted/20 px-3 py-2 text-xs">
              <Markdown className={markdownClassName}>
                {"```json\n" + formatJSON(toolCall.input) + "\n```"}
              </Markdown>
            </div>
          )}
          {toolCall.output && (
            <div className="border-t bg-muted/20 px-3 py-2 text-xs">
              {toolCall.isError && (
                <div className="mb-1 font-medium text-destructive">Error</div>
              )}
              <Markdown className={markdownClassName}>
                {formatAsCodeBlock(toolCall.output)}
              </Markdown>
            </div>
          )}
        </CollapsibleContent>
      )}
    </Collapsible>
  )
})

function formatJSON(input: string): string {
  try {
    return JSON.stringify(JSON.parse(input), null, 2)
  } catch {
    return input
  }
}

function formatAsCodeBlock(output: string): string {
  try {
    const parsed = JSON.parse(output)
    return "```json\n" + JSON.stringify(parsed, null, 2) + "\n```"
  } catch {
    return "```\n" + output + "\n```"
  }
}

// --- Message rendering ---

const markdownClassName = cn(
  "prose prose-sm dark:prose-invert max-w-none",
  "prose-pre:my-2 prose-code:before:content-none prose-code:after:content-none"
)

function ThinkingBlock({ content, isStreaming }: { content: string; isStreaming?: boolean }) {
  return (
    <Collapsible defaultOpen={isStreaming} className="rounded-lg border border-dashed border-muted-foreground/30">
      <CollapsibleTrigger className="flex w-full items-center gap-2 px-3 py-1.5 text-xs text-muted-foreground cursor-pointer hover:bg-accent/50 rounded-lg [[data-panel-open]_&]:rounded-b-none">
        {isStreaming ? (
          <Loader variant="typing" size="sm" />
        ) : (
          <IconChevronRight className="h-3 w-3 transition-transform [[data-panel-open]_&]:rotate-90" />
        )}
        <span>{isStreaming ? "Thinking..." : "Thought process"}</span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="border-t border-dashed border-muted-foreground/30 px-3 py-2 text-xs text-muted-foreground whitespace-pre-wrap max-h-64 overflow-y-auto">
          {content}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}

const AssistantMessage = memo(function AssistantMessage({ message }: { message: Message }) {
  const toolCalls = message.toolCalls ?? []
  const content = message.content

  return (
    <div className="flex flex-col gap-2">
      {message.thinking && (
        <ThinkingBlock content={message.thinking} isStreaming={message.isThinking} />
      )}
      {toolCalls.length > 0 && (
        <div className="flex flex-col gap-1.5">
          {toolCalls.map((tc) => (
            <ToolCallBlock key={tc.id} toolCall={tc} />
          ))}
        </div>
      )}
      {content && (
        <Markdown className={markdownClassName}>
          {content}
        </Markdown>
      )}
    </div>
  )
})

// --- User message ---

const UserMessage = memo(function UserMessage({ message }: { message: Message }) {
  return (
    <div className="flex justify-end">
      <div className="flex max-w-[85%] flex-col gap-2">
        {message.attachments && message.attachments.length > 0 && (
          <div className="flex flex-wrap justify-end gap-2">
            {message.attachments.map((att, i) =>
              isImageType(att.media_type) && att.data ? (
                <img
                  key={i}
                  src={`data:${att.media_type};base64,${att.data}`}
                  alt={att.filename}
                  className="max-h-48 max-w-full rounded-lg object-contain"
                />
              ) : (
                <div key={i} className="flex items-center gap-1.5 rounded-lg bg-muted px-3 py-1.5 text-xs text-muted-foreground">
                  <IconFileText className="h-3 w-3" />
                  {att.filename}
                </div>
              )
            )}
          </div>
        )}
        {message.content && (
          <div className="rounded-2xl bg-muted px-4 py-2.5 text-sm">
            {message.content}
          </div>
        )}
      </div>
    </div>
  )
})

// --- Reasoning control ---

function ReasoningControl({
  model,
  effort,
  onChange,
}: {
  model: ModelInfo | undefined
  effort: string
  onChange: (effort: string) => void
}) {
  if (!model?.can_reason) return null

  const levels = model.reasoning_levels

  const options = [{ value: "", label: "Fast" }, ...((levels ?? []).map((l) => ({ value: l, label: `Think: ${l}` })))]

  if (options.length > 2) {
    return (
      <Popover>
        <PopoverTrigger
          render={
            <Button variant="outline" size="sm" className="h-6 gap-1 rounded-full px-2 text-xs text-muted-foreground" />
          }
        >
          {options.find((o) => o.value === effort)?.label ?? "Fast"}
          <IconChevronRight className="h-3 w-3" />
        </PopoverTrigger>
        <PopoverContent align="end" className="w-36 p-1">
          {options.map((o) => (
            <button
              key={o.value}
              onClick={() => onChange(o.value)}
              className={cn(
                "flex w-full rounded-md px-2 py-1.5 text-xs cursor-pointer hover:bg-accent",
                o.value === effort && "bg-accent"
              )}
            >
              {o.label}
            </button>
          ))}
        </PopoverContent>
      </Popover>
    )
  }

  return (
    <Button
      variant={effort ? "outline" : "ghost"}
      size="sm"
      className={cn(
        "h-6 rounded-full px-2 text-xs",
        effort && "border-primary/50 bg-primary/10 text-primary"
      )}
      onClick={(e) => { e.stopPropagation(); onChange(effort ? "" : "on") }}
    >
      {effort ? "Thinking" : "Fast"}
    </Button>
  )
}

// --- Session cache ---

const MAX_SESSION_CACHE = 20

interface SessionCacheEntry {
  messages: Message[]
  usage: UsageInfo | null
  scrollTop: number
}

function setSessionCache(cache: Map<string, SessionCacheEntry>, id: string, entry: SessionCacheEntry) {
  cache.delete(id) // remove so re-insert updates recency order
  cache.set(id, entry)
  if (cache.size > MAX_SESSION_CACHE) {
    // Evict the oldest (least recently used) entry.
    const oldest = cache.keys().next().value
    if (oldest) cache.delete(oldest)
  }
}

// --- Chat component ---

interface ChatProps {
  session: SessionInfo | null
  onSessionsChanged: () => void
}

export default function Chat({ session, onSessionsChanged }: ChatProps) {
  const [messages, setMessages] = useState<Message[]>([])
  const [input, setInput] = useState("")
  const [attachments, setAttachments] = useState<PendingAttachment[]>([])
  const [streaming, setStreaming] = useState(false)
  const [loadingHistory, setLoadingHistory] = useState(false)
  const [usage, setUsage] = useState<UsageInfo | null>(null)
  const [models, setModels] = useState<ModelInfo[]>([])
  const [reasoningEffort, setReasoningEffort] = useState("")
  const fileInputRef = useRef<HTMLInputElement>(null)
  const streamingMsgId = useRef<string | null>(null)
  const sessionGeneration = useRef(0)
  const chatContainerRef = useRef<ChatContainerHandle>(null)
  const prevSessionIdRef = useRef<string | null>(null)
  const sessionCache = useRef(new Map<string, SessionCacheEntry>())
  // Refs that mirror state so the session-change effect can read latest values.
  const messagesRef = useRef<Message[]>(messages)
  messagesRef.current = messages
  const usageRef = useRef<UsageInfo | null>(usage)
  usageRef.current = usage
  const pendingScrollTop = useRef(0)

  // Capture scroll position synchronously before React updates the DOM.
  useLayoutEffect(() => {
    return () => {
      pendingScrollTop.current =
        chatContainerRef.current?.scrollElement?.scrollTop ?? 0
    }
  }, [session?.id])

  const isStopped = session?.status === "stopped"
  const isRoot = session ? !session.mode || session.mode === "coordinator" : false
  const wsSessionId = isStopped ? null : (session?.id ?? null)
  const { send, connected, setOnMessage } = useWebSocket(wsSessionId)

  // Fetch available models, and refresh when the tab regains focus.
  useEffect(() => {
    const fetchModels = () =>
      fetch("/api/models")
        .then((res) => (res.ok ? res.json() : []))
        .then((data: ModelInfo[]) => setModels(data ?? []))
        .catch(() => {})

    fetchModels()

    const onVisible = () => {
      if (document.visibilityState === "visible") fetchModels()
    }
    document.addEventListener("visibilitychange", onVisible)
    return () => document.removeEventListener("visibilitychange", onVisible)
  }, [])

  const currentModel = usage?.model || session?.model || ""
  const currentModelInfo = models.find((m) => m.id === currentModel)

  const handleModelChange = useCallback(
    (modelId: string) => {
      send({ type: "config", model: modelId })
      setUsage((u) => u ? { ...u, model: modelId, context_window: models.find((m) => m.id === modelId)?.context_window ?? u.context_window } : u)
      setReasoningEffort("")
    },
    [send, models]
  )

  const handleReasoningChange = useCallback(
    (effort: string) => {
      send({ type: "config", reasoning_effort: effort })
      setReasoningEffort(effort)
    },
    [send]
  )

  // Load message history when agent changes — with per-session caching.
  useEffect(() => {
    const gen = ++sessionGeneration.current

    setOnMessage(() => {}) // clear stale handler immediately

    // Save outgoing session state to cache (strip any in-flight streaming message).
    const prevId = prevSessionIdRef.current
    if (prevId) {
      const cachedMessages = streamingMsgId.current
        ? messagesRef.current.filter((m) => m.id !== streamingMsgId.current)
        : messagesRef.current
      setSessionCache(sessionCache.current, prevId, {
        messages: cachedMessages,
        usage: usageRef.current,
        scrollTop: pendingScrollTop.current,
      })
    }
    prevSessionIdRef.current = session?.id ?? null

    if (!session) {
      setMessages([])
      return
    }

    // Check cache for instant restore.
    const cached = sessionCache.current.get(session.id)
    if (cached) {
      setMessages(cached.messages)
      setUsage(cached.usage)
      setStreaming(false)
      streamingMsgId.current = null
      setLoadingHistory(false)

      // Restore scroll position after DOM renders (double rAF ensures React flush).
      const savedScrollTop = cached.scrollTop
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          const el = chatContainerRef.current?.scrollElement
          if (el) el.scrollTop = savedScrollTop
        })
      })
    } else {
      setMessages([])
      setStreaming(false)
      setUsage(null)
      streamingMsgId.current = null
      setLoadingHistory(true)
    }

    const ac = new AbortController()

    // Only fetch data if not restored from cache.
    if (!cached) {
      fetch(`/api/instances/${encodeURIComponent(session.id)}/usage`, {
        signal: ac.signal,
      })
        .then((res) => (res.ok ? res.json() : null))
        .then((data: UsageInfo | null) => {
          if (sessionGeneration.current === gen && data) setUsage(data)
        })
        .catch(() => {})
      fetch(`/api/instances/${encodeURIComponent(session.id)}/messages`, {
        signal: ac.signal,
      })
        .then((res) => {
          if (!res.ok) throw new Error(`HTTP ${res.status}`)
          return res.json()
        })
        .then((history: HistoryMessage[]) => {
          if (sessionGeneration.current !== gen) return
          setMessages(mergeHistoryMessages(history))
        })
        .catch((err: Error) => {
          if (err.name === "AbortError") return
          if (sessionGeneration.current !== gen) return
          setMessages([
            {
              id: crypto.randomUUID(),
              role: "assistant",
              content: "Failed to load conversation history.",
            },
          ])
        })
        .finally(() => {
          if (sessionGeneration.current === gen) setLoadingHistory(false)
        })
    }

    setOnMessage((msg: ChatWireMessage) => {
      if (sessionGeneration.current !== gen) return
      switch (msg.type) {
        case "delta": {
          if (!streamingMsgId.current) {
            const id = crypto.randomUUID()
            streamingMsgId.current = id
            setMessages((prev) => [
              ...prev,
              { id, role: "assistant", content: msg.content || "" },
            ])
          } else {
            const id = streamingMsgId.current
            setMessages((prev) => {
              const last = prev[prev.length - 1]
              if (last?.id !== id) return prev
              return [...prev.slice(0, -1), { ...last, content: last.content + (msg.content || "") }]
            })
          }
          break
        }
        case "tool_call": {
          if (!streamingMsgId.current) {
            const id = crypto.randomUUID()
            streamingMsgId.current = id
            setMessages((prev) => [
              ...prev,
              { id, role: "assistant", content: "" },
            ])
          }
          const id = streamingMsgId.current
          const tc: ToolCall = {
            id: msg.tool_call_id || crypto.randomUUID(),
            name: msg.tool_name || "unknown",
            input: msg.input,
            status: msg.status,
          }
          setMessages((prev) => {
            const last = prev[prev.length - 1]
            if (last?.id !== id) return prev
            return [...prev.slice(0, -1), { ...last, toolCalls: [...(last.toolCalls ?? []), tc] }]
          })
          break
        }
        case "tool_result": {
          if (!streamingMsgId.current) break
          const id = streamingMsgId.current
          setMessages((prev) => {
            const last = prev[prev.length - 1]
            if (last?.id !== id) return prev
            const updated = (last.toolCalls ?? []).map((tc) =>
              tc.id === msg.tool_call_id
                ? { ...tc, output: msg.output, isError: msg.is_error }
                : tc
            )
            return [...prev.slice(0, -1), { ...last, toolCalls: updated }]
          })
          break
        }
        case "reasoning_start": {
          if (!streamingMsgId.current) {
            const id = crypto.randomUUID()
            streamingMsgId.current = id
            setMessages((prev) => [
              ...prev,
              { id, role: "assistant", content: "", thinking: "", isThinking: true },
            ])
          } else {
            const id = streamingMsgId.current
            setMessages((prev) => {
              const last = prev[prev.length - 1]
              if (last?.id !== id) return prev
              return [...prev.slice(0, -1), { ...last, isThinking: true }]
            })
          }
          break
        }
        case "reasoning_delta": {
          if (!streamingMsgId.current) break
          const id = streamingMsgId.current
          setMessages((prev) => {
            const last = prev[prev.length - 1]
            if (last?.id !== id) return prev
            return [...prev.slice(0, -1), { ...last, thinking: (last.thinking || "") + (msg.content || "") }]
          })
          break
        }
        case "reasoning_end": {
          if (!streamingMsgId.current) break
          const id = streamingMsgId.current
          setMessages((prev) => {
            const last = prev[prev.length - 1]
            if (last?.id !== id) return prev
            return [...prev.slice(0, -1), { ...last, isThinking: false }]
          })
          break
        }
        case "done":
          if (msg.usage) setUsage(msg.usage)
          streamingMsgId.current = null
          setStreaming(false)
          break
        case "system":
          setMessages((prev) => [
            ...prev,
            {
              id: crypto.randomUUID(),
              role: "system",
              content: msg.content || "",
            },
          ])
          break
        case "error":
          streamingMsgId.current = null
          setStreaming(false)
          setMessages((prev) => [
            ...prev,
            {
              id: crypto.randomUUID(),
              role: "assistant",
              content: `Error: ${msg.content}`,
            },
          ])
          break
      }
    })

    return () => ac.abort()
  }, [session?.id, setOnMessage])

  const addAttachments = useCallback(async (files: FileList | File[]) => {
    const processed = await processFiles(files)
    setAttachments((prev) => {
      const combined = [...prev, ...processed]
      return combined.slice(0, MAX_ATTACHMENTS)
    })
  }, [])

  const removeAttachment = useCallback((id: string) => {
    setAttachments((prev) => prev.filter((a) => a.id !== id))
  }, [])

  const handlePaste = useCallback((e: React.ClipboardEvent) => {
    const files = e.clipboardData?.files
    if (files && files.length > 0) {
      const hasFiles = Array.from(files).some((f) => f.type.startsWith("image/"))
      if (hasFiles) {
        e.preventDefault()
        addAttachments(files)
      }
    }
  }, [addAttachments])

  const handleDrop = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    if (e.dataTransfer.files.length > 0) {
      addAttachments(e.dataTransfer.files)
    }
  }, [addAttachments])

  const handleDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault()
  }, [])

  const handleSend = () => {
    const text = input.trim()
    const hasAttachments = attachments.length > 0
    if ((!text && !hasAttachments) || streaming || !connected || isStopped) return

    const msgAttachments: MessageAttachment[] = attachments.map((a) => ({
      filename: a.file.name,
      media_type: a.mediaType,
      data: a.preview ? a.dataBase64 : undefined,
    }))

    setMessages((prev) => [
      ...prev,
      {
        id: crypto.randomUUID(),
        role: "user",
        content: text,
        attachments: msgAttachments.length > 0 ? msgAttachments : undefined,
      },
    ])

    const wireAttachments: ChatAttachment[] | undefined = hasAttachments
      ? attachments.map((a) => ({
          filename: a.file.name,
          data: a.dataBase64,
          media_type: a.mediaType,
        }))
      : undefined

    setInput("")
    setAttachments([])
    setStreaming(true)
    send({ type: "message", content: text, attachments: wireAttachments })
  }

  const handleStop = async () => {
    if (!session) return
    try {
      const res = await fetch(`/api/instances/${encodeURIComponent(session.id)}/stop`, {
        method: "POST",
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
    } catch { toast.error("Failed to stop session") }
    onSessionsChanged()
  }

  const handleStart = async () => {
    if (!session) return
    try {
      const res = await fetch(`/api/instances/${encodeURIComponent(session.id)}/start`, {
        method: "POST",
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
    } catch { toast.error("Failed to start session") }
    onSessionsChanged()
  }

  const handleDelete = async () => {
    if (!session) return
    try {
      const res = await fetch(`/api/instances/${encodeURIComponent(session.id)}`, {
        method: "DELETE",
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
    } catch {
      toast.error("Failed to delete session")
      return
    }
    sessionCache.current.delete(session.id)
    onSessionsChanged()
  }

  if (!session) {
    return (
      <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
        Select a session from the sidebar to start chatting.
      </div>
    )
  }

  return (
    <div className="flex flex-1 flex-col overflow-hidden">
      {/* Header */}
      <div className="flex h-12 items-center justify-between border-b px-4">
        <div className="flex items-center gap-2">
          <span
            className={cn("h-2 w-2 shrink-0 rounded-full", statusDotColor(session))}
          />
          <span className="font-heading text-sm font-medium">{session.name}</span>
          {!isStopped && !streaming && models.length > 0 && currentModel && (
            <ModelSelector
              models={models}
              currentModel={currentModel}
              onSelect={handleModelChange}
            />
          )}
          {isStopped && (
            <Badge variant="secondary" className="uppercase">
              stopped
            </Badge>
          )}
        </div>
        <div className="flex items-center gap-2">
          {usage && usage.event_count > 0 && <TokenCounter usage={usage} />}
          {!isRoot && (
          <DropdownMenu>
            <DropdownMenuTrigger className="inline-flex h-8 w-8 items-center justify-center rounded-md text-sm cursor-pointer transition-colors hover:bg-accent hover:text-accent-foreground">
              <IconDots className="h-4 w-4" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {!isStopped && (
                <DropdownMenuItem onClick={handleStop}>
                  <IconSquare className="mr-2 h-4 w-4" />
                  Stop
                </DropdownMenuItem>
              )}
              {isStopped && (
                <DropdownMenuItem onClick={handleStart}>
                  <IconPlayerPlay className="mr-2 h-4 w-4" />
                  Start
                </DropdownMenuItem>
              )}
              <DropdownMenuSeparator />
              <DropdownMenuItem
                onClick={handleDelete}
                variant="destructive"
              >
                <IconTrash className="mr-2 h-4 w-4" />
                Delete
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          )}
        </div>
      </div>

      {/* Messages */}
      {loadingHistory ? (
        <div className="mx-auto w-full max-w-3xl flex-1 flex flex-col gap-4 px-4 py-4">
          {/* User message skeleton */}
          <div className="flex justify-end">
            <Skeleton className="h-10 w-48 rounded-2xl" />
          </div>
          {/* Assistant message skeletons */}
          <div className="flex flex-col gap-2">
            <Skeleton className="h-4 w-full rounded" />
            <Skeleton className="h-4 w-4/5 rounded" />
            <Skeleton className="h-4 w-3/5 rounded" />
          </div>
          <div className="flex justify-end">
            <Skeleton className="h-10 w-36 rounded-2xl" />
          </div>
          <div className="flex flex-col gap-2">
            <Skeleton className="h-4 w-full rounded" />
            <Skeleton className="h-4 w-2/3 rounded" />
          </div>
        </div>
      ) : messages.length === 0 ? (
        <div className="flex flex-1 flex-col items-center justify-center gap-2">
          <p className="font-heading text-base font-medium text-foreground">
            {session.name}
          </p>
          {session.description && (
            <p className="text-sm text-muted-foreground">
              {session.description}
            </p>
          )}
          {isStopped && (
            <Button variant="outline" size="sm" onClick={handleStart}>
              <IconPlayerPlay className="mr-2 h-4 w-4" />
              Start session
            </Button>
          )}
        </div>
      ) : (
        <ChatContainerRoot ref={chatContainerRef} className="relative flex-1">
          <ChatContainerContent className="mx-auto w-full max-w-3xl flex flex-col gap-4 px-4 py-4">
            {messages.map((msg) => (
              <div key={msg.id}>
                {msg.role === "user" && <UserMessage message={msg} />}
                {msg.role === "assistant" && <AssistantMessage message={msg} />}
                {msg.role === "system" && (
                  <div className="flex justify-center">
                    <Badge variant="outline">
                      {msg.content}
                    </Badge>
                  </div>
                )}
              </div>
            ))}

            {streaming && !streamingMsgId.current && (
              <div>
                <Loader variant="typing" size="sm" />
              </div>
            )}
          </ChatContainerContent>

          {/* Scroll to bottom button */}
          <div className="pointer-events-none absolute inset-x-0 bottom-0 flex justify-center pb-4">
            <div className="pointer-events-auto">
              <ScrollButton />
            </div>
          </div>
        </ChatContainerRoot>
      )}

      {/* Input area */}
      {isStopped ? (
        <div className="mx-auto w-full max-w-3xl px-4 pb-4 pt-2">
          <div className="flex items-center justify-center gap-3 rounded-lg border border-dashed p-3 text-sm text-muted-foreground">
            Session is stopped.
            <Button variant="outline" size="sm" onClick={handleStart}>
              <IconPlayerPlay className="mr-2 h-4 w-4" />
              Start
            </Button>
          </div>
        </div>
      ) : (
        <div className="mx-auto w-full max-w-3xl px-4 pb-4 pt-2">
          <div
            onDrop={handleDrop}
            onDragOver={handleDragOver}
          >
            <PromptInput
              value={input}
              onValueChange={setInput}
              onSubmit={handleSend}
              isLoading={streaming}
              disabled={!connected}
              className="bg-muted/50"
            >
              {attachments.length > 0 && (
                <div className="flex flex-wrap gap-2 px-3 pt-3">
                  {attachments.map((att) => (
                    <div
                      key={att.id}
                      className="group relative flex items-center gap-1.5 rounded-lg border bg-background px-2 py-1.5 text-xs"
                    >
                      {att.preview ? (
                        <img src={att.preview} alt={att.file.name} className="h-8 w-8 rounded object-cover" />
                      ) : (
                        <IconFileText className="h-4 w-4 text-muted-foreground" />
                      )}
                      <span className="max-w-[120px] truncate">{att.file.name}</span>
                      <button
                        type="button"
                        onClick={() => removeAttachment(att.id)}
                        className="ml-0.5 rounded-full p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground cursor-pointer"
                      >
                        <IconX className="h-3 w-3" />
                      </button>
                    </div>
                  ))}
                </div>
              )}
              <PromptInputTextarea
                placeholder={
                  connected ? `Message ${session.name}...` : "Connecting..."
                }
                autoFocus
                onPaste={handlePaste}
              />
              <div className="flex items-center justify-between px-2">
                <div className="flex items-center gap-2">
                  <input
                    ref={fileInputRef}
                    type="file"
                    multiple
                    accept="image/*,text/*,application/json,application/xml,application/yaml,application/x-yaml,application/pdf"
                    className="hidden"
                    onChange={(e) => {
                      if (e.target.files) addAttachments(e.target.files)
                      e.target.value = ""
                    }}
                  />
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-8 w-8 rounded-full"
                    onClick={() => fileInputRef.current?.click()}
                    disabled={streaming || !connected}
                  >
                    <IconPaperclip className="h-4 w-4" />
                  </Button>
                </div>
                <div className="flex items-center gap-2">
                  {currentModelInfo?.can_reason && (
                    <ReasoningControl
                      model={currentModelInfo}
                      effort={reasoningEffort}
                      onChange={handleReasoningChange}
                    />
                  )}
                  <Button
                    size="icon"
                    className="h-8 w-8 rounded-full"
                    onClick={handleSend}
                    disabled={streaming || !connected || (!input.trim() && attachments.length === 0)}
                  >
                    <IconArrowUp className="h-4 w-4" />
                  </Button>
                </div>
              </div>
            </PromptInput>
          </div>

          {!connected && (
            <p className="mt-2 text-center text-xs text-muted-foreground">
              Connecting to {session.name}...
            </p>
          )}
        </div>
      )}
    </div>
  )
}
