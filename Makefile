# conductor — developer targets.
#
# The e2e harness (test/e2e/) is a Dockerized, fully isolated stack: a local
# bare-git forge, a mock GitHub API, a notify sink-catcher, and stub controllers —
# NO production GitHub/smee/repos, NO LLM keys.

.PHONY: build test vet fmt e2e e2e-live e2e-down e2e-packs ship-gate agent-image

# Go build / test / static checks.
build:
	go build ./...

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l .

# Hermetic e2e (CI-safe): stub controllers, no secrets. Ship gate for M6.
e2e:
	MODE=stub bash test/e2e/run.sh

# Live e2e (manual): real agents + mounted API keys. See test/e2e/README.md.
e2e-live:
	MODE=live bash test/e2e/run.sh

# Hermetic pack lifecycle e2e (issue #53): drives the real binary through
# init/plan/validate against examples/packs/review-kit from a local AND a git
# source. No Docker, no secrets, no egress.
e2e-packs:
	bash test/e2e/packs.sh

# Tear down a leftover e2e stack (e.g. after a KEEP=1 run) — this checkout's
# (test/e2e/run.sh names the project after it).
E2E_PROJECT ?= pc-e2e-$(shell basename "$(CURDIR)" | tr 'A-Z' 'a-z' | tr -c 'a-z0-9_\n' '-')
e2e-down:
	docker compose -f test/e2e/docker-compose.yml -p $(E2E_PROJECT) down -v --remove-orphans

# The M6 ship gate: static checks + race tests + the hermetic e2e suite.
ship-gate: fmt vet test e2e
	@echo "ship-gate: OK"

# conductor's agent image (the agent sandbox's container backend's default).
# Pin the CLIs / base with AGENT_IMAGE_ARGS, e.g.
#   make agent-image AGENT_IMAGE_ARGS='--build-arg CLAUDE_CODE_VERSION=2.1.281'
AGENT_IMAGE_ENGINE ?= $(shell command -v podman >/dev/null 2>&1 && echo podman || echo docker)
AGENT_IMAGE_VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
agent-image:
	$(AGENT_IMAGE_ENGINE) build -f images/agent/Dockerfile \
		--build-arg VERSION=$(AGENT_IMAGE_VERSION) $(AGENT_IMAGE_ARGS) \
		-t conductor-agent:$(AGENT_IMAGE_VERSION) -t conductor-agent:latest .
