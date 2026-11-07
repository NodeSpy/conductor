// The review + apply panel shared by every edit surface. It shows the one
// pipeline review: the diff (what changes, line by line), the effects (each
// change in words), the widening list surfaced loudly (the least-privilege
// guardrails a change loosens — strong defaults, opt-in footguns), and a
// two-step apply (Review → Apply, where Apply carries the step-up confirm token
// bound to the exact candidate just reviewed).
import type { JSX } from 'preact'
import { useState } from 'preact/hooks'
import { review, applyResult, cfgError, busy, apply, doReview, runValidate, validateMsg, restoreId, cancelRestore } from '../configStore'
import { authState } from '../api/auth'

// guided is for a flow that owns its own one apply button (the first bot): the
// panel then only shows what changes and what needs confirming, with no
// review, validate or apply buttons of its own and no list of effects (the
// flow says them in its own words).
export function ReviewPanel({ stagedCount, guided }: { stagedCount: number; guided?: boolean }): JSX.Element | null {
  const rev = review.value
  const applied = applyResult.value
  const restoring = !!restoreId.value
  const [confirming, setConfirming] = useState(false)

  if (applied) {
    return (
      <section class="panel" aria-label="Applied">
        <div class="banner ok" role="status">
          <p class="applied-head">
            Applied. conductor is reloading with your change; it takes a few seconds.
            {applied.backup_id ? ' The config it replaced was backed up first.' : ''}
          </p>
          {applied.reload_error ? (
            <p class="applied-warn">It couldn't be told to reload ({applied.reload_error}), so the change takes effect when conductor next restarts.</p>
          ) : null}
          <details class="applied-details">
            <summary>Details</summary>
            <dl>
              <dt>New config</dt>
              <dd class="mono">{applied.sha}</dd>
              {applied.backup_id ? (
                <>
                  <dt>Backup</dt>
                  <dd class="mono">{applied.backup_id}</dd>
                </>
              ) : null}
            </dl>
          </details>
        </div>
      </section>
    )
  }

  if (stagedCount === 0 && !restoring) return null
  if (guided && !rev && !cfgError.value) return null

  return (
    <section class="panel review" aria-label={restoring ? 'Review restore' : 'Review changes'}>
      <div class="panel-head">
        <span class="panel-title">
          {restoring ? 'Restore to before this backup' : guided ? 'What changes in the config' : `Review ${stagedCount} change${stagedCount === 1 ? '' : 's'}`}
        </span>
        {guided || restoring ? null : (
          <div class="row-actions">
            <button class="btn" type="button" disabled={busy.value} onClick={() => void doReview()}>
              {rev ? 'Review again' : 'Review changes'}
            </button>
          </div>
        )}
        {restoring ? (
          <button class="btn ghost sm" type="button" onClick={() => cancelRestore()}>
            Cancel
          </button>
        ) : null}
      </div>

      {cfgError.value ? <div class="banner err" role="alert">{cfgError.value}</div> : null}

      {rev ? (
        <>
          {rev.widening && rev.widening.length ? (
            <div class="banner attn widening" role="alert">
              <strong>This loosens a guardrail:</strong>
              <ul class="widen-list">
                {rev.widening.map((w) => (
                  <li>
                    {w.text} <span class="mono id">{w.path}</span>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}

          {/* Only a signed-in session is asked to confirm (an open dashboard
              has nobody to confirm as), so only then say so. */}
          {rev.guarded && rev.guarded.length && authState.value.signed_in ? (
            <div class="banner attn guarded" role="note" data-testid="review-guarded">
              {rev.guarded.length === 1 ? (
                <>You'll confirm it's you before this applies, because it {rev.guarded[0].text}.</>
              ) : (
                <>
                  You'll confirm it's you before this applies, because it:
                  <ul class="widen-list">
                    {rev.guarded.map((g) => (
                      <li>{g.text}</li>
                    ))}
                  </ul>
                </>
              )}
            </div>
          ) : null}

          {guided ? null : (
            <ul class="effects">
              {(rev.effects ?? []).map((e) => (
                <li class={e.widens ? 'widens' : ''}>
                  <span class="eff-text">{e.text}</span>
                  {e.text.includes(e.path) ? null : <span class="mono id">{e.path}</span>}
                </li>
              ))}
            </ul>
          )}

          <DiffView />

          {guided ? null : (
          <div class="validate-row">
            <button class="btn ghost" type="button" onClick={() => runValidate()} title="Checks the changed config on a copy, without touching the live one">
              Check it first
            </button>
            {validateMsg.value ? null : <span class="muted small">tries the new config on a copy; nothing changes yet</span>}
            {validateMsg.value ? (
              <span class={'validate-msg ' + validateMsg.value.type} data-vstate={validateMsg.value.type}>
                {validateMsg.value.type === 'start'
                  ? 'validating…'
                  : validateMsg.value.type === 'ok'
                    ? '✓ ' + (validateMsg.value.message ?? 'valid')
                    : '✗ ' + (validateMsg.value.message ?? 'invalid')}
              </span>
            ) : null}
          </div>
          )}

          {guided ? null : !rev.changed ? (
            <p class="muted">{restoring ? 'No change — the live config already matches this backup.' : 'No change — the staged edits match the current config.'}</p>
          ) : !confirming ? (
            <button class="btn primary" type="button" onClick={() => setConfirming(true)}>
              {restoring ? 'Restore…' : 'Apply…'}
            </button>
          ) : (
            <div class="confirm" role="group" aria-label={restoring ? 'Confirm restore' : 'Confirm apply'}>
              <span>{restoring ? 'Restore the config to this backup and reload the daemon?' : 'Apply to the live config and reload the daemon?'}</span>
              <button
                class="btn primary"
                type="button"
                disabled={busy.value}
                onClick={() => {
                  setConfirming(false)
                  void apply()
                }}
              >
                {restoring ? 'Confirm restore' : 'Confirm apply'}
              </button>
              <button class="btn ghost" type="button" onClick={() => setConfirming(false)}>
                Cancel
              </button>
            </div>
          )}
        </>
      ) : (
        <p class="muted">Review to see the diff, the effects, and anything that loosens a guardrail before you apply.</p>
      )}
    </section>
  )
}

function DiffView(): JSX.Element {
  const rev = review.value
  if (!rev || !rev.lines || rev.lines.length === 0) {
    return <pre class="diff empty">No textual change.</pre>
  }
  return (
    <pre class="diff" aria-label="Unified diff">
      {rev.lines.map((l) => (
        <div class={'dl ' + l.op}>
          <span class="gutter">{l.op === 'add' ? '+' : l.op === 'del' ? '-' : ' '}</span>
          <span class="dtext">{l.text}</span>
        </div>
      ))}
    </pre>
  )
}
