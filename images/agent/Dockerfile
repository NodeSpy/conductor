# conductor's agent image: the default image an agent runs in under the
# agent sandbox's container backend (isolation `mode: container`).
#
#   make agent-image                      # → conductor-agent:<version>, :latest
#   docker build -f images/agent/Dockerfile -t conductor-agent:latest .
#
# Pin it: every version below is a build arg, and BASE takes a digest
# (--build-arg BASE=node:24-bookworm-slim@sha256:…). conductor never pulls
# this image — it is built on the box; name another with `image:`.
#
# What is in it: the agent CLIs (Claude Code, Codex, opencode), git, and the
# usual tooling (bash, curl, jq, ripgrep, python3, make, …), plus a Linux
# conductor (static) at /usr/local/bin/conductor — `conductor call` inside,
# and the container's helper where the host's conductor is not a Linux
# binary (macOS).
#
# What an agent can install, and where it lasts: the agent runs as the
# operator's (non-root) uid with no capabilities, so it cannot apt-get into
# the image. It installs into its HOME — `npm i -g` (NPM_CONFIG_PREFIX is
# ~/.npm-global), `pip install` (PIP_USER: ~/.local), `go install`
# (~/go/bin), `cargo install` (~/.cargo/bin), a binary in ~/.local/bin or
# ~/bin — all on its PATH. With `persistent: true` HOME is the identity's
# volume and what it installs is there next time; anything written outside
# HOME and the workspace is lost with the container.

ARG BASE=node:24-bookworm-slim

FROM golang:1.26-alpine AS conductor
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=agent-image
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/conductor ./cmd/conductor

FROM ${BASE}
ARG CLAUDE_CODE_VERSION=2.1.281
ARG CODEX_VERSION=0.146.0
ARG OPENCODE_VERSION=1.19.0
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates git curl bash jq ripgrep python3 python3-pip python3-venv \
      make procps less unzip xz-utils file patch diffutils findutils \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g --no-fund --no-audit \
      "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
      "@openai/codex@${CODEX_VERSION}" \
      "opencode-ai@${OPENCODE_VERSION}" \
 && npm cache clean --force \
 && mkdir -p /home/agent && chmod 0777 /home/agent
COPY --from=conductor /out/conductor /usr/local/bin/conductor
ENV HOME=/home/agent
WORKDIR /home/agent
LABEL org.opencontainers.image.title="conductor-agent" \
      org.opencontainers.image.description="conductor's agent image: agent CLIs and tooling for the agent sandbox's container backend"
