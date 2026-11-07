// New workflow: name it, pick manual or an event you'll fill in afterwards,
// and it's staged into the config's triggers: list — the same review → apply
// pipeline every config edit uses. Once applied, it opens in its own builder
// page (#/workflows/<name>) where the Config file tab is a full YAML editor.
import type { JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { config, staged, applyResult, loadConfig, stage } from '../configStore'
import { ReviewPanel } from '../components/ReviewPanel'
import { go } from '../router'
import { api } from '../api/client'

const NAME_RE = /^[a-z][a-z0-9-]*$/

export function Builder(): JSX.Element {
  const [name, setName] = useState('')
  const [kind, setKind] = useState<'manual' | 'event'>('manual')
  const [touched, setTouched] = useState(false)
  const [staging, setStaging] = useState(false)
  const [waiting, setWaiting] = useState(false)
  const [gaveUp, setGaveUp] = useState(false)

  useEffect(() => void loadConfig(), [])

  const existing = (config.value?.tree?.triggers as { name?: string }[] | undefined) || []
  const taken = existing.some((t) => t?.name === name)
  const valid = NAME_RE.test(name) && !taken

  const appliedAtMount = useRef(applyResult.value)
  const applied = applyResult.value && applyResult.value !== appliedAtMount.current ? applyResult.value : null
  useEffect(() => {
    if (!applied) return
    setWaiting(true)
    setGaveUp(false)
    let tries = 0
    let stop = false
    const poll = () => {
      if (stop) return
      api
        .workflow(name)
        .then(() => {
          if (stop) return
          go('workflows/' + encodeURIComponent(name))
        })
        .catch(() => {
          if (stop) return
          if (++tries < 20) setTimeout(poll, 1000)
          else setGaveUp(true)
        })
    }
    const t = setTimeout(poll, 800)
    return () => {
      stop = true
      clearTimeout(t)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [applied])

  const create = async () => {
    if (!valid) {
      setTouched(true)
      return
    }
    setStaging(true)
    try {
      const next = [...existing, { name, on: 'manual', enabled: kind === 'manual' ? true : false, steps: [] }]
      await stage(['triggers'], next, `new workflow ${name}`)
    } finally {
      setStaging(false)
    }
  }

  const pendingTriggers = staged.value.filter((c) => c.path[0] === 'triggers').length

  if (waiting) {
    return (
      <div class="col m-page">
        <h1 class="screen-title serif">Build a workflow</h1>
        {gaveUp ? (
          <div class="banner attn" role="status">Applied, but <span class="mono">{name}</span> hasn't shown up yet. conductor may still be reloading — reload this page in a moment.</div>
        ) : (
          <p class="muted">Applying…</p>
        )}
      </div>
    )
  }

  return (
    <div class="col m-page">
      <nav class="crumbs" aria-label="Breadcrumb">
        <a href="#/workflows">Workflows</a>
        <span class="crumbs-sep">/</span>
        <span>New workflow</span>
      </nav>
      <h1 class="screen-title serif">Build a workflow</h1>
      <p class="muted">Name it, then shape its steps and its config in the builder.</p>

      {pendingTriggers === 0 ? (
        <section class="dl-card" style="max-width:480px">
          <div class="field">
            <label for="wf-new-name">Name</label>
            <input id="wf-new-name" class="input mono" value={name} placeholder="e.g. release-notify" onInput={(e) => setName(e.currentTarget.value)} onBlur={() => setTouched(true)} />
            {touched && name && !NAME_RE.test(name) ? <div class="ferr">Lowercase letters, digits and dashes.</div> : null}
            {touched && taken ? <div class="ferr">A workflow by that name exists.</div> : null}
          </div>
          <div class="field">
            <label>Kind</label>
            <div class="seg" role="group">
              <button type="button" class={kind === 'manual' ? 'on' : ''} aria-pressed={kind === 'manual'} onClick={() => setKind('manual')}>Manual</button>
              <button type="button" class={kind === 'event' ? 'on' : ''} aria-pressed={kind === 'event'} onClick={() => setKind('event')}>An event</button>
            </div>
            <div class="hint">{kind === 'manual' ? 'Started by you, from here or conductor run.' : "You'll set up the event in its config file after it's created."}</div>
          </div>
          <button class="btn primary" type="button" disabled={staging || !name} onClick={() => void create()}>Create</button>
        </section>
      ) : (
        <div class="banner attn" role="status">New workflow <span class="mono">{name}</span> is staged, not applied yet.</div>
      )}

      {pendingTriggers ? <ReviewPanel stagedCount={staged.value.length} /> : null}
    </div>
  )
}
