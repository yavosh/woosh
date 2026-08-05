BINARY  := woosh
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
GOFILES := $(shell find . -name '*.go' -not -path './.git/*')

.PHONY: all build test test-short vet fmt fmt-check check install clean help

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

## check: everything CI runs, plus the multicast tests
check: fmt-check vet test build

## install: put woosh in GOBIN
install:
	go install -ldflags '$(LDFLAGS)' .

## clean: remove the built binary
clean:
	rm -f $(BINARY)

## help: list the targets
help:
	@grep '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
