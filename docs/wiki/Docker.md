# Docker

conductor publishes a multi-arch (linux/amd64, linux/arm64) image to
`ghcr.io/nodespy/conductor`. The image runs the same static binary as the
released binaries — no database, no web UI — wired up for a container: config
and state are volumes, and agent dispatch links out to a runtime box over SSH
instead of baking a runtime CLI into the image.

## Pull and run

```sh
docker run -d --name conductor \
  -v ~/.config/conductor:/config \
  -v conductor-data:/data \
  -p 8080:8080 \
  ghcr.io/nodespy/conductor:latest
```

- `/config` — mount your `config.yaml`, `conductor.env`, and any `conf.d/`
  fragments. This is the same directory the installer seeds at
  `~/.config/conductor/` — see [[Installation]].
- `/data` — a named volume for state (`$HOME` inside the container is `/data`):
  run history, blob storage, dedup index, the secrets vault. Use a real
  volume, not a bind mount to a throwaway directory — this is the daemon's
  durable state.
- `-p 8080:8080` — only needed if you're running a `webhook`/`sentry`
  connector with an inbound `listen:` address, or the [[Callable-Service]]
  HTTP surface. Match it to whatever port your config actually listens on;
  omit it entirely for a cron/poll-only setup.

The entrypoint is `conductor`; the default command is
`run --config /config/config.yaml`. Override the command to run one-off
verbs against the same volumes, e.g.:

```sh
docker run --rm -v ~/.config/conductor:/config ghcr.io/nodespy/conductor:latest \
  validate --config /config/config.yaml
```

### Tags

- `:latest` — the most recent tagged release.
- `:vX.Y.Z` — a pinned release, matching [GitHub releases](https://github.com/NodeSpy/conductor/releases).

### docker-compose

```yaml
services:
  conductor:
    image: ghcr.io/nodespy/conductor:latest
    container_name: conductor
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - ~/.config/conductor:/config
      - conductor-data:/data
      - ~/.ssh/id_ed25519:/data/.ssh/id_ed25519:ro   # only if linking a runtime over SSH

volumes:
  conductor-data:
```

## Linking a runtime over SSH (agent dispatch)

The image does **not** embed a runtime (paseo/cli/acp/agent-deck) or any
agent CLI. Agent dispatch is a transport concern, not a packaging one: define
your runtime box under `hosts:` and point a runtime's `host:` at it, and
conductor drives the agent CLI there over SSH — see [[Hosts]] and
[[Runtimes]] for the full model.

1. Generate (or reuse) an SSH key that can reach your runtime box, and mount
   it read-only into the container:

   ```sh
   docker run -d --name conductor \
     -v ~/.config/conductor:/config \
     -v conductor-data:/data \
     -v ~/.ssh/conductor_ed25519:/data/.ssh/id_ed25519:ro \
     -p 8080:8080 \
     ghcr.io/nodespy/conductor:latest
   ```

   (`$HOME` inside the container is `/data`, so `~/.ssh` resolves to
   `/data/.ssh` — mount the key there, or point `hosts.<name>.key` at wherever
   you mounted it.)

2. Point a host entry at the runtime box and reference it from a runtime:

   ```yaml
   hosts:
     runtime-box:
       host: runtime.internal
       user: conductor
       key: /data/.ssh/id_ed25519
       known_hosts: /data/.ssh/known_hosts   # optional pin

   runtimes:
     paseo: { use: paseo, bin: paseo, host: runtime-box, default: true }
   ```

   The runtime box needs the actual runtime binary (`paseo`, or whatever
   `cli`/`acp`/`agent-deck` tool you're driving) and its own agent CLI
   installed and authenticated — the container never runs it locally. See
   [[Hosts]] for what executes remotely under each runtime type.

## Updating

Auto-update (`update.auto: true`) is meant for a long-lived host process that
can restart itself into a freshly-downloaded binary — that model doesn't fit
a container, where the binary comes from the image, not a self-download.
**Leave `update.auto` off** (the default) in a containerized deployment; update
by pulling a new image tag instead:

```sh
docker pull ghcr.io/nodespy/conductor:latest
docker stop conductor && docker rm conductor
docker run -d --name conductor ...   # same flags as before
```

Or, with compose: `docker compose pull && docker compose up -d`.

## Isolation caveat

`isolation:` sandboxing (see [[Isolation]]) has two modes that need
privileges the container itself may not have:

- **`mode: container`** launches agent work in a *further* nested container
  (`docker run` under the hood) — it needs the Docker socket mounted in
  (`-v /var/run/docker.sock:/var/run/docker.sock`) and a matching-architecture
  agent image. Mounting the host's Docker socket into a container is a
  meaningful privilege escalation; do it only if you understand that
  trade-off.
- **`mode: namespace`** needs Linux user/mount namespaces and cgroups, which
  requires the container to run with the right `userns`/capabilities — not
  available in a default, unprivileged container runtime.

If neither is workable in your deployment, isolate on a **linked host**
instead: give the runtime box its own `isolation:` block (`hosts.<name>.isolation`)
and let the SSH-linked runtime enforce sandboxing there, where you control the
privileges directly.

Related: [[Installation]] · [[Hosts]] · [[Runtimes]] · [[Isolation]] · [[Configuration]]
