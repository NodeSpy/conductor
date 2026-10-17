# conductor brand and design system: Daylight

This is the single source of truth for how conductor looks, reads and behaves on every surface: the README and docs, the dashboard, and anything after them. This file is the written guide. Each rule comes with its reason.

| | |
|---|---|
| **Brand book** (live, both themes, both densities) | [`system.html`](system.html) |
| **Tokens** (source of truth, W3C DTCG) | [`tokens/tokens.json`](tokens/tokens.json) → generated [`tokens/tokens.css`](tokens/tokens.css) |
| **Prototype → Daylight mapping** | [`tokens/migration.json`](tokens/migration.json), [`tokens/prototype-compat.css`](tokens/prototype-compat.css), and the table in the book (*Tokens › Migrating the dashboard*) |
| **Contrast report** (every declared pair, both themes) | [`tokens/CONTRAST.md`](tokens/CONTRAST.md) |
| **Logo** (official kit, one canonical location) | [`assets/`](assets/); the choice is recorded on [`logo-review.html`](logo-review.html) |
| **How we got here** | [`index.html`](index.html) (logo directions), [`directions-v2.html`](directions-v2.html) (system directions) |

**Daylight** is warm, light-first and built to be read: conductor as a calm colleague who tells you how things are going in a sentence before showing you a table. Daniel chose it over Broadsheet (editorial, densest) and Score (timeline-first, most graphic). It borrows one pattern from each: Broadsheet’s **“since you last looked”** digest for the overview, and Score’s **activity lanes** for the Activity page.

The logo is **Fermata, strengthened**: the hold-until-released arch, chosen by Daniel in October 2026. Its kit is in [`assets/`](assets/).

---

## Decisions at a glance

| Area | Decision | Why |
|---|---|---|
| Direction | Daylight: warm, light-first, sentence-first | Daniel found round one “engineery” and generic, and wanted conductor’s firehose easier to consume. Daylight leads with plain language and keeps colour for what needs attention. |
| Information design | Ten rules (below): summary first, plain headline over technical detail, three type levels, colour only for attention… | These are the heart of the system. The audits’ biggest problems (no first step, “what can my bots do?”, hashes up front) were information problems, not paint. |
| Palette | Cream and cocoa surfaces, sand neutrals, one ember accent | Warm paper reads as a product rather than a terminal, and one accent keeps “where do I act?” unambiguous. |
| Status | Colour only on what needs attention; passing work is ink | When eleven of sixteen rows are green, green means nothing and the two red rows are harder to find. |
| Type | Fraunces (titles) · Atkinson Hyperlegible Next (everything read) · Atkinson Hyperlegible Mono (code only) | A soft, upright serif gives a brand voice without the hard-to-read long italics of round one. Atkinson was designed by the Braille Institute so 1/l/I and 0/O stay distinct, which matters for ids and numbers. Mono stops being a decorative “machine voice”. |
| Size | Body 15px, lead 17px; nothing under 11.5px; mono ≥ 12px | The audits flagged “tiny mono text everywhere”. |
| Shape | Rounded: 10px controls, 14px cards, pill badges, warm soft shadows | Approachable, and the rounding groups content without extra rules. |
| Density | Comfortable 52px rows; compact 36px as a token set | Newcomers get room to read; the power user’s compact mode is the same content, tighter. |
| Theming robustness | Every page declares `color-scheme`; an explicit `data-theme` always beats the OS | The iPhone bug (below). |
| Tokens | Same DTCG architecture, generator and contrast gate as round one | Only the values changed. The gate checks 302 pairs; 0 fail. |

---

## Reading at a glance (information design)

conductor produces runs, cases, approvals, logs, diffs and bot activity around the clock. A person must be able to answer three questions in seconds: **does anything need me, is anything broken, and is everything else fine?** Every screen follows these rules. Before/after examples are in the book (*Reading at a glance*).

1. **Summary first, detail on demand.** Every screen opens with one plain sentence (“**5 things need you.** Everything else is running normally: 18 of 20 runs in the last 12 hours went fine.”). Health checks, raw counts and system internals sit one step down, never across the top of every page.
2. **A plain headline, the technical detail beneath.** “Replace the production web server”, then why it matters, then `p12 · infra@3f2a9c1d · +1 ~2 −0` in mono, last.
3. **Three levels of type, and only three.** What (title or bold lead-in), why it matters (regular, secondary ink), machine detail (mono, small, tertiary, at the end).
4. **Colour only for what needs attention.** Passing work is ink. Failed, waiting, running and needs-you get colour, always with a glyph and a word.
5. **Numbers line up.** Durations, costs and counts are right-aligned in tabular figures, with units (“12 min”, “$1.12”).
6. **Group and chunk.** Things to act on (“Needs you”) are separate from things that are merely true (“For your information”: an update that installs itself is not “needing you”). Groups are separated by space and a count, not boxes inside boxes.
7. **Lead with what changed.** Coming back, you see “Since you last looked” (counts, one line each, linked) before any feed.
8. **Timelines tell the story.** A run reads top to bottom: each step’s outcome in a sentence, the duration on the right, the failing step called out, and later steps marked “Not reached”.
9. **Human times and schedules.** “52 min ago (17:03)”, “Mondays at 06:00”. Cron strings and exact timestamps go on hover; ids and hashes are never first on a line.
10. **Whitespace that groups, density on request.** Comfortable by default; compact is the same content in the same order, only tighter.

### Patterns

- **Summary header:** title, one sentence that answers the page’s question (bold count first when something needs you), then actions.
- **Needs you list:** glyph · plain title · why it matters · machine details last · age · one verb. Approvals get the primary button.
- **Since you last looked** (from Broadsheet): the overview leads with what changed since your last visit, as counts with one line each. New failures and approvals carry their status colour; everything else is ink.
- **Run story:** a run as steps in sentences, top to bottom, with durations on the right.
- **Activity lanes** (from Score), for the Activity page: one named lane per workflow, time left to right, every run a mark whose shape and colour give its state (ink dot passed, red cross failed, blue arc running, ember diamond needs you, bars parked), and the “now” line in the accent. Lanes are labelled, not coloured.
- **Approval card:** needs-you kicker · plain title · what it wants to run · *Why* · what changes (adds / changes / replaces / destroys) · *Where / Effect / Undo* · origin · “Approve with passkey” / “Deny…” · technical details collapsed. On a phone the decision buttons stay pinned above the tab bar.

---

## Logo

**The idea.** A fermata tells an ensemble to hold until the conductor releases it. conductor works the same way: runs wait behind an approval, a bot pauses on “needs you”, and nothing moves until you sign. The arch over a point also reads as a signal arc over its source. The mark is an arch in logo ink and a dot in the ember accent.

**Why this drawing.** Round one’s Fermata was weak on light: on cream, its thin arch, small dot and light italic wordmark lost to the surface. The official mark is redrawn for light:
- **Arch:** crown 4 → 5.5 units, feet 3.2 → 5.
- **Dot:** 25% bigger, and higher under the arch.
- **Ink:** deeper on cream (`--color-logo-ink`, sand 975 `#1c1712`, deeper than body text).
- **Wordmark:** Fraunces SemiBold, Daylight’s heading face, instead of the thin italic.
- **16px cut:** redrawn with whole-pixel 3px feet.
- **No rays or baseline:** the sunrise rays and hairline baseline are not part of the mark. They cluttered it at small sizes, so they stay in illustrations only (the social card, empty states).

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
| `social-card.png` | 1280×640 repository social preview: cream, the lockup, one sentence, and the arch rising from the corner. |
| `README.md` | The file list and the exact dashboard-prototype changes. |

### Usage

- **Clear space:** one dot diameter on every side.
- **Minimum size:** mark 16px (use the 16px cut, never a scaled master), horizontal lockup 20px tall, stacked 72px wide.
- **In app chrome:** the inline mark at 20px tall beside the live-text wordmark (`--type-brand`: Fraunces SemiBold, `'SOFT' 0`).
- **Do** keep the dot in the accent and the arch in logo ink, or set the whole mark in one colour (ink, white, black, or white on ember).
- **Don’t** stretch, rotate (upside down the arch is a frown), recolour the arch, add effects, outline it, re-type the wordmark, place it on busy imagery without a solid field, or use it as a spinner.
- **Co-branding:** the partner logo goes to the right at equal optical height, after a hairline rule with two dot-widths of space on each side. conductor comes first on our surfaces, and the marks are never merged.
- **Never** bring back the mascot, the top hat or the Octocat pin.

### Explored directions (not for use)

Each was taken to a complete kit and tested in the same dashboard chrome before the choice. The round-two kits are in `logo-kits/` and round one’s are in `directions/`, kept for reference.

- **Gate** (a doorway with the dot standing in it): the clearest favicon of the set, but beside the lowercase wordmark its arch-with-legs reads as a bold “n”.
- **Cue** (a disc that gives way to the dot): the boldest silhouette, but a disc and a dot is abstract and says nothing about conductor without the name.
- **Conduit** (a C as a circuit, the dot passing through its gap): clear and strong, but a C with a dot is a crowded genre and it carries the least of conductor’s own idea.
- **Fermata, round one:** the same idea, but too thin on light. It became the official mark once strengthened.
- **Downbeat** (the prototype’s baton, rebuilt): a diagonal and a dot is a common shape with the least meaning.
- **Signature** (the wordmark as the logo): elegant but not ownable, and its brass accent collided with the warning colour.

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

### Status (`--color-status-{success,warning,danger,info,running,neutral}-{fg,bg,border,solid}`)

| State | Hue | Glyph | Means |
|---|---|---|---|
| success | green | check in a circle | done, passed, healthy |
| warning | amber | triangle | needs attention soon, degraded, waiting on something |
| danger | red | cross in a circle | failed, refused, destructive |
| info | violet | i in a circle (shadow mode: half-filled circle) | news, updates, shadow mode (observes, never acts) |
| running | sky | rotating arc | in progress right now |
| neutral | warm grey | pause bars (skipped: dash) | parked, idle, skipped, cancelled |

`fg` is for text and glyphs (AA on every surface and on its own wash). `bg` is a translucent wash, `border` is decorative, and `solid` is for dots and bars (3:1, never text). **Always pair the colour with its glyph and a word.** “Needs you” is not a status. It uses the accent with a diamond glyph.

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

**Weights:** 400 for body and cells; 600 for buttons, tabs, nav, labels and Fraunces titles; 700 for item titles and the bold lead-in of a summary sentence. **Numbers:** `tabular-nums` in every table, counter and timer. Our Fraunces subset has no tabular figures, so serif numbers are limited to one hero value per card.

### Scale (1rem = 16px)

| Token | px | Line | Face | Use |
|---|---|---|---|---|
| `display-xl` | 72 | 0.98 | Fraunces 600 | Brand pages, social card |
| `display` | 48 | 1.04 | Fraunces 600 | Docs landing, onboarding |
| `h1` | 34 | 1.1 | Fraunces 600 | Page titles and greetings |
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

- **Radii:** 4 · 6 · 10 (controls, wells) · 14 (cards) · 18 (overlays) · 22 (phone sheets) · pill (badges, counts, chips).
- **Elevation:** `--shadow-0…4`, warm brown-tinted on light (soft and diffuse) and simply darker on dark, where the lighter surface step carries the depth. Borders are 1px `border.subtle`. The focus ring is 2px with a 2px offset.

## Iconography

- **Set:** [Lucide](https://lucide.dev) (ISC licence). The subset in use is vendored in `icons/lucide/` with its licence. Each concept gets exactly one icon (see the grid in the book).
- **Style:** outline only, with round caps and joins, on a 24-unit grid with a 2-unit live-area padding. Daylight draws Lucide at a 1.75 stroke (rounded, a little softer than its default 2) at 16–20px, and 1.5 at 24px.
- **Sizes:** 18 beside body text (16 compact), 14 in badges, 20 in empty states and pickers, 22–24 in the phone tab bar.
- **Colour:** `icon.default` beside text, `icon.muted` for decoration, and a status colour only on status glyphs.
- **Status glyphs** (`icons/status/`, ours): success, warning, danger, info, running, neutral, skipped, shadow and needs-you, drawn on a 16-unit grid with a 1.5 stroke. Each has a distinct *shape*, so they work in one colour.
- **Never** show an icon alone without a label or tooltip (except a universal ×), fill an outline icon or mix sets. New icons follow Lucide's grid and stroke.

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
- **Tables and dense lists:** text left, numbers right in tabular figures, status as glyph + word, ids in mono at the end of the row, passing rows in ink (colour only on rows that need attention), focusable rows (j/k, x, Enter), and selection as the accent wash plus a 3px bar.
- **Badges** label state, **chips** filter, and the **count** (accent) appears only where a person is needed.
- **Banners** are for persistent conditions. **Toasts** are for events: 6 seconds (errors stay), above the phone tab bar, with an action.
- **Dialogs** are for decisions, **drawers** for detail beside a list, and **sheets** for the phone. All of them trap focus, close on Esc and return focus.
- **Steppers:** at most five steps, each named for the decision, not the file. The review lives inside the wizard, and Back keeps input.
- **Empty states:** say what appears here, why it's empty, and the one next step.
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

**Names:** bot · team · workflow · trigger · step · run · approval · case · folder · pack · plugin · connector · host · login · secret, each defined once in the book's table and used the same way in the UI, docs, CLI and README. Entity names are lowercase-kebab (`platform-sre`). Third-party services keep their own casing (GitHub, PagerDuty). **conductor is always lowercase**: rewrite the sentence rather than start it with the name.

**Fixed risk vocabulary:** “Does on its own” · “Asks you first” · “Never”, with the tier name second.

**Patterns** (rendered in the book):
- **Approvals** show *Why* (the reason, linked to the case), *Impact* (where, how much, how long) and *Undo* (how, and for how long), then “Approve and run” and “Deny…”. Technical details are collapsed.
- **Errors:** what happened, why, and what to do, with the raw code last, in mono.
- **Validation:** “Pick a channel to post to.”, not the schema path.
- **Confirmations:** the title states the action and object (“Delete the nightly-backup workflow?”) and the body states the consequences. A change that widens access needs a plain-words acknowledgement (“I understand homelab can sign in as me”). Never ask “Are you sure?”.
- **Toasts** are past tense and specific: “Approved. conductor is applying the plan.”, with a link.

---

## Theming robustness

**The iPhone bug.** On Daniel’s iPhone (paseo’s in-app browser, WebKit, OS in dark mode), `directions-v2.html` showed faint, light-grey text on the cream page, and every specimen labelled LIGHT rendered with a dark surface. **Cause:** the pages never declared a colour scheme. With no `color-scheme`, browsers that auto-darken pages repaint their colours: Chromium’s force-dark, and in-app WebViews that apply “algorithmic darkening”. That flipped our forced-light surfaces dark while some text was left in its light-theme colours. The token-pair contrast check passed, because the tokens were right; what the reader saw was not.

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
   - `--ok`, `--run`, `--wait`, `--fail`, `--idle`, `--shadow`: use `-fg` for text and glyphs, `-solid` for dots and bars. **Passing rows stop being green**: they are ink, with a neutral check glyph.
   - Inputs and checkboxes: `--color-border-control`.
4. **Type:** body to `--type-body-m` (15px). Titles to `--type-h1`/`--type-h2` (Fraunces, upright), with long or generated titles (proposal, run and step titles) moved to `--type-h3` in the sans. **Decorative mono becomes sans with tabular figures**: the top strip, crumbs, counts, times and costs. Mono stays only for ids, digests, YAML and logs, at least 12px.
5. **Shape:** radii from `--radius-*` (controls 10, cards 14, badges pill), shadows from `--shadow-*`. Drop the film grain and the dot grid on app surfaces.
6. **Information design:** apply the patterns: a summary sentence under each page title, the needs-you list, “since you last looked” on the overview, the run story on run pages, activity lanes on Live/Activity, and the approval card.
7. **Density:** `data-density="compact"` on `<html>` from a per-browser display preference. Replace fixed row and padding values with `--size-row`, `--pad-cell-*`, `--pad-card` and `--pad-page-*`. `.tbl.compact` becomes redundant.
8. **Logo:** copy `design/brand/assets/` into the prototype and follow its README: the favicon links, the strip mark in `js/ui.js` and the gate mark in `js/screens/access.js` (both from `mark-inline.html`), and the live-text wordmark in `--type-brand`.

| Prototype | Daylight token | Note |
|---|---|---|
| `--font-ui` | `--font-family-sans` | Instrument Sans → Atkinson Hyperlegible Next. Body goes 13.5 → 15px (--type-body-m). |
| `--font-title` | `--font-family-serif` | Instrument Serif italic → Fraunces SemiBold, upright. Use the --type-h1 / --type-h2 shorthands plus their -tracking and -variation. Long or generated titles move to --type-h3 (sans). |
| `--font-mono` | `--font-family-mono` | Martian Mono → Atkinson Hyperlegible Mono, and only for literal code, ids, YAML and logs. Strip cells, crumbs, counts, times and costs move to the sans with tabular-nums. |
| `--r-xs` | `--radius-badge` | 3 → pill (999). |
| `--r-sm` | `--radius-control` | 5 → 10. |
| `--r` | `--radius-card` | 8 → 14. |
| `--r-lg` | `--radius-overlay` | 12 → 18. |
| `--rail-w` | `--size-rail` | 224 → 236 (212 compact). |
| `--strip-h` | `--size-strip` | 34 → 60 (48 compact): the mono status strip becomes a calm top bar (search, health, avatar). |
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
| `--accent` | `--color-accent-default` | Fills only (buttons, counts, bars, the logo dot). Where it colours text (links, .cell.alert, crumbs:hover; 43 places), use --color-text-accent. |
| `--accent-2` | `--color-accent-hover` |  |
| `--accent-ink` | `--color-text-on-accent` |  |
| `--accent-wash` | `--color-accent-subtle` |  |
| `--ok` | `--color-status-success-fg` | Dots and bars: --color-status-success-solid. Passing rows stay ink: colour is for what needs attention. |
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

## Open decisions for Daniel

0. **The table status column** ([`explore/status-column.html`](explore/status-column.html)). The tinted capsule plus tinted row was judged generic. I recommend **D · Grouped, with the step trace**: runs that need a person sit in their own group at the top, each with a plain sentence and one action; running work comes next, then finished runs in quiet ink. The status column becomes a step trace (one mark per step, with the failing or waiting step marked). Emphasis comes from position and words, with no fills. Once it’s chosen, the components, the book and the mocks are updated to match.

1. **Where the brand files live long-term:** `design/brand/` or `.github/brand/`, which sets the README paths.
2. **The dashboard default theme:** follow the OS (as the tokens do now), or light unless the user picks dark. Daylight is designed light-first.
