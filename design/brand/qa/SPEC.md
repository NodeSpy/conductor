# Design QA spec: the dashboard prototype

What the dashboard prototype is checked against, and how. The findings go in
[`dashboard-deviations.md`](dashboard-deviations.md).

## Approved designs (the only references)

- **The Daylight mocks:** [`v2/mock.html?d=daylight`](../v2/mock.html?d=daylight), the default view of each screen: `s=overview`, `activity` (which holds the run history), `approval` (Terraform; `&tab=tech` for its Technical details tab, `&a=slack` for a simple action), `bot`, `perms` (a bot’s Access tab), `picker` (the step picker), `run` and `builder`. Anything with `&v=` (before, calm, scan) and the Broadsheet and Score directions are archived drafts, not references.
- **The brand book:** [`system.html`](../system.html) and [`BRAND.md`](../BRAND.md): tokens, type, colour, icons, components, patterns, voice, logo usage and the information architecture.
- **The status design:** grouped rows (needs attention → running now → finished) with margin marks (a mark and a word; passed is green, mark and word, in regular weight; in charts a passed bar is the soft `status.success.chart` body under a 2px `status.success.solid` cap (the legend swatch too), and the runs-per-hour strip stacks passed, waiting or parked (`run-strip.waiting`), running and failed, from the ground), from [`status/marks.js`](../status/marks.js); a coloured leading edge only on rows that need someone; step detail on the run page as a tree.
- **The logo:** Fermata, strengthened, from [`assets/`](../assets/) (the `fermata-bold` kit), in its approved lockups only.

Anything on a screen that isn’t in one of these is a deviation. Nothing on the [archive](../archive/index.html) is a reference.

## Removed from the system (any appearance is a deviation)

- **One lane per workflow,** or any view that grows linearly with the number of workflows, bots or runs. Activity is runs over time as one strip (failures marked) plus the exceptions grouped by workflow, with healthy workflows collapsed to a count, then the paged run list.
- **“Since you last looked”** digests, anywhere. The overview opens with **At a glance**: four tiles.
- **Greetings** (“Good afternoon, …”) and **header sentences that restate what is on the screen.**
- **The sunrise motif,** or any illustration made from the mark, and any altered, decorated or animated logo. Empty states are a plain sentence and an action, if there is one.
- **Meters or bars as risk or autonomy markers.** Risk is words, where someone decides.
- **Pills and capsules** for status, filters or tags, and **any count badge other than the one orange badge** (`--count-badge-*`, only for something new or waiting for you; a total is plain text); **tinted rows**; **step-dot traces in lists.**
- **Run tabs** and **previous/next** between runs; **export buttons**; **Pause/Run on the Overview**; **captions that explain obvious controls.**
- **The fingerprint** on anything that doesn’t authenticate (it belongs only on approve, deny and changes that widen access), including “Review” buttons.
- **Old names:** “Run history” (it is Activity), “Members” (they are bots, in folders), settings outside the one System area.

## How each screen is checked

- Every screen at desktop (1440×900) and phone (390×844), light and dark, in WebKit (iPhone 15 profile) and Chromium.
- Each deviation records what it is, where (screen, region), a screenshot pair (prototype · approved reference) and the exact fix (token, component, copy or removal).
- The prototype is read only: it is never restarted or edited.
