// Fixture data standing in for the Rust commands when the app is opened with
// `?mock` (or `?mock=empty`) in the URL — no Tauri, no daemon. Used for local
// dev (`npm run dev`) and for the conformance screenshots (see the plan:
// "render the app's UI in a browser ... with a mocked invoke layer").
import type { ApprovalView, AttentionSnapshot, ChatMessage, Conversation } from './types'

export function isMock(): boolean {
  return new URLSearchParams(location.search).has('mock')
}

function mockVariant(): string {
  return new URLSearchParams(location.search).get('mock') || 'full'
}

const NOW = () => new Date().toISOString()
const AGO = (min: number) => new Date(Date.now() - min * 60000).toISOString()

const attentionFull: AttentionSnapshot = {
  cursor: 'c1',
  items: [
    {
      key: 'p1',
      kind: 'proposal',
      title: 'platform-sre wants to restart the api-gateway pods',
      link: '#/bots/platform-sre/proposals/p1',
      subject: 'bot:platform-sre',
      severity: 'urgent',
      opened: AGO(4),
    },
    {
      key: 'pu1',
      kind: 'purchase',
      title: 'platform-sre wants to buy a $40 Datadog seat',
      link: '#/browser/purchases/pu1',
      subject: 'bot:platform-sre',
      severity: 'warn',
      opened: AGO(20),
    },
    {
      key: 'r1',
      kind: 'run_failed',
      title: 'Nightly backup sweep failed',
      detail: 'exit status 1: pg_dump: connection to database failed',
      link: 'https://daemon.example/#/runs/r-9f21',
      severity: 'warn',
      opened: AGO(55),
      source: 'run:r-9f21',
    },
    {
      key: 'c1',
      kind: 'connection',
      title: "GitHub's token needs refreshing",
      severity: 'info',
      opened: AGO(120),
    },
  ],
}

const attentionEmpty: AttentionSnapshot = { items: [], cursor: 'c0' }

const approvals: Record<string, ApprovalView> = {
  p1: {
    bot: 'platform-sre',
    proposal: 'p1',
    action_sha256: 'sha256:4f2a9c1e8b7d6a5f3c2e1d0a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e3f2a1b0c',
    digests: { approve: 'sha256:approve-p1-digest', deny: 'sha256:deny-p1-digest' },
    descriptions: {
      approve: 'Restart the api-gateway pods in production.',
      deny: 'Tell platform-sre not to restart the api-gateway pods.',
    },
    summary: 'api-gateway is returning elevated 5xx; a rolling restart has cleared this before.',
    operators: ['daniel'],
    purpose: 'approve',
  },
  pu1: {
    proposal: 'pu1',
    action_sha256: 'sha256:9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e0f9a8b',
    digests: { approve: 'sha256:approve-pu1-digest', deny: 'sha256:deny-pu1-digest' },
    descriptions: {
      approve: 'Buy a Datadog seat for $40.00.',
      deny: "Don't buy the Datadog seat.",
    },
    summary: 'platform-sre needs a seat to finish wiring up the new on-call dashboard.',
    operators: ['daniel'],
    purpose: 'browser.purchase',
  },
}

const conversationsFull: Conversation[] = [
  { id: 'team:platform', kind: 'team', name: 'platform', unread: 2, pinned: true, position: 0, last: { seq: 41, at: AGO(6), from: 'bot:platform-sre', text: 'Restarted the pods, 5xx rate is back to normal.' } },
  { id: 'bot:platform-sre', kind: 'bot', name: 'platform-sre', unread: 0, pinned: false, last: { seq: 12, at: AGO(90), from: 'operator:daniel', text: 'Can you check the backup job logs?' } },
]

const messagesByConv: Record<string, ChatMessage[]> = {
  'team:platform': [
    { seq: 39, at: AGO(30), from: 'operator:daniel', text: 'api-gateway is throwing 5xx again' },
    { seq: 40, at: AGO(10), from: 'bot:platform-sre', text: "Looking into it — I'll propose a restart if it's the usual leak." },
    { seq: 41, at: AGO(6), from: 'bot:platform-sre', text: 'Restarted the pods, 5xx rate is back to normal.' },
  ],
  'bot:platform-sre': [
    { seq: 11, at: AGO(95), from: 'operator:daniel', text: 'Can you check the backup job logs?' },
    { seq: 12, at: AGO(90), from: 'bot:platform-sre', text: 'On it — the nightly sweep failed on a connection error, looking now.' },
  ],
}

const botsFull = {
  bots: [
    {
      bot: 'platform-sre',
      description: 'Keeps the platform healthy: restarts, rollbacks, on-call triage.',
      team: 'platform',
      shadow: false,
      disabled: false,
      home_runtime: 'live',
      identity: { display_name: 'platform-sre' },
      open_cases: 2,
      pending_proposals: 1,
      live_workers: 1,
      capabilities: { does: ['read logs', 'restart a deployment'], asks: ['restart in production'], shadow: false },
    },
  ],
  teams: [
    { team: 'platform', description: 'Keeps production running.', bots: ['platform-sre'], operators: ['daniel'], identity: { display_name: 'platform' } },
  ],
}

function err(code: string, message: string): never {
  const e = new Error(message) as Error & { code?: string }
  e.code = code
  throw e
}

export async function mockInvoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  const variant = mockVariant()
  const empty = variant === 'empty'
  await new Promise((r) => setTimeout(r, 30))
  switch (cmd) {
    case 'attention':
      return (empty ? attentionEmpty : attentionFull) as T
    case 'dismiss':
      return undefined as T
    case 'approval_view': {
      const id = String(args?.id ?? '')
      const view = approvals[id]
      if (!view) err('not_found', `no such approval: ${id}`)
      return view as T
    }
    case 'decide':
      return undefined as T
    case 'conversations':
      return (empty ? [] : conversationsFull) as T
    case 'messages': {
      const id = String(args?.id ?? '')
      return (messagesByConv[id] || []) as T
    }
    case 'send_message': {
      const id = String(args?.id ?? '')
      const text = String(args?.text ?? '')
      const msg: ChatMessage = { seq: (messagesByConv[id]?.length || 0) + 100, at: NOW(), from: 'operator:daniel', text }
      return msg as T
    }
    case 'mark_read':
      return undefined as T
    case 'bots':
      if (empty) err('bots_not_configured', 'no bots are configured')
      return botsFull as T
    case 'bot': {
      const name = String(args?.name ?? '')
      const b = botsFull.bots.find((x) => x.bot === name)
      if (!b) err('not_found', `no such bot: ${name}`)
      return b as T
    }
    case 'bot_proposals': {
      const name = String(args?.name ?? '')
      return {
        bot: name,
        proposals: [
          {
            bot: name,
            proposal: 'p1',
            kind: 'action',
            tier: 'medium',
            uses: 'kubectl.restart',
            status: 'pending',
            digest: 'sha256:approve-p1-digest',
            summary: 'Restart the api-gateway pods',
            requester: 'operator:daniel',
            created: AGO(4),
          },
        ],
      } as T
    }
    case 'open_in_browser':
      return undefined as T
    case 'get_settings':
      return { daemon_url: 'https://box.tailnet.ts.net:7788', operator: 'daniel', ssh_public_key_path: '~/.ssh/id_ed25519.pub' } as T
    case 'default_ssh_key':
      return '~/.ssh/id_ed25519.pub' as T
    case 'save_settings':
    case 'complete_setup':
      return undefined as T
    default:
      throw new Error(`mock: unhandled command ${cmd}`)
  }
}
