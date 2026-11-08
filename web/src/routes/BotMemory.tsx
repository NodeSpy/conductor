// A bot's Memory tab.
import type { JSX } from 'preact'
import type { BotDetail as BotData } from '../api/bots'

// BotMemory: a placeholder for the prototype's full memory editor (CodeMirror
// 6, Write/Preview, [[ completion, evidence chips — docs/design/dashboard.md,
// CONFORMANCE "A bot's Memory"). Checked again in phase 8: the engine behind
// this is real (internal/bot.Repo — a git repo of typed Markdown documents,
// memops.go/memread.go/memrepo.go) and a CLI reaches it (memorycli.go), but
// the operator API the dashboard is built on (internal/bot.OperatorRoutes,
// httpapi.go) has no route for it at all — no list, read, write, evidence or
// history endpoint for a memory entry, only messages/tickets/attachments/
// teams/conversations. There is nothing an HTTP handler in this package could
// call. This stays a minimal dl-card naming what memory is and its kinds as
// plain tag chips until internal/bot grows that surface (a daemon feature,
// out of scope here — see /var/tmp/dashempty/gaps.md).
export function BotMemory({ bot }: { bot: BotData }): JSX.Element {
  return (
    <section class="dl-card">
      <header class="dl-ch"><h2>Memory</h2></header>
      <p class="dl-sub">The bot's memory, by kind (knowledge, runbooks, watch, log, handoff). It keeps writing its own; your edits land as one commit in your name.</p>
      {bot.memory_repo ? <p class="dl-sub">Repo <span class="mono">{bot.memory_repo}</span></p> : null}
      <p class="fx">
        {['Knowledge', 'Runbooks', 'Watch', 'Log', 'Handoff'].map((k) => <span class="tag">{k}</span>)}
      </p>
    </section>
  )
}

