import { useState, useRef, useEffect, useCallback } from "react"

export interface UsageInfo {
  // Per-turn totals (all steps in most recent turn)
  turn_input_tokens: number
  turn_output_tokens: number
  turn_cost: number
  // Last step context (actual context window usage)
  prompt_tokens: number
  completion_tokens: number
  // Cumulative session totals
  session_input_tokens: number
  session_output_tokens: number
  session_total_tokens: number
  session_cost: number
  event_count: number
  // Model info
  context_window: number
  model: string
}

export interface ChatAttachment {
  filename: string
  data: string      // base64-encoded content
  media_type: string // MIME type
}

export interface ChatWireMessage {
  type: "message" | "delta" | "done" | "error" | "system" | "tool_call" | "tool_result" | "config" | "reasoning_start" | "reasoning_delta" | "reasoning_end"
  role?: "user" | "assistant"
  content?: string
  tool_call_id?: string
  tool_name?: string
  input?: string
  output?: string
  is_error?: boolean
  status?: string
  usage?: UsageInfo
  model?: string
  reasoning_effort?: string
  attachments?: ChatAttachment[]
}

export function useWebSocket(sessionId: string | null) {
  const wsRef = useRef<WebSocket | null>(null)
  const [connected, setConnected] = useState(false)
  const onMessageRef = useRef<(msg: ChatWireMessage) => void>(() => {})
  const reconnectTimer = useRef<number | undefined>(undefined)
  const currentSessionId = useRef<string | null>(null)

  const cleanup = useCallback(() => {
    clearTimeout(reconnectTimer.current)
    reconnectTimer.current = undefined
    if (wsRef.current) {
      wsRef.current.onclose = null
      wsRef.current.close()
      wsRef.current = null
    }
    setConnected(false)
  }, [])

  const connectWs = useCallback(
    (id: string) => {
      cleanup()
      currentSessionId.current = id

      const protocol =
        window.location.protocol === "https:" ? "wss:" : "ws:"
      const ws = new WebSocket(
        `${protocol}//${window.location.host}/ws/chat?session_id=${encodeURIComponent(id)}`
      )

      ws.onopen = () => setConnected(true)
      ws.onclose = () => {
        setConnected(false)
        if (currentSessionId.current === id) {
          reconnectTimer.current = window.setTimeout(
            () => connectWs(id),
            3000
          )
        }
      }
      ws.onmessage = (e) => {
        try {
          onMessageRef.current(JSON.parse(e.data))
        } catch {
          /* ignore malformed messages */
        }
      }

      wsRef.current = ws
    },
    [cleanup]
  )

  useEffect(() => {
    if (sessionId) {
      connectWs(sessionId)
    } else {
      cleanup()
    }
    return cleanup
  }, [sessionId, connectWs, cleanup])

  const send = useCallback((msg: ChatWireMessage) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify(msg))
    }
  }, [])

  const setOnMessage = useCallback(
    (handler: (msg: ChatWireMessage) => void) => {
      onMessageRef.current = handler
    },
    []
  )

  return { send, connected, setOnMessage }
}
