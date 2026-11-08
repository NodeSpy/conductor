// A bot's "What it can do" tab.
import type { JSX } from 'preact'
import type { BotDetail as BotData } from '../api/bots'
import { displayName } from '../api/bots'
import { StatusMark } from '../components/StatusMark'
import { BotWallet } from './BotWallet'

export function BotAccess({ bot }: { bot: BotData }): JSX.Element {
  const does = bot.capabilities?.does || []
  const asks = bot.capabilities?.asks || []
  const grp = (label: string, s: string, items: string[], empty: string) => (
    <div class="cang" data-s={s}>
      <h3><StatusMark s={s} mkOnly label={label} />{label}</h3>
      <ul>{items.length ? items.map((v) => <li class="mono">{v}</li>) : <li class="dl-sub">{empty}</li>}</ul>
    </div>
  )
  return (
    <section class="dl-card dl-can">
      <header class="dl-ch"><h2>What {displayName(bot.identity, bot.bot)} can do</h2></header>
      {bot.shadow ? <p class="dl-sub">In practice mode: even what it does on its own is only recorded, never done.</p> : null}
      <div class="dl-can-grid">
        {grp('Does on its own', 'ok', does, bot.shadow ? "Nothing — it's in practice mode, so it only suggests." : 'Nothing: it asks first')}
        {grp('Asks you first', 'ember', asks, 'Nothing needs your approval')}
        {grp('Never', 'fail', [], 'Anything it isn’t granted')}
      </div>
      <BotWallet bot={bot.bot} team={bot.team} />
    </section>
  )
}

