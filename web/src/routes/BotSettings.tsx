// A bot's Settings tab.
import type { JSX } from 'preact'
import type { BotDetail as BotData } from '../api/bots'
import { useEffect, useMemo, useState } from 'preact/hooks'
import { IdentityPicker, emptyChoice, type IdentityChoice } from '../components/IdentityPicker'
import { ReviewPanel } from '../components/ReviewPanel'
import { staged, loadConfig, stage, cfgError } from '../configStore'

export function BotSettings({ bot }: { bot: BotData }): JSX.Element {
  const start = useMemo<IdentityChoice>(() => {
    const a = bot.identity?.avatar
    const base = emptyChoice()
    if (a?.icon) return { ...base, mode: 'icon', icon: a.icon, color: a.color || base.color }
    if (a?.shape) return { ...base, mode: 'face', shape: a.shape, eyes: a.eyes || base.eyes, color: a.color || base.color }
    return { ...base, color: bot.identity?.color || base.color }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bot.bot])
  const [choice, setChoice] = useState<IdentityChoice>(start)

  useEffect(() => { void loadConfig() }, [])

  async function stageFace() {
    await stage(['bots', bot.bot, 'avatar', 'color'], choice.color, `${bot.bot}: its face`)
    if (choice.mode === 'face') {
      await stage(['bots', bot.bot, 'avatar', 'shape'], choice.shape, `${bot.bot}: its face`)
      await stage(['bots', bot.bot, 'avatar', 'eyes'], choice.eyes, `${bot.bot}: its face`)
    } else {
      await stage(['bots', bot.bot, 'avatar', 'icon'], choice.icon, `${bot.bot}: its face`)
    }
  }

  return (
    <section class="dl-card">
      <header class="dl-ch"><h2>Settings</h2></header>
      <div class="form-field">
        <span>Its face</span>
        <IdentityPicker value={choice} onChange={setChoice} />
        <div class="row-actions">
          <button class="btn" type="button" data-testid="stage-face" onClick={() => void stageFace()}>Use this face</button>
        </div>
      </div>

      {cfgError.value ? <div class="banner err" role="alert">{cfgError.value}</div> : null}
      <ReviewPanel stagedCount={staged.value.length} />
    </section>
  )
}
