# Single hermetic image for the whole e2e stack (test/e2e/). It builds the real
# conductor binary plus every harness service (fake paseo CLI, mock GitHub API,
# sink-catcher, and the M3/M4 scaffolds) into one static-binary image, and each
# compose service just overrides `command:`. Runtime is alpine + git so the fake
# paseo can clone/commit/push against the bare-git forge. NO LLM, NO secrets.
#
# Build context is the REPO ROOT (see docker-compose.yml `context: ../..`), so the
# whole module is available to `go build`.

# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
# Prime the module cache from go.mod/go.sum first for build-cache friendliness.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -o /out/conductor ./cmd/conductor \
 && go build -o /out/fakepaseo     ./test/e2e/services/fakepaseo \
 && go build -o /out/mockgithub    ./test/e2e/services/mockgithub \
 && go build -o /out/sinkcatcher   ./test/e2e/services/sinkcatcher \
 && go build -o /out/fakeopencode  ./test/e2e/services/fakeopencode \
 && go build -o /out/fakeagentdeck ./test/e2e/services/fakeagentdeck \
 && go build -o /out/fakeacp       ./test/e2e/services/fakeacp \
 && go build -o /out/fakecli       ./test/e2e/services/fakecli \
 && go build -o /out/conductor-acme-engine ./test/plugins/acme-engine

# ---- runtime stage ----
FROM alpine:3.20
RUN apk add --no-cache git git-daemon curl bash coreutils openssl openssh \
 && adduser -D -h /home/conductor conductor
COPY --from=build /out/ /usr/local/bin/
# The reference ENGINE plugin (test/plugins/acme-engine) is a real out-of-process
# plugin, not a harness fake: group V points a step's `use:` straight at this
# path. verify-before-execute refuses a group/world-writable binary or parent, so
# the mode is pinned rather than left to the builder's umask.
RUN chmod 0755 /usr/local/bin/conductor-acme-engine
# The fake paseo is installed as `paseo` too, so a config without an explicit
# paseo_bin: still resolves it on PATH. The fake tool runtimes are installed under
# the exact names conductor's controllers exec on PATH (stub mode): the cli
# controller runs `claude`/`codex`, the opencode-native controller runs `opencode
# serve`, the agent-deck controller runs `agent-deck`. fakeacp is referenced by an
# explicit `command:` in the stub controllers config. In live mode the real CLIs are
# mounted ahead of these on PATH (see docker-compose.live.yml).
RUN ln -sf /usr/local/bin/fakepaseo     /usr/local/bin/paseo \
 && ln -sf /usr/local/bin/fakecli       /usr/local/bin/claude \
 && ln -sf /usr/local/bin/fakecli       /usr/local/bin/codex \
 && ln -sf /usr/local/bin/fakeopencode  /usr/local/bin/opencode \
 && ln -sf /usr/local/bin/fakeagentdeck /usr/local/bin/agent-deck
WORKDIR /home/conductor
