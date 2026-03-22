import type { AgentInfo } from '../App'

interface SidebarProps {
  agents: AgentInfo[]
  selectedId: string | null
  onSelect: (id: string) => void
}

const styles = {
  sidebar: {
    width: 220,
    minWidth: 220,
    background: 'var(--bg-surface)',
    borderRight: '1px solid var(--border)',
    display: 'flex',
    flexDirection: 'column' as const,
    overflow: 'hidden',
  },
  logo: {
    padding: '16px 16px 16px',
    borderBottom: '1px solid var(--border)',
  },
  logoText: {
    fontSize: 20,
    fontWeight: 700,
    color: 'var(--accent)',
    letterSpacing: '-0.5px',
  },
  sectionLabel: {
    fontSize: 11,
    fontWeight: 600,
    color: 'var(--text-muted)',
    textTransform: 'uppercase' as const,
    letterSpacing: '0.5px',
    padding: '16px 16px 6px',
  },
  agentList: {
    flex: 1,
    overflow: 'auto',
    padding: '0 8px 8px',
    display: 'flex',
    flexDirection: 'column' as const,
    gap: 2,
  },
  agentItem: (active: boolean) => ({
    padding: '8px 12px',
    borderRadius: 6,
    cursor: 'pointer',
    fontSize: 14,
    fontWeight: active ? 600 : 400,
    background: active ? 'var(--bg-elevated)' : 'transparent',
    color: active ? 'var(--text)' : 'var(--text-muted)',
    border: 'none',
    textAlign: 'left' as const,
    transition: 'background 0.15s',
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    width: '100%',
  }),
  modeDot: (mode: string) => ({
    width: 6,
    height: 6,
    borderRadius: '50%',
    background: mode === 'persistent' ? 'var(--green)' : 'var(--text-muted)',
    flexShrink: 0,
  }),
  agentName: {
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap' as const,
  },
  empty: {
    padding: '8px 16px',
    fontSize: 13,
    color: 'var(--text-muted)',
    fontStyle: 'italic' as const,
  },
}

export default function Sidebar({ agents, selectedId, onSelect }: SidebarProps) {
  return (
    <aside style={styles.sidebar}>
      <div style={styles.logo}>
        <span style={styles.logoText}>hive</span>
      </div>
      <div style={styles.sectionLabel}>Agents</div>
      <div style={styles.agentList}>
        {agents.length === 0 ? (
          <div style={styles.empty}>No agents running</div>
        ) : (
          agents.map(agent => (
            <button
              key={agent.id}
              style={styles.agentItem(agent.id === selectedId)}
              onClick={() => onSelect(agent.id)}
              title={agent.description || agent.name}
            >
              <span style={styles.modeDot(agent.mode)} />
              <span style={styles.agentName}>{agent.name}</span>
            </button>
          ))
        )}
      </div>
    </aside>
  )
}
