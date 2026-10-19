# conductor brand and design system: Daylight

This is the single source of truth for how conductor looks, reads and behaves on every surface: the README and docs, the dashboard, and anything after them. This file is the written guide, and each rule comes with its reason. It describes only the current system; earlier explorations and drafts are on the [archive](archive/index.html), clearly labelled as not current.

| | |
|---|---|
| **Brand book** (live, both themes, both densities) | [`system.html`](system.html) |
| **Approved screens** (the dashboard’s reference) | [`v2/mock.html?d=daylight`](v2/mock.html?d=daylight): Overview, Activity, approvals, a bot, a run, the builder |
| **Tokens** (source of truth, W3C DTCG) | [`tokens/tokens.json`](tokens/tokens.json) → generated [`tokens/tokens.css`](tokens/tokens.css) |
| **Prototype → Daylight mapping** | [`tokens/migration.json`](tokens/migration.json), [`tokens/prototype-compat.css`](tokens/prototype-compat.css), and *Tokens › Migrating the dashboard* in the book |
| **Contrast report** (every declared pair, both themes) | [`tokens/CONTRAST.md`](tokens/CONTRAST.md) |
| **Logo** (the official kit) | the `fermata-bold` kit, whose one canonical location is [`assets/`](assets/) (the other folders in `logo-kits/` are archived candidates) |
| **Design QA** (what the dashboard is checked against) | [`qa/SPEC.md`](qa/SPEC.md) |
| **Archive** (not the current system) | [`archive/index.html`](archive/index.html) |

## Changelog

- **2026-10-18. Bot identity.** Twelve bot faces (`bot-identity/faces/`, ids for `avatar.preset`), seven identity colours that keep clear of every status hue (`--color-bot-*`), where the colour may and may not go, the icon-and-colour option, the initials fallback and voice presets. A new book chapter shows the set in both themes, at both sizes, and the full face × colour grid (every face in every colour, at 48px and 24px).
- **2026-10-18. Passed is green, aligned with the dashboard.** “Passed is green” replaces the old rule that healthy stays quiet ink. The passed word is `status.success.fg` (green-700 light, 6.2:1; green-400 dark, 7.3:1) in regular weight. The passed dot (`status.success.solid`) is green-600 in light (4.0:1) and now green-500 in dark (5.5:1). `status.success.chart` is the dashboard’s softer mix: green-600 85% with the surface in light (#2ba163, 3.2:1), green-500 72% in dark (#27864c, 3.5:1). The runs-per-hour strip stacks from the ground: passed, waiting or parked, running, failed on top (marked), with a key under it. **One deliberate difference:** waiting and parked use `run-strip.waiting` (sand-600 light, sand-400 dark: 5.2:1 or more, and clear of passed for colour-blind readers) instead of `border.strong`, which is 2.5:1 in light and 2.0:1 in dark, below the 3:1 floor for a data mark. The chart fill clears 3:1 on cards (surface, raised), where every chart sits; on the light canvas it is 2.98:1.
- **2026-10-18. Passed is green.** The passed mark and word now use the success green everywhere, and chart bars for passed runs use a softer success fill with failures stacked on top in red. New token `status.success.chart`; `status.success.solid` moves from sage to green. A mostly-green screen still reads calm: passed is regular weight, while failures are bold, red, edged and listed first. Passed and parked still differ in hue and shape.
- **2026-10-17. Brought up to date with every decision so far.** Superseded explorations moved to the archive, which is labelled as not current and is out of the main navigation. The brand book now shows only the current system. This file was rewritten rather than appended to.
  - **Removed:** the “since you last looked” digest; one lane per workflow; meter or bar risk visuals; the greeting and summary sentences that restate the screen; the sunrise motif and every illustration made from the logo; pill or capsule badges, counts and filters; tinted rows; step traces in lists; the calm-round components (collapsed summary lines for normal work, the “one loud thing” card, the loudness budget and its check); run tabs and previous/next between runs; export buttons; Pause/Run on the overview; captions that explain obvious controls.
  - **Added or made current:** the object and action icons, environment tags and the fixed row anatomy; At a glance (four tiles); the runs-per-hour strip with exceptions grouped by workflow; the approved Overview; approval tabs for technical decisions, with a colourised plan, and a sticky decision bar; the passkey only on actions that authenticate; the information architecture (Activity holds the run history, one System area, bots in folders).
- **2026-10-17. Logo never used as illustration.** The sunrise motif removed from empty states, the overview, the social card and the mocks; a misuse rule added.
- **2026-10-17. Activity at scale; At a glance.** One lane per workflow replaced by a runs-over-time strip plus exceptions by workflow. The digest replaced by At a glance.
- **2026-10-17. Risk in words.** The meter risk marker withdrawn; risk is plain words where someone decides.
- **2026-10-17. The calm round reversed.** It hid information; the system keeps the information and makes it identifiable at a glance.
- **2026-10-16. Status decided.** Grouping (needs attention · running now · finished) with margin marks; passed and parked differ in hue and shape.
- **2026-10-16. Logo decided.** Fermata, strengthened.
- **2026-10-16. Direction decided.** Daylight, light and dark.

---

## Decisions at a glance

| Area | Decision | Why |
|---|---|---|
| Direction | Daylight, in light and dark. Pages follow the OS; an explicit theme always beats it. | Plain language first, colour kept for what needs attention, and legible type throughout. |
| Logo | Fermata, strengthened (the `fermata-bold` kit, in `assets/`). Never altered, decorated, animated or turned into an illustration. | It holds its own on cream and says what conductor does: nothing moves until you release it. |
| Information design | Keep the information; make every item identifiable at a glance (icons for what it is, environment tags, status marks, attention order, one row anatomy). Fold only hashes, ids and raw payloads. | The problem was never the amount; it was having to read every item to know what it was and whether it mattered. |
| Status | Grouped rows (needs attention → running now → finished) with margin marks: a mark and a word. Passed is green (mark and word) in regular weight. A coloured leading edge only on rows that need someone. No tinted rows, no pills, no step traces in lists. | Green says “passed” at a glance; weight, edge and position keep the rare failure first on a mostly-green screen. |
| Labels | Status labels, filters, counts and tags each have their own treatment, and none is a pill. | A pill for everything made states, facts and controls look alike. |
| Risk | Plain words where someone decides (approvals, a bot’s permissions): “Changes production”, “Spends $1,800”, “Destroys”, “Grants access”. No meters, bars or ladders. | Bars read as signal strength, not danger. The visual marker for risk is still a proposal on the archive, not part of the system. |
| Scale | No view grows linearly with the number of workflows, bots or runs. | One lane per workflow broke at twenty workflows. |
| Passkey | The fingerprint appears only on actions that authenticate: approve, deny and changes that widen access. Everything else is a normal Apply. Never on “Review”. | The icon promises a signature; using it elsewhere teaches people to ignore it. |
| Palette | Cream and cocoa surfaces, sand neutrals, one ember accent | Warm paper reads as a product rather than a terminal, and one accent keeps “where do I act?” unambiguous. |
| Type | Fraunces (titles) · Atkinson Hyperlegible Next (everything read) · Atkinson Hyperlegible Mono (code only) | A soft, upright serif for the voice; Atkinson keeps 1/l/I and 0/O distinct, which matters for ids and numbers. |
| Size | Body 15px, lead 17px; nothing under 11.5px; mono ≥ 12px | Small mono text everywhere made the prototype hard to read. |
| Density | Comfortable 52px rows; compact 36px as a token set | Room to read by default; the same content, tighter, on request. |
| Theming robustness | Every page declares `color-scheme`; an explicit `data-theme` always beats the OS | The iPhone bug (below). |
| Tokens | DTCG tokens, a generator and a contrast gate | The gate checks 302 pairs; 0 fail. |

---

## Information design

conductor produces runs, cases, approvals, logs, diffs and bot activity around the clock. **The test:** in about three seconds, without reading, a person can say what needs them, what broke and whether anything is dangerous. Every screen follows these rules; the book shows each one with a do and a don’t (*Information design*).

1. **Identifiable at a glance.** Every item shows what it is (an object icon and its word), where it happens (bot, target and an environment tag) and how it went (a status mark and its word), in the same columns on every list.
2. **The three-second test.** Position (groups), weight, the status marks and the icons carry the answer; the words confirm it.
3. **A plain headline, the technical detail beneath.** “Replace the production web server”, then why it matters, then `p12 · infra@3f2a9c1d · +1 ~2 −0` in mono, last.
4. **Keep the information; fold only what is secondary.** Everything a person uses to understand or decide stays visible. Only hashes, ids and raw payloads go behind a disclosure.
5. **Three levels of type, and only three.** What (title or bold lead-in), why it matters (regular, secondary ink), machine detail (mono, small, tertiary, at the end).
6. **Colour is the signal; failure comes first.** Passed is green, mark and word, in regular weight. What needs someone is bold, red or ember, gets a coloured leading edge, its reason in words and one action, and sits at the top. No row is ever tinted.
7. **Say the risk plainly, where you decide.** A few words on approvals and permissions; most verbs need no risk label.
8. **A header sentence must add information.** No greetings, and no sentence that restates what is below it. A header line exists only to say something not already on the screen: an expiry, a scope, a filter that hides rows.
9. **Views that don’t grow with the fleet.** Runs over time are one strip with failures marked; exceptions are grouped by workflow; healthy workflows collapse to a count.
10. **Step detail lives on the run page,** as a collapsible tree. Lists never carry step traces. No run tabs and no previous/next stepping between runs.
11. **Numbers line up:** right-aligned, tabular figures, with units.
12. **Human times and schedules:** “52 min ago (17:03)”, “Mondays at 06:00”; cron strings and exact timestamps on hover.
13. **Generous, then compact on request.** Comfortable by default; compact is the same content in the same order, only tighter.
14. **No captions that explain obvious controls,** and no export buttons or overview-level Pause/Run controls.

### The row anatomy (`status/rows.js`)

What (object icon and word) · title and one line of why · where (bot, target, environment tag) · status (mark and word) · when · one action. On a phone: the icon and title with the time, then the kind word, bot and environment, then the status. On an approval that spends money, destroys something or grants access, the plain-words risk leads the why line; production is said by the environment tag.

### Patterns

- **Overview** (approved; see the mock): four **At a glance** tiles (need you, running, failed, spent); **Needs you**, one row and one action per item; **Running now**, capped at three with “+N more”; the **runs-per-hour strip** with the failures named beneath it; **For your information**; and **Get started**, showing only the steps that remain. No greeting and no summary sentence.
- **Activity** (it holds what was the run history): the runs-per-hour strip with failures marked; the exceptions (failed, needs you) grouped by workflow, with healthy workflows collapsed to a count; then every run in the grouped table, running now and finished, paged. Filters are rectangles, not pills.
- **Page header:** the title, plus a sentence only when it adds information. Actions belong to the items, not the header.
- **Run page:** the step tree. A nested workflow call is one node with its own status; the failing path opens on its own.
- **Approval:** Overview and Technical details tabs only when the technical detail matters to the decision (a Terraform apply, with the plan colourised). Simple actions, such as a Slack post, keep the detail folded. Approve and Deny sit in a sticky bar at the bottom that never covers the title or the why; both carry the passkey, because both are signed.
- **Empty states:** a plain, useful sentence and an action, if there is one. No illustration.

### Information architecture

- **Now:** Overview · Needs you · Activity (the run history lives here).
- **Work:** Workflows · Bots (grouped by team; there are no “members” and no folders) · Browsers.
- **System:** Config · Secrets · Packs · Connections · Updates · Logs & audit · Access.
- On a phone: Overview · Needs you · Activity · Bots · More.

---

## Logo

**The idea.** A fermata tells an ensemble to hold until the conductor releases it. conductor works the same way: runs wait behind an approval, a bot pauses on “needs you”, and nothing moves until you sign. The arch over a point also reads as a signal arc over its source. The mark is an arch in logo ink and a dot in the ember accent.

**Why this drawing.** Round one’s Fermata was weak on light: on cream, its thin arch, small dot and light italic wordmark lost to the surface. The official mark is redrawn for light:
- **Arch:** crown 4 → 5.5 units, feet 3.2 → 5.
- **Dot:** 25% bigger, and higher under the arch.
- **Ink:** deeper on cream (`--color-logo-ink`, sand 975 `#1c1712`, deeper than body text).
- **Wordmark:** Fraunces SemiBold, Daylight’s heading face, instead of the thin italic.
- **16px cut:** redrawn with whole-pixel 3px feet.
- **No rays or baseline, anywhere:** the sunrise rays and hairline baseline are not part of the mark, and the mark is never turned into an illustration either.

### Files: `design/brand/assets/` (the one canonical location)

Generated by `tools/build.mjs` and `tools/render.mjs` from the geometry in `tools/logos.mjs`. Edit the geometry, never the outputs. The wordmarks are outlined, so no font is needed. Files with no suffix are for light backgrounds, `-dark` files are for dark, and `-mono` files use `currentColor`.

| File | Use |
|---|---|
| `logo-horizontal.svg` · `-dark` · `-mono` | **Default** lockup: README, docs, sign-in, co-branding. |
| `logo-stacked.svg` · `-dark` · `-mono` | Square spaces: title slides, stickers. |
| `logo-mark.svg` · `-dark` · `-mono` | Where the name is already present: app icon, avatars, the phone header. |
| `wordmark.svg` · `-dark` · `-mono` | Rare: when the mark is already nearby. |
| `mark-inline.html` | Inline SVG for app chrome, coloured by `--color-logo-ink` and `--color-accent-default`. |
| `favicon.svg` | The 16px cut for `<link rel="icon">` only; it switches ink with `prefers-color-scheme` in browser tabs. WebKit ignores that switch when the SVG is used as an `<img>`, so in pages use `favicon16-light.svg` / `favicon16-dark.svg` instead. |
| `favicon-16/32/48.png` (+ `-dark`) | PNG fallbacks. |
| `apple-touch-icon.png` (180, full-bleed) · `icon-192.png` · `icon-512.png` · `app-icon.svg` · `app-icon-light.svg` | Home screen and PWA. |
| `social-card.png` | 1280×640 repository social preview: cream, the lockup and one sentence. |
| `README.md` | The file list and the exact dashboard-prototype changes. |

### Usage

- **Clear space:** one dot diameter on every side.
- **Minimum size:** mark 16px (use the 16px cut, never a scaled master), horizontal lockup 20px tall, stacked 72px wide.
- **In app chrome:** the inline mark at 20px tall beside the live-text wordmark (`--type-brand`: Fraunces SemiBold, `'SOFT' 0`).
- **Do** keep the dot in the accent and the arch in logo ink, or set the whole mark in one colour (ink, white, black, or white on ember).
- **Don’t** stretch, rotate (upside down the arch is a frown), recolour the arch, add effects, outline it, re-type the wordmark, place it on busy imagery without a solid field, or use it as a spinner.
- **Never alter, decorate, animate or turn the logo into an illustration or motif:** no sunrise rays, horizon lines, cropped or enlarged arches, watermarks or loading animations. It appears only as the logo, in its approved lockups. Empty states and onboarding use a plain, useful sentence and an action, if there is one, with no illustration derived from the mark.
- **Co-branding:** the partner logo goes to the right at equal optical height, after a hairline rule with two dot-widths of space on each side. conductor comes first on our surfaces, and the marks are never merged.
- **Never** bring back the mascot, the top hat or the Octocat pin.

The candidates explored before this one are on the [archive](archive/index.html); none of them is for use.

---

## Colour

### Raw palettes (`--palette-*`)

- **sand** is the warm neutral ramp for text, borders and icons (50 lightest … 950), plus **975**, the logo ink.
- **cream** (light) and **cocoa** (dark) are six-step surface sets: 0 sunken · 1 canvas · 2 rail · 3 surface · 4 raised · 5 overlay. *Why separate sets:* surfaces need finer steps than a perceptual ramp gives. They are hand-tuned, warm, and frozen as hex.
- **ember** (the accent) and the status hues (**red, amber, green, teal, sky, blue, violet, magenta**) are 11-step OKLCH ramps, frozen in tokens.json so a later generator change can’t move a shipped colour.

### Semantic roles (`--color-*`)

| Role | Light | Dark | Use |
|---|---|---|---|
| `bg.canvas` | `#f7f3ec` | `#1d1915` | The page |
| `bg.rail` | `#f1ebe1` | `#211c18` | Sidebar and top bar |
| `bg.surface` | `#fffdf9` | `#26211c` | Cards |
| `bg.raised` | `#ffffff` | `#2e2822` | Selected rows, nested cards |
| `bg.sunken` | `#efe9df` | `#16130f` | Wells, code |
| `text.primary` | `#2a231c` | `#f4eee4` | Titles, body (AAA everywhere) |
| `text.secondary` | `#584e43` | `#ddd3c2` | The “why” line (AA everywhere) |
| `text.tertiary` | `#6e6457` | `#ada291` | Times, metadata, machine detail (AA everywhere) |
| `text.accent` | `#a33901` | `#ff8253` | Links and accent words. Never use the fill for text. |
| `accent.default` | `#cc4a0e` | `#ff6a2b` | Fills that mean “you / act here” |
| `border.control` | `#8f8577` | `#8f8577` | Input and checkbox edges (3:1) |
| `logo.ink` | `#1c1712` | `#f4eee4` | The mark’s ink |

### Accent rule

Ember marks the places a person is needed or can act: the needs-you count and kicker, the primary button, the focus ring, the logo dot and the chart’s “now” line. It is not used for decoration, headings, ids or values. *Why:* one colour with one meaning can be scanned.

### Status: one vocabulary of marks (`status/marks.js`)

Every status mark, in a table, card, the run strip, a legend, the overview, the run page or a phone, comes from `status/marks.js`. A state has the same shape, colour and word everywhere.

| State | Mark | Colour (light · dark) | Word |
|---|---|---|---|
| Passed | filled dot, small (r 3.4 on the 16 grid) | green `#059149` · `#28ad5e` (`status.success.solid`) | green text, regular weight (`status.success.fg`) |
| Running | open arc (spins; static under reduced motion) | blue `#2f76db` · sky `#51b7e8` | blue |
| Failed | cross | wine `#8a032e` · salmon `#ffa9b0` | red text (`status.danger.fg`) |
| Needs you | diamond | ember (`accent.default`) | ember text (`text.accent`) |
| Waiting / at risk | outlined triangle | amber | amber text |
| Parked | pause bars | slate `#506279` · `#677b94` | ink |
| Not reached | dash | slate (as parked) | ink |
| Shadow | half disc | violet | violet text |
| Staged (not a status) | dashed ring | blue | blue text |

- **Passed and parked never share a hue or a shape:** a green dot vs slate pause bars.
- **Charts:** runs per hour stack from the ground: passed in a softer green (`status.success.chart`: the mark’s green mixed with the surface, ≥ 3:1 on a card), waiting or parked in a warm neutral (`run-strip.waiting`), running in `status.running.solid`, and failed on top in `status.danger.solid` with the failed mark above it. 2px gaps between layers; a failure is never thinner than 4px. A screen of passes reads calm and the rare red pops first. The caption is the key: each layer’s swatch and count.
- **The mark always carries its colour, and so does its word.** Passed is regular weight; failed, needs you and waiting are bold; parked and not reached stay ink.
- **No tinted rows, pills or capsules.** In a list, rows are grouped: **Needs attention** first (failed or needs you), then **Running now**, then **Finished**, which stays listed and is paged. Only a row that needs someone gets a 3px leading edge in its mark’s colour, its reason as a plain sentence and one action.
- **Status labels, filters, counts and tags each have their own treatment, and none is a pill:** a status label is a mark and a word; a filter is a rectangle that toggles; a count is a numeral, in ember only where a person is needed; a tag (an environment, “read only”) is a small outlined label.
- **No step trace in lists.** Step detail lives on the run page as a collapsible tree, where a nested workflow call is one node with its own status and the failing path opens by itself.
- **Every chart mark is explained:** labelled in words beside it (the run strip, `data-labelled`) or listed in its legend. Legends are generated from the same list (`legend()`).

**Checks.**
- `tools/status-cvd.mjs` (the dataviz skill’s validator, run pairwise against `bg.surface`) writes `tokens/STATUS-CVD.md`. All marks are ≥ 3:1 on every surface and no two states share a shape. With passed green, colour alone separates most pairs for colour-blind readers; where it doesn’t, shape does: passed vs waiting in light (CVD ΔE 5.6, a filled dot vs an outlined triangle), passed vs needs you in light and passed vs failed in dark (the floor band, a dot vs a diamond or a cross). Passed vs parked is clearly different in hue (ΔE 14–20) and in shape.
- `tools/mark-check.mjs` loads every page in WebKit (iPhone and desktop) and Chromium, with the OS light and dark. It fails if a rendered mark isn’t its state’s token, a chart mark is neither labelled on the chart nor in its legend, a legacy status glyph bypasses the vocabulary, or a needs-attention row’s edge differs from its mark.

The explored alternatives are on the [archive](archive/index.html).

### Data visualisation (`--color-dataviz-*`)

Built and checked with the dataviz method (`validate_palette.js`) against `bg.surface` in both themes. Full output: `tokens/dataviz-validation.txt`.

- **Categorical (8, in order, never cycled):** teal · blue · amber · sky · red · violet · green · magenta. Adjacent CVD ΔE is 16.5 light / 12.8 dark (target ≥ 8), the normal-vision floor is 18.1 / 18.9 (≥ 15), and all eight are ≥ 3:1 on the surface in both themes. The first three also pass all-pairs, so scatter plots, maps and small multiples carry at most three series. A ninth series folds into “Other”.
- **Sequential:** teal, light → dark (flipped on dark). Light ordinal ramps start at index 3, so the pale end still clears 2:1.
- **Diverging:** blue ↔ red around a neutral midpoint, for deltas against a baseline.
- **Rules:** the accent never encodes data (it is only the “now” cursor); a status-meaning series uses status tokens; labels use text tokens, not the series colour; one axis only; 2px lines, 2px gaps between segments, a hover tooltip and a table view.

### Contrast policy

Every token used as text, as a control edge or as a data mark declares the backgrounds it sits on, and `node tools/tokens.mjs` checks each pair in both themes and **exits 1** below its minimum (7 primary text, 4.5 other text, 3 for control edges, focus rings, icons and marks). Daylight currently has **302 pairs and 0 failures**. Token pairs aren’t enough on their own: see [Theming robustness](#theming-robustness), which measures what a reader actually sees.

---

## Type

All three families are under the **SIL Open Font License 1.1**, self-hosted as latin subsets with `font-display: swap`. The licences are in `fonts/OFL-*.txt`, and the loading strategy is in `fonts/fonts.css`.

| Family | Role | Weights | Notes |
|---|---|---|---|
| **Fraunces** (Undercase Type) | Page and section titles, one hero number, the wordmark | 600, upright | Soft axis on (`'SOFT' 100`) for titles; the wordmark uses `'SOFT' 0`. Never italic. Preloaded. |
| **Atkinson Hyperlegible Next** (Braille Institute) | Everything people read and act on | 400 · 600 · 700 | True tabular figures. Preloaded. |
| **Atkinson Hyperlegible Mono** (Braille Institute) | Only literal code, ids, YAML, logs | 400 · 500 | Loaded on demand. |

**Weights:** 400 for body and cells; 600 for buttons, tabs, nav, labels and Fraunces titles; 700 for item titles and the bold lead-in of a sentence. **Numbers:** `tabular-nums` in every table, counter and timer. Our Fraunces subset has no tabular figures, so serif numbers are limited to one hero value per card.

### Scale (1rem = 16px)

| Token | px | Line | Face | Use |
|---|---|---|---|---|
| `display-xl` | 72 | 0.98 | Fraunces 600 | Brand pages, social card |
| `display` | 48 | 1.04 | Fraunces 600 | Docs landing, onboarding |
| `h1` | 34 | 1.1 | Fraunces 600 | Page titles |
| `h2` | 21 | 1.25 | Fraunces 600 | Card and section titles |
| `h3` | 17 | 1.35 | Atkinson 700 | Item and dialog titles; anything long or generated |
| `h4` | 15 | 1.4 | Atkinson 700 | Sub-sections, list titles |
| `body-l` | 17 | 1.55 | Atkinson 400 | Lead sentences, docs, README |
| `body-m` | 15 | 1.5 | Atkinson 400 | **Dashboard default** |
| `body-s` | 13.5 | 1.45 | Atkinson 400 | Secondary lines |
| `label` | 14 | 1.2 | Atkinson 600 | Buttons, tabs, labels, nav |
| `caption` | 12.5 | 1.4 | Atkinson 400 | Times, chart labels |
| `overline` | 11.5 | 1.3 | Atkinson 700 caps | Rare group labels |
| `numeric-l` | 30 | 1.0 | Fraunces 600 | One hero number |
| `brand` | 21 | 1.0 | Fraunces 600 | Live-text wordmark |
| `code` | 13 | 1.6 | Atkinson Mono | Code, YAML, logs |
| `code-s` | 12 | 1.45 | Atkinson Mono | Inline ids, last on the line |

Use `font: var(--type-h1); letter-spacing: var(--type-h1-tracking); font-variation-settings: var(--type-h1-variation);`.

**Serif rule:** five words or fewer, written by a person, 20px or larger, in a title role. Generated titles (proposal and run titles) are `h3` in Atkinson, with the machine details on a mono line below.

---

## Space, layout and density

- **Spacing:** a 4px base (`--space-*`: 0, 2, 4, 6, 8, 12, 16, 20, 24, 32, 40, 48, 64, 80, 96).
- **Breakpoints:** phone below 600px (designed at 390), tablet from 600 (834), desktop from 1024 (1440), wide from 1600 (1920). Pages are capped at 1280px.
- **Phone:** bottom tab bar, sheets instead of drawers, comfortable density only, 44px targets, approvals pinned above the tab bar.
- **Density** (`data-density="compact"` on any element): rows 52 → 36, controls 38 → 32, the top bar 60 → 48, the rail 236 → 212, card padding 22 → 16, page padding 36 → 20, table text 15 → 13.5. Type sizes otherwise stay the same: density buys rows, not smaller text.

## Shape and depth

- **Radii:** 4 · 6 · 10 (controls, wells) · 14 (cards) · 18 (overlays) · 22 (phone sheets) · pill (round avatars only; nothing else is a pill).
- **Elevation:** `--shadow-0…4`, warm brown-tinted on light (soft and diffuse) and simply darker on dark, where the lighter surface step carries the depth. Borders are 1px `border.subtle`. The focus ring is 2px with a 2px offset.

## Iconography

- **Set:** [Lucide](https://lucide.dev) (ISC licence). The subset in use is vendored in `icons/lucide/` with its licence.
- **One icon per kind of thing** (`status/rows.js`): approval `stamp` · run `circle-play` · bot `bot` · team `users` · workflow `workflow` · pull request `git-pull-request` · incident `siren` · deploy `rocket` · Terraform `layers` · cloud spend `receipt` · connector `plug` · login `key-round` · knowledge `book-open` · config change `settings-2`. A run’s state is its status mark beside it, never a different icon.
- **One icon per kind of action:** reads `eye` · writes / updates `pencil-line` · comments / posts `message-square` · pushes code `git-commit-horizontal` · applies infra `hammer` · spends money `banknote` · deletes / destroys `trash-2` · changes permissions `lock-open`.
- **Rules:** one meaning per icon, used for nothing else; always next to its word; never decorative; legible at 16px (`tools/icon-check.mjs` measures every glyph’s real 16px pixels).
- **Environment tags:** Production (solid square, strong outline, bold) · Staging (outlined square with a centre) · Local (dashed circle). Told apart by fill and shape, never by colour alone.
- **Style:** outline only, round caps and joins, on a 24-unit grid; drawn at a 1.75 stroke at 16–20px and 1.5 at 24px.
- **Sizes:** 16–18 beside body text, 14 in compact density, 20 in pickers, 22–24 in the phone tab bar.
- **Colour:** `icon.default` beside text and `icon.muted` in secondary positions; a status colour only on status marks.
- **Status marks** (`icons/status/`, ours): passed, running, failed, needs you, waiting, parked, not reached, shadow and staged, each a distinct *shape* on a 16-unit grid, so they work in one colour.
- **The passkey icon** (`fingerprint`) appears only on actions that authenticate: approve, deny and changes that widen access.
- **Never** show an icon alone without its word (except a universal ×), fill an outline icon or mix sets.

## Motion

- **Durations:** fast 120 (hover, press) · base 200 (reveal, tab content) · slow 320 (drawers, dialogs, sheets) · slower 480 (page reveals, chart draw-in) · spin 1200 (running glyph) · pulse 2400 (live dot).
- **Easing:** `out` (0.2, 0.7, 0.2, 1) is the default for arrivals and responses; `in` is for exits; `in-out` is for moves across the screen; `linear` is for loops only.
- **Moves:** colour changes, drawers and sheets from their edge, dialogs fading and rising 8px, toasts 4px, the running glyph and live dot, and the skeleton shimmer.
- **Never moves:** anything after an approval or deploy (no confetti), counting numbers, the logo, content under the pointer, layout after load, page transitions that delay content, and anything flashing more than three times a second.
- **Reduced motion** (the OS setting or `data-motion="reduced"`): travel distances become 0, spin and pulse stop (the running glyph stays as a static arc, labelled), and long durations become 120ms. Colour and opacity transitions remain.

## Components

The book shows each component's states (default, hover, focus, active, disabled, error, loading) in both themes and at either density. `book/components.css` shows the mapping onto CSS. The component tokens (`--button-*`, `--input-*`, `--table-*`…) alias semantic tokens, so a component never knows its theme or density.

- **Buttons:** primary (one per view, for what the person came to do), secondary, ghost and danger (destructive only, always confirmed); sizes sm / md / lg; loading keeps the label and adds the running glyph. Labels are verbs.
- **Fields:** label above, helper text below that turns into the error message. Errors show on blur or submit, never on an untouched form (the newcomer saw new workflows open “already red with errors”). Edges use `border.control`.
- **Value source:** the source is a quiet menu inside the field. Secret is the default; Vault item and Environment variable are listed; typed value and setting sit under “Other sources”. This replaces the five-way segmented control on every field.
- **Choice cards:** for decisions that need a sentence each (permission presets, connector methods). Five at most.
- **Tabs** switch views of one thing, and **segmented controls** switch a setting. Neither navigates.
- **Tables and dense lists:** the fixed row anatomy; grouped needs attention → running now → finished, with finished rows listed and paged. Text left, numbers right in tabular figures, status as mark + word, ids in mono at the end, focusable rows (j/k, x, Enter), and selection as the accent wash plus a 3px bar. No step traces in rows.
- **Status labels, filters, counts and tags:** a status label is a mark and a word; a filter is a rectangle that toggles; a count is a numeral (ember only where a person is needed); a tag is a small outlined label. None of them is a pill.
- **Approvals:** the strip above the title (what it is, environment or risk words, status, age and expiry), the title as a question, the why. Overview / Technical details tabs only when technical detail matters to the decision (a Terraform plan, colourised); otherwise details are folded. A sticky bar at the bottom holds Approve and Deny, both with the passkey; it never covers the title or the why.
- **Banners** are for persistent conditions. **Toasts** are for events: 6 seconds (errors stay), above the phone tab bar, with an action.
- **Dialogs** are for decisions, **drawers** for detail beside a list, and **sheets** for the phone. All of them trap focus, close on Esc and return focus.
- **Steppers:** at most five steps, each named for the decision, not the file. The review lives inside the wizard, and Back keeps input.
- **Empty states:** say what appears here, why it's empty, and the one next step, in a plain sentence with an action if there is one. No illustration, and never the logo as decoration.
- **Code, YAML and diffs:** the only place mono is used at length: `code` size on `bg.sunken`, with syntax tokens at AA on sunken and surface. **No space before a colon**, ever. Diffs use +/− gutter marks and a 2px bar as well as the tint. The header names the file and the enclosing key, not only a line number.

## Voice and tone

conductor talks to someone who is nervous about giving an AI access to production. It earns trust by being exact, not reassuring.

1. **Plain words first**, then the term: “Asks you first (propose)”.
2. **Say what changes:** what, where, how much, and whether it can be undone.
3. **State the risk plainly:** “homelab will be able to sign in to nas.home.example as you.”
4. **No internals:** no issue numbers, codenames (“hoff”, “rung”) or raw ids in headings. Hashes go under “Technical details”.
5. **Define every noun once,** with a dotted underline and a one-line definition on first use.
6. **Verbs on buttons:** “Delete workflow”, never “OK” or “Submit”.
7. **Humans read times:** “Mondays at 06:00”, “52 minutes ago”, with the exact value on hover.
8. **Calm, not cute:** no exclamation marks, “oops”, “please” or “I'm thinking”. Only bots speak in the first person, and only in chat.

**Names:** bot · team · workflow · trigger · step · run · approval · case · pack · plugin · connector · host · login · secret, each defined once in the book’s table and used the same way in the UI, docs, CLI and README. Bots are grouped by team (never “members”; there are no folders); **Activity** holds the run history; **System** is the area for Config, Secrets, Packs, Connections, Updates, Logs & audit and Access.

**Fixed risk vocabulary:** “Does on its own” · “Asks you first” · “Never”, with the tier name second.

**Patterns** (rendered in the book):
- **Approvals** show *Why* (the reason, linked to the case), *Impact* (where, how much, how long) and *Undo* (how, and for how long), then “Approve” and “Deny…”, both with the passkey because both are signed. Technical details are folded, or a tab when they matter to the decision.
- **Errors:** what happened, why, and what to do, with the raw code last, in mono.
- **Validation:** “Pick a channel to post to.”, not the schema path.
- **Confirmations:** the title states the action and object (“Delete the nightly-backup workflow?”) and the body states the consequences. A change that widens access needs a plain-words acknowledgement (“I understand homelab can sign in as me”). Never ask “Are you sure?”.
- **Toasts** are past tense and specific: “Approved. conductor is applying the plan.”, with a link.

---

## Bot identity

A bot is recognisable at a glance: its **face**, its **colour**, a **display name** (separate from its config id), a one-line **tagline** and a **voice**. The face and colour belong to the bot; they never stand for a state. The book shows the full set (*Bot identity*).

- **Faces** (`bot-identity/faces/`, one SVG each, with `manifest.json`): twelve friendly robot faces, each with its own head shape and expression: `beam` (cheerful), `focus` (focused), `curio` (curious), `doze` (sleepy), `grit` (determined), `imp` (mischievous), `calm` (calm), `wow` (surprised), `wink` (playful), `shy` (bashful), `chirp` (excited), `steady` (steady). The ids are the config’s `avatar.preset` values: stable, never renamed; add a face rather than change one. Drawn on a 48-unit grid with bold features, to read at 24px (rows, the facts line) and look good at 96px (the bot page, the chat header). Daylight’s own drawings, **never derived from the logo**: no arches and no dot under an arch. **Every face takes every colour:** the head is the bot’s colour and the features are always the theme’s ink, so the face and the colour are two separate choices and all 84 pairs work in both themes (the book shows the full face × colour grid).
- **Colours** (`--color-bot-<name>-bg`, `-fg`, `-rule`; `tools/bot-palette.mjs`): olive, lime, teal, lagoon, plum, rose and stone. Every hue is at least 20° from every status hue, and the fills differ in lightness and chroma from the status marks, so a bot’s colour never reads as a state. Pastel in light, deep in dark; the face is drawn in the theme’s ink and is AA on every fill. The checks are in [`bot-identity/PALETTE.md`](bot-identity/PALETTE.md).
- **Where the colour goes:** behind the bot’s face, wherever it appears, and a 3px rule on the bot’s own chat messages (`-rule`: the other theme’s fill, 3:1 on either page). The bot’s name stays ink. **Never** on status marks, buttons, links, counts, row edges, charts, focus rings or text.
- **An icon of your own:** any Lucide icon plus one of the seven colours, except the icons that already mean something (the object and action icons, the status marks, the nav icons, the passkey).
- **Fallback:** the initials (first letters of the first two words of the name) on a colour chosen from the bot’s id, so it is the same everywhere.
- **Voice presets:** Plain (the default), Warm, Terse and Playful (a light touch in chat only). Every preset keeps the voice rules above; approvals, errors and risk are always in conductor’s own voice.

## Theming robustness

**The iPhone bug.** On an iPhone (paseo’s in-app browser, WebKit, OS in dark mode), an early direction page (now on the archive) showed faint, light-grey text on the cream page, and every specimen labelled LIGHT rendered with a dark surface. **Cause:** the pages never declared a colour scheme. With no `color-scheme`, browsers that auto-darken pages repaint their colours: Chromium’s force-dark, and in-app WebViews that apply “algorithmic darkening”. That flipped our forced-light surfaces dark while some text was left in its light-theme colours. The token-pair contrast check passed, because the tokens were right; what the reader saw was not.

**The fix, at the source** (`tools/tokens.mjs` generates it into `tokens.css`):
- `:root { color-scheme: light dark; }` and `<meta name="color-scheme" content="light dark">` on every page, so the browser knows the page handles both and never auto-darkens it.
- `[data-theme="light"] { color-scheme: only light; }` and `[data-theme="dark"] { color-scheme: only dark; }`, declared on the element after the media query. An explicit theme always wins over the OS, at any nesting depth, and `only` forbids the UA from adjusting it.
- Light-only documents (the proposal pages) declare `color-scheme: only light`. Scoped specimens carry every token value plus their own `color-scheme`, so they are self-contained.

**Tests** (run sequentially; caches on local disk, `~/.cache/conductor-design/`):
- `node tools/theme-test.mjs` loads `tokens/test/theme-fixture.html` in every engine (WebKit with the iPhone 15 profile, WebKit desktop, Chromium desktop, and Chromium phone with force-dark), with the OS light and dark, and the root auto, light and dark. It checks every nested scope’s computed background and text against the tokens, **and the painted pixels** (which catches browser recolouring). There are 357 checks and 0 failures.
- `node tools/rendered-contrast.mjs` loads every page in every engine and OS mode and measures each visible text element’s computed colour, including opacity, over its composited background (WCAG AA: 4.5, or 3 for large text). Deliberate demos (disabled states, misuse examples, verbatim “before” screenshots) are marked `data-contrast="exempt"` and counted. The report goes to `~/.cache/conductor-design/reports/`.

**Don’t hide text with `font-size: 0`.** WebKit (the iPhone engine) aborts the page when text set in some variable webfonts is laid out at size 0. The book’s ramp labels crashed it on the iPhone profile. Clip instead: `color: transparent; overflow: hidden`, or a visually-hidden class.

---

## Tokens

- **Format:** [W3C Design Tokens](https://www.designtokens.org/) (DTCG 2025.10): colours as `{colorSpace, components, alpha?, hex}`, dimensions as `{value, unit}`, durations, cubic-béziers, shadows, and typography composites. Three tiers: `primitive` (raw values) → `semantic` (roles) → `component` (aliases of semantic only).
- **Modes:** mode-dependent tokens list every mode in `$extensions.mode` (the Terrazzo convention): `dark`/`light`, `comfortable`/`compact` and `standard`/`reduced`. `$value` is the default (dark, comfortable, standard); Daylight pages render light unless the OS or `data-theme` says dark.
- **CSS (`tokens/tokens.css`):** `--palette-*` (raw: never in components), `--color-*`, `--shadow-*`, `--type-*`, `--size-*`, `--pad-*`, `--gap-*`, `--radius-*`, `--border-*`, `--motion-*`, `--z-*`, `--layout-*` (semantic), and `--button-*`, `--input-*` and the like (component). The theme follows `prefers-color-scheme`; `[data-theme]`, `[data-density]` and `[data-motion]` work on **any element**, so a page can show both themes side by side (the book does).
- **Generator:** `node tools/tokens.mjs` writes `tokens.css`, `prototype-compat.css`, `contrast.json`, `CONTRAST.md` and `migration.json`, and fails on any contrast regression. `--check` only verifies. Other tools: `tools/ramps.mjs` (ramp derivation, run once), `tools/icons.mjs` (icon bundle for the book), and `tools/build.mjs` and `tools/render.mjs` (logo assets).
- **Adding a token:** add it to `tokens.json` at the lowest tier that makes sense. Declare its contrast intent if it colours text or marks, then re-run the generator.

---

## Migrating the dashboard (one pass)

The prototype adopts Daylight in one pass:

1. **Fonts:** copy `fonts/` (Fraunces, Atkinson Hyperlegible Next and Mono, with their OFL files) and load `fonts/fonts.css`. Preload Atkinson Next and Fraunces.
2. **Tokens:** load `tokens/tokens.css`, add `<meta name="color-scheme" content="light dark">`, and delete the `:root` and theme blocks in `css/app.css` (its lines 9–51). If you want the old names to keep working while you edit, also load `tokens/prototype-compat.css` and delete it at the end.
3. **Rename** every prototype variable to its Daylight token, using the table in `tokens/migration.json` (also in the book and summarised below). Each row has a note where the meaning or value changes, not just the name. The ones that change behaviour:
   - `--accent` used as **text** (43 places across the CSS): use `--color-text-accent`. Keep `--color-accent-default` for fills.
   - `--ink-3` → `--color-text-tertiary` (now AA on cards).
   - `--ok`, `--run`, `--wait`, `--fail`, `--idle`, `--shadow`: use `-fg` for text and glyphs, `-solid` for dots and bars. **Passed is green** (mark and word, regular weight), but rows are never tinted green.
   - Inputs and checkboxes: `--color-border-control`.
4. **Type:** body to `--type-body-m` (15px). Titles to `--type-h1`/`--type-h2` (Fraunces, upright), with long or generated titles (proposal, run and step titles) moved to `--type-h3` in the sans. **Decorative mono becomes sans with tabular figures**: the top strip, crumbs, counts, times and costs. Mono stays only for ids, digests, YAML and logs, at least 12px.
5. **Shape:** radii from `--radius-*` (controls 10, cards 14, tags 3; nothing is a pill), shadows from `--shadow-*`. Drop the film grain and the dot grid on app surfaces.
6. **Information design:** apply the rules and patterns above: the row anatomy with icons and environment tags; the approved Overview (At a glance, Needs you, Running now capped, the runs-per-hour strip, For your information, Get started); Activity with the strip, exceptions by workflow and the paged run list; the step tree on run pages; the approval tabs and sticky decision bar. Remove the greeting, the summary sentences that restate the screen, run tabs, previous/next between runs, export buttons, Pause/Run on the overview, pills and tinted rows.
7. **Density:** `data-density="compact"` on `<html>` from a per-browser display preference. Replace fixed row and padding values with `--size-row`, `--pad-cell-*`, `--pad-card` and `--pad-page-*`. `.tbl.compact` becomes redundant.
8. **Logo:** copy `design/brand/assets/` into the prototype and follow its README: the favicon links, the strip mark in `js/ui.js` and the gate mark in `js/screens/access.js` (both from `mark-inline.html`), and the live-text wordmark in `--type-brand`.

| Prototype | Daylight token | Note |
|---|---|---|
| `--font-ui` | `--font-family-sans` | Instrument Sans → Atkinson Hyperlegible Next. Body goes 13.5 → 15px (--type-body-m). |
| `--font-title` | `--font-family-serif` | Instrument Serif italic → Fraunces SemiBold, upright. Use the --type-h1 / --type-h2 shorthands plus their -tracking and -variation. Long or generated titles move to --type-h3 (sans). |
| `--font-mono` | `--font-family-mono` | Martian Mono → Atkinson Hyperlegible Mono, and only for literal code, ids, YAML and logs. Strip cells, crumbs, counts, times and costs move to the sans with tabular-nums. |
| `--r-xs` | `--radius-badge` | 3 → 3: tags only. Nothing is a pill. |
| `--r-sm` | `--radius-control` | 5 → 10. |
| `--r` | `--radius-card` | 8 → 14. |
| `--r-lg` | `--radius-overlay` | 12 → 18. |
| `--rail-w` | `--size-rail` | 224 → 236 (212 compact). |
| `--strip-h` | `--size-strip` | 34 → 60 (48 compact): the mono status strip becomes a quiet top bar (search, health, avatar). |
| `--ease` | `--motion-easing-out` |  |
| `--t-fast` | `--motion-duration-fast` | Drops to 0 under reduced motion. |
| `--t` | `--motion-duration-base` | 220 → 200ms. |
| `--bg` | `--color-bg-canvas` | Dark #0f100d → cocoa #1d1915; light #efeadf → cream #f7f3ec. Daylight is light-first. |
| `--bg-1` | `--color-bg-rail` | Dark #13140f → #211c18; light #f4f0e6 → #f1ebe1. |
| `--bg-2` | `--color-bg-surface` | Dark #181914 → #26211c; light #f9f6ef → #fffdf9. |
| `--bg-3` | `--color-bg-raised` | Dark #1f201a → #2e2822; light #fffdf8 → #ffffff. |
| `--bg-4` | `--color-bg-overlay` | Dark #272821 → #36302a; light #ffffff. |
| `--line` | `--color-border-subtle` | Dark #2a2b23 → #352e27; light #ddd6c5 → #ece5d8. |
| `--line-2` | `--color-border-default` | Inputs and checkboxes use --color-border-control (3:1) instead. |
| `--line-3` | `--color-border-strong` |  |
| `--ink` | `--color-text-primary` | Dark #ece8dc → #f4eee4; light #1b1a15 → #2a231c. |
| `--ink-2` | `--color-text-secondary` | Dark #b6b1a2 → #ddd3c2; light #514d43 → #584e43. |
| `--ink-3` | `--color-text-tertiary` | Changed for AA: dark #807c6f → #ada291, light #847f71 → #6e6457. The old values failed 4.5:1 on cards. |
| `--ink-4` | `--color-text-disabled` | Disabled controls only. |
| `--accent` | `--color-accent-default` | Fills only (primary buttons, the active nav bar, the logo dot). Counts are ember text. Where it colours text (links, .cell.alert, crumbs:hover; 43 places), use --color-text-accent. |
| `--accent-2` | `--color-accent-hover` |  |
| `--accent-ink` | `--color-text-on-accent` |  |
| `--accent-wash` | `--color-accent-subtle` |  |
| `--ok` | `--color-status-success-fg` | The passed word is green (this token); the passed dot is --color-status-success-solid; chart bars for passed runs are --color-status-success-chart. Rows are never tinted. |
| `--run` | `--color-status-running-fg` | Dots and bars: --color-status-running-solid. |
| `--wait` | `--color-status-warning-fg` | Dots and bars: --color-status-warning-solid. |
| `--fail` | `--color-status-danger-fg` | Dots and bars: --color-status-danger-solid. |
| `--idle` | `--color-status-neutral-fg` |  |
| `--shadow` | `--color-status-info-fg` | Shadow mode is shown as info (observes, never acts) with its own glyph. |
| `--ok-wash` | `--color-status-success-bg` |  |
| `--run-wash` | `--color-status-running-bg` |  |
| `--wait-wash` | `--color-status-warning-bg` |  |
| `--fail-wash` | `--color-status-danger-bg` |  |
| `--idle-wash` | `--color-status-neutral-bg` |  |
| `--shadow-wash` | `--color-status-info-bg` |  |
| `--code-key` | `--color-code-key` |  |
| `--code-str` | `--color-code-string` |  |
| `--code-num` | `--color-code-number` |  |
| `--code-com` | `--color-code-comment` | Light #9a9482 → #6e6457 (comments were 2.9:1). |
| `--code-anchor` | `--color-code-anchor` |  |
| `--code-bool` | `--color-code-bool` |  |
| `--dot` | `--color-decor-dot` | Daylight drops the dot grid on app surfaces; keep it for brand pages only. |
| `--elev` | `--shadow-2` | Warm, softer shadows in light. |
| `--elev-pop` | `--shadow-3` | The old value included a 1px ring; add border: 1px solid var(--color-border-default). |
| `--grain-op` | `--opacity-grain` | Daylight drops the film grain on app surfaces. |

---

## The README change (ready; lands with the dashboard work)

**Don’t edit the README yet.** This lands when the dashboard work goes into the real repo. Then:

1. Replace README lines 1–5 (the `<h1 align="center">conductor</h1>` heading and the `conductor.png` image) with:

   ```html
   <p align="center">
     <picture>
       <source media="(prefers-color-scheme: dark)" srcset="design/brand/assets/logo-horizontal-dark.svg">
       <img src="design/brand/assets/logo-horizontal.svg" alt="conductor" width="360">
     </picture>
   </p>
   ```

   The lockup carries the name, so the `<h1>` goes. Keep the tagline paragraph that follows as it is.
2. Delete `conductor.png` (808 KB) once nothing references it (`git grep conductor.png`).
3. Upload `design/brand/assets/social-card.png` under GitHub → Settings → General → Social preview.
4. If the brand files move to `.github/brand/`, update the two `srcset`/`src` paths.

---

## Open decisions

1. **Where the brand files live long-term:** `design/brand/` or `.github/brand/`, which sets the README paths.
2. **The visual marker for risk** (beyond the words): the proposal is on the archive and is not part of the system until it is decided.
