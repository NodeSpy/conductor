// A memory or knowledge repo browser: a bot's Memory tab ("bot:<name>") and a
// team's Knowledge tab ("team:<name>").
import type { JSX } from 'preact'

export function RepoView({ target }: { target: string }): JSX.Element {
  return <section class="dl-card" data-repo={target} />
}
