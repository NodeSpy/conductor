// Every daemon call goes through a typed Rust Tauri command — never a
// browser fetch (see desktop/README.md and the plan: "all daemon traffic
// through Rust"). This wraps `@tauri-apps/api/core` `invoke`, and falls back
// to fixture data when the page is opened with `?mock` in the URL (set by
// `npm run dev -- --mode mock` or by hand), so screens can be built and
// screenshotted for conformance without Tauri or a running daemon.
import { invoke as tauriInvoke } from '@tauri-apps/api/core'
import { mockInvoke, isMock } from './mock'

export function invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  if (isMock()) return mockInvoke<T>(cmd, args)
  return tauriInvoke<T>(cmd, args)
}
