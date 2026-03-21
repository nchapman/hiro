import { useState, useEffect } from 'react'

interface WorkerInfo {
  id: string
  agent_name: string
  description: string
  skills: string[]
  connected_at: string
}

interface SwarmStatus {
  swarm_code: string
  worker_count: number
  active_tasks: number
}

const styles = {
  container: {
    flex: 1,
    overflow: 'auto',
    padding: 24,
  },
  header: {
    fontSize: 20,
    fontWeight: 700,
    marginBottom: 24,
  },
  grid: {
    display: 'grid',
    gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))',
    gap: 16,
    marginBottom: 32,
  },
  stat: {
    background: 'var(--bg-surface)',
    border: '1px solid var(--border)',
    borderRadius: 8,
    padding: 16,
  },
  statLabel: {
    fontSize: 12,
    color: 'var(--text-muted)',
    textTransform: 'uppercase' as const,
    letterSpacing: '0.5px',
    marginBottom: 4,
  },
  statValue: {
    fontSize: 28,
    fontWeight: 700,
  },
  section: {
    marginBottom: 24,
  },
  sectionTitle: {
    fontSize: 14,
    fontWeight: 600,
    marginBottom: 12,
    color: 'var(--text-muted)',
    textTransform: 'uppercase' as const,
    letterSpacing: '0.5px',
  },
  workerCard: {
    background: 'var(--bg-surface)',
    border: '1px solid var(--border)',
    borderRadius: 8,
    padding: 16,
    marginBottom: 8,
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  workerName: {
    fontSize: 14,
    fontWeight: 600,
  },
  workerSkills: {
    display: 'flex',
    gap: 6,
  },
  skillBadge: {
    fontSize: 11,
    padding: '2px 8px',
    borderRadius: 12,
    background: 'var(--accent-dim)',
    color: 'var(--accent)',
  },
  statusDot: (online: boolean) => ({
    width: 8,
    height: 8,
    borderRadius: '50%',
    background: online ? 'var(--green)' : 'var(--red)',
    display: 'inline-block',
    marginRight: 8,
  }),
  empty: {
    color: 'var(--text-muted)',
    fontSize: 14,
    fontStyle: 'italic' as const,
  },
}

export default function Dashboard() {
  const [status, setStatus] = useState<SwarmStatus | null>(null)
  const [workers, setWorkers] = useState<WorkerInfo[]>([])

  useEffect(() => {
    const fetchData = async () => {
      try {
        const [statusRes, workersRes] = await Promise.all([
          fetch('/api/swarm'),
          fetch('/api/workers'),
        ])
        setStatus(await statusRes.json())
        setWorkers(await workersRes.json())
      } catch {
        // API not available yet — that's fine during dev
      }
    }

    fetchData()
    const interval = setInterval(fetchData, 5000)
    return () => clearInterval(interval)
  }, [])

  return (
    <div style={styles.container}>
      <div style={styles.header}>Swarm Dashboard</div>

      <div style={styles.grid}>
        <div style={styles.stat}>
          <div style={styles.statLabel}>Workers</div>
          <div style={styles.statValue}>{status?.worker_count ?? '—'}</div>
        </div>
        <div style={styles.stat}>
          <div style={styles.statLabel}>Active Tasks</div>
          <div style={styles.statValue}>{status?.active_tasks ?? '—'}</div>
        </div>
        <div style={styles.stat}>
          <div style={styles.statLabel}>Swarm Code</div>
          <div style={{ ...styles.statValue, fontSize: 16, fontFamily: 'monospace' }}>
            {status?.swarm_code ?? '—'}
          </div>
        </div>
      </div>

      <div style={styles.section}>
        <div style={styles.sectionTitle}>Connected Workers</div>
        {workers.length === 0 ? (
          <div style={styles.empty}>No workers connected</div>
        ) : (
          workers.map(w => (
            <div key={w.id} style={styles.workerCard}>
              <div>
                <div style={styles.workerName}>
                  <span style={styles.statusDot(true)} />
                  {w.agent_name}
                </div>
                {w.description && (
                  <div style={{ fontSize: 12, color: 'var(--text-muted)', marginTop: 4 }}>
                    {w.description}
                  </div>
                )}
              </div>
              <div style={styles.workerSkills}>
                {w.skills?.map(skill => (
                  <span key={skill} style={styles.skillBadge}>{skill}</span>
                ))}
              </div>
            </div>
          ))
        )}
      </div>
    </div>
  )
}
