import type { JSX } from 'preact'
import { useEffect } from 'preact/hooks'
import { Shell } from './components/Shell'
import { Overview } from './routes/Overview'
import { Config } from './routes/Config'
import { Secrets } from './routes/Secrets'
import { Packs } from './routes/Packs'
import { Connections } from './routes/Connections'
import { System } from './routes/System'
import { Audit } from './routes/Audit'
import { load, startLive } from './state'
import { route } from './router'

export function App(): JSX.Element {
  useEffect(() => {
    void load()
    const stop = startLive()
    return stop
  }, [])
  return (
    <Shell>
      <Screen />
    </Shell>
  )
}

function Screen(): JSX.Element {
  switch (route.value) {
    case 'config':
      return <Config />
    case 'secrets':
      return <Secrets />
    case 'packs':
      return <Packs />
    case 'connections':
      return <Connections />
    case 'system':
      return <System />
    case 'audit':
      return <Audit />
    default:
      return <Overview />
  }
}
