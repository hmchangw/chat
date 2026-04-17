import { NatsProvider, useNats } from './context/NatsContext'
import LoginPage from './pages/LoginPage'
import ChatPage from './pages/ChatPage'

function AppContent() {
  const { connected } = useNats()

  if (!connected) {
    return <LoginPage />
  }

  return <ChatPage />
}

export default function App() {
  return (
    <NatsProvider>
      <AppContent />
    </NatsProvider>
  )
}
