# Event journal, attention and the desktop client

## Why

Clients learn what conductor is doing from three separate live streams:

- the dashboard hub (`GET /api/v1/events`): run events with ids, resumable from
  a 10k in-memory ring;
- the chat hub (`GET /v1/conversations/stream`): no ids, no resume;
- the ticket hub (`GET /v1/tickets/stream`): no ids, no resume.

None of them survive a restart, and all three drop events for a slow reader
without telling it. The dashboard hub restarts its ids at 1 on every boot, so a
client resuming with `Last-Event-ID: 5000` sees neither a gap nor a reset. A
client that fetches a snapshot and then opens a stream can miss whatever
happened in between.

That is fine for a page someone is looking at. It is not fine for a client
whose job is to tell an operator, reliably, that something needs them.

## The journal

One append-only, durable log of events, owned by core.

```
Event { seq, topic, type, subject?, ts, data }
Cursor "<epoch>-<seq>"
```

- **Durable.** Events are appended to JSONL segment files under the state
  directory (`<state>/journal/`). `seq` is monotonic for the life of the
  journal. `epoch` is a random id stored with the journal; it changes only
  when the journal is recreated (deleted, corrupted beyond recovery).
- **Synchronous appends.** A producer appends when its state change commits,
  not via a lossy fan-out afterwards.
- **Replay from disk.** A subscriber gives a cursor. If the epoch matches and
  the cursor is still within retention, everything after it is replayed from
  the segments, then the subscriber follows live. Otherwise the journal sends
  one `reset` event carrying the current cursor and the client refetches its
  snapshots.
- **No silent drops.** Each live subscriber has a bounded buffer. A subscriber
  that falls behind is disconnected, not skipped; it reconnects with its cursor
  and replays. Delivery is at-least-once; clients ignore a `seq` they have
  already seen.
- **Retention.** Segments rotate by size and are deleted by age and total size.
  Nothing a client needs to be correct depends on retention: anything that must
  be remembered beyond it is state with its own snapshot (attention, below).
- **Generic.** Topics are strings. Core does not know what a chat or a ticket
  is; producers choose the topic and the payload.

Topics in use: `run` (run and step lifecycle), `chat` (a recorded message),
`ticket` (a ticket changed), `attention` (an item opened or resolved).

### HTTP

- `GET /api/v1/events?topics=a,b` — SSE. Resume with `Last-Event-ID` (or
  `?cursor=`). Each event carries `id: <cursor>`, `event: <topic>`, and the
  envelope as JSON `data:`. A `: heartbeat` comment every 15s.
- `GET /api/v1/events/since?cursor=&topics=&limit=` — the same events as a
  JSON page: `{events, cursor, reset}`. For clients that wake up, catch up and
  go back to sleep.

The old `/v1/conversations/stream` and `/v1/tickets/stream` stay for the
operator socket's existing clients.

## Attention

"What needs the operator" is state, not a stream, so it is a view kept next
to the journal.

```
Item { key, kind, title, detail?, link?, subject?, opened, severity }
```

Items come from two kinds of producer:

- **Sources** report what is open right now (pending proposals, purchase
  approvals waiting on the operator, tickets marked ready). The attention
  service reconciles every source on a short interval and whenever a producer
  nudges it, diffs against the open set, and appends `opened`/`resolved`
  events. The source is the truth, so a restart or a missed nudge can't leave
  an item stuck open or lost.
- **Raised items** exist only as an event (a run failed, a run escalated, a
  workflow handed work to a live agent). They are kept in the attention
  store until something resolves them (the same run later succeeds) or the
  operator dismisses them.

The open set is persisted, so the view survives a restart.

- `GET /api/v1/attention` — `{items, cursor}`. `cursor` is the journal position
  the items reflect: follow `/api/v1/events?topics=attention` from exactly
  there and no transition is missed.
- `POST /api/v1/attention/{key}/dismiss` — resolve a raised item.

Every attention transition is an event on the `attention` topic:
`{type: "opened" | "resolved", data: Item}`.

## The desktop client

A Tauri application in `desktop/`. It is a client of the dashboard API, nothing
more; core has no knowledge of it.

- **Sign-in.** The native side signs in with the existing SSH challenge
  (`/api/v1/auth/ssh/begin` → sign with the local SSH agent → `finish`) and
  holds the resulting session. No new credential type.
- **Subscription.** The native side holds its own `/api/v1/events` stream for
  `attention` and `chat`, keeps its cursor on disk, and on start or wake
  catches up through the snapshot and the journal before notifying (coalesced:
  "3 things need you").
- **Window.** The main window loads the daemon's own dashboard URL, so the page
  keeps its same-origin session, CSRF and step-up checks unchanged.
- **Approvals.** The page offers "Sign with SSH agent" for sign-in and step-up
  when it runs inside the app. The one native command it may call signs only in
  the dashboard's login and step-up namespaces, and shows a native confirmation
  naming what is being signed.
- **Daemon location.** The daemon URL is configured on first run: localhost for
  a daemon on the same machine, the tailnet name for a remote one.
