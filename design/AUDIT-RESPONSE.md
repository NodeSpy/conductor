# UX audit response

## Round 2 (the four adversarial reports)

Source: `round2-fixes.md` (QA `bugs/REPORT.md`, brand `brand/REPORT.md`, `newcomer/REPORT-r2.md`, `power/REPORT-r2.md`).
Every item marked **fixed** here has a check in `design/dashboard-tests/round2.spec.js` (or the spec named), run on
Chromium desktop and phone and the WebKit iPhone sweep; nothing is claimed from reading the code. Earlier claims the
reviewers proved false are corrected in the round-1 tables below (marked *corrected in round 2*).

**Decided otherwise, so not done:** the Overview greeting and summary line (bugs B8, brand B1, power N18) stay removed,
on review ("useless info"); a summary sentence on every page (brand M1) is replaced by the rule that a header sentence
must say something the page doesn't (`home.spec.js`); approval tabs (brand m4) are gone after a later review: the approval is one column in decision order with the
plan and the audit data folded (`approvals.spec.js`, `toggles.spec.js`).

### Blockers

| ID | Finding | Status | What changed |
|---|---|---|---|
| power N2, N3 | The review screen lied: a one-field form edit rewrote other nodes; the comment count was a constant | fixed | Forms apply only the fields you changed (the team form diffs what it opened with against what you submit); the node check names the paths the diff touches and counts comments kept, added, removed and moved. The `conductor validate` disclosure prints the CLI's lines without the dashboard's annotations (still simulated in the prototype). |
| power N4, N14, N15; newcomer N1, N3 | Security statements contradicted the grants | fixed | One function (`UI.verbMode`) says whether a bot runs a verb on its own, asks, or never; the three groups, the table's column ("never: refused") and Safety's count all use it. A connection says which bots reach it, from their grants. The approval names the bot's own approvers. The pack wizard says only an armed trigger widens, matching the review. |
| power N1 | The enforced egress allowlist was refused | fixed | `deny: true` + `egress` under namespace or container validates (Isolation.md "enforced"); egress without `deny`, or under `user`, is flagged as advisory only. |
| bugs B5, M4, M12; power N7, N8 | Counts and times disagreed | fixed | Home's figures, its strip and "failed at", Activity, folder roll-ups and workflow figures all come from the run records; a failure is timed at its finish everywhere; the tile's parts add up. Needs you is derived from what needs a person (approvals, failed and waiting runs, a waiting browser or login sign-in), so anything marked "Needs you" elsewhere is in it and its count. Times still regenerate per page load in the demo data (they're relative to now), so two loads minutes apart differ by those minutes. |
| power N5, newcomer N5, bugs M7, brand M13 | Signing dialog focus | fixed | Every dialog opens on itself (Enter does nothing until you move to a button), traps Tab, and returns focus to its opener however it closes. Menus take ↑/↓/Home/End/Esc. A skip link reaches the page (on an approval, its decision). |
| bugs B1, B2, B3; power N6 | Connector picker regression | fixed | Add a connector is the catalog's searchable picker (built-in / official plugin / custom source, from-git last); the instance name is prefilled (`-2` when taken) and a vault entry named for the service is chosen; selects carry one empty choice. |
| bugs B6 | Double-click staged a nameless bot | fixed | A double-click is one action everywhere; New bot refuses an invalid state. |
| bugs B7, M5, M6; brand B3, M6; newcomer N4, N7 | Phone | fixed | The phone header follows every route; an approval on a phone has no bot shell, title and why above a one-row bar; the entrance animation moves content but never hides it. |
| bugs B4, brand B2 | The tier ladder still shipped | fixed (the earlier claim was false) | Removed from Safety and team ceilings; a tier name is plain text, never a coloured or struck-through capsule. |

### Majors

| ID | Finding | Status | What changed |
|---|---|---|---|
| bugs M8–M10, brand M2–M4, newcomer N13/N14 | Status discipline | fixed, with two notes | Marks only on states, each with its word; healthy rows carry no mark; Working/pending/stopped/idle words replaced by the vocabulary; case and release states are plain words; "N to do" is text, "N failed" has the failed mark; no capsules (a label is plain text); the team page takes the bot page's header; installed catalog cards have no edge. *Notes:* Passed and Parked already differ in hue and shape (sage dot, slate bars, measured); only their words share ink, as BRAND.md specifies. The approved mock has no leading edge on the Overview's needs list, so none was added there (brand M3). The approved mock tints the "Replaces" tile, so it stays. |
| brand B4 | Generated titles in the serif | fixed | Run, proposal and case titles use the sans h3 style at the h2 size; the type spec allows the serif only on five words or fewer. |
| newcomer N2 | Step picker risk words missing | fixed | "reads only / changes things / high impact" on every action, reads first in each group; no duplicate built-ins; descriptions wrap in the sans. |
| bugs M1–M3, newcomer N6/N10, power N17/N19/N20/N23 | Run page | fixed | The primary action on a failed check redoes the step that made the change; each running run streams its own workflow's log; the time column never wraps; the story is a sentence; a parked run doesn't stutter; a running run is "signed when it finishes"; ↑/↓ move between steps. |
| newcomer N8, N11, N12, N17; bugs m10 | Validation | fixed | Slack channels, name and folder lengths, `..` in folders, Unicode letters allowed; a bare `*` repo scope refused; watching nothing asks first; a deny reason is capped at 500 characters and long text wraps. |
| power N11, N12, N13, N16 | Invented semantics | fixed | A sole candidate is bound automatically and nothing is written for it; Env describes the scrubbed sandbox env and marks workflow-wide env and precedence **NEW**; Pause promises only the documented switch; a workflow's folder is a comment its trigger still loads with. |
| brand M5, M7, M8 | Tokens | fixed, with one note | Disabled buttons use the disabled tokens at full opacity; repeated row actions are secondary; compact text follows the density token; dates are one 24-hour format. *Note:* nav weight 500 and the 12px group label match the approved mock (`mock.css` `.rail nav a`, `.rail .grp`); the book and the mock disagree, so it's left for the brand designer. |
| brand M11, bugs m1–m8 | Voice | fixed | No codenames; page titles match the nav; "1 actions", "creds:", "imports:" and the doubled "reachable" fixed; conductor lowercase; field labels in sentence case; plain dialog kickers; ⌘K's empty state is one paragraph and shows status words. |
| bugs M13, newcomer N15 | Get started and Display preferences | fixed | Hide has Undo and a "Show Get started" item in your menu; Display preferences opens Config at that card; its row height is stated correctly (36px). |
| bugs M15 | Downloads remained | fixed | No run-record or log downloads; Env export gone. |
| brand M12 | Sign-in | fixed | No decorative passkey ring; no app shell while signed out; the kit's horizontal logo file. |

### After round 2: hierarchy (review: "using the tabs on approvals breaks the page")

- **Approval**: no tabs; what will change → why → effect and undo → who asked, always in view; the full plan opens in
  place; ids, hashes and the signed digest fold into one quiet audit line; the approval stands alone at every width.
- **The same lens elsewhere**: a case leads with its state and its actions (routing one line, audit facts folded,
  reassign in its menu); workflows show folder and owner as a quiet line under the name; connections lead with which
  bots reach them, not event counts; config's files and browsers' per-identity settings fold.
- **Checked**: `toggles.spec.js` clicks every tab, segmented control, disclosure and switch on every route (desktop and
  phone) and fails if the page breaks, loses its title or empties, or if an approval loses what will change, why, who
  asked or the decision bar.

### Not done (and why)

- Bulk on the bots tree, retarget connector / set model in bulk (power N24): scope, unchanged from round 1.
- Comment placement on a new trigger's dash (power minor): the real emitter's job.
- A dedicated proposal route for phones (power N22): the bot shell is hidden on phones instead, same result.
- The Overview's left column ends early on desktop (bugs m11): the approved layout; not changed without review.


Responses to the two audits of the dashboard prototype: **Sam** (newcomer,
`newcomer/REPORT.md`) and **Riley** (power user, `power/REPORT.md`). Each
finding is **fixed**, **changed differently** (with why), or **deferred**
(with why). "Spec" names the Playwright file in `design/dashboard-tests/`
that proves it. The design doc (`docs/design/dashboard.md`) describes the
result, mostly in §3, §4.4, §4.11, §4.12, §6.5 and §7.

## Sam (newcomer)

### Blockers

| ID | Finding | Status | What changed | Spec |
|---|---|---|---|---|
| B1 | No onboarding, no first step | fixed | Overview says what conductor is in one line; **Get started** (connect a service, make a bot that watches something, check what bots can do, try an approval) with ticks derived from the setup, hideable; "The score" is **Runs, last 12h**; health is a collapsed **System** card. | onboarding |
| B2 | New bot watches nothing; expert form | fixed | Guided **New bot** (Purpose → Watches → Tells you where → What it may do → Review), presets in words, Advanced collapsed; it writes the bot and one trigger per watched event. A bot that watches nothing says so, with **Add what it watches**. | onboarding |
| B3 | "What can my bots do without asking?" | fixed | Every bot's Access tab opens with *Does on its own / Asks you first / Never*; a tier ladder says what each tier means; the global **Safety** page lists every bot, linked from the "sandbox enforcing" chip. | semantics, teams |

### Major

| ID | Finding | Status | What changed | Spec |
|---|---|---|---|---|
| M1 | Approvals don't say why or the risk | fixed | Plain title, **Why** and **Impact** first; Terraform summarized in tiles (adds/changes/replaces); hashes under a "Technical details" disclosure. | approvals |
| M2 | Slack approval hides the message; `chat:U-THIRD` unexplained | fixed | The full payload, long text untruncated; a banner explains a requester who isn't an operator (and one for event-started work). | approvals |
| M3 | Connecting Slack/GitHub assumes tokens | fixed | **Connect with Slack / GitHub**: an app-manifest flow (mocked) that stores the returned tokens in the vault and stages the connector with references; the manual form stays for people with tokens; it then says what bots could do. Sensitive fields default to Vault. | connect, valuesource |
| M4 | Bot vs workflow vs team vs pack unexplained | fixed | One-line definitions at the top of Bots, Workflows and Packs; dotted glossary terms everywhere and a `#/glossary`; "synthesized" and owner badges explain themselves; Workflows points to bots for judgment, and the bot wizard's triggers appear in Workflows. | onboarding |
| M5 | New workflow opens in an error state; templating hidden | fixed | Untouched required fields are a neutral to-do checklist ("Choose what starts this workflow", "Fill in labels"), not errors; the insert button is labelled **Insert from the event**. | builder, builder2 |
| M6 | Step picker gives no sense of risk | fixed | Every action is tagged *reads only*, *changes things* or *high impact*, sorted by risk; descriptions wrap to two lines. (Per-team "needs approval" tags: changed differently, see below.) | builder |
| M7 | Every edit is a passkey-signed deploy | fixed | The rule: a passkey only for changes that widen what bots or agents can do (the one classifier) and for every approval or deny. Everything else gets a plain **Apply** after the diff (still validated on a copy, backed up, rollback-able); the review says why a passkey is or isn't needed. Theme, density and the usage display are local display preferences, instant, not config. | passkey, usage, scale |
| M8 | Folders grant logins | fixed | Folders are never principals: login and credential scopes name bots (`identities`), with a team shortcut that expands to its bots. | logins |
| M9 | "binds credential to undefined" | fixed | Credential bindings use the real shape; the staged summary and acknowledgement name the bots. | logins |
| M10 | Failed run: no next step; audit not scoped | fixed | **What happened / What you can do** (see the fix diff, retry from fix, compare with the last good run, dismiss); audit filtered to the run; "Record" is **Download run record**. | runs |
| M11 | ⌘K finds names but not actions | fixed | ⌘K indexes actions (that run), approvals, connectors, verbs, settings, config paths and terms; an empty result suggests what to try. | keys |

### Minor

| ID | Status | What changed |
|---|---|---|
| m1 top-bar jargon, overflow | fixed | Every strip chip explains itself ("agents ok", update link, the address); on phones the strip is a status dot plus the needs-you count, the rest one tap away. |
| m2 overview counts | fixed | Tiles in words ("6 bots", "3 in a live session"); counts checked against the data (numbers spec). |
| m3 unlabeled tree icons | changed differently (Daylight, calm); *corrected in round 2* | A folder rolls up what needs you first ("1 needs you", "2 failing"), so there is nothing to decode and no legend. |
| m4 issue numbers in the UI | fixed | None left in copy or diff headers (bots:, browser:, planned features). |
| m5 undefined terms | fixed | Glossary popovers and `#/glossary` (provenance, widening, digest, turn, sweep, hand-off, case, home session, fleet/rung, tiers …). |
| m6 Add a login "teleports" to Config | changed differently | Review stays the one shared apply page (one pipeline for every config change), but it names where you came from and returns you there; the reason a passkey is needed is stated. |
| m7 wrong diff location label | fixed | Hunk headers name the key path of the first changed line (`usage.source`, `teams.platform.collections.vendors`). |
| m8 page fades to blank after approving | fixed | Re-renders after a decision don't animate. |
| m9 deny without a reason | fixed | Deny takes a reason, sent to the bot. Deny still needs the passkey (every approval and deny is signed). |
| m10 Guards card layout | fixed | Each guard is one line of text. |
| m11 raw cron | fixed | "Mondays at 06:00", the raw expression on hover. |
| m12 chat reply truncation, "(operator trust)" | fixed | A plain reply. |
| m13 duplicate action handler | fixed | Renamed; any duplicate now logs an error, so the suite fails on it. |
| m14 logins count mismatch | fixed | One count (credentials plus saved sessions) everywhere. |

### Phone

| ID | Status | What changed |
|---|---|---|
| P1 Approve 1,700px down | fixed (*corrected in round 2*: the shell was still there) | On phones a proposal opens without the bot shell, summary first, with a one-row sticky Approve/Deny bar. |
| P2 clipped controls | fixed | Tab rows scroll with an edge fade and keep the current tab in view; page-head and bot/team header actions beyond two fold into ⋯; tables become cards. |
| P3 nav highlight jumps | fixed | The origin tab stays highlighted and Back returns there. |
| P4 toast over the nav | fixed | Toasts sit above the tab bar and the safe area. |
| P5 legend clipped | fixed | The legend wraps. |

### Polish

| Finding | Status |
|---|---|
| Access "Review change" looks enabled | fixed: it appears only once the policy changed. |
| lowercase labels | fixed: Description, Escalation. |
| space before colons in diffs | fixed: the line-number style no longer leaks into the code cell (`.dl > span`). |
| serif on long proposal titles | fixed: proposal titles are short plain sentences in the UI face. |
| "Show planned features" does nothing visible | changed differently: the toggle now says what it does; the multi-user previews themselves are deferred (below). |

## Riley (power user)

### Blockers

| ID | Finding | Status | What changed | Spec |
|---|---|---|---|---|
| B1 | Map-form `skill.verbs` crashes the builder | fixed | `skill.verbs` is list-or-map with resource-scope chips; any internal render error locks the list on the last good version with the YAML still canonical. | semantics |
| B2 | "Valid on a copy" accepts what conductor rejects | fixed | Strict decode from the real schema (unknown keys, budget keys, typed filters, isolation rules, `<instance>.<verb>`), optional prompt, `use: cli` + `command:`; the checks show literal `conductor validate` output. | semantics |
| B3 | Team ceiling not enforced | fixed | Teams on conductor's schema; looser tiers and promotions above the ceiling are disabled with the reason and refused on validation ("Team ceilings" check). | teams |
| B4 | Approval hides what you sign | fixed | The full canonical payload on the page and in the seal modal; redaction said explicitly. | approvals |

### Major

| ID | Finding | Status | What changed | Spec |
|---|---|---|---|---|
| M1 | Run detail panels wrong | fixed | One run record per run: per-step selection (↑/↓), step-scoped log/diff, tabs only for what the step recorded, run-scoped audit, time axis, **compare two runs**, consistent fake data. | runs |
| M2 | No bulk operations | partly fixed | Checkboxes, shift-click ranges and a sticky bulk bar on **workflows** (move to folder, enable, disable; one change set, one diff) and **Activity** (retry the failed, stop the running; *corrected in round 2*: export was retired); knowledge proposals already had bulk approve/deny. **Deferred:** bulk on the bots tree, packs, connectors, secrets and cases (the tree already has drag-and-drop and Move to; the pattern is in place to extend). | bulk |
| M3 | Classifier misses and invents widenings | fixed | One classifier (`S.classify`) for every edit surface, itemized with YAML paths; official packs aren't flagged as a new source. | semantics |
| M4 | Pack install has no review | fixed | Browse → **Add** opens the routed install wizard: roles, arming with repos (refused without them), a pack plan with grants shown loudly, the `conductor.lock.yaml` hunk in the diff. **Deferred:** the dependency tree (no pack in the catalog declares dependencies yet). | packs |
| M5 | Four spellings of vault references | fixed | One syntax, `{{ vault "<vault>" "<key>" }}` (label `vault · key`); secrets keyed by `<vault>/<key>`, names only as dashboard aliases; broker grants `<vault>/<key>` with `secrets_via` shown. | valuesource, semantics |
| M6 | No env management or effective env | fixed | **Env** tab by scope with uses, move-to-vault, import (export retired); effective environment on the Env tab, run steps and bot workers. | env |
| M7 | Builder hides real constructs | mostly fixed | Hooks lane (start/done/fail/stop) and step hook badges; `output_schema` summary; the isolation editor covers mode, container image, network egress/deny, fs, limits and privileged; filters take typed keys, `not_` forms and an `expr` row; "+N keys only in YAML" with an in-place YAML editor; the picker has Run a command, Wait for a condition, Hand to a team, Fail the run. **Deferred:** a visual AND/OR group builder for list-form filters (edit them in the YAML pane; they're kept and validated). | builder2, semantics |
| M8 | Test can't use real events | fixed | Past events of the trigger's source from history, **Paste a payload…**, and "no gh.pull_request events in the last 14 days" when history has none. | builder2 |
| M9 | Nothing paginates; no density | fixed / changed differently | Run history pages by cursor (50 + Load more); **Compact** density (one-line rows, the bot tree too); tiles capped ("3 + N more"); the runs panel shows the busiest six and filters by folder. Virtual scrolling was **changed to cursor paging**, which matches the cursor-paginated API and keeps the DOM small. | scale |
| M10 | ⌘K shallow; actions don't act | fixed | Indexes the staged-aware view including config paths, actions execute (destructive ones confirm), no duplicates; `?`, `/`, g-chords, `j`/`k`/Enter/`x`. | keys |
| M11 | Split diff truncates the new side | fixed | Each side scrolls independently with rows aligned; word-level highlights in changed lines. | runs |
| M12 | Pack trust labels wrong | fixed | official registry / **local · yours** / third party (needs `pack_trust`). | packs |
| M13 | Inheritance mislabelled | fixed | inherits / same as team / narrows computed correctly; the tier ladder shown once. | teams |
| M14 | Phone Approve out of reach | fixed | Sticky Approve/Deny bar. | approvals, iphone |

### Minor

| Finding | Status |
|---|---|
| `not_not_label_any` | fixed (step A). |
| key order (filter/policy after steps) | fixed: a new trigger key is inserted in canonical order before `steps:`. |
| new-trigger comment placement | deferred: not reproduced in the rebuilt New workflow flow; left for the real builder's emitter. |
| flow style lost in pack bindings | changed differently: pack overrides are now a YAML editor you write, so the style is yours. |
| "CODE · run: command" label | fixed: `use: cli · <command>`. |
| Retry on a running step | fixed: **Restart this step**, stop first, confirmed. |
| inconsistent fake data (r-7e40) | fixed: ci-fixer runs have ci-fixer's steps. |
| spend shown with usage off | fixed: outputs carry no cost or tokens unless usage display is on. |
| wrong verb prefix | fixed: `<instance>.<verb>` everywhere (`tf-apply.apply`). |
| raw-JSON overrides | fixed: YAML editor listing the pack's addressable members; unknown ones refused. |
| folder Move picker "none" and "top level" | fixed: one "top level". |
| restore has no revert-one | fixed: **Revert this change**, and **Restore to before this** names the later changes it undoes. |
| shallow audit filters | fixed: actor, run (`?run=` from run detail) and free text (cases, verbs, paths). |
| unscoped runs show `#0` | fixed: the repo alone, or "—". |
| mixed timezones | fixed: backup ids are labelled UTC, shown beside local time. |

### Polish

| Finding | Status |
|---|---|
| diff typography (space before colons) | fixed. |
| parallel lanes truncate | fixed: branch cards show two lines. |
| agent card height / grouping | fixed with Daylight: the agent card is three named groups (What it does · Where it runs · Isolation, with "isolation ·" dropped from every label), the rarely used keys under "More options" and "+N keys only in YAML". |
| YAML pane stops at 2/3 height | fixed: it fills the column. |

## Also done during the audit work (later directions)

- **iPhone (WebKit, Safari's floating toolbar).** The shell is a locked `100dvh` column with an in-flow tab bar (it was `100vh`, which reaches under the toolbar), safe areas honoured, the status strip collapsed on phones. WebKit iPhone 15 projects (OS dark/light × theme auto/light/dark) with the toolbar and insets simulated sweep every route (iphone spec, `run-webkit.sh`).
- **One theme at a time.** An explicit choice always wins over the OS for every surface and text colour; with none, the page follows the OS live. The cause of the drift: boot saved whatever theme a first visit started with as an explicit choice. Contrast tokens nudged so rendered text is ≥ 3:1 in both themes (layout and iphone specs).
- **Passkey rule** (M7 above), **display preferences local**.
- **Knowledge on conductor's real content kinds**, collections on teams and bots.
- **Logo as one swappable asset**: `design/dashboard-prototype/assets/logo/` (mark, lockup, favicon) referenced only through `assets/logo/logo.js` and `UI.logo()`.

## Daylight (the items that waited for it are done)

The restyle the audit response held back is done: Daylight is adopted across the prototype in one pass (tokens,
fonts, the Fermata logo kit, components, type, density, copy), and then, after the first review of it ("it all looks so
busy"), the information design went exception-first (`docs/design/dashboard.md` §3.0). What changed for findings:

- **Riley polish, agent card grouping**: done (above).
- **Sam polish, serif on long titles**: now a rule the suite enforces (`daylight.spec.js`: the serif only on titles of
  five words or fewer, at 20px or more); run, case and proposal titles are the UI face.
- **Sam m1 top-bar jargon**: the top bar now says how things are in one word ("All systems normal") and the needs-you
  count; the rest moved into its status menu.
- **Sam B1 the score**: the lanes are Score's pattern, calm: passing runs a faint tick strip, failures and waits
  labelled where they are, running work in ink.
- **Riley M9 scale**: Compact is wired to Daylight's density tokens (36px rows); phones stay comfortable; finished
  runs fold into one line by default.
- **Status badges**: replaced by margin marks (glyph + word, no capsule, a leading edge only on rows that need
  someone), drawn only by the shared helpers so the brand's final status system swaps in one place; one state has
  one look across the app, running in ink (`status.spec.js`).
- **After the first Daylight pass, a reset to the approved designs**: the approved mocks and the chosen status column
  are now the spec, screen by screen (`design/CONFORMANCE.md`). The calm variant, the risk ladder, object and action
  icons and the row-anatomy grid were removed; risk appears only inside approval content, in plain words.

## Deferred, with why

- Bulk on bots tree, packs, connectors, secrets, cases (M2): scope; the component and the one-diff staging are in place.
- Pack dependency tree (M4): no catalog pack declares dependencies yet.
- Visual AND/OR filter groups (M7): kept and validated in YAML; a visual builder is a larger piece.
- New-trigger comment placement (Riley minor): belongs to the real emitter.
- Planned multi-user previews (queued direction, not an audit finding): not built; the toggle says so.
- Per-team "needs approval" tags in the step picker (Sam M6 suggestion): risk visuals wait for the brand's proposal (pending review); whether a given bot must ask depends on the bot, which the Safety page and each bot's "What it can do" tab show.
