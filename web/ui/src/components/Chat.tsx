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

// --- Types ---

interface ModelInfo {
  id: string
  name: string
  can_reason: boolean
  reasoning_levels?: string[]
  context_window: number
}

interface ToolCall {
  id: string
  name: string
  input?: string
  output?: string
  isError?: boolean
  status?: string
}

interface MessageAttachment {
  filename: string
  media_type: string
  data?: string // base64; populated for image previews and when loaded from history
}

interface Message {
  id: string
  role: "user" | "assistant" | "system"
  content: string
  toolCalls?: ToolCall[]
  thinking?: string
  isThinking?: boolean // true while reasoning is streaming
  attachments?: MessageAttachment[]
}

interface PendingAttachment {
  id: string
  file: File
  preview?: string   // data URL for image thumbnails
  dataBase64: string  // base64-encoded content
  mediaType: string
}

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
      // Strip the data URL prefix to get raw base64
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

interface HistoryMessage {
  role: "user" | "assistant" | "tool"
  content: string
  raw_json?: string
  timestamp?: string
}

// --- Fantasy Message JSON parsing (from raw_json in history DB) ---
//
// Fantasy serializes messages as: {"role": "...", "content": [{"type": "...", "data": {...}}]}
// Types use hyphens: "text", "tool-call", "tool-result"
// Tool results are in separate messages with role "tool"

interface FantasyMessage {
  role: string
  content: Array<{ type: string; data: Record<string, unknown> }>
}

function parseFantasyMessage(rawJSON: string): { content: string; toolCalls: ToolCall[]; thinking: string; attachments: MessageAttachment[] } {
  try {
    const msg: FantasyMessage = JSON.parse(rawJSON)
    const textParts: string[] = []
    const thinkingParts: string[] = []
    const toolCalls: ToolCall[] = []
    const attachments: MessageAttachment[] = []

    for (const part of msg.content) {
      switch (part.type) {
        case "text":
          if (typeof part.data.text === "string") textParts.push(part.data.text)
          break
        case "reasoning":
          if (typeof part.data.text === "string") thinkingParts.push(part.data.text)
          break
        case "file":
          attachments.push({
            filename: (part.data.filename as string) || "file",
            media_type: (part.data.media_type as string) || "",
            data: part.data.data as string | undefined,
          })
          break
        case "tool-call":
          toolCalls.push({
            id: (part.data.tool_call_id as string) || crypto.randomUUID(),
            name: (part.data.tool_name as string) || "unknown",
            input: part.data.input as string | undefined,
            status: part.data.status as string | undefined,
          })
          break
        case "tool-result": {
          const callID = part.data.tool_call_id as string
          const result = extractToolOutput(part.data.output)
          if (callID && result) {
            const tc = toolCalls.find((t) => t.id === callID)
            if (tc) { tc.output = result.output; tc.isError = result.isError }
          }
          break
        }
      }
    }

    return { content: textParts.join(""), toolCalls, thinking: thinkingParts.join(""), attachments }
  } catch {
    return { content: "", toolCalls: [], thinking: "", attachments: [] }
  }
}

// Extract tool results from a tool-role message's raw_json.
// Returns a map of tool_call_id → { output, isError }.
function parseToolResults(rawJSON: string): Map<string, { output: string; isError: boolean }> {
  const results = new Map<string, { output: string; isError: boolean }>()
  try {
    const msg: FantasyMessage = JSON.parse(rawJSON)
    for (const part of msg.content) {
      if (part.type === "tool-result") {
        const callID = part.data.tool_call_id as string
        const result = extractToolOutput(part.data.output)
        if (callID && result) {
          results.set(callID, result)
        }
      }
    }
  } catch { /* ignore */ }
  return results
}

// Safely extract output from a fantasy tool result's nested output structure.
// Output format: {"type": "text", "data": {"text": "..."}} or {"type": "error", "data": {"error": "..."}}
function extractToolOutput(raw: unknown): { output: string; isError: boolean } | null {
  if (!raw || typeof raw !== "object") return null
  const obj = raw as { type?: string; data?: Record<string, unknown> }
  if (obj.type === "text" && typeof obj.data?.text === "string") {
    return { output: obj.data.text, isError: false }
  }
  if (obj.type === "error" && typeof obj.data?.error === "string") {
    return { output: obj.data.error, isError: true }
  }
  return null
}

// Merge history messages into a flat list for rendering.
// An agentic turn may span multiple DB rows: assistant (tool calls) → tool (results) →
// assistant (more tool calls) → tool (results) → assistant (final text). These are
// consolidated into a single Message with all tool calls and the final text content.
function mergeHistoryMessages(history: HistoryMessage[]): Message[] {
  const messages: Message[] = []
  let current: Message | undefined // accumulator for the current assistant turn

  function flushCurrent() {
    if (current) {
      messages.push(current)
      current = undefined
    }
  }

  for (const m of history) {
    if (m.role === "tool" && m.raw_json) {
      if (current) {
        // Attach results to the current assistant message's tool calls
        const results = parseToolResults(m.raw_json)
        for (const [callID, result] of results) {
          const target = current.toolCalls?.find((t) => t.id === callID)
          if (target) {
            target.output = result.output
            target.isError = result.isError
          }
        }
      }
      // Tool rows are always consumed (never rendered directly)
      continue
    }

    if (m.role === "assistant" && m.raw_json) {
      const parsed = parseFantasyMessage(m.raw_json)
      if (parsed.toolCalls.length > 0 || parsed.thinking) {
        // Accumulate tool calls/thinking into current turn (or start a new one)
        if (!current) {
          current = { id: crypto.randomUUID(), role: "assistant", content: "", toolCalls: [] }
        }
        if (parsed.toolCalls.length > 0) {
          current.toolCalls = [...(current.toolCalls ?? []), ...parsed.toolCalls]
        }
        if (parsed.thinking) {
          current.thinking = (current.thinking || "") + parsed.thinking
        }
        if (parsed.content) current.content += parsed.content
        continue
      }
      // Text-only assistant message — append content to current turn or start new
      if (current) {
        if (parsed.content) current.content += parsed.content
        else current.content += m.content
        flushCurrent()
        continue
      }
    }

    // Non-assistant message or no active turn — flush and emit
    flushCurrent()
    // Parse attachments from user messages (images stored as FileParts in raw_json).
    let userAttachments: MessageAttachment[] | undefined
    if (m.role === "user" && m.raw_json) {
      const parsed = parseFantasyMessage(m.raw_json)
      if (parsed.attachments.length > 0) userAttachments = parsed.attachments
    }
    messages.push({
      id: crypto.randomUUID(),
      role: m.role as Message["role"],
      content: m.content,
      attachments: userAttachments,
    })
  }

  flushCurrent()
  return messages
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
  // If it's valid JSON, pretty-print with syntax highlighting
  try {
    const parsed = JSON.parse(output)
    return "```json\n" + JSON.stringify(parsed, null, 2) + "\n```"
  } catch {
    // Plain text — render as a generic code block
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

// --- Token counter ---

function formatTokenCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

function formatCost(cost: number): string {
  if (cost < 0.01) return `$${cost.toFixed(4)}`
  return `$${cost.toFixed(2)}`
}

function TokenCounter({ usage }: { usage: UsageInfo }) {
  const contextUsed = usage.prompt_tokens + usage.completion_tokens
  const pct = usage.context_window > 0
    ? (contextUsed / usage.context_window) * 100
    : 0
  const pctColor = pct > 80 ? "text-red-500" : pct > 60 ? "text-yellow-500" : "text-green-600"

  return (
    <div className="group relative">
      <div className="flex items-center gap-1 rounded-full border px-2.5 py-0.5 text-xs tabular-nums text-muted-foreground cursor-default">
        <span>{formatTokenCount(contextUsed)}</span>
        <span>/</span>
        <span>{formatTokenCount(usage.context_window)}</span>
      </div>

      {/* Hover card */}
      <div className="pointer-events-none absolute right-0 top-full z-50 mt-2 opacity-0 transition-opacity group-hover:pointer-events-auto group-hover:opacity-100">
        <div className="w-56 rounded-lg border bg-popover p-3 text-sm shadow-md">
          <table className="w-full">
            <tbody>
              <tr>
                <td className="py-0.5 text-muted-foreground">Context</td>
                <td className={cn("py-0.5 text-right tabular-nums font-medium", pctColor)}>
                  {pct.toFixed(1)}%
                </td>
              </tr>
              <tr>
                <td className="py-0.5 text-muted-foreground">Turn input</td>
                <td className="py-0.5 text-right tabular-nums">
                  {usage.turn_input_tokens.toLocaleString()}
                </td>
              </tr>
              <tr>
                <td className="py-0.5 text-muted-foreground">Turn output</td>
                <td className="py-0.5 text-right tabular-nums">
                  {usage.turn_output_tokens.toLocaleString()}
                </td>
              </tr>
              <tr>
                <td className="border-t pt-1.5 text-muted-foreground">Turn cost</td>
                <td className="border-t pt-1.5 text-right tabular-nums">
                  {formatCost(usage.turn_cost)}
                </td>
              </tr>
              {usage.session_cost > 0 && (
                <tr>
                  <td className="py-0.5 text-muted-foreground">Session cost</td>
                  <td className="py-0.5 text-right tabular-nums">
                    {formatCost(usage.session_cost)}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  )
}

// --- Model selector ---

function ModelSelector({
  models,
  currentModel,
  onSelect,
}: {
  models: ModelInfo[]
  currentModel: string
  onSelect: (id: string) => void
}) {
  const currentName = models.find((m) => m.id === currentModel)?.name || currentModel
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState("")
  const listRef = useRef<HTMLDivElement>(null)
  const filtered = search
    ? models.filter(
        (m) =>
          m.id.toLowerCase().includes(search.toLowerCase()) ||
          m.name.toLowerCase().includes(search.toLowerCase())
      )
    : models

  // Scroll the selected model into view when the dropdown opens.
  useEffect(() => {
    if (!open || search) return
    requestAnimationFrame(() => {
      const el = listRef.current?.querySelector("[data-selected]")
      if (el) el.scrollIntoView({ block: "center" })
    })
  }, [open, search])

  return (
    <div className="relative">
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs text-muted-foreground hover:bg-accent cursor-pointer"
      >
        <span>{currentName}</span>
        <ChevronDown className="h-3 w-3" />
      </button>

      {open && (
        <>
          <div className="fixed inset-0 z-40" onClick={() => setOpen(false)} />
          <div className="absolute left-0 top-full z-50 mt-1 w-72 rounded-lg border bg-popover shadow-md">
            {models.length > 10 && (
              <div className="border-b p-2">
                <input
                  type="text"
                  className="w-full rounded-md border bg-transparent px-2 py-1 text-xs outline-none placeholder:text-muted-foreground"
                  placeholder="Search models..."
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  autoFocus
                />
              </div>
            )}
            <div ref={listRef} className="max-h-64 overflow-y-auto p-1">
              {filtered.map((m) => (
                <button
                  key={m.id}
                  data-selected={m.id === currentModel ? "" : undefined}
                  onClick={() => {
                    onSelect(m.id)
                    setOpen(false)
                    setSearch("")
                  }}
                  className={cn(
                    "flex w-full items-center justify-between rounded-md px-2 py-1.5 text-left text-xs hover:bg-accent cursor-pointer",
                    m.id === currentModel && "bg-accent"
                  )}
                >
                  <div className="flex flex-col">
                    <span className="font-medium">{m.name || m.id}</span>
                    {m.name && m.name !== m.id && (
                      <span className="text-[10px] text-muted-foreground">{m.id}</span>
                    )}
                  </div>
                  <div className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
                    {m.can_reason && <span className="rounded bg-muted px-1">reason</span>}
                    <span>{formatTokenCount(m.context_window)}</span>
                  </div>
                </button>
              ))}
              {filtered.length === 0 && (
                <div className="px-2 py-3 text-center text-xs text-muted-foreground">
                  No models found
                </div>
              )}
            </div>
          </div>
        </>
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

  // Binary toggle for older models without levels
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

// Status dot color helper
function statusDotColor(session: SessionInfo): string {
  if (session.status === "stopped") return "bg-gray-400"
  if (session.mode === "ephemeral") return "bg-violet-500"
  return "bg-green-500"
}

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
  // Don't connect WebSocket for stopped sessions
  const wsSessionId = isStopped ? null : (session?.id ?? null)
  const { send, connected, setOnMessage } = useWebSocket(wsSessionId)

  // Fetch available models once.
  useEffect(() => {
    fetch("/api/models")
      .then((res) => (res.ok ? res.json() : []))
      .then((data: ModelInfo[]) => setModels(data ?? []))
      .catch(() => {})
  }, [])

  const currentModel = usage?.model || session?.model || ""
  const currentModelInfo = models.find((m) => m.id === currentModel)

  const handleModelChange = useCallback(
    (modelId: string) => {
      send({ type: "config", model: modelId })
      // Optimistically update usage model and reset reasoning.
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

    // Fetch usage data for this session.
    fetch(`/api/sessions/${encodeURIComponent(session.id)}/usage`, {
      signal: ac.signal,
    })
      .then((res) => (res.ok ? res.json() : null))
      .then((data: UsageInfo | null) => {
        if (sessionGeneration.current === gen && data) setUsage(data)
      })
      .catch(() => {})

    fetch(`/api/sessions/${encodeURIComponent(session.id)}/messages`, {
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
            setMessages((prev) =>
              prev.map((m) =>
                m.id === id
                  ? { ...m, content: m.content + (msg.content || "") }
                  : m
              )
            )
          }
          break
        }
        case "tool_call": {
          // Ensure we have a streaming message to attach to
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
          setMessages((prev) =>
            prev.map((m) =>
              m.id === id
                ? { ...m, toolCalls: [...(m.toolCalls ?? []), tc] }
                : m
            )
          )
          break
        }
        case "tool_result": {
          if (!streamingMsgId.current) break
          const id = streamingMsgId.current
          setMessages((prev) =>
            prev.map((m) => {
              if (m.id !== id) return m
              const updated = (m.toolCalls ?? []).map((tc) =>
                tc.id === msg.tool_call_id
                  ? { ...tc, output: msg.output, isError: msg.is_error }
                  : tc
              )
              return { ...m, toolCalls: updated }
            })
          )
          break
        }
        case "reasoning_start": {
          // Ensure we have a streaming message to attach thinking to.
          if (!streamingMsgId.current) {
            const id = crypto.randomUUID()
            streamingMsgId.current = id
            setMessages((prev) => [
              ...prev,
              { id, role: "assistant", content: "", thinking: "", isThinking: true },
            ])
          } else {
            const id = streamingMsgId.current
            setMessages((prev) =>
              prev.map((m) =>
                m.id === id ? { ...m, isThinking: true } : m
              )
            )
          }
          break
        }
        case "reasoning_delta": {
          if (!streamingMsgId.current) break
          const id = streamingMsgId.current
          setMessages((prev) =>
            prev.map((m) =>
              m.id === id
                ? { ...m, thinking: (m.thinking || "") + (msg.content || "") }
                : m
            )
          )
          break
        }
        case "reasoning_end": {
          if (!streamingMsgId.current) break
          const id = streamingMsgId.current
          setMessages((prev) =>
            prev.map((m) =>
              m.id === id ? { ...m, isThinking: false } : m
            )
          )
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
      // Only intercept image files — text pastes should go into the textarea.
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

    // Build optimistic user message with attachment info.
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

    // Build wire message with attachments.
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
    const res = await fetch(`/api/sessions/${encodeURIComponent(session.id)}/stop`, {
      method: "POST",
    })
    if (!res.ok) console.error("Failed to stop session:", res.status, await res.text())
    onSessionsChanged()
  }

  const handleStart = async () => {
    if (!session) return
    const res = await fetch(`/api/sessions/${encodeURIComponent(session.id)}/start`, {
      method: "POST",
    })
    if (!res.ok) console.error("Failed to start session:", res.status, await res.text())
    onSessionsChanged()
  }

  const handleDelete = async () => {
    if (!session) return
    const res = await fetch(`/api/sessions/${encodeURIComponent(session.id)}`, {
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
