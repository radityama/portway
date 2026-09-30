.PHONY: setup dev dev-credentials doctor docker-up docker-down test test-race fuzz lint fmt fmt-check typecheck build check integration e2e load-test

FUZZTIME ?= 10s

setup:
	node scripts/setup.mjs

dev:
	node scripts/dev.mjs

dev-credentials:
	go run ./cmd/dev-init --force

doctor:
	node scripts/doctor.mjs

docker-up:
	docker compose --env-file .env -f deploy/docker/docker-compose.yml up -d --wait --wait-timeout 60

docker-down:
	docker compose --env-file .env -f deploy/docker/docker-compose.yml down

test:
	go test ./...
	pnpm test

test-race:
	go test -race ./...

fuzz:
	go test ./internal/protocol -run '^$$' -fuzz '^FuzzDecode$$' -fuzztime=$(FUZZTIME) -parallel=2
	go test ./internal/protocol -run '^$$' -fuzz '^FuzzFrameRoundTrip$$' -fuzztime=$(FUZZTIME) -parallel=2
	go test ./internal/protocol -run '^$$' -fuzz '^FuzzHandshake$$' -fuzztime=$(FUZZTIME) -parallel=2

lint:
	go vet ./...
	pnpm lint

fmt:
	gofmt -w cmd internal tests
	pnpm format

fmt-check:
	node scripts/check-go-format.mjs
	pnpm format:check

typecheck:
	pnpm typecheck

build:
	mkdir -p bin
	go build -o bin/portway ./cmd/portway
	go build -o bin/portway-relay ./cmd/relay
	pnpm build

check: fmt-check test test-race lint typecheck build

integration:
	go test ./tests/integration/...

e2e:
	go test ./tests/e2e/...

load-test:
	go test ./tests/load/...
