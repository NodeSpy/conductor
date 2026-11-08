// The Bots list's actions (prototype js/screens/botlist.js "actions: each
// change is staged into the one diff"): move to a team, clone, practice mode
// on/off, archive / switch back on, change the model, add a bot to a team.
// Each stages config edits and then leads to Review & apply.
import type { JSX } from 'preact'
import { displayName, type BotSummary, type TeamSummary } from '../api/bots'
import { ConfirmModal, FormModal } from '../components/FormModal'
import { cfgError, stage, stageDelete } from '../configStore'
import { cfgTree, toReview } from './TeamForm'

/* eslint-disable @typescript-eslint/no-explicit-any */
const SOLO = '(solo)'
const clone = <T,>(o: T): T => JSON.parse(JSON.stringify(o ?? {}))

// stageIn edits one key inside a bot's collection (autonomy, worker) by
// staging the whole collection: a config written in flow style
// (`autonomy: { shadow: false }`) can't take an edit at a single leaf.
async function stageIn(name: string, coll: string, mutate: (c: Record<string, any>) => void, summary: string): Promise<void> {
  const c = clone(cfgTree().bots?.[name]?.[coll] || {})
  mutate(c)
  if (Object.keys(c).length) await stage(['bots', name, coll], c, summary)
  else if (cfgTree().bots?.[name]?.[coll] !== undefined) await stageDelete(['bots', name, coll], summary)
}

export const botPractice = (b: BotSummary): boolean => {
  const t = cfgTree()
  return !!(b.shadow || t.bots?.[b.bot]?.autonomy?.shadow || (b.team && t.teams?.[b.team]?.autonomy?.shadow))
}

// moveTo stages one change per bot that isn't already on the team.
export async function moveTo(bots: BotSummary[], names: string[], team: string): Promise<number> {
  let n = 0
  for (const name of names) {
    const cur = bots.find((b) => b.bot === name)?.team || ''
    if (cur === team) continue
    if (team) {
      await stage(['bots', name, 'team'], team, `${name} joins team ${team}`)
      // a team in practice mode can't have a bot that says it acts for real:
      // the bot takes the team's rule (its own explicit "live" goes)
      const t = cfgTree()
      if (t.teams?.[team]?.autonomy?.shadow === true && t.bots?.[name]?.autonomy?.shadow === false) {
        await stageIn(name, 'autonomy', (a) => delete a.shadow, `${name} takes team ${team}'s practice mode`)
      }
    } else await stageDelete(['bots', name, 'team'], `${name} leaves its team`)
    n++
    if (cfgError.value) break
  }
  return n
}

export async function setPractice(names: string[], on: boolean): Promise<void> {
  for (const n of names) {
    await stageIn(n, 'autonomy', (a) => (on ? (a.shadow = true) : delete a.shadow), on ? `${n} in practice mode` : `${n} goes live`)
    if (cfgError.value) return
  }
}

export async function setArchived(names: string[], on: boolean): Promise<void> {
  for (const n of names) {
    if (on) await stage(['bots', n, 'disabled'], true, `archive ${n}`)
    else await stageDelete(['bots', n, 'disabled'], `switch ${n} back on`)
    if (cfgError.value) return
  }
}

const teamOptions = (teams: TeamSummary[]): [string, string][] => [[SOLO, 'No team (solo)'] as [string, string]].concat(teams.map((t) => [t.team, t.team]))

export function MoveForm({ bots, teams, names, onClose }: { bots: BotSummary[]; teams: TeamSummary[]; names: string[]; onClose: () => void }): JSX.Element {
  const one = names.length === 1
  const cur = one ? bots.find((b) => b.bot === names[0])?.team || SOLO : SOLO
  return (
    <FormModal
      testid="move-form"
      caps={one ? `bots.${names[0]}.team` : 'bots'}
      title={one ? `Move ${names[0]} to a team` : `Move ${names.length} bots to a team`}
      values={{ team: cur }}
      fields={[{ k: 'team', type: 'enum', label: 'Team', req: true, options: teamOptions(teams) }]}
      note={one ? undefined : 'One staged change per bot, reviewed together as one diff. A bot takes its new team’s rules: the most it may do can only narrow.'}
      submit={one ? 'Stage move' : 'Stage moves'}
      onClose={onClose}
      onSubmit={async (v) => {
        const n = await moveTo(bots, names, v.team === SOLO ? '' : v.team)
        if (cfgError.value) return cfgError.value
        if (!n) return 'No change: already there.'
        toReview()
      }}
    />
  )
}

export function ModelForm({ names, onClose }: { names: string[]; onClose: () => void }): JSX.Element {
  return (
    <FormModal
      testid="model-form"
      caps="bots"
      title={`Change the model for ${names.length} bot${names.length > 1 ? 's' : ''}`}
      fields={[{ k: 'model', type: 'string', req: true, label: 'Model (or fleet)', ph: 'claude-sonnet-5', help: 'A model id or a fleet name (fixer, reviewer…).' }]}
      submit="Stage changes"
      onClose={onClose}
      onSubmit={async (v) => {
        for (const n of names) {
          await stageIn(n, 'worker', (w) => (w.model = v.model), `${n} uses ${v.model}`)
          if (cfgError.value) return cfgError.value
        }
        toReview()
      }}
    />
  )
}

export function ArchiveConfirm({ names, onClose }: { names: string[]; onClose: () => void }): JSX.Element {
  const one = names.length === 1
  return (
    <ConfirmModal
      title={one ? `Archive ${names[0]}?` : `Archive ${names.length} bots?`}
      body={`${one ? 'It stops' : 'An archived bot stops'}: it hears no events and starts nothing. Its memory and history stay, and you can switch it back on. This stages the change for review.`}
      ok="Stage archive"
      danger
      onClose={onClose}
      onOk={async () => {
        await setArchived(names, true)
        toReview()
      }}
    />
  )
}

export function CloneForm({ bots, name, onClose }: { bots: BotSummary[]; name: string; onClose: () => void }): JSX.Element {
  return (
    <FormModal
      testid="clone-form"
      caps={`a copy of ${name}`}
      title={`Clone ${name}`}
      values={{ name: `${name}-2` }}
      fields={[
        {
          k: 'name', type: 'string', req: true, label: 'Name', pattern: /^[a-z][a-z0-9-]*$/, patternMsg: 'Lowercase letters, digits and dashes.',
          validate: (x) => (bots.some((b) => b.bot === x) || cfgTree().bots?.[x] ? 'A bot by that name exists.' : ''),
        },
      ]}
      note="The copy takes its settings, permissions and team; not its memory or history. It starts in practice mode."
      submit="Stage the copy"
      onClose={onClose}
      onSubmit={async (v) => {
        const c: any = clone(cfgTree().bots?.[name] || {})
        c.autonomy = { ...(c.autonomy || {}), shadow: true }
        delete c.disabled
        // the copy is its own bot: not the original's display name
        if (c.display_name) c.display_name = `${c.display_name} (copy)`
        await stage(['bots', v.name], c, `new bot ${v.name}, a copy of ${name}`)
        if (cfgError.value) return cfgError.value
        toReview()
      }}
    />
  )
}

export function AddToTeamForm({ bots, team, onClose }: { bots: BotSummary[]; team: string; onClose: () => void }): JSX.Element {
  const others = bots.filter((b) => b.team !== team)
  const newHref = `#/bots/new?team=${encodeURIComponent(team)}`
  return (
    <FormModal
      testid="add-to-team"
      caps={`team ${team}`}
      title={`Add a bot to ${team}`}
      fields={[
        {
          k: 'bot', type: 'enum', req: true, label: 'Bot',
          options: others.map((b) => [b.bot, `${displayName(b.identity, b.bot)}${b.team ? ` · now on ${b.team}` : ' · no team'}`]),
          create: { label: 'A new bot', onCreate: () => { onClose(); location.hash = newHref } },
        },
      ]}
      note={`It takes ${team}’s rules; the most it may do can only narrow.`}
      submit="Stage it"
      onClose={onClose}
      onSubmit={async (v) => {
        if (!v.bot) return 'Pick a bot, or make a new one.'
        await moveTo(bots, [v.bot], team)
        if (cfgError.value) return cfgError.value
        toReview()
      }}
    />
  )
}
