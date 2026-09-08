# Production image for the conductor daemon.
#
#   docker run -d --name conductor \
#     -v ~/.config/conductor:/config \        # config.yaml + conductor.env + conf.d/
#     -v conductor-data:/data \               # state: history, blobs, dedup, vault
#     -p 8080:8080 \                          # whatever your webhook.listen port is
#     ghcr.io/nodespy/conductor:latest
#
# Agent dispatch does NOT need a runtime baked in. Point a runtime at another box
# over SSH (a `hosts:` entry + the runtime's `host:`); conductor runs the agent CLI
# there. Mount an SSH key for the link. Keep `update.auto` off in a container —
# update by pulling a new image.

FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/conductor ./cmd/conductor

FROM alpine:3.20
RUN apk add --no-cache ca-certificates git openssh-client bash coreutils curl tzdata github-cli \
 && adduser -D -h /home/conductor conductor \
 && mkdir -p /config /data \
 && chown -R conductor:conductor /config /data
COPY --from=build /out/conductor /usr/local/bin/conductor
ENV HOME=/data
USER conductor
VOLUME ["/config", "/data"]
EXPOSE 8080
ENTRYPOINT ["conductor"]
CMD ["run", "--config", "/config/config.yaml"]
