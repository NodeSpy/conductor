# paseo-conductor

Event-driven agent orchestration for your local [Paseo](https://paseo.sh) daemon.

paseo-conductor receives events from external services and runs Paseo coding agents (or
deterministic tools) on your machine in response. **GitHub is the first integration**; the core is
integration-agnostic so more can be added.

It is **webhook-primary** (GitHub App → [smee.io](https://smee.io) → conductor) and **single-user**:
no database, no web UI, no accounts — one Go binary run as a `systemd --user` service.

## What it does (GitHub)

On **your authored PRs**, it autonomously reacts to:

| Kind | Trigger | Action |
| --- | --- | --- |
| `merge_conflict` | PR becomes conflicting | agent: resolve + push |
| `pr_behind` | PR is behind base | `gh pr update-branch` |
| `failing_checks` | CI fails | flake-rerun once, then agent: fix + push |
| `changes_requested` | a review requests changes | agent: address + push |
| `new_comment` | a comment/bugbot review | agent: act + reply |
| `merge_ready` *(opt-in, M4)* | fully green + approved + threads resolved | `gh pr merge` |

Plus: **`review_requested`** (M2) runs [critique](https://github.com/EdnitionCode/critique) on PRs
where your review is requested; **`self_review`** (M2, opt-in) critiques your own PRs; and issue
kinds **`issue_assigned` / `issue_ready` / `issue_project_moved`** (M3) start work on a fresh branch.

## Scheduled jobs (cron integration)

A second built-in integration, `cron`, runs actions on a schedule — the same `command`/`agent`
actions the GitHub integration uses. Each schedule takes a `cron` spec (standard 5-field, or
`@daily`/`@every 6h`) or an `every: <duration>`, plus an `action`:

```yaml
integrations:
  - type: cron
    name: chores
    schedules:
      - name: rate-limit-check
        every: 6h
        action: { type: command, backend: local, command: ["gh", "api", "rate_limit"] }
```

Agent actions with no repo context run in the base workspace (`checkout: none`).

## Identity & rate limits

The rate-limit pain came from doing reads on your personal `gh` token. paseo-conductor separates
duties:

- **Reads/enrichment** (PR state, checks, Projects v2) use the **GitHub App installation token** —
  its own generous rate pool. The API is used freely (REST or GraphQL).
- **API writes/posts** (review replies, critique's submitted review) use **your `gh` token**, so
  they're authored **as you**, never a bot.
- **Commits & pushes** go over **SSH** with your git identity — no token, no API cost.

## Install (released binary, one-liner)

The repo is private, so the installer uses the authenticated `gh` CLI to fetch the release asset
for your OS/arch (mac amd64/arm64, linux amd64/arm64/386):

```sh
gh api repos/NodeSpy/paseo-conductor/contents/scripts/install-release.sh \
  -H "Accept: application/vnd.github.raw" | bash
```

Pin a version with `... | bash -s v0.2.0`. Installs to `~/.local/bin/paseo-conductor`.

### Updating

```sh
paseo-conductor update            # self-update to the latest release (uses gh)
paseo-conductor update --tag v0.2.0   # or pin a version; --force to reinstall
```

Or let it update itself — enable `update.auto` in config and the running daemon checks a few times a
day, installs any new release, and re-execs into it:

```yaml
update:
  auto: true
  interval: 8h        # default; check cadence
  apply: true         # re-exec into the new binary after updating
```

## Install from source

```sh
git clone https://github.com/NodeSpy/paseo-conductor.git
cd paseo-conductor
./scripts/install.sh          # builds the binary, installs the systemd --user unit, seeds config
```

Then:

```sh
paseo-conductor validate
systemctl --user daemon-reload
systemctl --user enable --now paseo-conductor
loginctl enable-linger "$USER"       # keep running across logout/reboot
journalctl --user -u paseo-conductor -f
```

Requires the local `paseo` CLI (authenticated to your daemon) and `gh` on PATH.

## GitHub App setup

1. Create a GitHub App (org or personal). **Webhook URL = your smee channel** (create one at
   https://smee.io). Set a **webhook secret**.
2. **Permissions:** Contents (RW), Pull requests (RW), Issues (RW), Checks (R), Metadata (R).
   For auto-merge add Administration/merge as needed; for the Projects trigger add Projects (R).
3. **Subscribe to events:** pull_request, pull_request_review, pull_request_review_comment,
   issue_comment, check_run, check_suite, workflow_run, push, issues. Add
   `pull_request_review_thread` for M4 auto-merge and `projects_v2_item` for the M3 project trigger.
4. **Generate a private key** and save it at the `private_key_path` in your config.
5. **Install the App** on the repos/orgs you want covered.
6. Put the App id, key path, and webhook secret in `config.yaml` / `conductor.env`.

### Transports: smee and/or direct HTTP

Set `webhook.smee_url`, `webhook.listen`, or both:

- **smee.io** — no inbound port; the daemon subscribes to an SSE channel. Easiest to start. Caveat:
  smee re-serializes the JSON body, so HMAC verification usually won't match — keep
  `verify_signature: false` (the channel URL is the shared secret).
- **Direct HTTP** — `listen: 127.0.0.1:8787` (optional `path: /webhook`) runs a plain webhook
  receiver. Point the GitHub App's webhook URL at it (typically via your own tunnel, e.g. pangolin).
  The raw body is intact here, so **set `verify_signature: true`**.

## Configuration

See [`config.example.yaml`](config.example.yaml). The shape:

- `integrations:` — a list of typed instances. Each github instance has an `app`, a `webhook`
  (smee), an optional `sweep`, optional shared `defaults`, and a **`rules`** list.
- **rules** — the first rule whose `match` (repo globs and/or Project v2 criteria) applies wins,
  merged over `defaults`. Each rule carries its own `reviewer`, `assignee`, and `actions`.
- **actions** — `(kind → action)`. `type: agent` runs a Paseo agent (references an `agents:`
  profile + a prompt); `type: command` runs a subprocess (default backend `local`).
- `agents:` — reusable named profiles (`provider`, `model`, `workspace`, `archive_when_done`, …).
- `control:` — kill switch (`enabled`), pause label, global `shadow`.
- `notify:` — Paseo push + escalation comment.
- `store:` — dedup state + audit log, with TTL/LRU/rotation bounds.

## Commands

```
paseo-conductor run                    # start the daemon (the systemd unit runs this)
paseo-conductor validate               # load & validate config, then exit
paseo-conductor replay <event.json>    # run a saved webhook through the pipeline (dry-run)
paseo-conductor sweep                  # one catch-up sweep (dry-run print)
paseo-conductor version
```

`replay` fixtures are `{"event": "<x-github-event>", "body": { ...payload... }}` — see
[`testdata/`](testdata/).

## Safety

- **Kill switch**: `control.enabled: false`, or `paseo-conductor` shadow mode
  (`control.shadow: true`) runs everything but skips the final push/merge/post.
- **Loop-safety**: per-`(pr,kind,head)` attempt caps; on the cap it **escalates** (notifies you)
  instead of looping. A running-agent guard avoids double-dispatch.
- **Auto-merge is opt-in** (`merge_ready`, disabled by default, label-gated).

## License

Private (NodeSpy).
