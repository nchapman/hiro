type View = 'chat' | 'dashboard'

interface SidebarProps {
  view: View
  onNavigate: (view: View) => void
}

const styles = {
  sidebar: {
    width: 220,
    background: 'var(--bg-surface)',
    borderRight: '1px solid var(--border)',
    display: 'flex',
    flexDirection: 'column' as const,
    padding: '16px 0',
  },
  logo: {
    padding: '0 16px 16px',
    borderBottom: '1px solid var(--border)',
    marginBottom: 8,
  },
  logoText: {
    fontSize: 20,
    fontWeight: 700,
    color: 'var(--accent)',
    letterSpacing: '-0.5px',
  },
  nav: {
    display: 'flex',
    flexDirection: 'column' as const,
    gap: 2,
    padding: '8px 8px',
  },
  navItem: (active: boolean) => ({
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
  }),
}

export default function Sidebar({ view, onNavigate }: SidebarProps) {
  return (
    <aside style={styles.sidebar}>
      <div style={styles.logo}>
        <span style={styles.logoText}>hive</span>
      </div>
      <nav style={styles.nav}>
        <button
          style={styles.navItem(view === 'chat')}
          onClick={() => onNavigate('chat')}
        >
          Chat
        </button>
        <button
          style={styles.navItem(view === 'dashboard')}
          onClick={() => onNavigate('dashboard')}
        >
          Dashboard
        </button>
      </nav>
    </aside>
  )
}
