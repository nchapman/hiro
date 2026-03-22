import { useState, useRef, useEffect, useCallback } from 'react'
import type { AgentInfo } from '../App'

interface Message {
  id: string
  role: 'user' | 'assistant'
  content: string
}

interface ChatWireMessage {
  type: 'message' | 'delta' | 'done' | 'error'
  role?: 'user' | 'assistant'
  content?: string
}

interface HistoryMessage {
  role: 'user' | 'assistant'
  content: string
  timestamp?: string
}

function useWebSocket(agentId: string | null) {
  const wsRef = useRef<WebSocket | null>(null)
  const [connected, setConnected] = useState(false)
  const onMessageRef = useRef<(msg: ChatWireMessage) => void>(() => {})
  const reconnectTimer = useRef<number | undefined>(undefined)
  const currentAgentId = useRef<string | null>(null)

  const cleanup = useCallback(() => {
    clearTimeout(reconnectTimer.current)
    reconnectTimer.current = undefined
    if (wsRef.current) {
      wsRef.current.onclose = null // prevent reconnect
      wsRef.current.close()
      wsRef.current = null
    }
    setConnected(false)
  }, [])

  const connectWs = useCallback((id: string) => {
    cleanup()
    currentAgentId.current = id

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${protocol}//${window.location.host}/ws/chat?agent_id=${encodeURIComponent(id)}`)

    ws.onopen = () => setConnected(true)
    ws.onclose = () => {
      setConnected(false)
      // Auto-reconnect after 3 seconds if still targeting same agent
      if (currentAgentId.current === id) {
        reconnectTimer.current = window.setTimeout(() => connectWs(id), 3000)
      }
    }
    ws.onmessage = (e) => {
      try {
        onMessageRef.current(JSON.parse(e.data))
      } catch { /* ignore malformed messages */ }
    }

    wsRef.current = ws
  }, [cleanup])

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

  const setOnMessage = useCallback((handler: (msg: ChatWireMessage) => void) => {
    onMessageRef.current = handler
  }, [])

  return { send, connected, setOnMessage }
}

const styles = {
  container: {
    flex: 1,
    display: 'flex',
    flexDirection: 'column' as const,
    minHeight: 0,
    overflow: 'hidden',
  },
  header: {
    padding: '12px 20px',
    borderBottom: '1px solid var(--border)',
    fontSize: 14,
    color: 'var(--text-muted)',
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    flexShrink: 0,
  },
  headerName: {
    color: 'var(--text)',
    fontWeight: 600,
  },
  statusDot: (connected: boolean) => ({
    width: 6,
    height: 6,
    borderRadius: '50%',
    background: connected ? 'var(--green)' : 'var(--red)',
  }),
  messages: {
    flex: 1,
    overflowY: 'auto' as const,
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
    flexShrink: 0,
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
  noAgent: {
    flex: 1,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    color: 'var(--text-muted)',
    fontSize: 14,
  },
}

interface ChatProps {
  agent: AgentInfo | null
}

export default function Chat({ agent }: ChatProps) {
  const [messages, setMessages] = useState<Message[]>([])
  const [input, setInput] = useState('')
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

    fetch(`/api/agents/${encodeURIComponent(agent.id)}/messages`, { signal: ac.signal })
      .then(res => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        return res.json()
      })
      .then((history: HistoryMessage[]) => {
        if (agentGeneration.current !== gen) return
        setMessages(history.map(m => ({
          id: crypto.randomUUID(),
          role: m.role,
          content: m.content,
        })))
      })
      .catch(err => {
        if (err.name === 'AbortError') return
        if (agentGeneration.current !== gen) return
        setMessages([{ id: crypto.randomUUID(), role: 'assistant', content: 'Failed to load conversation history.' }])
      })
      .finally(() => {
        if (agentGeneration.current === gen) setLoadingHistory(false)
      })

    // Set up WebSocket message handler with the same generation guard
    setOnMessage((msg: ChatWireMessage) => {
      if (agentGeneration.current !== gen) return
      switch (msg.type) {
        case 'delta': {
          if (!streamingMsgId.current) {
            const id = crypto.randomUUID()
            streamingMsgId.current = id
            setMessages(prev => [...prev, {
              id,
              role: 'assistant',
              content: msg.content || '',
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
          }])
          break
      }
    })

    return () => ac.abort()
  }, [agent?.id, setOnMessage])

  // Scroll to bottom on new messages
  useEffect(() => {
    const container = messagesContainer.current
    if (!container) return
    const isNearBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 100
    if (isNearBottom) {
      messagesEnd.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [messages])

  const handleSend = () => {
    const text = input.trim()
    if (!text || streaming || !connected) return

    setMessages(prev => [...prev, {
      id: crypto.randomUUID(),
      role: 'user',
      content: text,
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

  if (!agent) {
    return (
      <div style={styles.noAgent}>
        Select an agent from the sidebar to start chatting.
      </div>
    )
  }

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <span style={styles.statusDot(connected)} />
        <span style={styles.headerName}>{agent.name}</span>
        {agent.description && <span>— {agent.description}</span>}
        {!connected && <span>(connecting...)</span>}
      </div>
      {loadingHistory ? (
        <div style={styles.empty}>Loading history...</div>
      ) : messages.length === 0 ? (
        <div style={styles.empty}>Send a message to start a conversation.</div>
      ) : (
        <div style={styles.messages} ref={messagesContainer}>
          {messages.map(msg => (
            <div key={msg.id} style={styles.message(msg.role)}>
              {msg.content || (msg.role === 'assistant' ? '...' : '')}
            </div>
          ))}
          {streaming && !streamingMsgId.current && (
            <div style={styles.message('assistant')}>
              <span style={{ color: 'var(--text-muted)' }}>Thinking...</span>
            </div>
          )}
          <div ref={messagesEnd} />
        </div>
      )}
      <div style={styles.inputArea}>
        <textarea
          style={styles.input}
          value={input}
          onChange={e => setInput(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder={connected ? `Message ${agent.name}...` : 'Connecting...'}
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
