import { useState, useEffect, useCallback } from "react"
import { cn } from "@/lib/utils"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  ChatContainerRoot,
  ChatContainerContent,
} from "@/components/prompt-kit/chat-container"
import { AssistantMessage, UserMessage } from "@/pages/chat/ChatMessages"
import { mergeHistoryMessages } from "@/lib/chat-parser"
import type { Message, HistoryMessage } from "@/lib/chat-types"

interface SessionEntry {
  id: string
  channel_type: string
  channel_id?: string
  status: string
  created_at: string
  stopped_at?: string
  message_count: number
}

interface SessionsModalProps {
  instanceId: string
  instanceName: string
  open: boolean
  onOpenChange: (open: boolean) => void
}

function channelLabel(s: SessionEntry): string {
  switch (s.channel_type) {
    case "web":
      return "Web"
    case "telegram":
    case "tg":
      return s.channel_id ? `Telegram ${s.channel_id}` : "Telegram"
    case "slack":
      return s.channel_id ? `Slack ${s.channel_id}` : "Slack"
    case "agent":
      return "Agent"
    case "trigger":
      return "Scheduled"
    default:
      return s.channel_type || "Unknown"
  }
}

function formatDate(iso: string): string {
  if (!iso) return ""
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" }) +
    " " +
    d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
}

export default function SessionsModal({
  instanceId,
  instanceName,
  open,
  onOpenChange,
}: SessionsModalProps) {
  const [sessions, setSessions] = useState<SessionEntry[]>([])
  const [loadingSessions, setLoadingSessions] = useState(false)
  const [sessionError, setSessionError] = useState<string | null>(null)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [messages, setMessages] = useState<Message[]>([])
  const [loadingMessages, setLoadingMessages] = useState(false)

  // Fetch session list.
  const fetchSessions = useCallback(async () => {
    setLoadingSessions(true)
    setSessionError(null)
    try {
      const res = await fetch(`/api/instances/${encodeURIComponent(instanceId)}/sessions`)
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const data: SessionEntry[] = await res.json()
      // Show newest first.
      setSessions(data.reverse())
    } catch {
      setSessions([])
      setSessionError("Failed to load sessions")
    } finally {
      setLoadingSessions(false)
    }
  }, [instanceId])

  // Fetch on open and auto-refresh every 10s while open.
  useEffect(() => {
    if (!open) return
    fetchSessions()
    setSelectedId(null)
    setMessages([])
    const id = setInterval(fetchSessions, 10_000)
    return () => clearInterval(id)
  }, [open, fetchSessions])

  // Fetch messages when a session is selected.
  useEffect(() => {
    if (!selectedId) {
      setMessages([])
      return
    }
    const ac = new AbortController()
    setLoadingMessages(true)
    fetch(`/api/sessions/${encodeURIComponent(selectedId)}/messages`, { signal: ac.signal })
      .then((res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        return res.json()
      })
      .then((history: HistoryMessage[]) => {
        setMessages(mergeHistoryMessages(history))
      })
      .catch((err: Error) => {
        if (err.name === "AbortError") return
        setMessages([])
      })
      .finally(() => setLoadingMessages(false))
    return () => ac.abort()
  }, [selectedId])

  const selected = sessions.find((s) => s.id === selectedId)

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-4xl h-[80vh] flex flex-col p-0 gap-0">
        <DialogHeader className="px-4 pt-4 pb-3 border-b shrink-0">
          <DialogTitle>{instanceName} &mdash; Sessions</DialogTitle>
        </DialogHeader>

        <div className="flex flex-1 min-h-0">
          {/* Session list sidebar */}
          <div className="w-64 min-w-64 border-r flex flex-col">
            <ScrollArea className="flex-1">
              {loadingSessions && sessions.length === 0 ? (
                <div className="flex flex-col gap-1 p-2">
                  {Array.from({ length: 5 }).map((_, i) => (
                    <div key={i} className="flex flex-col gap-1 p-2">
                      <Skeleton className="h-3.5 w-24 rounded" />
                      <Skeleton className="h-3 w-16 rounded" />
                    </div>
                  ))}
                </div>
              ) : sessionError ? (
                <p className="p-4 text-sm text-destructive">
                  {sessionError}
                </p>
              ) : sessions.length === 0 ? (
                <p className="p-4 text-sm text-muted-foreground italic">
                  No sessions
                </p>
              ) : (
                <div className="flex flex-col gap-0.5 p-1">
                  {sessions.map((s) => (
                    <button
                      key={s.id}
                      onClick={() => setSelectedId(s.id)}
                      className={cn(
                        "flex flex-col gap-0.5 rounded-md px-3 py-2 text-left text-sm transition-colors cursor-pointer",
                        s.id === selectedId
                          ? "bg-accent text-accent-foreground"
                          : "text-muted-foreground hover:bg-accent/50 hover:text-accent-foreground"
                      )}
                    >
                      <div className="flex items-center gap-2">
                        <span
                          className={cn(
                            "h-1.5 w-1.5 shrink-0 rounded-full",
                            s.status === "running" ? "bg-green-500" : "bg-gray-400"
                          )}
                        />
                        <span className="font-medium truncate">
                          {channelLabel(s)}
                        </span>
                        <span className="ml-auto text-xs text-muted-foreground tabular-nums">
                          {s.message_count}
                        </span>
                      </div>
                      <span className="text-xs text-muted-foreground pl-3.5">
                        {formatDate(s.created_at)}
                        {s.stopped_at && " — " + formatDate(s.stopped_at)}
                      </span>
                    </button>
                  ))}
                </div>
              )}
            </ScrollArea>
          </div>

          {/* Message viewer */}
          <div className="flex-1 flex flex-col min-w-0">
            {!selectedId ? (
              <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
                Select a session to view
              </div>
            ) : loadingMessages ? (
              <div className="mx-auto w-full max-w-3xl flex-1 flex flex-col gap-4 px-4 py-4">
                <div className="flex justify-end">
                  <Skeleton className="h-10 w-48 rounded-2xl" />
                </div>
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
              <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
                {selected ? "No messages in this session" : "Select a session to view"}
              </div>
            ) : (
              <ChatContainerRoot className="flex-1">
                <ChatContainerContent className="mx-auto w-full max-w-3xl flex flex-col gap-4 px-4 py-4">
                  {messages.map((msg) => (
                    <div key={msg.id}>
                      {msg.role === "user" && <UserMessage message={msg} />}
                      {msg.role === "assistant" && <AssistantMessage message={msg} />}
                      {msg.role === "system" && (
                        <div className="flex justify-center">
                          <div className="rounded-md border px-3 py-2 text-xs text-muted-foreground whitespace-pre font-mono">
                            {msg.content}
                          </div>
                        </div>
                      )}
                    </div>
                  ))}
                </ChatContainerContent>
              </ChatContainerRoot>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
