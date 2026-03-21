import { useState, useRef, useEffect, useCallback } from 'react'

interface Message {
  id: string
  role: 'user' | 'assistant'
  content: string
  timestamp: Date
}

interface ChatWireMessage {
  type: 'message' | 'delta' | 'done' | 'error'
  role?: 'user' | 'assistant'
  content?: string
}

function useWebSocket() {
  const wsRef = useRef<WebSocket | null>(null)
  const [connected, setConnected] = useState(false)
  const onMessageRef = useRef<(msg: ChatWireMessage) => void>(() => {})
  const reconnectTimer = useRef<number | undefined>(undefined)

  const connectWs = useCallback(() => {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${protocol}//${window.location.host}/ws/chat`)

    ws.onopen = () => setConnected(true)
    ws.onclose = () => {
      setConnected(false)
      // Auto-reconnect after 3 seconds
      reconnectTimer.current = window.setTimeout(connectWs, 3000)
    }
    ws.onmessage = (e) => {
      try {
        onMessageRef.current(JSON.parse(e.data))
      } catch { /* ignore malformed messages */ }
    }

    wsRef.current = ws
  }, [])

  const connect = useCallback((onMessage: (msg: ChatWireMessage) => void) => {
    onMessageRef.current = onMessage
    connectWs()
    return () => {
      clearTimeout(reconnectTimer.current)
      wsRef.current?.close()
    }
  }, [connectWs])

  const send = useCallback((msg: ChatWireMessage) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify(msg))
    }
  }, [])

  return { connect, send, connected }
}

const styles = {
  container: {
    flex: 1,
    display: 'flex',
    flexDirection: 'column' as const,
  },
  header: {
    padding: '12px 20px',
    borderBottom: '1px solid var(--border)',
    fontSize: 14,
    color: 'var(--text-muted)',
    display: 'flex',
    alignItems: 'center',
    gap: 8,
  },
  statusDot: (connected: boolean) => ({
    width: 6,
    height: 6,
    borderRadius: '50%',
    background: connected ? 'var(--green)' : 'var(--red)',
  }),
  messages: {
    flex: 1,
    overflow: 'auto',
    padding: 20,
    display: 'flex',
    flexDirection: 'column' as const,
    gap: 16,
  },
  message: (role: 'user' | 'assistant') => ({
    maxWidth: '70%',
    alignSelf: role === 'user' ? 'flex-end' as const : 'flex-start' as const,
    background: role === 'user' ? 'var(--accent-dim)' : 'var(--bg-elevated)',
    padding: '10px 14px',
    borderRadius: 12,
    fontSize: 14,
    lineHeight: 1.5,
    whiteSpace: 'pre-wrap' as const,
  }),
  inputArea: {
    padding: 16,
    borderTop: '1px solid var(--border)',
    display: 'flex',
    gap: 8,
  },
  input: {
    flex: 1,
    padding: '10px 14px',
    background: 'var(--bg-elevated)',
    border: '1px solid var(--border)',
    borderRadius: 8,
    color: 'var(--text)',
    fontSize: 14,
    outline: 'none',
    resize: 'none' as const,
    fontFamily: 'inherit',
  },
  send: (disabled: boolean) => ({
    padding: '10px 20px',
    background: disabled ? 'var(--bg-elevated)' : 'var(--accent)',
    color: disabled ? 'var(--text-muted)' : '#000',
    border: 'none',
    borderRadius: 8,
    fontSize: 14,
    fontWeight: 600,
    cursor: disabled ? 'default' : 'pointer',
  }),
  empty: {
    flex: 1,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    color: 'var(--text-muted)',
    fontSize: 14,
  },
}

export default function Chat() {
  const [messages, setMessages] = useState<Message[]>([])
  const [input, setInput] = useState('')
  const [streaming, setStreaming] = useState(false)
  const messagesEnd = useRef<HTMLDivElement>(null)
  const streamingMsgId = useRef<string | null>(null)
  const { connect, send, connected } = useWebSocket()

  // Auto-scroll only when near the bottom
  const messagesContainer = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const container = messagesContainer.current
    if (!container) return
    const isNearBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 100
    if (isNearBottom) {
      messagesEnd.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [messages])

  useEffect(() => {
    return connect((msg) => {
      switch (msg.type) {
        case 'delta': {
          // Append delta to the current streaming message
          if (!streamingMsgId.current) {
            const id = crypto.randomUUID()
            streamingMsgId.current = id
            setMessages(prev => [...prev, {
              id,
              role: 'assistant',
              content: msg.content || '',
              timestamp: new Date(),
            }])
          } else {
            const id = streamingMsgId.current
            setMessages(prev =>
              prev.map(m => m.id === id
                ? { ...m, content: m.content + (msg.content || '') }
                : m
              )
            )
          }
          break
        }
        case 'done':
          streamingMsgId.current = null
          setStreaming(false)
          break
        case 'error':
          streamingMsgId.current = null
          setStreaming(false)
          setMessages(prev => [...prev, {
            id: crypto.randomUUID(),
            role: 'assistant',
            content: `Error: ${msg.content}`,
            timestamp: new Date(),
          }])
          break
      }
    })
  }, [connect])

  const handleSend = () => {
    const text = input.trim()
    if (!text || streaming || !connected) return

    setMessages(prev => [...prev, {
      id: crypto.randomUUID(),
      role: 'user',
      content: text,
      timestamp: new Date(),
    }])
    setInput('')
    setStreaming(true)
    send({ type: 'message', content: text })
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSend()
    }
  }

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <span style={styles.statusDot(connected)} />
        Swarm Chat {!connected && '(connecting...)'}
      </div>
      {messages.length === 0 ? (
        <div style={styles.empty}>Send a message to start a conversation with the swarm.</div>
      ) : (
        <div style={styles.messages} ref={messagesContainer}>
          {messages.map(msg => (
            <div key={msg.id} style={styles.message(msg.role)}>
              {msg.content}
            </div>
          ))}
          <div ref={messagesEnd} />
        </div>
      )}
      <div style={styles.inputArea}>
        <textarea
          style={styles.input}
          value={input}
          onChange={e => setInput(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder={connected ? 'Message the swarm...' : 'Connecting...'}
          disabled={!connected}
          rows={1}
        />
        <button
          style={styles.send(streaming || !connected)}
          onClick={handleSend}
          disabled={streaming || !connected}
        >
          {streaming ? '...' : 'Send'}
        </button>
      </div>
    </div>
  )
}
