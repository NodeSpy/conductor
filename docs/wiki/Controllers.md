# Controllers → Runtimes

The `controllers:` block is the legacy name for **[[Runtimes]]** — the same
registry, resolution order, session models, broker, and capability handling.
Legacy `controllers:` config still loads (it merges with `runtimes:`; a name
may not appear in both), and agent profiles may keep `controller:` as the
selector.

New in the runtimes model:

- `bin:` on a paseo-type runtime replaces the global `paseo_bin`.
- `host:` on cli/acp/agent-deck runtimes launches them on a [[Hosts]] SSH
  target.

See [[Runtimes]] for the current reference.
