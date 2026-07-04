.PHONY: build test integration-test install fmt vet lint tidy check clean

build:
	go build -o chisel .

test:
	go test ./...

# Live-network tests against the real OpenCode Go API — needs CHISEL_API_KEY.
integration-test:
	go test -tags=integration ./...

fmt:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi

vet:
	go vet ./...

# Catches a direct import mismarked as indirect in go.mod (or the
# reverse) — go.sum staying consistent doesn't catch that, only tidy
# does. -diff reports without rewriting go.mod/go.sum, so this is safe
# to run in CI.
tidy:
	go mod tidy -diff

# golangci-lint isn't a Go stdlib tool — install it once via
# https://golangci-lint.run/welcome/install/ (a package manager or the
# install script; `go install` is not the currently recommended path).
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not found — see https://golangci-lint.run/welcome/install/"; exit 1; \
	}
	golangci-lint run ./...

check: fmt vet lint tidy test
	@echo "all checks passed"

install:
	go install .

clean:
	rm -f chisel
