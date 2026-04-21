import { NatsProvider, useNats } from './context/NatsContext'
import { RoomEventsProvider } from './context/RoomEventsContext'
import LoginPage from './pages/LoginPage'
import ChatPage from './pages/ChatPage'

function AppContent() {
  const { connected } = useNats()

  if (!connected) {
    return <LoginPage />
  }

  return (
    <RoomEventsProvider>
      <ChatPage />
    </RoomEventsProvider>
  )
}

export default function App() {
  return (
    <NatsProvider>
      <AppContent />
    </NatsProvider>
  )
}
