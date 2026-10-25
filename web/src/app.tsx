import type { JSX } from 'preact'
import { useEffect } from 'preact/hooks'
import { Shell } from './components/Shell'
import { Overview } from './routes/Overview'
import { load, startLive } from './state'

export function App(): JSX.Element {
  useEffect(() => {
    void load()
    const stop = startLive()
    return stop
  }, [])
  return (
    <Shell>
      <Overview />
    </Shell>
  )
}
