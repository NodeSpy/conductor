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
  Connections, Secrets, Packs, **Safety** and **System** (Config, Updates, Logs & audit and Access, under one item with
  its own sub-navigation). Updates is not in the rail; an available update is said quietly in the status menu and only
  escalates when it needs a person.
- The avatar opens a small menu (theme, display preferences, glossary, the prototype's demo data, Lock): the mock has
  no place for these.
- When config changes are staged, the top bar shows "N not applied" (the staged-change mark) linking to the review.

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
- **At a glance** tiles at the top (approved in review). The Running tile links to Activity filtered to running
  and carries no list of names.
- **Running now** shows at most three runs, longest-running first, then "+N more running →"; no step counts (the status
  rule); hidden when nothing runs.
- **No sunrise** (the logo rule). Cost ("$8.40 spent") appears only when usage display is on (it is off by default).
- **Get started** appears only while something is left, with the next step as the one primary button.

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
pull request, Dismiss); What happened as the step tree (number, mark, what it did, how long; nested workflow calls as
one node); Details (Started with the clock, Took, Triggered by in words, Run id, the signature).

Deliberate differences:
- **One page for a running and a finished run**: a running run's actions are Stop run… (confirmed) and its running step
  shows its live log; **Open in paseo** appears only on a step with a live paseo session.
- **Back to Activity** (keeping Activity's filters and scroll) instead of crumbs; no run tabs or previous/next.
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
passkey, Deny…, who can approve, Technical details folded); What it can do in three groups; the Timeline.

Deliberate differences:
- What the bot is doing now is a **status mark and a word** ("Working on case c31"), not the mock's capsule (the status
  rule).
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
approved screens' patterns: one compact row per thing (its mark, name and kind, what it's doing or why it needs you, one
action and a ⋯ menu), a page title (with a sentence only when it says something the rows don't), quiet secondary
panels, technical detail folded.

| Screen | What it follows | Prototype |
|---|---|---|
| Needs you | Home's Needs you list, every item | ![](conformance/needs-desktop-light-prototype.jpg) |
| Connections | the row anatomy; the attention state as the reason line | ![](conformance/connections-desktop-light-prototype.jpg) |
| Secrets | one line each: name and vault, used by in words, changed; the reference in the row's detail | ![](conformance/secrets-desktop-light-prototype.jpg) |
| Packs | compact rows; Add a pack opens the catalog; guided overrides | ![](conformance/packs-desktop-light-prototype.jpg) |
| Pack overrides | roles → compatible connections, triggers with consent, steps via the builder, YAML second | ![](conformance/pack-overrides-desktop-light-prototype.jpg) |
| Safety | the sandbox in plain words; exceptions; containers; each bot's three groups | ![](conformance/safety-desktop-light-prototype.jpg) |
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
