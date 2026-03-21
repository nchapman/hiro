import { useState, useRef, useEffect } from 'react'

interface Message {
  id: string
  role: 'user' | 'assistant'
  content: string
  timestamp: Date
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
  },
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
  send: {
    padding: '10px 20px',
    background: 'var(--accent)',
    color: '#000',
    border: 'none',
    borderRadius: 8,
    fontSize: 14,
    fontWeight: 600,
    cursor: 'pointer',
  },
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
  const messagesEnd = useRef<HTMLDivElement>(null)

  useEffect(() => {
    messagesEnd.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  const send = () => {
    const text = input.trim()
    if (!text) return

    const userMsg: Message = {
      id: crypto.randomUUID(),
      role: 'user',
      content: text,
      timestamp: new Date(),
    }
    setMessages(prev => [...prev, userMsg])
    setInput('')

    // TODO: Send to backend via WebSocket and stream response
    // For now, echo back a placeholder
    setTimeout(() => {
      const assistantMsg: Message = {
        id: crypto.randomUUID(),
        role: 'assistant',
        content: `[Hive is not connected to an LLM yet. You said: "${text}"]`,
        timestamp: new Date(),
      }
      setMessages(prev => [...prev, assistantMsg])
    }, 300)
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      send()
    }
  }

  return (
    <div style={styles.container}>
      <div style={styles.header}>Swarm Chat</div>
      {messages.length === 0 ? (
        <div style={styles.empty}>Send a message to start a conversation with the swarm.</div>
      ) : (
        <div style={styles.messages}>
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
          placeholder="Message the swarm..."
          rows={1}
        />
        <button style={styles.send} onClick={send}>Send</button>
      </div>
    </div>
  )
}
