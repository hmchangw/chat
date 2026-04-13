import { NatsProvider, useNats } from './context/NatsContext'
import LoginPage from './pages/LoginPage'

function AppContent() {
  const { connected } = useNats()

  if (!connected) {
    return <LoginPage />
  }

  return <div className="app">Connected! (Chat page coming next)</div>
}

export default function App() {
  return (
    <NatsProvider>
      <AppContent />
    </NatsProvider>
  )
}
