// The desktop app (desktop/) loads this dashboard in its own window and offers
// one native command: sign an SSH challenge with the operator's SSH agent, after
// a native confirmation that names what is being signed. In a browser none of
// this exists and every caller falls back to the paste flow.
type Invoke = (cmd: string, args: Record<string, unknown>) => Promise<unknown>

function invoker(): Invoke | undefined {
  const w = window as unknown as { __TAURI__?: { core?: { invoke?: Invoke } } }
  return w.__TAURI__?.core?.invoke
}

// inDesktopApp reports whether the page runs inside the desktop app.
export function inDesktopApp(): boolean {
  return invoker() !== undefined
}

// desktopSSHSign signs message in namespace with the app's configured key and
// returns the armored signature. Rejects when the operator declines.
export async function desktopSSHSign(namespace: string, message: string): Promise<string> {
  const invoke = invoker()
  if (!invoke) throw new Error('not running in the desktop app')
  const sig = await invoke('ssh_sign', { namespace, message })
  if (typeof sig !== 'string' || !sig.includes('BEGIN SSH SIGNATURE')) throw new Error('the desktop app returned no signature')
  return sig
}

// desktopOperator is the operator the app is set up for, so the sign-in page
// can sign the window in on its own.
export async function desktopOperator(): Promise<string> {
  const invoke = invoker()
  if (!invoke) throw new Error('not running in the desktop app')
  const op = await invoke('desktop_operator', {})
  if (typeof op !== 'string' || !op) throw new Error('the desktop app has no operator set up')
  return op
}
