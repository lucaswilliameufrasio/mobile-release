.PHONY: test lint build
test:
	go test -race ./...
lint:
	go vet ./...
build:
	go build -o bin/mobile-release ./cmd/mobile-release
