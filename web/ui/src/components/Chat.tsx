import { useState, useRef, useEffect, useCallback } from "react"
import { Button } from "@/components/ui/button"
import {
  ArrowUp,
  Wrench,
  ChevronRight,
  ChevronDown,
  MoreHorizontal,
  Square,
  Play,
  Trash2,
  Paperclip,
  X,
  FileText,
} from "lucide-react"
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu"
import { cn } from "@/lib/utils"
import { useWebSocket } from "@/hooks/use-websocket"
import type { ChatWireMessage, ChatAttachment, UsageInfo } from "@/hooks/use-websocket"
import type { SessionInfo } from "@/App"
import {
  ChatContainerRoot,
  ChatContainerContent,
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
import ModelSelector from "@/components/ModelSelector"
import TokenCounter from "@/components/TokenCounter"

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

function ToolCallBlock({ toolCall }: { toolCall: ToolCall }) {
  const [expanded, setExpanded] = useState(false)
  const hasDetails = toolCall.input || toolCall.output

  return (
    <div className="rounded-lg border">
      <button
        type="button"
        onClick={() => hasDetails && setExpanded(!expanded)}
        className={cn(
          "flex w-full items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-muted-foreground transition-colors",
          hasDetails && "cursor-pointer hover:bg-muted/50"
        )}
      >
        <Wrench className="h-3 w-3 shrink-0" />
        {toolCall.status || toolCall.name}
        <span className="flex-1" />
        {hasDetails && (
          expanded
            ? <ChevronDown className="h-3 w-3 shrink-0" />
            : <ChevronRight className="h-3 w-3 shrink-0" />
        )}
      </button>

      {expanded && (
        <>
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
        </>
      )}
    </div>
  )
}

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
  const [expanded, setExpanded] = useState(false)

  return (
    <div className="rounded-lg border border-dashed border-muted-foreground/30">
      <button
        onClick={() => setExpanded(!expanded)}
        className="flex w-full items-center gap-2 px-3 py-1.5 text-xs text-muted-foreground cursor-pointer hover:bg-accent/50 rounded-lg"
      >
        {isStreaming ? (
          <Loader variant="typing" size="sm" />
        ) : (
          <ChevronRight className={cn("h-3 w-3 transition-transform", expanded && "rotate-90")} />
        )}
        <span>{isStreaming ? "Thinking..." : "Thought process"}</span>
      </button>
      {(expanded || isStreaming) && (
        <div className="border-t border-dashed border-muted-foreground/30 px-3 py-2 text-xs text-muted-foreground whitespace-pre-wrap max-h-64 overflow-y-auto">
          {content}
        </div>
      )}
    </div>
  )
}

function AssistantMessage({ message }: { message: Message }) {
  const toolCalls = message.toolCalls ?? []
  const content = message.content

  return (
    <div className="space-y-2">
      {message.thinking && (
        <ThinkingBlock content={message.thinking} isStreaming={message.isThinking} />
      )}
      {toolCalls.length > 0 && (
        <div className="space-y-1.5">
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
}

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

  if (levels && levels.length > 0) {
    return (
      <select
        value={effort || ""}
        onChange={(e) => onChange(e.target.value)}
        onMouseDown={(e) => e.stopPropagation()}
        onClick={(e) => e.stopPropagation()}
        className="rounded-full border bg-transparent px-2 py-0.5 text-xs text-muted-foreground outline-none cursor-pointer"
      >
        <option value="">Fast</option>
        {levels.map((l) => (
          <option key={l} value={l}>
            Think: {l}
          </option>
        ))}
      </select>
    )
  }

  return (
    <button
      onClick={(e) => { e.stopPropagation(); onChange(effort ? "" : "on") }}
      onMouseDown={(e) => e.stopPropagation()}
      className={cn(
        "rounded-full border px-2 py-0.5 text-xs cursor-pointer",
        effort
          ? "border-green-500/50 bg-green-500/10 text-green-600"
          : "text-muted-foreground hover:bg-accent"
      )}
    >
      {effort ? "Thinking" : "Fast"}
    </button>
  )
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

  // Load message history when agent changes
  useEffect(() => {
    const gen = ++sessionGeneration.current

    if (!session) {
      setMessages([])
      return
    }

    const ac = new AbortController()
    setMessages([])
    setStreaming(false)
    setUsage(null)
    streamingMsgId.current = null
    setLoadingHistory(true)

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
    const res = await fetch(`/api/instances/${encodeURIComponent(session.id)}/stop`, {
      method: "POST",
    })
    if (!res.ok) console.error("Failed to stop session:", res.status, await res.text())
    onSessionsChanged()
  }

  const handleStart = async () => {
    if (!session) return
    const res = await fetch(`/api/instances/${encodeURIComponent(session.id)}/start`, {
      method: "POST",
    })
    if (!res.ok) console.error("Failed to start session:", res.status, await res.text())
    onSessionsChanged()
  }

  const handleDelete = async () => {
    if (!session) return
    const res = await fetch(`/api/instances/${encodeURIComponent(session.id)}`, {
      method: "DELETE",
    })
    if (!res.ok) console.error("Failed to delete session:", res.status, await res.text())
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
      <div className="flex items-center justify-between border-b px-4 py-2">
        <div className="flex items-center gap-2">
          <span
            className={cn("h-2 w-2 shrink-0 rounded-full", statusDotColor(session))}
          />
          <span className="font-medium">{session.name}</span>
          {!isStopped && !streaming && models.length > 0 && currentModel && (
            <ModelSelector
              models={models}
              currentModel={currentModel}
              onSelect={handleModelChange}
            />
          )}
          {isStopped && (
            <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] font-medium uppercase text-muted-foreground">
              stopped
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {usage && usage.event_count > 0 && <TokenCounter usage={usage} />}
          {!isRoot && (
          <DropdownMenu>
            <DropdownMenuTrigger className="inline-flex h-8 w-8 items-center justify-center rounded-md text-sm cursor-pointer transition-colors hover:bg-accent hover:text-accent-foreground">
              <MoreHorizontal className="h-4 w-4" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {!isStopped && (
                <DropdownMenuItem onClick={handleStop}>
                  <Square className="mr-2 h-4 w-4" />
                  Stop
                </DropdownMenuItem>
              )}
              {isStopped && (
                <DropdownMenuItem onClick={handleStart}>
                  <Play className="mr-2 h-4 w-4" />
                  Start
                </DropdownMenuItem>
              )}
              <DropdownMenuSeparator />
              <DropdownMenuItem
                onClick={handleDelete}
                variant="destructive"
              >
                <Trash2 className="mr-2 h-4 w-4" />
                Delete
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          )}
        </div>
      </div>

      {/* Messages */}
      {loadingHistory ? (
        <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
          Loading history...
        </div>
      ) : messages.length === 0 ? (
        <div className="flex flex-1 flex-col items-center justify-center gap-2">
          <p className="text-lg font-medium text-foreground">
            {session.name}
          </p>
          {session.description && (
            <p className="text-sm text-muted-foreground">
              {session.description}
            </p>
          )}
          {isStopped && (
            <Button variant="outline" size="sm" onClick={handleStart}>
              <Play className="mr-2 h-4 w-4" />
              Start session
            </Button>
          )}
        </div>
      ) : (
        <ChatContainerRoot className="relative flex-1">
          <ChatContainerContent className="mx-auto w-full max-w-3xl space-y-4 px-4 py-4">
            {messages.map((msg) => (
              <div key={msg.id}>
                {msg.role === "user" && (
                  <div className="flex justify-end">
                    <div className="max-w-[85%] space-y-2">
                      {msg.attachments && msg.attachments.length > 0 && (
                        <div className="flex flex-wrap justify-end gap-2">
                          {msg.attachments.map((att, i) =>
                            isImageType(att.media_type) && att.data ? (
                              <img
                                key={i}
                                src={`data:${att.media_type};base64,${att.data}`}
                                alt={att.filename}
                                className="max-h-48 max-w-full rounded-lg object-contain"
                              />
                            ) : (
                              <div key={i} className="flex items-center gap-1.5 rounded-lg bg-muted px-3 py-1.5 text-xs text-muted-foreground">
                                <FileText className="h-3 w-3" />
                                {att.filename}
                              </div>
                            )
                          )}
                        </div>
                      )}
                      {msg.content && (
                        <div className="rounded-2xl bg-muted px-4 py-2.5 text-sm">
                          {msg.content}
                        </div>
                      )}
                    </div>
                  </div>
                )}

                {msg.role === "assistant" && (
                  <AssistantMessage message={msg} />
                )}

                {msg.role === "system" && (
                  <div className="flex justify-center">
                    <span className="rounded-full border px-3 py-1 text-xs text-muted-foreground">
                      {msg.content}
                    </span>
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
              <Play className="mr-2 h-4 w-4" />
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
                        <FileText className="h-4 w-4 text-muted-foreground" />
                      )}
                      <span className="max-w-[120px] truncate">{att.file.name}</span>
                      <button
                        type="button"
                        onClick={() => removeAttachment(att.id)}
                        className="ml-0.5 rounded-full p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground cursor-pointer"
                      >
                        <X className="h-3 w-3" />
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
                    <Paperclip className="h-4 w-4" />
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
                    <ArrowUp className="h-4 w-4" />
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
