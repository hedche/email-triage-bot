BINARY  := email-triage-bot
PKG     := ./cmd/email-triage-bot
VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet fmt lint docker run-dry hooks clean

## build: compile the binary into ./dist
build:
	@mkdir -p dist
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY) $(PKG)

## test: run the unit tests (no test may touch the network)
test:
	go test ./...

## vet: go vet over every package
vet:
	go vet ./...

## fmt: rewrite sources with gofmt
fmt:
	gofmt -w .

## lint: fail if anything is not gofmt'd (what CI checks)
lint:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "These files are not gofmt'd:"; echo "$$unformatted"; exit 1; \
	fi

## docker: build the image locally for this machine's architecture
docker:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY):local .

## run-dry: a Dry Run against the mailboxes in .env.local, writing a Decision Log
run-dry:
	@test -f .env.local || { echo "no .env.local — copy .env.example and fill it in"; exit 1; }
	@mkdir -p dist
	set -a; . ./.env.local; set +a; \
	go run $(PKG) run --dry-run --decisions-out dist/decisions.jsonl

## hooks: enable the gitleaks pre-commit hook in this clone
hooks:
	git config core.hooksPath .githooks
	@echo "core.hooksPath set to .githooks (needs: brew install gitleaks)"

## clean: remove build output
clean:
	rm -rf dist
