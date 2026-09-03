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
RUN go build -o /out/paseo-conductor ./cmd/paseo-conductor \
 && go build -o /out/fakepaseo     ./test/e2e/services/fakepaseo \
 && go build -o /out/mockgithub    ./test/e2e/services/mockgithub \
 && go build -o /out/sinkcatcher   ./test/e2e/services/sinkcatcher \
 && go build -o /out/fakeopencode  ./test/e2e/services/fakeopencode \
 && go build -o /out/fakeagentdeck ./test/e2e/services/fakeagentdeck \
 && go build -o /out/fakeacp       ./test/e2e/services/fakeacp

# ---- runtime stage ----
FROM alpine:3.20
RUN apk add --no-cache git git-daemon curl bash coreutils openssl \
 && adduser -D -h /home/conductor conductor
COPY --from=build /out/ /usr/local/bin/
# The fake paseo is installed as `paseo` too, so a config without an explicit
# paseo_bin: still resolves it on PATH.
RUN ln -sf /usr/local/bin/fakepaseo /usr/local/bin/paseo
WORKDIR /home/conductor
