// A screen's first-visit state: what this place is for and the one thing to do
// next, in place of a bare "No X." line or a table with nothing in it. `hero`
// is the big welcome for a screen that is the starting point for its kind of
// thing (Bots); the default is the quieter panel-sized version.
import type { ComponentChildren, JSX } from 'preact'

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
  action,
  secondary,
  hero,
  testid,
}: {
  title: string
  children?: ComponentChildren
  art?: ComponentChildren
  action?: EmptyAction
  secondary?: EmptyAction
  hero?: boolean
  testid?: string
}): JSX.Element {
  return (
    <section class={'empty-state' + (hero ? ' hero' : '')} data-testid={testid} aria-label={title}>
      {art ? <div class="empty-art" aria-hidden="true">{art}</div> : null}
      <h2 class="empty-title serif">{title}</h2>
      {children ? <div class="empty-body">{children}</div> : null}
      {action || secondary ? (
        <div class="empty-actions">
          {action ? <ActionButton a={action} primary hero={hero} /> : null}
          {secondary ? <ActionButton a={secondary} /> : null}
        </div>
      ) : null}
    </section>
  )
}

function ActionButton({ a, primary, hero }: { a: EmptyAction; primary?: boolean; hero?: boolean }): JSX.Element {
  const cls = primary ? 'btn primary' + (hero ? ' empty-cta' : '') : 'btn ghost'
  return a.href ? (
    <a class={cls} href={a.href} data-testid={a.testid}>{a.label}</a>
  ) : (
    <button class={cls} type="button" data-testid={a.testid} disabled={a.disabled} onClick={a.onClick}>{a.label}</button>
  )
}
