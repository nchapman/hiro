import { useState, useRef, useEffect } from "react"
import { Button } from "@/components/ui/button"
import { ArrowUp } from "lucide-react"
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
          <ChatContainerContent className="mx-auto w-full max-w-3xl px-4 py-4">
            {messages.map((msg) => (
              <div key={msg.id}>
                {msg.role === "user" && (
                  <div className="flex justify-end py-2">
                    <div className="max-w-[85%] rounded-2xl bg-muted px-4 py-2.5 text-sm">
                      {msg.content}
                    </div>
                  </div>
                )}

                {msg.role === "assistant" && (
                  <div className="py-2">
                    <Markdown
                      className={cn(
                        "prose prose-sm dark:prose-invert max-w-none",
                        "prose-pre:my-2 prose-code:before:content-none prose-code:after:content-none"
                      )}
                    >
                      {msg.content || "..."}
                    </Markdown>
                  </div>
                )}

                {msg.role === "system" && (
                  <div className="flex justify-center py-2">
                    <span className="rounded-full border px-3 py-1 text-xs text-muted-foreground">
                      {msg.content}
                    </span>
                  </div>
                )}
              </div>
            ))}

            {streaming && !streamingMsgId.current && (
              <div className="py-2">
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
