import { useState, useEffect, useCallback, useRef } from 'react'
import Sidebar from './components/Sidebar'
import Chat from './components/Chat'

export interface AgentInfo {
  id: string
  name: string
  mode: string
  description?: string
}

export default function App() {
  const [agents, setAgents] = useState<AgentInfo[]>([])
  const [selectedAgentId, setSelectedAgentId] = useState<string | null>(null)
  const hasAutoSelected = useRef(false)

  const fetchAgents = useCallback(async () => {
    try {
      const res = await fetch('/api/agents')
      if (res.ok) {
        const data: AgentInfo[] = await res.json()
        setAgents(data)
        // Auto-select the first persistent agent on initial load
        if (!hasAutoSelected.current && data.length > 0) {
          const persistent = data.find(a => a.mode === 'persistent')
          if (persistent) {
            setSelectedAgentId(persistent.id)
            hasAutoSelected.current = true
          }
        }
      }
    } catch { /* API unavailable */ }
  }, [])

  useEffect(() => {
    fetchAgents()
    const interval = setInterval(fetchAgents, 10000)
    return () => clearInterval(interval)
  }, [fetchAgents])

  const handleSelect = useCallback((id: string) => {
    hasAutoSelected.current = true
    setSelectedAgentId(id)
  }, [])

  const selectedAgent = agents.find(a => a.id === selectedAgentId) ?? null

  return (
    <>
      <Sidebar
        agents={agents}
        selectedId={selectedAgentId}
        onSelect={handleSelect}
      />
      <main style={{ flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0, overflow: 'hidden' }}>
        <Chat agent={selectedAgent} />
      </main>
    </>
  )
}
