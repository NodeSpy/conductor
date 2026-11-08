// A bot's Memory tab: its memory repo, browsed and edited by kind
// (components/RepoView.tsx over internal/bot/httpapi_repo.go).
import type { JSX } from 'preact'
import type { BotDetail as BotData } from '../api/bots'
import { RepoView } from '../components/RepoView'

export function BotMemory({ bot }: { bot: BotData }): JSX.Element {
  return <RepoView target={'bot:' + bot.bot} title={bot.memory_repo || undefined} />
}
