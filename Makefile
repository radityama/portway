.PHONY: setup dev dev-credentials doctor docker-up docker-down test test-race fuzz lint fmt fmt-check typecheck build check control-integration database-integration fleet-integration domain-integration dashboard-integration observability-integration integration e2e load-test

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
	go test ./internal/protocol -run '^$$' -fuzz '^FuzzStreamPayloads$$' -fuzztime=$(FUZZTIME) -parallel=2
	go test ./internal/control -run '^$$' -fuzz '^FuzzControlJSON$$' -fuzztime=$(FUZZTIME) -parallel=2
	go test ./internal/httpwire -run '^$$' -fuzz '^FuzzWebSocketResponse$$' -fuzztime=$(FUZZTIME) -parallel=2

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
	go build -o bin/portway-cert ./cmd/certctl
	pnpm build

check: fmt-check test test-race lint typecheck build control-integration database-integration fleet-integration domain-integration dashboard-integration observability-integration

control-integration: build
	pnpm test:control

database-integration: build
	pnpm test:database
	pnpm test:database-control

fleet-integration: build
	pnpm test:fleet-api
	pnpm test:fleet

domain-integration: build
	pnpm test:domains-api
	pnpm test:domains

dashboard-integration: build
	pnpm test:dashboard

observability-integration: build
	pnpm test:observability

integration:
	go test ./tests/integration/...

e2e:
	go test ./tests/e2e/...

load-test:
	go test ./tests/load/...
