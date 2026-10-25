# conductor — developer targets.
#
# The e2e harness (test/e2e/) is a Dockerized, fully isolated stack: a local
# bare-git forge, a mock GitHub API, a notify sink-catcher, and stub controllers —
# NO production GitHub/smee/repos, NO LLM keys.

.PHONY: build test vet fmt e2e e2e-live e2e-down e2e-packs ship-gate web web-types web-check

# Go build / test / static checks.
build:
	go build ./...

# --- dashboard web UI (internal/dashboard) ---
#
# The SPA source lives in web/ and builds into internal/dashboard/dist/, which
# is COMMITTED so `go build`/`go install` never need Node (design §8). Node is a
# build-time dependency only.

# Build the SPA into the embedded dist/. Reproducible from package-lock.json.
web:
	cd web && npm ci && npm run build

# Regenerate web/src/api/types.ts from the Go API types (internal/dashboard/api).
# Dev-time only; the generated file is committed, so a normal build needs no tygo.
web-types:
	cd web && go run github.com/gzuidhof/tygo@v0.2.21 generate

# CI check: the committed dist/ must match a fresh build from web/ (reproducible).
web-check:
	cd web && npm ci && npm run build
	@git diff --quiet -- internal/dashboard/dist || \
		{ echo "internal/dashboard/dist is out of date — run 'make web' and commit the result"; git --no-pager diff --stat -- internal/dashboard/dist; exit 1; }
	@echo "web-check: dist is reproducible from web/"

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

# Tear down a leftover e2e stack (e.g. after a KEEP=1 run).
e2e-down:
	docker compose -f test/e2e/docker-compose.yml -p pc-e2e down -v --remove-orphans

# The M6 ship gate: static checks + race tests + the hermetic e2e suite.
ship-gate: fmt vet test e2e
	@echo "ship-gate: OK"
