import { useState, useRef, useEffect } from "react"
import { Button } from "@/components/ui/button"
import { ArrowUp, Wrench, ChevronRight, ChevronDown } from "lucide-react"
import { cn } from "@/lib/utils"
import { useWebSocket } from "@/hooks/use-websocket"
import type { ChatWireMessage } from "@/hooks/use-websocket"
import type { AgentInfo } from "@/App"
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
        {toolCall.name}
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

// --- Chat component ---

interface ChatProps {
  agent: AgentInfo | null
}

export default function Chat({ agent }: ChatProps) {
  const [messages, setMessages] = useState<Message[]>([])
  const [input, setInput] = useState("")
  const [streaming, setStreaming] = useState(false)
  const [loadingHistory, setLoadingHistory] = useState(false)
  const streamingMsgId = useRef<string | null>(null)
  const agentGeneration = useRef(0)
  const { send, connected, setOnMessage } = useWebSocket(agent?.id ?? null)

  // Load message history when agent changes
  useEffect(() => {
    const gen = ++agentGeneration.current

    if (!agent) {
      setMessages([])
      return
    }

    const ac = new AbortController()
    setMessages([])
    setStreaming(false)
    streamingMsgId.current = null
    setLoadingHistory(true)

    fetch(`/api/agents/${encodeURIComponent(agent.id)}/messages`, {
      signal: ac.signal,
    })
      .then((res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        return res.json()
      })
      .then((history: HistoryMessage[]) => {
        if (agentGeneration.current !== gen) return
        setMessages(mergeHistoryMessages(history))
      })
      .catch((err: Error) => {
        if (err.name === "AbortError") return
        if (agentGeneration.current !== gen) return
        setMessages([
          {
            id: crypto.randomUUID(),
            role: "assistant",
            content: "Failed to load conversation history.",
          },
        ])
      })
      .finally(() => {
        if (agentGeneration.current === gen) setLoadingHistory(false)
      })

    setOnMessage((msg: ChatWireMessage) => {
      if (agentGeneration.current !== gen) return
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
  }, [agent?.id, setOnMessage])

  const handleSend = () => {
    const text = input.trim()
    if (!text || streaming || !connected) return

    setMessages((prev) => [
      ...prev,
      { id: crypto.randomUUID(), role: "user", content: text },
    ])
    setInput("")
    setStreaming(true)
    send({ type: "message", content: text })
  }

  if (!agent) {
    return (
      <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
        Select an agent from the sidebar to start chatting.
      </div>
    )
  }

  return (
    <div className="flex flex-1 flex-col overflow-hidden">
      {/* Messages */}
      {loadingHistory ? (
        <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
          Loading history...
        </div>
      ) : messages.length === 0 ? (
        <div className="flex flex-1 flex-col items-center justify-center gap-2">
          <p className="text-lg font-medium text-foreground">
            {agent.name}
          </p>
          {agent.description && (
            <p className="text-sm text-muted-foreground">
              {agent.description}
            </p>
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
              connected ? `Message ${agent.name}...` : "Connecting..."
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
            Connecting to {agent.name}...
          </p>
        )}
      </div>
    </div>
  )
}
