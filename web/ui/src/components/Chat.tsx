import { useState, useRef, useEffect } from "react"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { ScrollArea } from "@/components/ui/scroll-area"
import { SendHorizontal } from "lucide-react"
import { cn } from "@/lib/utils"
import { useWebSocket } from "@/hooks/use-websocket"
import type { ChatWireMessage } from "@/hooks/use-websocket"
import type { AgentInfo } from "@/App"

interface Message {
  id: string
  role: "user" | "assistant" | "system"
  content: string
}

interface HistoryMessage {
  role: "user" | "assistant"
  content: string
  timestamp?: string
}

interface ChatProps {
  agent: AgentInfo | null
}

export default function Chat({ agent }: ChatProps) {
  const [messages, setMessages] = useState<Message[]>([])
  const [input, setInput] = useState("")
  const [streaming, setStreaming] = useState(false)
  const [loadingHistory, setLoadingHistory] = useState(false)
  const messagesEnd = useRef<HTMLDivElement>(null)
  const streamingMsgId = useRef<string | null>(null)
  const messagesContainer = useRef<HTMLDivElement>(null)
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
        setMessages(
          history.map((m) => ({
            id: crypto.randomUUID(),
            role: m.role,
            content: m.content,
          }))
        )
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

  // Scroll to bottom on new messages
  useEffect(() => {
    const container = messagesContainer.current
    if (!container) return
    const isNearBottom =
      container.scrollHeight - container.scrollTop - container.clientHeight <
      100
    if (isNearBottom) {
      messagesEnd.current?.scrollIntoView({ behavior: "smooth" })
    }
  }, [messages])

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

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault()
      handleSend()
    }
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
      {/* Header */}
      <div className="flex shrink-0 items-center gap-2 border-b px-5 py-3 text-sm text-muted-foreground">
        <span
          className={cn(
            "h-1.5 w-1.5 rounded-full",
            connected ? "bg-green-500" : "bg-destructive"
          )}
        />
        <span className="font-semibold text-foreground">{agent.name}</span>
        {agent.description && <span>— {agent.description}</span>}
        {!connected && <span>(connecting...)</span>}
      </div>

      {/* Messages */}
      {loadingHistory ? (
        <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
          Loading history...
        </div>
      ) : messages.length === 0 ? (
        <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
          Send a message to start a conversation.
        </div>
      ) : (
        <ScrollArea className="flex-1" ref={messagesContainer}>
          <div className="flex flex-col gap-4 p-5">
            {messages.map((msg) => (
              <div
                key={msg.id}
                className={cn(
                  "max-w-[70%] whitespace-pre-wrap rounded-xl px-3.5 py-2.5 text-sm leading-relaxed",
                  msg.role === "user" &&
                    "self-end bg-primary text-primary-foreground",
                  msg.role === "assistant" && "self-start bg-muted",
                  msg.role === "system" &&
                    "max-w-[90%] self-center rounded-lg border px-3.5 py-2 text-xs text-muted-foreground"
                )}
              >
                {msg.content || (msg.role === "assistant" ? "..." : "")}
              </div>
            ))}
            {streaming && !streamingMsgId.current && (
              <div className="max-w-[70%] self-start whitespace-pre-wrap rounded-xl bg-muted px-3.5 py-2.5 text-sm">
                <span className="text-muted-foreground">Thinking...</span>
              </div>
            )}
            <div ref={messagesEnd} />
          </div>
        </ScrollArea>
      )}

      {/* Input */}
      <div className="flex shrink-0 gap-2 border-t p-4">
        <Textarea
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder={
            connected ? `Message ${agent.name}...` : "Connecting..."
          }
          disabled={!connected}
          rows={1}
          className="min-h-[40px] flex-1 resize-none"
        />
        <Button
          onClick={handleSend}
          disabled={streaming || !connected}
          size="icon"
          className="h-10 w-10 shrink-0"
        >
          <SendHorizontal className="h-4 w-4" />
        </Button>
      </div>
    </div>
  )
}
