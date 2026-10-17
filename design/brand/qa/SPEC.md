# Design QA spec: the dashboard prototype

What the dashboard prototype is checked against, and how. The findings go in
[`dashboard-deviations.md`](dashboard-deviations.md).

## Approved designs (the only references)

- **Daylight mocks:** [`v2/mock.html?d=daylight`](../v2/mock.html?d=daylight), every screen (`s=overview|run|approval|history|builder|activity|bot`). The Broadsheet and Score directions and the `&v=before` views are records, not references. The `&v=scan` screens are a proposal under review, not yet approved.
- **The brand book:** [`system.html`](../system.html) and [`BRAND.md`](../BRAND.md): tokens, type, colour, components, patterns, voice, logo usage.
- **The status column:** grouped rows (needs attention · running · finished) with margin marks, from [`status/marks.js`](../status/marks.js); no tinted rows or capsules; step detail on the run page as a tree.
- **The logo:** Fermata, strengthened, from [`assets/`](../assets/), in its approved lockups only.

Anything on a screen that isn’t in one of these is a deviation.

## Removed from the system (any appearance is a deviation)

- **One lane per workflow** (the Score lane view). Activity is **runs over time as one aggregate** (runs per hour, failures marked) **plus the exceptions grouped by workflow**, with healthy workflows collapsed to a count.
- **“Since you last looked”** digests, anywhere. The overview pattern is **At a glance**: a few key counts (need you, failed, running, went fine, spent).
- **The sunrise motif**, or any illustration made from the mark: rays, horizon lines, cropped or enlarged arches, watermarks, animated or decorated logos. Empty states and onboarding are a plain sentence and an action, if there is one.
- **Meters or bars as risk or autonomy markers.** Tiers and risk are words.

## How each screen is checked

- Every screen at desktop (1440×900) and phone (390×844), light and dark, in WebKit (iPhone 15 profile) and Chromium.
- Each deviation records what it is, where (screen, region), a screenshot pair (prototype · approved reference) and the exact fix (token, component, copy or removal).
- The prototype is read only: it is never restarted or edited.
