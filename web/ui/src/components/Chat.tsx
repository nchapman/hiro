import { useState, useRef, useEffect } from "react"
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
import type { ChatWireMessage, UsageInfo } from "@/hooks/use-websocket"
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
  PromptInputActions,
} from "@/components/prompt-kit/prompt-input"

// --- Types ---

interface ToolCall {
  id: string
  name: string
  input?: string
  output?: string
  isError?: boolean
  status?: string
}

interface Message {
  id: string
  role: "user" | "assistant" | "system"
  content: string
  toolCalls?: ToolCall[]
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

function parseFantasyMessage(rawJSON: string): { content: string; toolCalls: ToolCall[] } {
  try {
    const msg: FantasyMessage = JSON.parse(rawJSON)
    const textParts: string[] = []
    const toolCalls: ToolCall[] = []

    for (const part of msg.content) {
      switch (part.type) {
        case "text":
          if (typeof part.data.text === "string") textParts.push(part.data.text)
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

    return { content: textParts.join(""), toolCalls }
  } catch {
    return { content: "", toolCalls: [] }
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
      if (parsed.toolCalls.length > 0) {
        // Accumulate tool calls into current turn (or start a new one)
        if (!current) {
          current = { id: crypto.randomUUID(), role: "assistant", content: "", toolCalls: [] }
        }
        current.toolCalls = [...(current.toolCalls ?? []), ...parsed.toolCalls]
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
    messages.push({
      id: crypto.randomUUID(),
      role: m.role as Message["role"],
      content: m.content,
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

function AssistantMessage({ message }: { message: Message }) {
  const toolCalls = message.toolCalls ?? []
  const content = message.content

  return (
    <div className="space-y-2">
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
  const pct = usage.context_window > 0
    ? (usage.prompt_tokens / usage.context_window) * 100
    : 0
  const pctColor = pct > 80 ? "text-red-500" : pct > 60 ? "text-yellow-500" : "text-green-600"

  return (
    <div className="group relative">
      <div className="flex items-center gap-1 rounded-full border px-2.5 py-0.5 text-xs tabular-nums text-muted-foreground cursor-default">
        <span>{formatTokenCount(usage.turn_total)}</span>
        <span>/</span>
        <span>{formatTokenCount(usage.context_window)}</span>
      </div>

      {/* Hover card */}
      <div className="pointer-events-none absolute right-0 top-full z-50 mt-2 opacity-0 transition-opacity group-hover:pointer-events-auto group-hover:opacity-100">
        <div className="w-56 rounded-lg border bg-popover p-3 text-sm shadow-md">
          <table className="w-full">
            <tbody>
              <tr>
                <td className="py-0.5 text-muted-foreground">Context usage</td>
                <td className={cn("py-0.5 text-right tabular-nums font-medium", pctColor)}>
                  {pct.toFixed(1)}%
                </td>
              </tr>
              <tr>
                <td className="py-0.5 text-muted-foreground">Prompt tokens</td>
                <td className="py-0.5 text-right tabular-nums">
                  {usage.prompt_tokens.toLocaleString()}
                </td>
              </tr>
              <tr>
                <td className="py-0.5 text-muted-foreground">Completion</td>
                <td className="py-0.5 text-right tabular-nums">
                  {usage.completion_tokens.toLocaleString()}
                </td>
              </tr>
              <tr>
                <td className="border-t pt-1.5 text-muted-foreground">Total</td>
                <td className="border-t pt-1.5 text-right tabular-nums">
                  {usage.turn_total.toLocaleString()} / {usage.context_window.toLocaleString()}
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
  const [streaming, setStreaming] = useState(false)
  const [loadingHistory, setLoadingHistory] = useState(false)
  const [usage, setUsage] = useState<UsageInfo | null>(null)
  const streamingMsgId = useRef<string | null>(null)
  const sessionGeneration = useRef(0)
  const isStopped = session?.status === "stopped"
  const isRoot = session ? !session.mode || session.mode === "coordinator" : false
  // Don't connect WebSocket for stopped sessions
  const wsSessionId = isStopped ? null : (session?.id ?? null)
  const { send, connected, setOnMessage } = useWebSocket(wsSessionId)

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

  const handleSend = () => {
    const text = input.trim()
    if (!text || streaming || !connected || isStopped) return

    setMessages((prev) => [
      ...prev,
      { id: crypto.randomUUID(), role: "user", content: text },
    ])
    setInput("")
    setStreaming(true)
    send({ type: "message", content: text })
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
          <span className="text-xs text-muted-foreground">{session.mode}</span>
          {usage && usage.event_count > 0 && <TokenCounter usage={usage} />}
          {isStopped && (
            <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] font-medium uppercase text-muted-foreground">
              stopped
            </span>
          )}
        </div>
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
                    <div className="max-w-[85%] rounded-2xl bg-muted px-4 py-2.5 text-sm">
                      {msg.content}
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
          <PromptInput
            value={input}
            onValueChange={setInput}
            onSubmit={handleSend}
            isLoading={streaming}
            disabled={!connected}
            className="bg-muted/50"
          >
            <PromptInputTextarea
              placeholder={
                connected ? `Message ${session.name}...` : "Connecting..."
              }
              autoFocus
            />
            <PromptInputActions>
              <Button
                size="icon"
                className="h-8 w-8 rounded-full"
                onClick={handleSend}
                disabled={streaming || !connected || !input.trim()}
              >
                <ArrowUp className="h-4 w-4" />
              </Button>
            </PromptInputActions>
          </PromptInput>

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
