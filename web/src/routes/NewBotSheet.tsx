// New bot (design §4.4): a sheet over the roster, restyled into Daylight's
// generic modal (design §3: .modal-scrim/.modal, a bottom sheet on a phone —
// see app.css). It composes the bot — a display name, what it's for, a team
// (its defaults shown as inherited), and a face from the identity picker — and
// stages it as a config change that lands through the same review/apply
// pipeline as every other edit. A new bot starts in practice mode.
//
// Left out of this port (the prototype's botwizard.js has no backend yet):
// the multi-step wizard chrome and the live "what it inherits from its team"
// preview — the team picker narrates it in one line instead.
import type { JSX } from 'preact'
import { useState } from 'preact/hooks'
import { cfgError, stage as stageEdit } from '../configStore'
import { IdentityPicker, emptyChoice, type IdentityChoice } from '../components/IdentityPicker'
import type { TeamSummary } from '../api/bots'
import { humanError } from '../api/errors'

// workers is the cli runtimes a bot's workers can run on: with one it is used
// without asking; with several the bot needs to be told which.
export function NewBotSheet({ teams, workers, onClose, onStaged }: { teams: TeamSummary[]; workers?: string[] | null; onClose: () => void; onStaged: () => void }): JSX.Element {
  const [name, setName] = useState('')
  const [display, setDisplay] = useState('')
  const [desc, setDesc] = useState('')
  const [team, setTeam] = useState('')
  const [worker, setWorker] = useState(workers?.[0] ?? '')
  const [choice, setChoice] = useState<IdentityChoice>(emptyChoice())
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const id = name.trim().toLowerCase().replace(/[^a-z0-9_-]+/g, '-')

  // stage records one field; the store keeps a failed stage's error rather
  // than throwing, so stop at the first one instead of closing as if done.
  async function stage(path: string[], value: unknown, summary: string) {
    await stageEdit(path, value, summary)
    if (cfgError.value) throw new Error(cfgError.value)
  }

  async function create() {
    if (!id) {
      setErr('give the bot a name')
      return
    }
    setBusy(true)
    setErr('')
    try {
      // Stage the bot's fields; a brand-new node lands as a block through the
      // config pipeline's section add. Practice mode (shadow) is the default.
      await stage(['bots', id, 'description'], desc || 'A bot.', `define bot ${id}`)
      if (display) await stage(['bots', id, 'display_name'], display, `${id}: name`)
      if (team) await stage(['bots', id, 'team'], team, `${id}: team`)
      if (workers && workers.length > 1 && worker) await stage(['bots', id, 'worker', 'runtime'], worker, `${id}: where it works`)
      await stage(['bots', id, 'avatar', 'color'], choice.color, `${id}: face`)
      if (choice.mode === 'face') {
        await stage(['bots', id, 'avatar', 'shape'], choice.shape, `${id}: face`)
        await stage(['bots', id, 'avatar', 'eyes'], choice.eyes, `${id}: face`)
      } else {
        await stage(['bots', id, 'avatar', 'icon'], choice.icon, `${id}: face`)
      }
      onStaged()
    } catch (e) {
      setErr(humanError(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="modal-scrim" onClick={onClose}>
      <div class="modal" role="dialog" aria-label="New bot" onClick={(e) => e.stopPropagation()}>
        <h2>New bot</h2>
        <div class="modal-body stack">
          <label class="field">
            <span>Name</span>
            <input class="input" value={name} placeholder="atlas" onInput={(e) => setName((e.target as HTMLInputElement).value)} />
            {id && id !== name ? <span class="hint">id: {id}</span> : null}
          </label>
          <label class="field">
            <span>Display name</span>
            <input class="input" value={display} placeholder="Atlas" onInput={(e) => setDisplay((e.target as HTMLInputElement).value)} />
          </label>
          <label class="field">
            <span>What it's for</span>
            <textarea class="textarea" rows={2} value={desc} onInput={(e) => setDesc((e.target as HTMLTextAreaElement).value)} />
          </label>
          {teams.length ? (
            <label class="field">
              <span>Team</span>
              <select class="select" value={team} onChange={(e) => setTeam((e.target as HTMLSelectElement).value)}>
                <option value="">Solo — not on a team</option>
                {teams.map((t) => <option value={t.team}>{t.team}</option>)}
              </select>
              {team ? <span class="hint">It takes {team}'s rules; the most it may do can only narrow.</span> : null}
            </label>
          ) : null}

          {workers && workers.length > 1 ? (
            <label class="field">
              <span>Where it works</span>
              <select class="select" value={worker} onChange={(e) => setWorker((e.target as HTMLSelectElement).value)}>
                {workers.map((w) => <option value={w}>{w}</option>)}
              </select>
            </label>
          ) : null}

          <div class="field">
            <span>Its face</span>
            <IdentityPicker value={choice} onChange={setChoice} />
          </div>

          {err ? <div class="banner err">{err}</div> : null}
          <p class="dl-sub">A new bot starts in practice mode. Staging it adds it to a change you review and apply on the Bots screen.</p>
        </div>
        <div class="modal-foot row-actions">
          <button class="btn" type="button" onClick={onClose}>Cancel</button>
          <button class="btn primary" type="button" disabled={busy} data-testid="create-bot" onClick={() => void create()}>Stage new bot</button>
        </div>
      </div>
    </div>
  )
}
