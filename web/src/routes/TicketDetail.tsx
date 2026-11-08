// A ticket's page (prototype js/screens/tickets.js ticketBody, ported line for
// line): where it stands in words, its facts, a draft's "Mark ready", the
// actions the status machine allows (Mark done, Reopen, Decline; Back to draft
// from ready), how it's routed and, folded, the ids for audit; the description
// (Edit), its comments (the chat thread and composer, files included), and the
// Details rail (status, who has it, priority, due, labels, each changeable),
// its linked approvals and workers, its spending limit (passkey to widen), its
// purchases and its history in words. Everything goes through the ticket API
// (PATCH /api/v1/tickets/{id}, POST …/comments).
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { botsApi, openTicketStream, who, type Ticket, type ProposalRow } from '../api/bots'
import { browser, money, type Purchase } from '../api/browser'
import { Icon } from '../components/Icon'
import { StatusMark } from '../components/StatusMark'
import { FactsLine } from '../components/Facts'
import { Menu } from '../components/Menu'
import { FormModal, ConfirmModal, type FormField } from '../components/FormModal'
import { ChatRows, Composer, peopleFor, Md, type Row } from '../components/ChatThread'
import { TicketBudget } from '../components/TicketSpend'
import { agoText, nameOf, toast, ToastHost } from '../components/ChatBits'
import {
  statusWord, statusMark, by, askedBy, assigneeWord, ticketFacts, teamOf, priWord, PRI, dueWord, whoOptions, histWord, isOpen,
  useMarkReady, loadNames, TicketGlyph, ALL_STATUSES, type Names,
} from '../components/TicketKit'
import { humanError } from '../api/errors'
import { EmptyState } from '../components/EmptyState'

export function TicketDetail({ num }: { num: string }): JSX.Element {
  const [t, setT] = useState<Ticket | null>(null)
  const [n, setN] = useState<Names>({ bots: {}, teams: {} })
  const [missing, setMissing] = useState<'' | 'gone' | 'nobots'>('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [more, setMore] = useState<HTMLElement | null>(null)
  const [form, setForm] = useState<'who' | 'priority' | 'due' | 'labels' | 'edit' | null>(null)
  const [declining, setDeclining] = useState(false)
  const [props, setProps] = useState<ProposalRow[]>([])
  const [buys, setBuys] = useState<Purchase[]>([])
  const [labelsInUse, setLabelsInUse] = useState<string[]>([])

  const load = () =>
    botsApi
      .ticket(num)
      .then((v) => {
        setT(v)
        setMissing('')
        setErr('')
      })
      .catch((e) => {
        const m = String(e?.message || e)
        if (/no bots on this/i.test(m)) setMissing('nobots')
        else if (/no ticket|not found|404|unknown ticket/i.test(m)) setMissing('gone')
        else setErr(humanError(e))
      })
  useEffect(() => {
    void load()
    loadNames(setN)
    botsApi.tickets({ status: ALL_STATUSES }).then((r) => setLabelsInUse([...new Set((r.tickets || []).flatMap((x) => x.labels || []))].sort())).catch(() => {})
    const stop = openTicketStream((x) => {
      if (!x || (x as { ticket?: string }).ticket === num || !(x as { ticket?: string }).ticket) void load()
    })
    return stop
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [num])
  const holder = t?.holder || (t?.assignee && who(t.assignee).kind === 'bot' ? who(t.assignee).name : '')
  useEffect(() => {
    if (!t || !holder) return
    botsApi.botProposals(holder).then((r) => setProps((r.proposals || []).filter((p) => p.case === t.id))).catch(() => setProps([]))
    browser.purchases().then((r) => setBuys((r.purchases || []).filter((p) => p.case === t.id && (!p.bot || p.bot === holder)))).catch(() => setBuys([]))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [t?.id, holder, t?.updated])
  const [markReady, readyModal] = useMarkReady(n, () => void load())

  if (missing) {
    return (
      <div class="tk-page">
        <nav class="dl-crumbs" aria-label="Where you are"><a href="#/tickets">Tickets</a></nav>
        {missing === 'nobots' ? (
          <EmptyState hero testid="ticket-empty" art={<TicketGlyph />} title="Tickets need a bot to work on them" action={{ label: 'Create a bot', href: '#/bots' }}>
            <p>A ticket is one piece of work you hand to a bot or a team. There are no bots on this conductor yet, so start with one.</p>
          </EmptyState>
        ) : (
          <EmptyState hero testid="ticket-empty" art={<TicketGlyph />} title="No ticket here" action={{ label: 'Tickets', href: '#/tickets' }}>
            <p>There’s no ticket {num}. It may have a different number; every ticket is on the Tickets page.</p>
          </EmptyState>
        )}
      </div>
    )
  }
  if (!t) {
    return (
      <div class="tk-page">
        <nav class="dl-crumbs" aria-label="Where you are"><a href="#/tickets">Tickets</a></nav>
        {err ? <div class="banner err" role="alert">{err}</div> : <p class="dl-sub">Loading {num}…</p>}
      </div>
    )
  }

  const open = isOpen(t)
  const draft = t.status === 'draft'
  const aw = t.assignee ? who(t.assignee) : null
  const botName = aw?.kind === 'bot' ? aw.name : ''
  const team = teamOf(t, n)
  const nm = botName ? nameOf(n.bots[botName]?.identity, botName) : aw?.kind === 'team' ? `whichever bot on team ${aw.name} takes it` : 'whoever takes it'
  const pendingProp = props.find((p) => p.status === 'pending')

  async function act(body: Record<string, unknown>, done?: [string, string]) {
    setBusy(true)
    try {
      await botsApi.patchTicket(num, body)
      await load()
      if (done) toast(done[0], done[1], 'ok')
    } catch (e) {
      toast('That didn’t go through', humanError(e), 'fail')
    } finally {
      setBusy(false)
    }
  }

  const routeLine = (() => {
    const r = (t as Ticket & { routing?: { team?: string; via?: string; lead?: string } }).routing
    if (r && r.team && botName) return `Routed through team ${r.team}${r.via ? ` (${r.via})` : ''} to ${nameOf(n.bots[botName]?.identity, botName)}.`
    if (botName) return 'Assigned to it directly.'
    if (aw?.kind === 'team') return `Assigned to team ${aw.name}: its lead or routing picks a bot.`
    return 'Not assigned yet: nobody works on it until it is.'
  })()

  const pick = (k: 'who' | 'priority' | 'due' | 'labels', label: string, val: string) => (
    <div class="tk-f">
      <span class="dl-lbl">{label}</span>
      <button class="tk-pick" type="button" data-testid={`tk-set-${k}`} aria-label={`${label}: ${val}. Change`} onClick={() => setForm(k)}>
        {val}
        <Icon name="chevd" />
      </button>
    </div>
  )

  const rows: Row[] = (t.comments || []).map((c, i) => {
    const w = who(c.by)
    if (w.kind === 'bot') return { key: 'c' + i, who: 'bot', bot: w.name, at: c.at, text: c.text, attachments: c.attachments }
    if (w.kind === 'operator') return { key: 'c' + i, who: 'op', at: c.at, text: c.text, attachments: c.attachments, opName: askedBy({ ...t, created_by: c.by }, n) === 'you' ? undefined : w.name }
    return { key: 'c' + i, who: 'sys', at: c.at, text: c.text }
  })

  const hist = [...(t.history || [])].sort((a, b) => Date.parse(b.at) - Date.parse(a.at))

  const fields: Record<string, { title: string; f: FormField; def: string }> = {
    who: { title: 'Assign the ticket', f: { k: 'who', type: 'enum', label: 'Assign to', options: whoOptions(n) }, def: t.assignee || '' },
    priority: { title: 'Priority', f: { k: 'priority', type: 'enum', label: 'Priority', options: PRI }, def: t.priority || 'normal' },
    due: { title: 'Due date', f: { k: 'due', type: 'string', label: 'Due date (YYYY-MM-DD, empty for none)', ph: '2026-10-25', validate: (x) => (!x || /^\d{4}-\d\d-\d\d$/.test(x) ? '' : 'A date like 2026-10-25') }, def: t.due ? t.due.slice(0, 10) : '' },
    labels: { title: 'Labels', f: { k: 'labels', type: 'string', label: 'Labels', ph: 'bug, web', help: labelsInUse.length ? `In use: ${labelsInUse.join(', ')}` : 'Separate them with commas' }, def: (t.labels || []).join(', ') },
  }

  return (
    <>
      <nav class="dl-crumbs" aria-label="Where you are">
        <a href="#/tickets">Tickets</a>
        <span class="sep" aria-hidden="true">›</span>
        <span class="cur" aria-current="page">{t.title}</span>
      </nav>

      <section class="dl-card case-head tk-head">
        <div class="tk-ht">
          <StatusMark s={statusMark(t.status)} lg mkOnly label={statusWord(t.status)} />
          <h1 class="case-title dl-h1-long">{t.title}</h1>
          <button class="icon-btn" type="button" aria-label="More for this ticket" data-testid="tk-more" onClick={(e) => setMore(e.currentTarget as HTMLElement)}>
            <Icon name="more" />
          </button>
        </div>
        <p class="dl-sub case-state" data-testid="ticket-status">
          {statusWord(t.status)}
          {t.urgent && open ? <> · <b class="danger-text">urgent</b></> : null}
          {' · '}
          {draft ? `${by(t, n)} ${agoText(t.created)}` : `${open ? 'opened' : 'closed'} ${agoText(t.created)} by ${askedBy(t, n)}`}
        </p>
        <FactsLine facts={ticketFacts(t, n)} />
        {draft ? (
          <div class="tk-ready">
            <p>
              <b>A draft.</b> {t.assignee ? `${assigneeWord(t, n)} can help shape it, but won’t start` : 'Nobody works on it'} until you mark it ready{t.assignee ? '' : ', and it needs someone to do it first'}.
            </p>
            <button class="btn primary" type="button" disabled={busy} data-testid="mark-ready" onClick={() => markReady(t)}>
              <Icon name="check" />Mark ready
            </button>
          </div>
        ) : null}
        <div class="dl-actions">
          {t.status === 'waiting' && pendingProp ? (
            <a class="btn primary" href={`#/bots/${pendingProp.bot}/proposals/${pendingProp.proposal}`}><Icon name="attention" />Review what it asks</a>
          ) : null}
          {draft ? null : (
            <button class="btn secondary" type="button" disabled={busy} data-testid={open ? 'mark-done' : 'reopen'} onClick={() => void act({ status: open ? 'done' : 'reopen' }, open ? ['Done', t.title] : ['Reopened', t.assignee ? `${assigneeWord(t, n)} may pick it up again` : 'back as a draft: it needs someone to do it'])}>
              <Icon name={open ? 'check' : 'undo'} />
              {open ? 'Mark done' : 'Reopen'}
            </button>
          )}
          {open ? (
            <button class="btn secondary" type="button" disabled={busy} data-testid="decline" onClick={() => setDeclining(true)}>
              <Icon name="x" />Decline
            </button>
          ) : null}
          {botName && !draft ? (
            <button class="btn secondary" type="button" aria-disabled="true" data-tip={!open ? 'Reopen the ticket to hand off part of it' : `${nameOf(n.bots[botName]?.identity, botName)} hands off parts of its own work: ask it in the comments`} title={!open ? 'Reopen the ticket to hand off part of it' : `${nameOf(n.bots[botName]?.identity, botName)} hands off parts of its own work: ask it in the comments`}>
              <Icon name="send" />Hand off part of it
            </button>
          ) : null}
        </div>
        <p class="dl-sub case-route">{routeLine}</p>
        <details class="dl-audit-d">
          <summary><Icon name="chev" />For audit: the ticket id and who asked, as recorded</summary>
          <dl class="dl-facts">
            <dt>Ticket</dt>
            <dd class="mono">{t.ticket}{t.id && t.id !== t.ticket ? ` · case ${t.id}` : ''}</dd>
            <dt>Asked by</dt>
            <dd><span class="mono">{t.requester || t.created_by || 'unknown'}</span>{t.requester_class ? ` (${t.requester_class})` : ''}; carried through every hop, so whoever works it runs with no more trust than that</dd>
          </dl>
        </details>
      </section>

      <div class="grid g-side tk-grid">
        <div class="stack">
          <section class="dl-card tk-desc">
            <header class="dl-ch"><h2>Description</h2><span class="dl-sp"></span><button class="btn sm ghost" type="button" data-testid="tk-edit" onClick={() => setForm('edit')}><Icon name="edit" />Edit</button></header>
            {t.description ? <p class="tk-body"><Md text={t.description} /></p> : <p class="dl-sub">No description.</p>}
          </section>
          <section class="panel chat tk-chat">
            <div class="panel-head"><h3>Comments</h3><span class="dl-sub">{botName ? `${nameOf(n.bots[botName]?.identity, botName)} reads them and answers here` : 'The bot that takes it reads them'}</span></div>
            <div class="cm-thread" id="msgs" role="log" aria-label="Comments" data-t={t.ticket} data-testid="chat-scroll">
              {rows.length ? <ChatRows rows={rows} bots={n.bots} attBot={() => (holder ? [holder] : [])} /> : <p class="cm-empty dl-sub">No comments yet.</p>}
            </div>
            <Composer
              ticket
              label="Comment"
              placeholder="Add a comment"
              people={peopleFor('bot', '', n.bots, n.teams)}
              bots={n.bots}
              canAttach={!!holder}
              noFilesWhy="Files go on a ticket a bot holds: assign it to a bot first"
              onSend={async (text, files) => {
                if (files.length) await botsApi.commentWithFiles(num, text, files)
                else await botsApi.comment(num, text)
                await load()
              }}
            />
            <div class="cm-drop" aria-hidden="true"><Icon name="attach" />Drop to attach to your comment</div>
          </section>
        </div>

        <div class="stack tk-side">
          <section class="panel">
            <div class="panel-head"><h3>Details</h3></div>
            <div class="tk-fields">
              <div class="tk-f"><span class="dl-lbl">Status</span><span class="tk-st"><StatusMark s={statusMark(t.status)} mkOnly /> {statusWord(t.status)}</span></div>
              {pick('who', 'Assigned to', assigneeWord(t, n))}
              {botName && team ? <div class="tk-f"><span class="dl-lbl">Team</span><a class="dl-link" href={`#/teams/${team}`}>{team}</a></div> : null}
              {pick('priority', 'Priority', priWord(t.priority))}
              {pick('due', 'Due', t.due ? `${t.due.slice(0, 10)} · ${dueWord(t.due)}` : 'No date')}
              {pick('labels', 'Labels', (t.labels || []).join(', ') || 'None')}
            </div>
          </section>
          {props.length || (t.workers || []).length ? (
            <section class="panel">
              <div class="panel-head"><h3>Linked work</h3></div>
              <ul class="cm-side-l">
                {props.map((p) => (
                  <li key={p.proposal}>
                    <a class="cm-side-a" href={`#/bots/${p.bot}/proposals/${p.proposal}`}>
                      <b><StatusMark s={p.status === 'pending' ? 'ember' : p.status === 'executed' || p.status === 'approved' ? 'ok' : 'idle'} mkOnly /> {p.summary || p.action || p.uses}</b>
                      <span class="dl-sub">{p.status === 'pending' ? 'Waiting for you' : p.status}</span>
                    </a>
                  </li>
                ))}
                {(t.workers || []).map((w) => (
                  <li key={w}>
                    <a class="cm-side-a" href={`#/bots/${holder}`}>
                      <b><StatusMark s="idle" mkOnly /> A worker</b>
                      <span class="dl-sub mono">{w}</span>
                    </a>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}
          <TicketBudget t={t} nm={nm} onChanged={() => void load()} />
          {buys.length ? (
            <section class="panel wl-tk">
              <div class="panel-head"><h3>Purchases</h3></div>
              <ul class="cm-side-l">
                {buys.map((p) => (
                  <li key={p.id}>
                    <a class="cm-side-a" href={`#/browser/purchases/${p.id}`}>
                      <b><StatusMark s={p.status === 'pending' ? 'ember' : p.status === 'completed' || p.status === 'used' ? 'ok' : p.status === 'denied' || p.status === 'refused' ? 'fail' : 'idle'} mkOnly /> {(p.items || []).map((x) => (x as { name?: string }).name).filter(Boolean).join(', ') || p.why || 'A purchase'}</b>
                      <span class="dl-sub">{p.status} · {money(p.total, p.currency)} · {p.merchant.replace(/^https?:\/\//, '')}</span>
                    </a>
                  </li>
                ))}
              </ul>
            </section>
          ) : null}
          <section class="panel">
            <div class="panel-head"><h3>History</h3></div>
            <ol class="tk-hist">
              {hist.map((h, i) => (
                <li key={i}><span>{histWord(h, n)}</span><span class="dl-sub">{agoText(h.at)}</span></li>
              ))}
            </ol>
          </section>
        </div>
      </div>

      {more ? (
        <Menu
          anchor={more}
          label="More for this ticket"
          onClose={() => setMore(null)}
          items={[
            { label: 'Assign…', icon: 'users', onClick: () => setForm('who') },
            { label: 'Edit…', icon: 'edit', onClick: () => setForm('edit') },
            ...(t.status === 'ready' ? [{ label: 'Back to draft', icon: 'undo', onClick: () => void act({ status: 'draft' }, ['Back to draft', 'work pauses until it’s ready again']) }] : []),
            '-',
            {
              label: 'Copy a link to it',
              icon: 'copy',
              onClick: () => {
                const url = location.origin + location.pathname + '#/tickets/' + t.ticket
                navigator.clipboard?.writeText(url).then(() => toast('Link copied', t.title, 'ok')).catch(() => toast('Couldn’t copy the link', url, 'fail'))
              },
            },
          ]}
        />
      ) : null}
      {form === 'edit' ? (
        <FormModal
          caps="ticket"
          title="Edit the ticket"
          values={{ title: t.title, desc: t.description || '' }}
          fields={[{ k: 'title', type: 'string', req: true }, { k: 'desc', type: 'text', label: 'Description', rows: 6 }]}
          submit="Save"
          testid="tk-edit-form"
          onSubmit={async (v) => {
            try {
              await botsApi.patchTicket(num, { title: v.title, description: v.desc || '' })
              await load()
            } catch (e) {
              return humanError(e)
            }
          }}
          onClose={() => setForm(null)}
        />
      ) : form ? (
        <FormModal
          caps="ticket"
          title={fields[form].title}
          values={{ [form]: fields[form].def }}
          fields={[fields[form].f]}
          submit="Save"
          testid={`tk-set-${form}-form`}
          onSubmit={async (v) => {
            const val = v[form] || ''
            const body: Record<string, unknown> =
              form === 'who' ? { assignee: val } : form === 'labels' ? { labels: val.split(',').map((x) => x.trim()).filter(Boolean) } : form === 'due' ? { due: val || 'none' } : { priority: val }
            try {
              await botsApi.patchTicket(num, body)
              await load()
            } catch (e) {
              return humanError(e)
            }
          }}
          onClose={() => setForm(null)}
        />
      ) : null}
      {declining ? (
        <ConfirmModal
          title="Decline this ticket?"
          body={<>“{t.title}” closes without being done. You can reopen it.</>}
          ok="Decline"
          danger
          onOk={() => act({ status: 'declined' }, ['Declined', t.title])}
          onClose={() => setDeclining(false)}
        />
      ) : null}
      {readyModal}
      <ToastHost />
    </>
  )
}
