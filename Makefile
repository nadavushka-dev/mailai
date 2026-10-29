.PHONY: check test build hooks sandbox sandbox-down

VERSION ?= v0.0.0-dev.$(shell TZ=UTC git show -s --format=%cd --date=format-local:%Y%m%d%H%M%S HEAD 2>/dev/null || echo 0).$(shell git rev-parse --short=7 HEAD 2>/dev/null || echo 0)
LDFLAGS := -X github.com/nadavushka-dev/mailai/internal/buildinfo.Version=$(VERSION)

check: ## everything the pre-commit hook runs
	@test -z "$$(gofmt -l .)" || (gofmt -l . && false)
	go vet ./...
	go test -race -count=1 ./...

test:
	go test -race ./...

build:
	go build -ldflags "$(LDFLAGS)" -o bin/mailai ./cmd/mailai

hooks:
	git config core.hooksPath .githooks

sandbox: ## three throwaway Linux boxes (s1 s2 s3) running this tree, one daemon each (e2e/sandbox.sh shell s1)
	e2e/sandbox.sh up

sandbox-down:
	e2e/sandbox.sh down
