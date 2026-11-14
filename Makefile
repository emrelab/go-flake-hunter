.PHONY: all build test test-short test-repeat lint vuln cost vet fmt cover fixtures clean run smoke demo auto auto-next auto-verify auto-digest

BINARY := go-flake-hunter
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

all: fmt vet test build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/go-flake-hunter

run: build
	./$(BINARY) help

test:
	go test -race ./...

# Fast loop: unit tests only. The integration layer drives real `go test`
# subprocesses and belongs to `make test` / CI; a red unit test is what a
# contributor wants to hear about in seconds.
test-short:
	go test -race -short ./...

# Static analysis gate. Same binary CI runs, so a local pass means the job
# passes; no config file on purpose — the defaults are the standard here.
lint:
	golangci-lint run ./...

# Known vulnerabilities in the stdlib and in the toolchain this binary ships
# with. Zero dependencies means no module supply chain, not no CVEs.
vuln:
	govulncheck ./...

# action.yml's step scripts are validated locally, without GitHub.
smoke:
	scripts/action-smoke.sh

# The README's demo GIF, rendered from the binary's real output over
# testdata/fixtures/order-basic. Re-run it when the sample output changes.
demo: build
	python3 scripts/make-demo-gif.py

# Using our own tool on ourselves: a state leak would surface here.
test-repeat:
	go test -count=10 ./...

# Where the suite's wall time goes, and how many subprocesses it spawns.
# Operator tool, not a gate: see the header of the script.
cost:
	scripts/test-cost.sh

# Autonomous run over the issue tracker. No cron: a human starts it, see
# docs/AUTO.md. MAX caps how many issues one run may touch.
auto:
	scripts/auto/run.sh $(MAX)

auto-next:
	scripts/auto/next.sh

auto-verify:
	scripts/auto/verify.sh

# The human's half of the loop: open auto/* PRs with their CI state, blocked
# issues, and whether the cron is armed for a queue that still has work.
# Read-only, and NOT a monitor — its output changes every hour by design.
auto-digest:
	scripts/auto/digest.sh

vet:
	go vet ./...

fmt:
	gofmt -l -w .

cover:
	# -coverpkg is required, not decoration: the CLI's integration tests live in
	# their own packages (internal/cli/hunt, ...), so without it each package is
	# only measured by the tests sitting inside it — internal/cli would report
	# ~0% while its tests all live next door. Measured: 51.1% without, 78.0%
	# with, on the same tree.
	go test -coverprofile=coverage.out -coverpkg=./... ./...
	go tool cover -func=coverage.out | tail -1

# Each fixture is its own module; the root module ignores them, they build separately.
fixtures:
	@for d in testdata/fixtures/*/; do \
		echo "== $$d"; (cd "$$d" && go build ./... && go vet ./...) || exit 1; \
	done

clean:
	rm -f $(BINARY) coverage.out
	rm -rf .go-flake-hunter
