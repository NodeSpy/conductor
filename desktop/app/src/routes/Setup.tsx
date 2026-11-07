// Setup/settings: daemon URL, operator name and SSH key path. Moved from
// desktop/ui/setup.html into the app as a route, same behaviour: get/save
// settings, default_ssh_key, complete_setup (operator lowercased — conductor
// operator names are lowercase). Unlike before, completing setup does not
// navigate anywhere: the main window never loads the daemon's origin now,
// it just starts the background subscription and this route sends the
// operator on to Needs you.
import type { JSX } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import { api, ApiError } from '../api/client'
import { go } from '../router'

export function Setup(): JSX.Element {
  const [daemonUrl, setDaemonUrl] = useState('')
  const [operator, setOperator] = useState('')
  const [sshKey, setSshKey] = useState('')
  const [status, setStatus] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    api
      .getSettings()
      .then((s) => {
        setDaemonUrl(s.daemon_url || '')
        setOperator(s.operator || '')
        setSshKey(s.ssh_public_key_path || '')
        if (!s.ssh_public_key_path) {
          void api.defaultSshKey().then((p) => {
            if (p) setSshKey(p)
          })
        }
      })
      .catch(() => {
        /* first run */
      })
  }, [])

  const submit = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    setStatus('Connecting…')
    try {
      await api.saveSettings({
        daemon_url: daemonUrl.trim(),
        operator: operator.trim().toLowerCase(),
        ssh_public_key_path: sshKey.trim(),
      })
      await api.completeSetup()
      setStatus('Connected.')
      go('needs')
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : String(e))
      setStatus('')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="page dl-page" style="max-width:480px;margin:0 auto">
      <div class="dl-phead">
        <div>
          <h1>Connect to a daemon</h1>
        </div>
      </div>
      <form onSubmit={submit} class="stack">
        <label class="gate-field">
          <span class="gate-group-label">Daemon URL</span>
          <input value={daemonUrl} placeholder="https://box.tailnet.ts.net:7788" onInput={(e) => setDaemonUrl((e.target as HTMLInputElement).value)} data-testid="setup-url" />
        </label>
        <label class="gate-field">
          <span class="gate-group-label">Operator</span>
          <input value={operator} placeholder="your operator name" onInput={(e) => setOperator((e.target as HTMLInputElement).value)} data-testid="setup-operator" />
        </label>
        <label class="gate-field">
          <span class="gate-group-label">SSH public key</span>
          <input value={sshKey} placeholder="~/.ssh/id_ed25519.pub" onInput={(e) => setSshKey((e.target as HTMLInputElement).value)} data-testid="setup-key" />
        </label>
        <button class="btn primary lg block" type="submit" disabled={busy} data-testid="setup-save">
          {busy ? 'Connecting…' : 'Save and connect'}
        </button>
        {status ? <p class="acc-note">{status}</p> : null}
        {err ? (
          <p class="gate-err" role="alert">
            {err}
          </p>
        ) : null}
      </form>
    </div>
  )
}
