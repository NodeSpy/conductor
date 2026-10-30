// New bot (design §4.4): a sheet over the roster. It composes the bot — a
// display name, what it's for, a team (its defaults shown as inherited), and a
// face from the identity picker — and stages it as a config change that lands
// through the same review/apply pipeline as every other edit. A new bot starts
// in practice mode.
import type { JSX } from 'preact'
import { useState } from 'preact/hooks'
import { stage } from '../configStore'
import { IdentityPicker, emptyChoice, type IdentityChoice } from '../components/IdentityPicker'
import type { TeamSummary } from '../api/bots'

export function NewBotSheet({ teams, onClose, onStaged }: { teams: TeamSummary[]; onClose: () => void; onStaged: () => void }): JSX.Element {
  const [name, setName] = useState('')
  const [display, setDisplay] = useState('')
  const [desc, setDesc] = useState('')
  const [team, setTeam] = useState('')
  const [choice, setChoice] = useState<IdentityChoice>(emptyChoice())
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const id = name.trim().toLowerCase().replace(/[^a-z0-9_-]+/g, '-')

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
      await stage(['bots', id, 'avatar', 'color'], choice.color, `${id}: face`)
      if (choice.mode === 'face') {
        await stage(['bots', id, 'avatar', 'shape'], choice.shape, `${id}: face`)
        await stage(['bots', id, 'avatar', 'eyes'], choice.eyes, `${id}: face`)
      } else {
        await stage(['bots', id, 'avatar', 'icon'], choice.icon, `${id}: face`)
      }
      onStaged()
    } catch (e) {
      setErr(String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="sheet-backdrop" onClick={onClose}>
      <aside class="sheet" role="dialog" aria-label="New bot" onClick={(e) => e.stopPropagation()}>
        <div class="sheet-head">
          <h2 class="serif">New bot</h2>
          <button class="btn ghost sm" type="button" onClick={onClose}>Close</button>
        </div>
        <div class="sheet-body">
          <label class="form-field">
            <span>Name</span>
            <input class="field" value={name} placeholder="atlas" onInput={(e) => setName((e.target as HTMLInputElement).value)} />
            {id && id !== name ? <small class="muted">id: {id}</small> : null}
          </label>
          <label class="form-field">
            <span>Display name</span>
            <input class="field" value={display} placeholder="Atlas" onInput={(e) => setDisplay((e.target as HTMLInputElement).value)} />
          </label>
          <label class="form-field">
            <span>What it's for</span>
            <textarea class="field" rows={2} value={desc} onInput={(e) => setDesc((e.target as HTMLTextAreaElement).value)} />
          </label>
          <label class="form-field">
            <span>Team</span>
            <select class="field" value={team} onChange={(e) => setTeam((e.target as HTMLSelectElement).value)}>
              <option value="">Solo</option>
              {teams.map((t) => <option value={t.team}>{t.team}</option>)}
            </select>
          </label>

          <div class="form-field">
            <span>Its face</span>
            <IdentityPicker value={choice} onChange={setChoice} />
          </div>

          {err ? <div class="banner err">{err}</div> : null}
          <p class="muted">A new bot starts in practice mode. It's staged — review and apply it on the Bots or Config screen.</p>
        </div>
        <div class="sheet-foot">
          <button class="btn" type="button" onClick={onClose}>Cancel</button>
          <button class="btn primary" type="button" disabled={busy} data-testid="create-bot" onClick={() => void create()}>Stage new bot</button>
        </div>
      </aside>
    </div>
  )
}
