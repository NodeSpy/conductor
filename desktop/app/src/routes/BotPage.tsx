// A bot's page: its facts and its proposals, approved inline. Ported from
// bots.js's shell + proposals tab, trimmed to what the daemon's read API
// gives this app (no settings/access editing here — out of scope).
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api, ApiError } from '../api/client'
import type { BotDetail, ProposalRow } from '../api/types'
import { displayName } from '../api/types'
import { seg } from '../router'
import { Icon, Mark } from '../components/icons'
import { Approval } from '../components/Approval'

export function BotPage(): JSX.Element {
  const name = seg(1)
  const [bot, setBot] = useState<BotDetail | null>(null)
  const [proposals, setProposals] = useState<ProposalRow[]>([])
  const [err, setErr] = useState('')
  const [openId, setOpenId] = useState<string | null>(null)

  const load = () => {
    setErr('')
    Promise.all([api.bot(name), api.botProposals(name)])
      .then(([b, p]) => {
        setBot(b)
        setProposals(p.proposals)
      })
      .catch((e) => setErr(e instanceof ApiError ? e.message : String(e)))
  }

  useEffect(load, [name])

  if (err) return <div class="page dl-page"><p class="gate-err" role="alert">{err}</p></div>
  if (!bot) return <div class="page dl-page"><p class="dl-sub">Loading…</p></div>

  const pending = proposals.filter((p) => p.status === 'pending')
  const past = proposals.filter((p) => p.status !== 'pending')

  return (
    <div class="page dl-page">
      <div class="dl-phead">
        <div>
          <h1>{displayName(bot.identity, bot.bot)}</h1>
          <p class="lede-2">{bot.description}</p>
        </div>
        <div class="dl-actions tight">
          <a class="btn secondary" href={`#/chat/bot/${encodeURIComponent(bot.bot)}`}>
            <Icon name="message" />
            Message
          </a>
        </div>
      </div>

      <section class="dl-card">
        <header class="dl-ch">
          <h2>Waiting for you</h2>
          {pending.length ? (
            <span class="count hot" aria-label={`${pending.length}`}>
              {pending.length}
            </span>
          ) : null}
        </header>
        {pending.length === 0 ? (
          <p class="dl-sub">Nothing waits for you. When {bot.bot} wants to do something it has to ask about, it shows up here.</p>
        ) : openId ? (
          <div class="prop-single">
            <Approval
              kind="proposal"
              bot={bot.bot}
              id={openId}
              onDecided={() => {
                setOpenId(null)
                load()
              }}
            />
          </div>
        ) : (
          <ul class="dl-list">
            {pending.map((p) => (
              <li class="dl-need proposal-row" key={p.proposal}>
                <a class="dl-need-a" href="#" onClick={(e) => { e.preventDefault(); setOpenId(p.proposal) }}>
                  <span class="dl-ng">
                    <Mark s="ember" lg label />
                  </span>
                  <span class="dl-nt">
                    <b>{p.summary || p.action || p.proposal}</b>
                  </span>
                  <span class="dl-na">
                    <span class="btn sm secondary">Review</span>
                  </span>
                </a>
              </li>
            ))}
          </ul>
        )}
      </section>

      {past.length ? (
        <details class="dl-card dl-files-d">
          <summary class="panel-head">
            <h3>Past decisions</h3>
            <span class="muted small">{past.length}</span>
          </summary>
          <ul class="dl-list">
            {past.map((p) => (
              <li class="dl-need proposal-row" key={p.proposal}>
                <span class="dl-ng">
                  <Mark s={p.status === 'approved' || p.status === 'executed' ? 'ok' : 'fail'} lg />
                </span>
                <span class="dl-nt">
                  <b>{p.summary || p.action || p.proposal}</b>
                  <span class="dl-why">{p.status}</span>
                </span>
              </li>
            ))}
          </ul>
        </details>
      ) : null}
    </div>
  )
}
