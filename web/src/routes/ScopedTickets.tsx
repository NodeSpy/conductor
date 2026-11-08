// The Tickets tab of a bot's or a team's page: the tickets assigned to it.
import type { JSX } from 'preact'

export function ScopedTickets({ assignee }: { assignee: string }): JSX.Element {
  return <section class="tk-in" data-assignee={assignee} />
}
