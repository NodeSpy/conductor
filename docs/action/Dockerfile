# The conductor-action image: the slim conductor binary + whatever engine
# plugins you bake in, running `conductor once` against the job's event.
#
# Build context is the conductor repo root:
#
#   docker build -f docs/action/Dockerfile -t conductor-action .
#
# The action itself declares `image: Dockerfile`, so Actions builds it per job;
# a published action would push this image and reference it by digest instead.
#
# Deliberately NOT the production daemon image (/Dockerfile): that one has a
# /config + /data volume layout, a non-root user with a persistent HOME, and a
# `run` entrypoint. A runner mounts its workspace at /github/workspace, runs as
# whatever uid Actions picked, and lives for one event.

FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=action
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/conductor ./cmd/conductor

# Engine pre-bake (optional). `conductor plugin add <ref>` fetches an engine
# into the install state; doing it HERE means the job's code steps need no
# network. Anything not baked in is fetched by `conductor init` at job time.
FROM build AS engines
ARG ENGINES=""
ENV XDG_STATE_HOME=/opt/conductor-state
RUN mkdir -p "$XDG_STATE_HOME" \
 && for e in ${ENGINES}; do /out/conductor plugin add "$e" || exit 1; done

FROM alpine:3.20
# git: the cli runtime provisions its own worktree. gh + ca-certificates: the
# forge writes. bash/coreutils: what a `use: cli` step usually shells.
RUN apk add --no-cache ca-certificates git openssh-client bash coreutils curl tzdata github-cli
COPY --from=build /out/conductor /usr/local/bin/conductor
COPY --from=engines /opt/conductor-state /opt/conductor-state
COPY docs/action/entrypoint.sh /usr/local/bin/conductor-action
RUN chmod +x /usr/local/bin/conductor-action

# The runner's uid is not ours to choose, so state goes somewhere world-
# writable and ephemeral: one-shot mode defaults its own store to a temp dir
# anyway (docs/design O4), and the job's container is destroyed after the step.
ENV XDG_STATE_HOME=/opt/conductor-state
ENV HOME=/tmp
WORKDIR /github/workspace
ENTRYPOINT ["conductor-action"]
