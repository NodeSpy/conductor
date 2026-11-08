// A generated modal form and a confirm dialog, ported from the prototype's
// UI.form / UI.confirm (js/ui.js): the same .scrim > .modal markup, a
// .modal-head with caps and title, a .modal-body form of .field rows (label
// with required/optional, the input, a hint, an inline error), an error
// callout, an optional note, and a .modal-foot with Cancel and the primary
// action. onSubmit returns an error string to show (the form stays open), or
// nothing to close.
import type { ComponentChildren, JSX } from 'preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { Icon } from './Icon'

export interface FormField {
  k: string
  label?: string
  type?: 'string' | 'text' | 'enum' | 'seg'
  req?: boolean
  readonly?: boolean
  mono?: boolean
  rows?: number
  // [value, label, disabledReason?]
  options?: [string, string, string?][]
  // An enum's first choice that opens a create flow instead of selecting.
  create?: { label: string; onCreate: () => void }
  help?: ComponentChildren
  ph?: string
  def?: string
  pattern?: RegExp
  patternMsg?: string
  validate?: (v: string, all: Record<string, string>) => string
  show?: (all: Record<string, string>) => boolean
}

export type Values = Record<string, string>

const labelOf = (f: FormField) =>
  f.label ?? f.k.split('.').pop()!.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase())

export function FormModal({
  caps,
  title,
  intro,
  fields,
  values,
  note,
  submit = 'Save',
  submitIcon,
  wide,
  testid,
  onSubmit,
  onClose,
}: {
  caps?: ComponentChildren
  title: ComponentChildren
  intro?: ComponentChildren
  fields: FormField[]
  values?: Values
  note?: ComponentChildren
  submit?: string
  submitIcon?: string
  wide?: boolean
  testid?: string
  onSubmit: (v: Values, clear: (k: string) => void) => Promise<string | void> | string | void
  onClose: () => void
}): JSX.Element {
  const [vals, setVals] = useState<Values>(() => {
    const o: Values = {}
    for (const f of fields) o[f.k] = values?.[f.k] ?? f.def ?? (f.type === 'enum' && f.req && f.options?.length ? f.options[0][0] : '')
    return o
  })
  const [errs, setErrs] = useState<Record<string, string>>({})
  const [formErr, setFormErr] = useState('')
  const [busy, setBusy] = useState(false)
  const formRef = useRef<HTMLFormElement>(null)

  useEffect(() => {
    const t = setTimeout(() => {
      if (window.innerWidth <= 720) return
      formRef.current?.querySelector<HTMLElement>('input:not([type=hidden]):not([readonly]),select,textarea')?.focus()
    }, 30)
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => {
      clearTimeout(t)
      document.removeEventListener('keydown', onKey)
    }
  }, [])

  const visible = fields.filter((f) => !f.show || f.show(vals))
  const set = (k: string, v: string) => setVals((o) => ({ ...o, [k]: v }))

  async function go() {
    const e: Record<string, string> = {}
    for (const f of visible) {
      const v = (vals[f.k] ?? '').trim()
      let err = ''
      if (f.req && !v) err = 'Required.'
      else if (f.pattern && v && !f.pattern.test(v)) err = f.patternMsg || 'Invalid value.'
      else if (f.validate) err = f.validate(v, vals) || ''
      if (err) e[f.k] = err
    }
    setErrs(e)
    if (Object.keys(e).length) return
    const out: Values = {}
    for (const f of visible) if ((vals[f.k] ?? '').trim() !== '') out[f.k] = f.type === 'text' ? vals[f.k] : vals[f.k].trim()
    setBusy(true)
    setFormErr('')
    try {
      const r = await onSubmit(out, (k) => set(k, ''))
      if (typeof r === 'string' && r) setFormErr(r)
      else onClose()
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="scrim" id="scrim">
      <div class={'modal' + (wide ? ' wide' : '')} role="dialog" aria-modal="true" tabIndex={-1} data-testid={testid}>
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
        <form
          ref={formRef}
          class="modal-body stack"
          noValidate
          onSubmit={(e) => {
            e.preventDefault()
            void go()
          }}
        >
          {intro ? <div class="small dim">{intro}</div> : null}
          {visible.map((f) => (
            <FieldRow key={f.k} f={f} v={vals[f.k] ?? ''} err={errs[f.k]} set={(v) => set(f.k, v)} />
          ))}
          <div class={'form-err callout danger small' + (formErr ? '' : ' hide')} role="alert">
            {formErr}
          </div>
          {note ? <div class="callout small">{note}</div> : null}
        </form>
        <div class="modal-foot">
          <button type="button" class="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button type="button" class="btn primary" disabled={busy} data-testid={testid ? testid + '-submit' : undefined} onClick={() => void go()}>
            {submitIcon ? <Icon name={submitIcon} /> : null}
            {submit}
          </button>
        </div>
      </div>
    </div>
  )
}

function FieldRow({ f, v, err, set }: { f: FormField; v: string; err?: string; set: (v: string) => void }): JSX.Element {
  const id = 'f-' + f.k.replace(/[^\w-]/g, '_')
  let input: JSX.Element
  switch (f.type) {
    case 'text':
      input = (
        <textarea
          class={'textarea' + (f.mono ? ' mono' : '')}
          id={id}
          data-k={f.k}
          rows={f.rows ?? 3}
          placeholder={f.ph ?? ''}
          value={v}
          autocomplete="off"
          spellcheck={false}
          onInput={(e) => set((e.target as HTMLTextAreaElement).value)}
        />
      )
      break
    case 'enum':
      input = f.readonly ? (
        <input class="input mono" id={id} data-k={f.k} value={f.options?.find((o) => o[0] === v)?.[1] ?? v} readOnly />
      ) : (
        <select
          class="select mono"
          id={id}
          data-k={f.k}
          value={v}
          onChange={(e) => {
            const x = (e.target as HTMLSelectElement).value
            if (x === '__new__' && f.create) {
              ;(e.target as HTMLSelectElement).value = v
              f.create.onCreate()
              return
            }
            set(x)
          }}
        >
          {f.create ? <option value="__new__">+ {f.create.label}…</option> : null}
          {!f.req ? <option value="">— none —</option> : null}
          {(f.options ?? []).map(([ov, ol]) => (
            <option value={ov} selected={ov === v}>
              {ol}
            </option>
          ))}
        </select>
      )
      break
    case 'seg':
      input = (
        <div class="seg" role="group" aria-label={labelOf(f)}>
          {(f.options ?? []).map(([ov, ol, dis]) => (
            <button
              type="button"
              class={ov === v ? 'on' : ''}
              aria-pressed={ov === v}
              aria-disabled={dis ? 'true' : undefined}
              title={dis || undefined}
              onClick={() => {
                if (!dis) set(ov)
              }}
            >
              {ol}
            </button>
          ))}
        </div>
      )
      break
    default:
      input = (
        <input
          class={'input' + (f.mono === false ? '' : ' mono')}
          id={id}
          data-k={f.k}
          value={v}
          placeholder={f.ph ?? ''}
          readOnly={f.readonly}
          autocomplete="off"
          onInput={(e) => set((e.target as HTMLInputElement).value)}
        />
      )
  }
  return (
    <div class={'field' + (err ? ' bad' : '')} data-field={f.k}>
      <label for={id}>
        {labelOf(f)}{' '}
        {f.req ? (
          <span class="req" aria-label="required">
            *
          </span>
        ) : (
          <span class="opt">optional</span>
        )}
      </label>
      {input}
      {f.help ? <div class="hint">{f.help}</div> : null}
      <div class="ferr" role="alert">
        {err ?? ''}
      </div>
    </div>
  )
}

// ConfirmModal is the prototype's UI.confirm: a title, a sentence, Cancel and
// the action (danger-styled for a destructive one, with Cancel focused).
export function ConfirmModal({
  title,
  body,
  ok = 'Confirm',
  cancel = 'Cancel',
  danger,
  onOk,
  onClose,
}: {
  title: ComponentChildren
  body: ComponentChildren
  ok?: string
  cancel?: string
  danger?: boolean
  onOk: () => void | Promise<void>
  onClose: () => void
}): JSX.Element {
  const cancelRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (danger) setTimeout(() => cancelRef.current?.focus(), 20)
  }, [])
  return (
    <div class="scrim" id="scrim">
      <div class="modal" role="dialog" aria-modal="true" tabIndex={-1}>
        <span class="sheet-grip only-phone" aria-hidden="true"></span>
        <div class="modal-head">
          <div>
            <h2>{title}</h2>
          </div>
          <button class="icon-btn" type="button" aria-label="Close" onClick={onClose}>
            <Icon name="x" />
          </button>
        </div>
        <div class="modal-body small dim">{body}</div>
        <div class="modal-foot">
          <button ref={cancelRef} class="btn ghost" type="button" onClick={onClose}>
            {cancel}
          </button>
          <button
            class={'btn ' + (danger ? 'danger' : 'primary')}
            type="button"
            data-testid="confirm-ok"
            onClick={async () => {
              await onOk()
              onClose()
            }}
          >
            {ok}
          </button>
        </div>
      </div>
    </div>
  )
}
