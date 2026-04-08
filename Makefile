.PHONY: build test test-local test-isolation test-netiso test-online test-cluster test-cluster-relay check lint clean web build-dev docker docker-up docker-down proto

# Auto-load .env variables. Command-line overrides (make VAR=x) take precedence.
# Variables are NOT exported globally — only test targets pass what they need.
-include .env

BINARY := hiro
PKG := github.com/nchapman/hiro

build: web
	go build -o $(BINARY) ./cmd/hiro

test:
	docker build --target test -t hiro-test .
	docker run --rm --init hiro-test

test-local:
	go test -race ./... -v -count=1

test-isolation:
	docker build --target test -t hiro-test .
	docker run --rm --init --cap-add NET_ADMIN hiro-test go test ./internal/agent/... -tags=isolation -v -count=1

test-netiso:
	docker compose -f docker-compose.test-netiso.yml build test
	docker compose -f docker-compose.test-netiso.yml run --rm test; \
	EXIT=$$?; \
	docker compose -f docker-compose.test-netiso.yml down; \
	exit $$EXIT

test-online:
	@if [ -z "$(HIRO_API_KEY)" ]; then echo "HIRO_API_KEY must be set"; exit 1; fi
	@# Build production image and start hiro server
	docker compose -f docker-compose.yml -f docker-compose.e2e.yml build hiro-e2e
	docker compose -f docker-compose.yml -f docker-compose.e2e.yml up -d hiro-e2e
	@# Discover the mapped port and run e2e tests on the host
	@PORT=$$(docker compose -f docker-compose.yml -f docker-compose.e2e.yml port hiro-e2e 8080 | cut -d: -f2); \
	HIRO_E2E_URL=http://localhost:$$PORT \
	HIRO_E2E_CONTAINER=$$(docker compose -f docker-compose.yml -f docker-compose.e2e.yml ps -q hiro-e2e) \
	HIRO_API_KEY=$(HIRO_API_KEY) HIRO_PROVIDER=$(HIRO_PROVIDER) HIRO_MODEL=$(HIRO_MODEL) \
	go test ./tests/e2e/... -tags=e2e -v -count=1 -timeout=10m; \
	EXIT=$$?; \
	docker compose -f docker-compose.yml -f docker-compose.e2e.yml down -v; \
	exit $$EXIT

test-cluster:
	@if [ -z "$(HIRO_API_KEY)" ]; then echo "HIRO_API_KEY must be set"; exit 1; fi; \
	mkdir -p tests/e2e_cluster/leader-config; \
	printf "cluster:\n  mode: leader\n" > tests/e2e_cluster/leader-config/config.yaml; \
	export HIRO_API_KEY=$(HIRO_API_KEY) HIRO_PROVIDER=$(HIRO_PROVIDER) HIRO_MODEL=$(HIRO_MODEL); \
	docker compose -f docker-compose.cluster.yml build; \
	docker compose -f docker-compose.cluster.yml up -d; \
	sleep 3; \
	PORT=$$(docker compose -f docker-compose.cluster.yml port leader 8080 | cut -d: -f2); \
	LEADER_ID=$$(docker compose -f docker-compose.cluster.yml ps -q leader); \
	WORKER_ID=$$(docker compose -f docker-compose.cluster.yml ps -q worker); \
	HIRO_E2E_URL=http://localhost:$$PORT \
	HIRO_LEADER_CONTAINER=$$LEADER_ID \
	HIRO_WORKER_CONTAINER=$$WORKER_ID \
	go test ./tests/e2e_cluster/... -tags=e2e_cluster -v -count=1 -timeout=10m; \
	EXIT=$$?; \
	echo "=== LEADER LOGS ==="; \
	docker compose -f docker-compose.cluster.yml logs leader --tail=50; \
	echo "=== WORKER LOGS ==="; \
	docker compose -f docker-compose.cluster.yml logs worker --tail=50; \
	docker compose -f docker-compose.cluster.yml down -v; \
	rm -rf tests/e2e_cluster/leader-config; \
	exit $$EXIT

test-cluster-relay:
	@if [ -z "$(HIRO_API_KEY)" ]; then echo "HIRO_API_KEY must be set"; exit 1; fi; \
	SWARM=$$(openssl rand -hex 16); \
	mkdir -p tests/e2e_cluster/leader-config; \
	printf "cluster:\n  mode: leader\n" > tests/e2e_cluster/leader-config/config.yaml; \
	export HIRO_API_KEY=$(HIRO_API_KEY) HIRO_PROVIDER=$(HIRO_PROVIDER) HIRO_MODEL=$(HIRO_MODEL); \
	docker compose -f docker-compose.cluster-relay.yml build; \
	export HIRO_SWARM_CODE=$$SWARM; \
	docker compose -f docker-compose.cluster-relay.yml up -d; \
	echo "Waiting for leader + relay registration..."; \
	sleep 15; \
	PORT=$$(docker compose -f docker-compose.cluster-relay.yml port leader 8080 | cut -d: -f2); \
	LEADER_ID=$$(docker compose -f docker-compose.cluster-relay.yml ps -q leader); \
	WORKER_ID=$$(docker compose -f docker-compose.cluster-relay.yml ps -q worker); \
	HIRO_E2E_URL=http://localhost:$$PORT \
	HIRO_LEADER_CONTAINER=$$LEADER_ID \
	HIRO_WORKER_CONTAINER=$$WORKER_ID \
	go test ./tests/e2e_cluster/... -tags=e2e_cluster -v -count=1 -timeout=10m; \
	EXIT=$$?; \
	echo "=== LEADER LOGS ==="; \
	docker compose -f docker-compose.cluster-relay.yml logs leader --tail=50; \
	echo "=== WORKER LOGS ==="; \
	docker compose -f docker-compose.cluster-relay.yml logs worker --tail=50; \
	docker compose -f docker-compose.cluster-relay.yml down -v; \
	rm -rf tests/e2e_cluster/leader-config; \
	exit $$EXIT

lint:
	golangci-lint run ./...

check:
	docker build --target test -t hiro-test .
	docker run --rm --init hiro-test sh -c "go test -race ./... -v -count=1 && go vet ./..."

clean:
	rm -f $(BINARY)
	rm -rf web/ui/dist

web:
	cd web/ui && npm install && npm run build

# Build without web UI (for development)
build-dev:
	go build -tags dev -o $(BINARY) ./cmd/hiro

docker:
	docker compose build

docker-up:
	docker compose up

docker-down:
	docker compose down

proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		internal/ipc/proto/hiro.proto
