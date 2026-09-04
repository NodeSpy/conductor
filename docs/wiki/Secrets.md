# Secrets

`conductor.env` works exactly as before: `${VAR}` in the config resolves from
the process environment / the sibling chmod-600 env file at load, and a
referenced-but-unset variable is a load error naming it. That is the
baseline; everything else is opt-in.

## Secret references

Anywhere a credential goes (`token:`, `bot_token:`, ssh `key:` material, verb
options, the named block below), a value may be a scheme reference instead of
a literal:

| reference | resolves via |
|---|---|
| `env:GH_PAT` | process environment (same pool as `${GH_PAT}`) |
| `op://Vault/Item/field` | 1Password (`op read`; Service Account token or Connect) |
| `pass:conductor/gh-token` | `pass` (the GPG store; first line of the entry) |
| `vault:gh-token` | conductor's built-in encrypted vault (below) |
| `file:/run/secrets/gh` | a mounted file (systemd `LoadCredential`, docker, k8s) |

Resolved values are cached in memory for the process lifetime, **redacted**
from logs and the audit trail, and never written back to disk. `conductor
secrets check` resolves every reference and reports each one's state without
printing values. A secret that will not resolve **disables the connector that
needs it and notifies** — the daemon boots and runs the rest.

## The named `secrets:` block

```yaml
secrets:
  gh: op://Private/GitHub/token
```

names a reused reference once; templates read `{{.secrets.gh}}`. Named
secrets are not passed into code steps' `ctx` — pass one explicitly when code
genuinely needs it.

## The built-in vault

`vault:` references read a secretbox-encrypted JSON file
(`~/.config/conductor/vault.json`), managed with:

```
conductor vault init          # create the vault + seed a generated key file
conductor vault add <name>    # value on stdin
conductor vault show|ls|rm
```

## Unlocking — non-interactive by design

`op`/`pass`/`vault` each bottom out at a bootstrap secret. conductor
auto-updates and restarts itself, so the bootstrap must be retrievable
**without a human at each restart** — an interactive passphrase would hang
the next boot. The vault master key resolves in this order:

1. `$CONDUCTOR_VAULT_KEY` — set in the environment or `conductor.env`.
2. `$CREDENTIALS_DIRECTORY/conductor-vault-key` — a systemd encrypted
   credential (`systemd-creds encrypt`, TPM/host-bound; no plaintext at rest).
   Best on Linux.
3. The OS keyring, read through the platform CLI (no cgo) — automatic once
   the login session is unlocked. Best on macOS. Seed it once:
   - macOS Keychain: `security add-generic-password -s conductor-vault-key
     -a "$USER" -w '<key material>'` (read back with `security
     find-generic-password -s conductor-vault-key -w`).
   - libsecret (GNOME Keyring/KWallet): `secret-tool store --label
     'conductor vault key' service conductor-vault-key` (read back with
     `secret-tool lookup service conductor-vault-key`).
   A missing tool, locked keyring, or absent entry falls through silently.
   Note a headless Linux box usually has no unlocked keyring — prefer
   systemd-creds there.
4. `<vault dir>/vault.key` — a chmod-600 key file seeded by `conductor vault
   init` or `conductor unlock`. Simplest; with full-disk encryption the
   practical exposure is a running-host compromise, not the disk.

Key material that decodes as 32 base64 bytes is used directly; anything else
is treated as a passphrase and stretched with scrypt against a per-vault
salt. `conductor unlock` seeds the key file interactively — one-time seeding,
after which restarts are unattended.

Related: [[Configuration]] · [[Connectors]] · [[Commands]]
