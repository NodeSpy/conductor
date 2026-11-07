import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { Shell } from './components/Shell'
import { Needs } from './routes/Needs'
import { Chat } from './routes/Chat'
import { Bots } from './routes/Bots'
import { BotPage } from './routes/BotPage'
import { Setup } from './routes/Setup'
import { api } from './api/client'
import { route } from './router'
import { wireLiveUpdates } from './state'
import { isMock } from './api/mock'

export function App(): JSX.Element {
  const [configured, setConfigured] = useState<boolean | null>(isMock() ? true : null)

  useEffect(() => {
    void wireLiveUpdates()
    if (isMock()) return
    api
      .getSettings()
      .then((s) => setConfigured(!!(s.daemon_url && s.operator && s.ssh_public_key_path)))
      .catch(() => setConfigured(false))
  }, [])

  void route.value // re-render on route change

  if (configured === null) return <div class="page dl-page" />
  if (!configured && route.value.split('/')[0] !== 'setup') {
    location.hash = '#/setup'
    return <div class="page dl-page" />
  }

  const seg0 = route.value.split('/')[0] || 'needs'

  if (seg0 === 'setup') return <Setup />

  let screen: JSX.Element
  if (seg0 === 'chat') screen = <Chat />
  else if (seg0 === 'bots' && route.value.split('/')[1]) screen = <BotPage />
  else if (seg0 === 'bots') screen = <Bots />
  else screen = <Needs />

  return <Shell>{screen}</Shell>
}
