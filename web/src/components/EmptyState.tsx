// A screen's first-visit state (Daylight's UI.empty: a plain sentence saying
// what will be here, one primary action, nothing bare or broken). `hero` is
// the bigger welcome for a screen that is the starting point for its kind of
// thing (Bots); `small` is the quiet in-card version (Needs you, a list's own
// empty row). Structure and classes are the prototype's (css/components.css,
// css/app.css `.empty`); `art` is a departure with no approved equivalent yet
// (a decorative glyph above the title) kept for screens that already show one;
// `icon` is the plain form of it: one line icon from the shared set, for a
// hero whose screen has no glyph of its own.
import type { ComponentChildren, JSX } from 'preact'
import { Icon } from './Icon'

export interface EmptyAction {
  label: string
  href?: string
  onClick?: () => void
  testid?: string
  disabled?: boolean
}

export function EmptyState({
  title,
  children,
  art,
  icon,
  action,
  secondary,
  hero,
  small,
  testid,
}: {
  title: string
  children?: ComponentChildren
  art?: ComponentChildren
  icon?: string
  action?: EmptyAction
  secondary?: EmptyAction
  hero?: boolean
  small?: boolean
  testid?: string
}): JSX.Element {
  return (
    <div class={'empty' + (small ? ' small' : '') + (hero ? ' hero' : '')} data-testid={testid} role="status">
      {art || icon ? (
        <div class="empty-art" aria-hidden="true">
          {art || <Icon name={icon!} class="empty-ic" />}
        </div>
      ) : null}
      <h2 class="serif">{title}</h2>
      {children}
      {action || secondary ? (
        <div class="row">
          {action ? <ActionButton a={action} primary lg={hero} /> : null}
          {secondary ? <ActionButton a={secondary} lg={hero} /> : null}
        </div>
      ) : null}
    </div>
  )
}

function ActionButton({ a, primary, lg }: { a: EmptyAction; primary?: boolean; lg?: boolean }): JSX.Element {
  const cls = (primary ? 'btn primary' : 'btn ghost') + (lg ? ' lg' : '')
  return a.href ? (
    <a class={cls} href={a.href} data-testid={a.testid}>
      {a.label}
    </a>
  ) : (
    <button class={cls} type="button" data-testid={a.testid} disabled={a.disabled} onClick={a.onClick}>
      {a.label}
    </button>
  )
}
