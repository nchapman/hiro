import { useState } from 'react'
import Sidebar from './components/Sidebar'
import Chat from './components/Chat'
import Dashboard from './components/Dashboard'

type View = 'chat' | 'dashboard'

export default function App() {
  const [view, setView] = useState<View>('chat')

  return (
    <>
      <Sidebar view={view} onNavigate={setView} />
      <main style={{ flex: 1, display: 'flex', flexDirection: 'column' }}>
        {view === 'chat' ? <Chat /> : <Dashboard />}
      </main>
    </>
  )
}
