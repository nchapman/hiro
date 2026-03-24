import { useState, useRef, useEffect, useCallback } from "react"

export interface ChatWireMessage {
  type: "message" | "delta" | "done" | "error" | "system" | "tool_call" | "tool_result"
  role?: "user" | "assistant"
  content?: string
  tool_call_id?: string
  tool_name?: string
  input?: string
  output?: string
  is_error?: boolean
}

export function useWebSocket(agentId: string | null) {
  const wsRef = useRef<WebSocket | null>(null)
  const [connected, setConnected] = useState(false)
  const onMessageRef = useRef<(msg: ChatWireMessage) => void>(() => {})
  const reconnectTimer = useRef<number | undefined>(undefined)
  const currentAgentId = useRef<string | null>(null)

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
      currentAgentId.current = id

      const protocol =
        window.location.protocol === "https:" ? "wss:" : "ws:"
      const ws = new WebSocket(
        `${protocol}//${window.location.host}/ws/chat?agent_id=${encodeURIComponent(id)}`
      )

      ws.onopen = () => setConnected(true)
      ws.onclose = () => {
        setConnected(false)
        if (currentAgentId.current === id) {
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
    if (agentId) {
      connectWs(agentId)
    } else {
      cleanup()
    }
    return cleanup
  }, [agentId, connectWs, cleanup])

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
