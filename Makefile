BINARY  := woosh
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
GOFILES := $(shell find . -name '*.go' -not -path './.git/*')

# Quality tools run through "go run tool@version", so they never enter go.mod
# and woosh stays free of dependencies. Pin them: an unpinned tool turns a
# green build red on somebody else's release day.
STATICCHECK  := honnef.co/go/tools/cmd/staticcheck@v0.7.0
GOVULNCHECK  := golang.org/x/vuln/cmd/govulncheck@v1.6.0

# Platforms that must keep compiling.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

# Floor for statement coverage, in percent.
COVER_MIN := 60

.PHONY: all build test test-short vet fmt fmt-check staticcheck vulncheck \
        no-deps cross cover quality check install clean help

all: build

## build: compile the woosh binary
build: $(BINARY)

$(BINARY): $(GOFILES) go.mod
	go build -ldflags '$(LDFLAGS)' -o $@ .

## test: run every test, including the ones that need multicast
test:
	go test -race ./...

## test-short: skip the tests that need working multicast
test-short:
	go test -short -race ./...

## vet: run go vet
vet:
	go vet ./...

## fmt: format the source in place
fmt:
	gofmt -w .

## fmt-check: fail if any file needs formatting
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "these files need gofmt:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	@echo "gofmt: clean"

## staticcheck: run the staticcheck linter
staticcheck:
	go run $(STATICCHECK) ./...
	@echo "staticcheck: clean"

## vulncheck: check the standard library and toolchain for known holes
vulncheck:
	go run $(GOVULNCHECK) ./...

## no-deps: fail if anything outside the standard library creeps in
no-deps:
	@if grep -q '^require' go.mod; then \
		echo "go.mod has a require: woosh is standard library only"; \
		grep '^require' -A20 go.mod; \
		exit 1; \
	fi
	@if [ -f go.sum ]; then \
		echo "go.sum exists: woosh is standard library only"; \
		exit 1; \
	fi
	@echo "no-deps: standard library only"

## cross: check that every supported platform still compiles
cross:
	@for target in $(PLATFORMS); do \
		os=$$(dirname $$target); arch=$$(basename $$target); \
		GOOS=$$os GOARCH=$$arch go build -o /dev/null . || exit 1; \
		echo "  compiles: $$os/$$arch"; \
	done

## cover: report statement coverage and fail below COVER_MIN
cover:
	@go test -short -covermode=atomic -coverprofile=coverage.out ./... >/dev/null
	@go tool cover -func=coverage.out | tail -1
	@total=$$(go tool cover -func=coverage.out | tail -1 | awk '{print $$NF}' | tr -d '%'); \
	awk -v got="$$total" -v min="$(COVER_MIN)" 'BEGIN { \
		if (got + 0 < min + 0) { \
			printf "coverage %.1f%% is below the %s%% floor\n", got, min; exit 1 \
		} \
		printf "coverage %.1f%% clears the %s%% floor\n", got, min \
	}'

## quality: every guard that does not need a second machine
quality: fmt-check vet staticcheck vulncheck no-deps cross cover

## check: the quality guards, plus the full test suite and a build
check: quality test build

## install: put woosh in GOBIN
install:
	go install -ldflags '$(LDFLAGS)' .

## clean: remove the built binary and the coverage profile
clean:
	rm -f $(BINARY) coverage.out

## help: list the targets
help:
	@grep '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
