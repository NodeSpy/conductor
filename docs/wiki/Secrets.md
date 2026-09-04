# Secrets & vaults

`conductor.env` works exactly as before: `${VAR}` in the config resolves from
the process environment / the sibling chmod-600 env file at load, and a
referenced-but-unset variable is a load error naming it. `env:VAR` is the
same pool. **Env is the baseline, not a vault** — implicit, always available,
nothing to declare.

Everything beyond env is a **vault** declared in the `vaults:` section (a
named map, like `stores:` / `hosts:`):

```yaml
vaults:
  house: { type: conductor }                  # ~/.config/conductor/vault.json
  op:    { type: onepassword, account: acme, service_account: env:OP_SA }
  store: { type: pass, prefix: conductor }
  files: { type: file, dir: /run/secrets }
  hcv:   { type: hashicorp, addr: https://vault.internal, unlock: { token: env:VAULT_TOKEN } }
```

| type | connection | write | list | keys |
|---|---|---|---|---|
| `conductor` | `path?` (default `~/.config/conductor/vault.json`), `unlock: { key: <ref> }` | yes | yes | entry names |
| `onepassword` | `account?`, `service_account?` (a bootstrap ref → `OP_SERVICE_ACCOUNT_TOKEN`) | no | no | op item paths: `Private/GitHub/token` |
| `pass` | `prefix?` (a store subdirectory) | yes | no | entry names (first line is the secret) |
| `file` | `dir` (a mounted secrets directory: systemd `LoadCredential`, docker, k8s) | no | yes | file names (may not escape `dir`) |
| `hashicorp` | `addr`, `mount?` (default `secret`), `namespace?`, `unlock: { token: <ref> }` | yes | no | KV v2 `path/to/secret` or `path#field` (field default `value`) |

## One reference syntax

Anywhere a credential goes (`token:`, `bot_token:`, a store's `password:`,
step options, prompts):

```
{{ vault "op" "Private/GitHub/token" }}
{{ .vaults.house.gh_token }}              # field form, for simple key names
```

Config-field references resolve at load/reload; step templates resolve at
render. The field form (`.vaults.<name>.<key>`) is preloaded from listable
vaults (`conductor`, `file`); path-keyed vaults (`onepassword`, `pass`,
`hashicorp`) use the function form. Values are cached in memory for the
process lifetime and never written back to config.

**Tainting.** Every value read through a vault — a config reference, the
`vault` template function, a `<name>.read` verb — is marked sensitive and
**redacted from logs and the audit trail, even after it flows into a later
step** (a `.read` output templated into a `slack.post` shows as `«redacted»`
in the audit; the wire gets the real value).

A reference to an unknown vault name fails `conductor validate`. The old
scheme URIs (`op://…`, `pass:…`, `vault:…`, `file:…`) and the named
`secrets:` block were replaced by this model — auto-migration rewrites them
at boot (see [[Migration]]), and an unmigrated reference fails loudly rather
than passing through as a literal.

## Read/write verbs

Each vault is addressable from steps and hooks:

```yaml
- { id: rt, uses: op.read,    options: { key: Private/GitHub/token } }   # → { value }
- { uses: house.write, options: { key: rotated, value: "{{.rt.value}}" } }
```

`read` works on every type; `write` only on writable backends (`conductor`,
`pass`, `hashicorp`) — a write against `onepassword`/`file` is a load error
(the verb is not declared). A workflow can rotate a secret and store it
back; the REST connector's OAuth2 token rotation is exactly this write path
(see `token_vault` in [[Configuration]]).

## The CLI

```
conductor vault <name> init [--sensitive]   create a conductor-type vault
conductor vault <name> add <key>            store a value (stdin; writable backends)
conductor vault <name> get <key>            print one entry (verification)
conductor vault <name> ls                   list entries (conductor/file)
conductor vault <name> rm <key>             delete an entry (writable backends)
```

`<name>` is a `vaults:` entry — declare it first, then `init`. Write ops on
a read-only backend error naming the vault and its type.

## Failure posture

`conductor secrets check` unlocks every vault and resolves every reference,
reporting each state without printing values. A vault that won't unlock
**disables the connectors and steps that depend on it and notifies** — the
daemon boots and runs the rest; every dependent use carries the recorded
unlock failure. Structural `vaults:` errors (unknown type, missing
`dir:`/`addr:`) are load errors: config bugs, not runtime conditions.

## Unlocking — non-interactive by design

Each vault's `unlock:` bottoms out at one bootstrap secret (a master key, a
service-account token). conductor auto-updates and restarts itself, so the
bootstrap must be retrievable **without a human at each restart**. An
`unlock:` value is one of:

| ref | reads |
|---|---|
| `creds:` / `creds:<name>` | `$CREDENTIALS_DIRECTORY/<name>` — a systemd encrypted credential (`systemd-creds encrypt`, TPM/host-bound; no plaintext at rest). Best on Linux. |
| `keyring:` / `keyring:<service>` | the OS keyring via the platform CLI (no cgo): macOS Keychain `security`, libsecret `secret-tool`. Automatic once the login session is unlocked; best on macOS. Headless Linux boxes usually have no unlocked keyring — prefer `creds:`. |
| `env:VAR` | process environment / `conductor.env`. Simplest; plaintext at rest is largely covered by full-disk encryption. |
| `file:/path` | a chmod-600 file (trailing newline trimmed). |
| anything else | the literal material itself — valid but discouraged; prefer an indirection. |

A `conductor` vault with no `unlock:` uses the default chain, best for
headless first:

1. `$CONDUCTOR_VAULT_KEY` (environment / `conductor.env`)
2. `$CREDENTIALS_DIRECTORY/conductor-vault-key` (systemd credential)
3. the OS keyring, service `conductor-vault-key` — seed once with
   `security add-generic-password -s conductor-vault-key -a "$USER" -w '<key>'`
   (macOS) or `secret-tool store --label 'conductor vault key' service
   conductor-vault-key` (libsecret)
4. `<vault dir>/vault.key` — the chmod-600 key file `conductor vault <name>
   init` or `conductor unlock` seeds

Key material that decodes as 32 base64 bytes is used directly; anything else
is treated as a passphrase and stretched with scrypt against the per-vault
salt recorded in the file header.

## The `conductor` vault format — hardened to survive being committed

The whole `{name → value}` map is serialized, padded to a fixed 256-byte
bucket, and sealed as ONE secretbox blob: entry **names** and the entry
**count** are ciphertext, and the blob length only reveals the size bucket.
The header records the scrypt parameters (`n`/`r`/`p`/salt); the master key
is never in the file. The format is versioned (currently 2) — an unknown
version is a clean load error.

If the vault file might be committed (even publicly):

- Keep the master key OUT of the repo: a systemd credential, the OS keyring,
  or a gitignored random key file. A random 32-byte key (what `init`
  generates) cannot be guessed; a passphrase can be — use a strong one and
  pick `--sensitive` (scrypt N=2^20, ~32× the work per guess over the
  default 2^15). The chosen cost is recorded in the header and used at
  unlock.
- Git history is permanent: a key that was ever committed stays extractable
  from history even after the file is deleted — rotate the secrets it
  sealed.

Related: [[Configuration]] · [[Connectors]] · [[Commands]] · [[Migration]]
