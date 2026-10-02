GO ?= go

.PHONY: build test race check
build:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -trimpath -o bin/hopr ./cmd/hopr
test:
	$(GO) test ./...
race:
	$(GO) test -race ./...
check: test race
	$(GO) vet ./...
