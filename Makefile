SHELL := /bin/sh

GO ?= go
GOFMT ?= gofmt
PNPM ?= pnpm
DOCKER ?= docker
COMPOSE := $(DOCKER) compose -f docker-compose.yaml

VERSION ?= dev
BIN_DIR ?= bin
IMAGE_TAG ?= mobile-release:local
APP_PORT ?= 8080
POSTGRES_HOST_PORT ?= 55432
VALKEY_HOST_PORT ?= 56379

.DEFAULT_GOAL := help

.PHONY: help setup deps tidy fmt fmt-check lint vet check quality \
	test-unit test-race test web-check web-build web-dev web-install \
	build build-version \
	infra-up infra-down infra-reset infra-logs infra-ps infra-stats \
	db-shell valkey-cli \
	docker-build docker-size docker-up docker-down docker-logs docker-shell \
	compose-config clean

help:
	@printf '%s\n' 'mobile-release — available targets:'
	@printf '%s\n' '' \
		'  make setup          install Go and frontend dependencies' \
		'  make deps           download Go modules' \
		'  make tidy           tidy Go modules' \
		'  make fmt            format Go files' \
		'  make fmt-check      fail when Go files need formatting' \
		'  make lint           run go vet' \
		'  make check          format check, lint, build and frontend checks' \
		'  make quality        run the complete local quality gate' \
		'' \
		'  make test-unit      run Go tests once' \
		'  make test-race      run Go tests with the race detector' \
		'  make test            run backend and frontend checks' \
		'' \
		'  make web-install     install frontend dependencies' \
		'  make web-dev         start the Svelte dev server' \
		'  make web-check       run svelte-check and TypeScript checks' \
		'  make web-build       build the frontend' \
		'' \
		'  make build           build all Go packages' \
		'  make build-version   build the CLI with a version string' \
		'' \
		'  make infra-up        start Postgres, Valkey and app' \
		'  make infra-down      stop local infrastructure without deleting data' \
		'  make infra-reset     stop infrastructure and delete the Postgres volume' \
		'  make infra-logs      follow infrastructure logs' \
		'  make infra-ps        show infrastructure status' \
		'  make infra-stats     show container CPU and memory usage' \
		'  make db-shell        open psql against local Postgres' \
		'  make valkey-cli      open the Valkey CLI' \
		'' \
		'  make docker-build    build the production image' \
		'  make docker-size     show the production image size' \
		'  make docker-up       build and start the Compose stack' \
		'  make docker-down     stop the Compose stack' \
		'  make docker-logs     follow app logs' \
		'  make docker-shell    open a shell in the app container' \
		'' \
		'  make clean           remove local build output'

setup: deps web-install

deps:
	$(GO) mod download

tidy:
	$(GO) mod tidy

fmt:
	$(GOFMT) -w $$(find . -name '*.go' -not -path './.git/*')

fmt-check:
	@test -z "$$($(GOFMT) -l $$(find . -name '*.go' -not -path './.git/*'))" || \
		{ printf '%s\n' 'Go files need formatting; run make fmt'; exit 1; }

lint: vet

vet:
	$(GO) vet ./...

check: fmt-check lint build web-check web-build compose-config

quality: check test-race

test-unit:
	$(GO) test -count=1 ./...

test-race:
	$(GO) test -race -count=1 ./...

test: test-unit web-check web-build

web-install:
	$(PNPM) --dir apps/web install --frozen-lockfile

web-dev:
	$(PNPM) --dir apps/web dev

web-check:
	$(PNPM) --dir apps/web check

web-build:
	$(PNPM) --dir apps/web build

build:
	$(GO) build ./...

build-version:
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN_DIR)/mobile-release ./cmd/mobile-release

compose-config:
	$(COMPOSE) config --quiet

infra-up:
	APP_HOST_PORT=$(APP_PORT) POSTGRES_HOST_PORT=$(POSTGRES_HOST_PORT) VALKEY_HOST_PORT=$(VALKEY_HOST_PORT) $(COMPOSE) up -d --wait

infra-down:
	$(COMPOSE) down --remove-orphans

infra-reset:
	$(COMPOSE) down -v --remove-orphans

infra-logs:
	$(COMPOSE) logs -f

infra-ps:
	$(COMPOSE) ps

infra-stats:
	$(DOCKER) stats --no-stream --format 'table {{.Name}}\t{{.MemUsage}}\t{{.MemPerc}}\t{{.CPUPerc}}'

db-shell:
	$(DOCKER) compose -f docker-compose.yaml exec postgres psql -U mobile_release -d mobile_release

valkey-cli:
	$(DOCKER) compose -f docker-compose.yaml exec valkey valkey-cli

docker-build:
	$(DOCKER) build -t $(IMAGE_TAG) .

docker-size:
	@$(DOCKER) image ls $(IMAGE_TAG) --format '{{.Repository}}:{{.Tag}} {{.Size}}'

docker-up:
	$(MAKE) infra-up

docker-down:
	$(MAKE) infra-down

docker-logs:
	$(COMPOSE) logs -f app

docker-shell:
	$(COMPOSE) exec app sh

clean:
	rm -rf $(BIN_DIR) apps/web/dist
