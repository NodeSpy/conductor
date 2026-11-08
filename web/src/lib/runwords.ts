// Small run wording helpers shared by Home, Activity and a run's page
// (prototype js/ui.js UI.hm, UI.runningFor, UI.runTarget).

/** "14:02": a clock time (UI.hm). */
export function hm(iso: string): string {
  return new Date(iso).toTimeString().slice(0, 5)
}

/** How long a live run has been going, in whole minutes (UI.runningFor). */
export function runningFor(iso: string): string {
  const m = Math.floor((Date.now() - new Date(iso).getTime()) / 60000)
  return m < 1 ? 'under a minute' : m < 60 ? `${m} min` : `${Math.floor(m / 60)} h ${m % 60} min`
}

/** What a run is about ("acme/app#12"), when its event names one (UI.runTarget). */
export function runTarget(r: { repo?: string; number?: number }): string {
  return r.repo ? `${r.repo}${r.number ? `#${r.number}` : ''}` : ''
}

/** A run's plain headline (UI.runTitle): "Running deploy", "deploy finished", "deploy failed";
 *  a bot's own run: "scout is working" / "scout finished a task" / "scout’s task failed". */
export function runTitle(r: { workflow: string; status: string }): string {
  const i = r.status === 'running' ? 0 : r.status === 'failed' ? 2 : 1
  if (r.workflow.startsWith('bot:')) {
    const b = r.workflow.slice(4)
    return [`${b} is working`, `${b} finished a task`, `${b}’s task failed`][i]
  }
  return [`Running ${r.workflow}`, `${r.workflow} finished`, `${r.workflow} failed`][i]
}
