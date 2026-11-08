// Small form parts the Browsers screens share (the prototype's UI.fieldHTML
// select-multi, wallet.js moneyField, and the .scrim > .modal shell UI.modal
// draws): who may use something (bots and teams as removable chips plus an
// "add" select), an amount with the $ inside, and a plain modal frame.
import type { ComponentChildren, JSX } from 'preact'
import { useEffect, useLayoutEffect, useRef } from 'preact/hooks'
import { Icon } from './Icon'
import { idOptions, roster, loadRoster } from '../lib/browserwords'

/** Bots and teams, several at once (select-multi): chips with a remove button, then "+ add…". */
export function WhoPicker({
  id,
  value,
  onChange,
  label = 'Who may use it',
}: {
  id: string
  value: string[]
  onChange: (v: string[]) => void
  label?: string
}): JSX.Element {
  useEffect(loadRoster, [])
  void roster.value
  const all = idOptions([])
  const labOf = (x: string) => all.find(([v]) => v === x)?.[1] || x
  return (
    <div class="listed" data-listk="who" id={id}>
      {value.map((x) => (
        <span class="chip-x" key={x}>
          <span class={labOf(x) === x ? 'mono' : 'chip-l'} data-v={x}>{labOf(x)}</span>
          <button type="button" aria-label={`Remove ${labOf(x)}`} onClick={() => onChange(value.filter((y) => y !== x))}>
            <Icon name="x" />
          </button>
        </span>
      ))}
      <select
        class="select mono list-add-sel"
        aria-label={`Add ${label}`}
        value=""
        onChange={(e) => {
          const v = (e.target as HTMLSelectElement).value
          ;(e.target as HTMLSelectElement).value = ''
          if (v && !value.includes(v)) onChange([...value, v])
        }}
      >
        <option value="">+ add…</option>
        {idOptions(value).map(([v, l]) => (
          <option value={v} key={v}>{l}</option>
        ))}
      </select>
    </div>
  )
}

/** A field row: label (required or optional), the control, a hint and its error. */
export function Field({
  k,
  label,
  req,
  hint,
  err,
  forId,
  children,
}: {
  k: string
  label: ComponentChildren
  req?: boolean
  hint?: ComponentChildren
  err?: string
  forId?: string
  children: ComponentChildren
}): JSX.Element {
  return (
    <div class={'field' + (err ? ' bad' : '')} data-field={k}>
      <label for={forId}>
        {label} {req ? <span class="req" aria-label="required">*</span> : <span class="opt">optional</span>}
      </label>
      {children}
      {hint ? <div class="hint">{hint}</div> : null}
      <div class="ferr" role="alert">{err || ''}</div>
    </div>
  )
}

/** An amount in dollars: the $ inside the field, no example number that could pass for a real limit. */
export function MoneyInput({ id, value, onInput }: { id: string; value: string; onInput: (v: string) => void }): JSX.Element {
  return (
    <div class="wl-money">
      <span aria-hidden="true">$</span>
      <input class="input mono" id={id} inputMode="decimal" autocomplete="off" placeholder="Amount" value={value} onInput={(e) => onInput((e.target as HTMLInputElement).value)} />
    </div>
  )
}

/** The modal frame (UI.modal): caps and title, a body, a foot. */
export function BrModal({
  caps,
  title,
  children,
  foot,
  onClose,
  testid,
}: {
  caps?: ComponentChildren
  title: ComponentChildren
  children: ComponentChildren
  foot: ComponentChildren
  onClose: () => void
  testid?: string
}): JSX.Element {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [])
  return (
    <div class="scrim" id="scrim">
      <div class="modal" role="dialog" aria-modal="true" tabIndex={-1} data-testid={testid}>
        <span class="sheet-grip only-phone" aria-hidden="true"></span>
        <div class="modal-head">
          <div>
            {caps ? <div class="caps">{caps}</div> : null}
            <h2>{title}</h2>
          </div>
          <button class="icon-btn" type="button" aria-label="Close" onClick={onClose}>
            <Icon name="x" />
          </button>
        </div>
        <div class="modal-body stack">{children}</div>
        <div class="modal-foot">{foot}</div>
      </div>
    </div>
  )
}

/** A money string → a number, NaN when it isn't an amount (up to two decimals). */
export function moneyOf(v: string): number {
  const s = String(v || '').replace(/[$,\s]/g, '')
  return /^\d+(\.\d{1,2})?$/.test(s) ? Number(s) : NaN
}

/** The step indicator (UI.stepper): numbered steps, done ones ticked, with the phone's compact form. */
export function Stepper({ steps, at, states }: { steps: string[]; at: number; states?: string[] }): JSX.Element {
  const st = states || steps.map((_, i) => (i < at ? 'done' : i === at ? 'cur' : ''))
  const cur = Math.max(0, st.findIndex((x) => /cur|bad/.test(x)))
  const done = st.filter((x) => x === 'done').length
  const n = steps.length
  const k = st.every((x) => x === 'done') ? n : cur + 1
  const kk = Math.min(k, n)
  const pct = (Math.max(done, k - 1 + (st[cur] && /cur/.test(st[cur]) ? 0.5 : 0)) / n) * 100
  // UI.fitSteppers: the labelled steps only when they fit; otherwise the compact form.
  const ref = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    const fit = () => {
      const el = ref.current
      if (!el) return
      el.classList.remove('compact')
      if (el.scrollWidth > el.clientWidth + 1) el.classList.add('compact')
    }
    fit()
    window.addEventListener('resize', fit)
    return () => window.removeEventListener('resize', fit)
  })
  return (
    <div ref={ref} class="stepper apply-steps" role="list" aria-label={`Step ${kk} of ${n}: ${steps[kk - 1]}`} data-n={n}>
      {steps.map((x, i) => (
        <div class={'as ' + st[i]} role="listitem" aria-current={/cur|bad/.test(st[i]) ? 'step' : undefined} key={i}>
          <b>{st[i] === 'done' ? '✓' : i + 1}</b>
          <span class="as-l">{x}</span>
        </div>
      ))}
      <div class="step-compact" aria-hidden="true">
        <span class="sc-t">Step {kk} of {n} · <b>{steps[kk - 1]}</b></span>
        <span class="step-bar"><i style={`width:${pct}%`} /></span>
      </div>
    </div>
  )
}

/** A control that can't be used here, and why (UI.off): aria-disabled with the reason as its tip. */
export const off = (reason: string | false | null | undefined): Record<string, string> =>
  reason ? { 'aria-disabled': 'true', 'data-reason': reason, 'data-tip': reason, title: reason } : {}

/** The crumbs above a page (UI.head crumbs): links, then the current page. */
export function BrCrumbs({ trail }: { trail: [string, string?][] }): JSX.Element {
  return (
    <nav class="dl-crumbs" aria-label="Where you are">
      {trail.map(([label, href], i) => (
        <>
          {i ? <span class="sep" aria-hidden="true">›</span> : null}
          {href ? <a href={href}>{label}</a> : <span class="cur" aria-current="page">{label}</span>}
        </>
      ))}
    </nav>
  )
}
