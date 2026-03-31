import { useState, useRef, useEffect, useLayoutEffect, useCallback } from "react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { HugeiconsIcon } from "@hugeicons/react"
import {
  MoreHorizontalIcon,
  StopIcon,
  PlayIcon,
  Delete01Icon,
} from "@hugeicons/core-free-icons"
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
import type { ChatWireMessage, UsageInfo } from "@/hooks/use-websocket"
import type { SessionInfo } from "@/App"
import {
  ChatContainerRoot,
  ChatContainerContent,
  type ChatContainerHandle,
} from "@/components/prompt-kit/chat-container"
import { ScrollButton } from "@/components/prompt-kit/scroll-button"
import { Loader } from "@/components/prompt-kit/loader"
import type { ModelInfo, ToolCall, Message, MessageAttachment } from "@/lib/chat-types"
import type { ChatAttachment } from "@/hooks/use-websocket"
import { mergeHistoryMessages } from "@/lib/chat-parser"
import { statusDotColor } from "@/lib/session-utils"
import ModelSelector from "@/pages/chat/ModelSelector"
import TokenCounter from "@/pages/chat/TokenCounter"
import { AssistantMessage, UserMessage } from "@/pages/chat/ChatMessages"
import ChatInputArea from "@/pages/chat/ChatInput"

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
  const [streaming, setStreaming] = useState(false)
  const [loadingHistory, setLoadingHistory] = useState(false)
  const [usage, setUsage] = useState<UsageInfo | null>(null)
  const [models, setModels] = useState<ModelInfo[]>([])
  const [reasoningEffort, setReasoningEffort] = useState("")
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
        .then((history: import("@/lib/chat-types").HistoryMessage[]) => {
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

  const handleSend = useCallback(
    (text: string, wireAttachments: ChatAttachment[] | undefined, displayAttachments: MessageAttachment[]) => {
      setMessages((prev) => [
        ...prev,
        {
          id: crypto.randomUUID(),
          role: "user",
          content: text,
          attachments: displayAttachments.length > 0 ? displayAttachments : undefined,
        },
      ])
      setStreaming(true)
      send({ type: "message", content: text, attachments: wireAttachments })
    },
    [send]
  )

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
              <HugeiconsIcon icon={MoreHorizontalIcon} className="h-4 w-4" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {!isStopped && (
                <DropdownMenuItem onClick={handleStop}>
                  <HugeiconsIcon icon={StopIcon} className="mr-2 h-4 w-4" />
                  Stop
                </DropdownMenuItem>
              )}
              {isStopped && (
                <DropdownMenuItem onClick={handleStart}>
                  <HugeiconsIcon icon={PlayIcon} className="mr-2 h-4 w-4" />
                  Start
                </DropdownMenuItem>
              )}
              <DropdownMenuSeparator />
              <DropdownMenuItem
                onClick={handleDelete}
                variant="destructive"
              >
                <HugeiconsIcon icon={Delete01Icon} className="mr-2 h-4 w-4" />
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
              <HugeiconsIcon icon={PlayIcon} className="mr-2 h-4 w-4" />
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
              <HugeiconsIcon icon={PlayIcon} className="mr-2 h-4 w-4" />
              Start
            </Button>
          </div>
        </div>
      ) : (
        <ChatInputArea
          sessionName={session.name}
          connected={connected}
          streaming={streaming}
          currentModelInfo={currentModelInfo}
          reasoningEffort={reasoningEffort}
          onReasoningChange={handleReasoningChange}
          onSend={handleSend}
        />
      )}
    </div>
  )
}
