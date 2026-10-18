# Dashboard prototype: conformance to the approved designs

The approved designs are the spec. This file says, for each screen, which approved source it follows, where it
deliberately departs (and why), and shows the approved mock beside the prototype: desktop and phone, light and dark.
It is the input to the brand designer's deviation review.

**Approved sources** (all on `origin/design/brand`):

- **Daylight, as in its mocks:** `design/brand/v2/` (`mock.html`, `mock.js`, `mock.css`, and `directions.js` for the
  Daylight values): Home, the failed run, the bot page with the terraform approval, run history, the builder. Rendered
  with `&v=before` (every row drawn: the later "calm" variant was reversed).
- **Status:** D's grouping with A's margin marks, as on `design/brand/explore/status-column.html` (the combined version,
  every row drawn): "Needs attention · N" → "Running now" → "Finished"; a mark plus a word; a coloured leading edge only
  on rows that need someone; the reason under the name; no capsules, no row tints, no step counts; passed and parked
  distinct in hue and shape; one vocabulary everywhere (`design/brand/status/marks.js`).
- **The Daylight brand book:** `system.html`, `tokens/tokens.css` (copied verbatim), `book/components.css`, `BRAND.md`.
- **The logo:** Fermata, strengthened, only as the logo (no sunrise motif or other derivative anywhere).
- **Home as it stands** (approved in review, then the greeting and its summary line removed in review): the four At a glance tiles, Needs you
  as plain-sentence items with one action each, Running now (at most three), the last 12 hours as a runs-per-hour strip
  with failures named, For your information, and Get started showing only what's left. Its patterns (one row anatomy,
  one action per item, quiet secondary panels, an aggregate plus its exceptions) are the model for every other screen.

**Not approved, so not built:** risk meters or any risk-ladder visual (risk appears only inside approval content, as
plain words such as "Touches production"); step counts as status; per-workflow lanes; ad-hoc icons or colours;
"Since you last looked"; exports; anything else not visible in the approved mocks.

Screenshots are in `design/conformance/` (`<screen>-<desktop|phone>-<light|dark>-<approved|prototype>.jpg`), rendered
by `design/conformance/render.js` from the brand checkout and the prototype on the same data.

## Shell: rail, top bar, phone header and tab bar

Source: v2 `mock.js` `shell()` and `mock.css` (shell, phone).

Matches: the rail full height with the brand on top and the host card at its foot; the quiet top bar (search, how
things are as a status mark and a word, the avatar); the phone header (the logo and the page name) and the five-item tab
bar.

Deliberate differences:
- **Navigation** follows the later structural decisions: Run history is merged into **Activity**; the Settings group is
  Connections, Secrets, Packs and **System** (Config, Safety, Updates, Logs & audit and Access, under one item with its
  own sub-navigation; old `#/safety` links open the Safety tab). There is no top-bar status menu: what needs a person
  is in Needs you, and the machine card at the rail's foot (More on a phone) says how the machine is and, quietly, its
  update; an update escalates (attention edge on the card, and an item in Needs you) only when it needs a person.
  Pause all automation lives on the Safety tab.
- The avatar opens a small menu (theme, display preferences, glossary, the prototype's demo data, Lock): the mock has
  no place for these.
- When config changes are staged, the top bar shows "N not applied" (the staged-change mark) linking to the review.
- The phone header's page name follows every route (it went stale before round 2); the rail's link weight (500) and the
  12px group label are the mock's, which the book's type sheet disagrees with (left for the brand designer).
- A skip link ("Skip to the page") appears on keyboard focus; sign-in shows no rail, top bar or tab bar, and uses the
  kit's horizontal logo file.

| | Approved | Prototype |
|---|---|---|
| desktop, light | ![](conformance/overview-desktop-light-approved.jpg) | ![](conformance/overview-desktop-light-prototype.jpg) |

## Home (the Overview)

Source: v2 `overview()` (Daylight), and Home as approved in review.

Matches: Needs you as the mock's list (the mark, a plain title, why, the machine detail
last in mono, the age, one verb, Review primary for approvals); Running now; Last 12 hours (the figure, hour bars with
failures stacked in the danger colour, the failures named); For your information.

Deliberate differences:
- **No greeting or summary sentence** (removed in review): they carried nothing the tiles and Needs you don't, and the
  summary contradicted itself ("everything else is running normally" beside failed runs). The page opens on At a glance.
- **At a glance** tiles at the top (approved in review); after the usability walkthrough, the Approvals tile became
  **Needs you** (the badge's count, broken down by kind), so the tiles and the badge agree. The Running tile links to Activity filtered to running
  and carries no list of names.
- **Running now** shows at most three runs, longest-running first, then "+N more running →"; no step counts (the status
  rule); hidden when nothing runs.
- **No sunrise** (the logo rule). Cost ("$8.40 spent") appears only when usage display is on (it is off by default).
- **Get started** appears only while something is left; its steps are peers, each with the same secondary button.

| | Approved | Prototype |
|---|---|---|
| desktop, light | ![](conformance/overview-desktop-light-approved.jpg) | ![](conformance/overview-desktop-light-prototype.jpg) |
| desktop, dark | ![](conformance/overview-desktop-dark-approved.jpg) | ![](conformance/overview-desktop-dark-prototype.jpg) |
| phone, light | ![](conformance/overview-phone-light-approved.jpg) | ![](conformance/overview-phone-light-prototype.jpg) |
| phone, dark | ![](conformance/overview-phone-dark-approved.jpg) | ![](conformance/overview-phone-dark-prototype.jpg) |

## Activity (Run history merged in)

Source: v2 `historyDaylight()` and `status-column.html` "Chosen before calm: every row drawn".

Matches: the page title and its sentence ("Last 12 hours: 16 runs. 3 need attention, 3 are running and 10 finished.");
the grouped table (Needs attention with its count in the accent, each row's reason in its state's colour under the
name, a 3px leading edge, one action; Running now; Finished in quieter ink); the status column as mark plus word; When,
Took, ID; the phone's grouped list.

Deliberate differences:
- **Run history and Activity are one page** (a structural decision): the filters (status, workflow, bot, folder, repo),
  the time range (up to all time) and search sit on the page; Finished is paged ("Show 20 more of N").
- **Runs per hour** for the chosen range sits above the table: the Overview's approved strip, the same size for ten
  workflows or five hundred. The per-workflow lanes are gone (they don't scale).
- **Selection**: a checkbox column; while anything is checked, the header row becomes a pinned selection bar
  ("3 selected", Retry…, Stop…, Select all N matching, Clear). The same bar serves every list with bulk select.
- No Export. Cost appears only when usage display is on.

| | Approved | Prototype |
|---|---|---|
| desktop, light | ![](conformance/activity-desktop-light-approved.jpg) | ![](conformance/activity-desktop-light-prototype.jpg) |
| desktop, dark | ![](conformance/activity-desktop-dark-approved.jpg) | ![](conformance/activity-desktop-dark-prototype.jpg) |
| phone, light | ![](conformance/activity-phone-light-approved.jpg) | ![](conformance/activity-phone-light-prototype.jpg) |
| phone, dark | ![](conformance/activity-phone-dark-approved.jpg) | ![](conformance/activity-phone-dark-prototype.jpg) |

## A run (finished or running)

Source: v2 `runPage()` and `runTreeDaylight()`, and the status-column run tree.

Matches: the plain title, the summary sentence, the status mark on the right; the actions row (Retry primary, Open the
pull request, Dismiss: when a check failed on an earlier step's change, the primary retries from that step and rerunning
the check alone is secondary); What happened as the step tree (number, mark, what it did, how long; nested workflow calls as
one node); Details (Started with the clock, Took, Triggered by in words, Run id, the signature).

Deliberate differences:
- **One page for a running and a finished run**: a running run's actions are Stop run… (confirmed) and its running step
  shows its live log; **Open in paseo** appears only on a step with a live paseo session.
- **Back to Activity** (keeping Activity's filters and scroll) instead of crumbs; no run tabs or previous/next.
- The title is the **sans** (the h3 style at the h2 size): BRAND.md's serif rule puts generated titles in Atkinson,
  though the mock sets it in Fraunces (a conflict for the brand designer to settle at the source).
- No "Download the run record" (export is retired). A running run's record says "signed when it finishes".
- Below the tree, the **selected step** (outputs, inputs, log, diff) instead of the mock's "Why the tests failed" card,
  which is that view for the failing step. An Audit card sits under Details.

| | Approved | Prototype |
|---|---|---|
| desktop, light | ![](conformance/run-desktop-light-approved.jpg) | ![](conformance/run-desktop-light-prototype.jpg) |
| desktop, dark | ![](conformance/run-desktop-dark-approved.jpg) | ![](conformance/run-desktop-dark-prototype.jpg) |
| phone, light | ![](conformance/run-phone-light-approved.jpg) | ![](conformance/run-phone-light-prototype.jpg) |
| phone, dark | ![](conformance/run-phone-dark-approved.jpg) | ![](conformance/run-phone-dark-prototype.jpg) |

## A bot, and an approval

Source: v2 `approvalPage()`.

Matches: the bot head (avatar, name, its purpose in a line); the tabs (Approvals, Chat, Cases, Memory, What it can do,
Settings); the approval card (the needs-you kicker with age and expiry, the plain title, what it asks, Why, the four
change counts with Replaces in the warning colour and its note, Where / Effect / Undo, who started it, Approve with
passkey, Deny…, who can approve, the technical detail folded); What it can do in three groups.

Deliberate differences:
- What the bot is doing now is a **status mark and a word** ("Running", "Parked", "Shadow"), not the mock's capsule (the
  status rule's vocabulary).
- The proposal title is the **sans** (BRAND.md's serif rule for generated titles; the mock sets it in Fraunces).
- **No tabs** (from review: a tab hid the why and the change summary). One column in the order the decision needs:
  what will change (the counts, the replaces note, the plan's resources by action in Terraform's colours), why,
  where/effect/undo, who asked. The full plan opens in place below them (a resource in the summary opens it at its
  block); the integrity data (proposal id and tier, verb, command, commit, plan sha256, case, digest, signer) is one
  quiet folded line at the end of the card, smaller and muted when open. The decision bar is pinned.
- The approval **stands alone at every width**: no bot head or tabs above it, one Back to where it was opened from;
  the side column keeps only what the bot can do (the Timeline repeated the kicker and the origin).
- **On a phone** the approval stands alone: no bot head, tabs or crumbs above it, one Back to where it was opened from,
  and Approve and Deny in one row.
- "Who can approve" names the bot's own approvers (not the team's).
- Rotate session, Sweep now, Deliver an event and Workers sit in a **⋯ menu** beside the head (the mock has no place for
  them).

| | Approved | Prototype |
|---|---|---|
| desktop, light | ![](conformance/approval-desktop-light-approved.jpg) | ![](conformance/approval-desktop-light-prototype.jpg) |
| desktop, dark | ![](conformance/approval-desktop-dark-approved.jpg) | ![](conformance/approval-desktop-dark-prototype.jpg) |
| phone, light | ![](conformance/approval-phone-light-approved.jpg) | ![](conformance/approval-phone-light-prototype.jpg) |
| phone, dark | ![](conformance/approval-phone-dark-approved.jpg) | ![](conformance/approval-phone-dark-prototype.jpg) |

## The workflow builder

Source: v2 `builderPage()`.

Matches: crumbs, the name, what it does, Undo / Test / Save and apply; the tabs (Recipe, Config file, Runs, Settings);
the When box in words; the step cards (number, title over summary, the kind with its icon, chevron); Add a step; Try it,
At a glance and the config file beside.

Deliberate differences:
- A step card **opens in place** to its form (the builder's editing), and its title is the step's id: config has no
  separate step titles.
- **Save and apply** goes through the review diff (the config pipeline), as everything does.

| | Approved | Prototype |
|---|---|---|
| desktop, light | ![](conformance/builder-desktop-light-approved.jpg) | ![](conformance/builder-desktop-light-prototype.jpg) |
| desktop, dark | ![](conformance/builder-desktop-dark-approved.jpg) | ![](conformance/builder-desktop-dark-prototype.jpg) |
| phone, light | ![](conformance/builder-phone-light-approved.jpg) | ![](conformance/builder-phone-light-prototype.jpg) |
| phone, dark | ![](conformance/builder-phone-dark-approved.jpg) | ![](conformance/builder-phone-dark-prototype.jpg) |

## Screens without a mock (brand book components only, in the approved screens' patterns)

Each uses only the brand book's components (cards, the status mark, labels, buttons, fields, tables, tabs) and the
approved screens' patterns: one compact row per thing (its name and kind, what it's doing, one action and a ⋯ menu;
only a row that needs someone carries a mark, with its reason), a page title that matches the nav (with a sentence only
when it says something the rows don't), labels as plain words (no capsules), quiet secondary panels, technical detail
folded. Teams take the bot page's header.

| Screen | What it follows | Prototype |
|---|---|---|
| Needs you | Home's Needs you list, every item | ![](conformance/needs-desktop-light-prototype.jpg) |
| Connections | what conductor can use: Services (each service, built in, official or from a source, with its version, an update, and its connections under it), Agent runtimes, Code languages, Machines; Add a service opens one catalog (search, or a git URL) | ![](conformance/connections-desktop-light-prototype.jpg) |
| Secrets | two tabs, one system: Secrets and Settings (settings and environment variables) share one row (readable name with the exact reference and Copy, used by, value or where it lives, ⋯ Change the value / Copy the reference / Delete) and the same two-sentence explanation; the filter bar; vaults always open at the foot of Secrets with their own Add a vault | ![](conformance/secrets-desktop-light-prototype.jpg) |
| Packs | packs only (bundles of workflows): Installed and Browse; compact rows; Add a pack opens the catalog; guided overrides | ![](conformance/packs-desktop-light-prototype.jpg) |
| Pack overrides | roles → compatible connections, triggers with consent, steps via the builder, YAML second | ![](conformance/pack-overrides-desktop-light-prototype.jpg) |
| Safety | the sandbox in plain words; exceptions; containers; each bot's three groups (no tier ladder) | ![](conformance/safety-desktop-light-prototype.jpg) |
| System · Config | applied changes full width, readable at a glance | ![](conformance/config-desktop-light-prototype.jpg) |
| System · Updates | plain words first; technical detail folded | ![](conformance/updates-desktop-light-prototype.jpg) |
| System · Access | who can get in, passkeys, sessions, when it asks; technical detail folded | ![](conformance/access-desktop-light-prototype.jpg) |
| Browsers · Logins | compact rows; vault entries one click away | ![](conformance/logins-desktop-light-prototype.jpg) |

## How conformance is checked

- `dashboard-tests/status.spec.js`: every status goes through the shared mark; one state, one look across the app.
- `dashboard-tests/sweep.spec.js`: every route at 1440, 900 and 390 pixels: no text and controls overlapping inside a
  card, nothing spilling out of its card, no phrase squeezed to one word per line, no list content centred by accident
  or rows blown up, no dropped interpolation (an unescaped `<name>` or a gap before a full stop).
- `dashboard-tests/daylight.spec.js`: tokens only, the type floors, the serif only on short titles, no capsules.
- `dashboard-tests/iphone.spec.js` (WebKit, iPhone 15): one theme, nothing under Safari's toolbar, the logo at 16px in
  both themes, rendered contrast.
