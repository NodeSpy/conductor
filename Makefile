# conductor — developer targets.
#
# The e2e harness (test/e2e/) is a Dockerized, fully isolated stack: a local
# bare-git forge, a mock GitHub API, a notify sink-catcher, and stub controllers —
# NO production GitHub/smee/repos, NO LLM keys.

.PHONY: build test vet fmt e2e e2e-live e2e-down ship-gate

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

# Tear down a leftover e2e stack (e.g. after a KEEP=1 run).
e2e-down:
	docker compose -f test/e2e/docker-compose.yml -p pc-e2e down -v --remove-orphans

# The M6 ship gate: static checks + race tests + the hermetic e2e suite.
ship-gate: fmt vet test e2e
	@echo "ship-gate: OK"
