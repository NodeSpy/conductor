// The review + apply panel shared by every edit surface. It shows the one
// pipeline review: the diff (what changes, line by line), the effects (each
// change in words), the widening list surfaced loudly (the least-privilege
// guardrails a change loosens — strong defaults, opt-in footguns), and a
// two-step apply (Review → Apply, where Apply carries the step-up confirm token
// bound to the exact candidate just reviewed).
import type { JSX } from 'preact'
import { useState } from 'preact/hooks'
import { review, applyResult, cfgError, busy, apply, doReview, runValidate, validateMsg } from '../configStore'

export function ReviewPanel({ stagedCount }: { stagedCount: number }): JSX.Element | null {
  const rev = review.value
  const applied = applyResult.value
  const [confirming, setConfirming] = useState(false)

  if (applied) {
    return (
      <section class="panel" aria-label="Applied">
        <div class="banner ok" role="status">
          Applied (config {applied.sha}). The daemon is reloading its config.
          {applied.backup_id ? <span class="mono"> Backup {applied.backup_id} saved — restore it from History.</span> : null}
          {applied.reload_error ? <span> (reload signal failed: {applied.reload_error})</span> : null}
        </div>
      </section>
    )
  }

  if (stagedCount === 0) return null

  return (
    <section class="panel review" aria-label="Review changes">
      <div class="panel-head">
        <span class="panel-title">Review {stagedCount} change{stagedCount === 1 ? '' : 's'}</span>
        <div class="row-actions">
          <button class="btn" type="button" disabled={busy.value} onClick={() => void doReview()}>
            Review changes
          </button>
        </div>
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

          {rev.guarded && rev.guarded.length ? (
            <div class="banner attn guarded" role="note" data-testid="review-guarded">
              <strong>This changes who can sign in, or how:</strong>
              <ul class="widen-list">
                {rev.guarded.map((g) => (
                  <li>
                    {g.text} <span class="mono id">{g.path}</span>
                  </li>
                ))}
              </ul>
              Applying it asks you to confirm it's you.
            </div>
          ) : null}

          <ul class="effects">
            {(rev.effects ?? []).map((e) => (
              <li class={e.widens ? 'widens' : ''}>
                <span class="eff-text">{e.text}</span>
                <span class="mono id">{e.path}</span>
              </li>
            ))}
          </ul>

          <DiffView />

          <div class="validate-row">
            <button class="btn ghost" type="button" onClick={() => runValidate()}>
              Validate on a copy
            </button>
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

          {!rev.changed ? (
            <p class="muted">No change — the staged edits match the current config.</p>
          ) : !confirming ? (
            <button class="btn primary" type="button" onClick={() => setConfirming(true)}>
              Apply…
            </button>
          ) : (
            <div class="confirm" role="group" aria-label="Confirm apply">
              <span>Apply to the live config and reload the daemon?</span>
              <button
                class="btn primary"
                type="button"
                disabled={busy.value}
                onClick={() => {
                  setConfirming(false)
                  void apply()
                }}
              >
                Confirm apply
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
