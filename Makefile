.PHONY: build test test-local test-isolation test-online check clean web build-dev docker docker-up docker-down proto

BINARY := hive
PKG := github.com/nchapman/hivebot

build: web
	go build -o $(BINARY) ./cmd/hive

test:
	docker build --target test -t hive-test .
	docker run --rm --init hive-test

test-local:
	go test -race ./... -v -count=1

test-isolation:
	docker build --target test -t hive-test .
	docker run --rm --init hive-test go test ./internal/agent/... -tags=isolation -v -count=1

test-online:
	@if [ -z "$$HIVE_API_KEY" ]; then echo "HIVE_API_KEY must be set"; exit 1; fi
	@# Build production image and start hive server
	docker compose -f docker-compose.yml -f docker-compose.e2e.yml build hive-e2e
	docker compose -f docker-compose.yml -f docker-compose.e2e.yml up -d hive-e2e
	@# Discover the mapped port and run e2e tests on the host
	@PORT=$$(docker compose -f docker-compose.yml -f docker-compose.e2e.yml port hive-e2e 8080 | cut -d: -f2); \
	HIVE_E2E_URL=http://localhost:$$PORT \
	HIVE_E2E_CONTAINER=$$(docker compose -f docker-compose.yml -f docker-compose.e2e.yml ps -q hive-e2e) \
	go test ./tests/e2e/... -tags=e2e -v -count=1 -timeout=10m; \
	EXIT=$$?; \
	docker compose -f docker-compose.yml -f docker-compose.e2e.yml down -v; \
	exit $$EXIT

check:
	docker build --target test -t hive-test .
	docker run --rm --init hive-test sh -c "go test -race ./... -v -count=1 && go vet ./..."

clean:
	rm -f $(BINARY)
	rm -rf web/ui/dist

web:
	cd web/ui && npm install && npm run build

# Build without web UI (for development)
build-dev:
	go build -tags dev -o $(BINARY) ./cmd/hive

docker:
	docker compose build

docker-up:
	docker compose up

docker-down:
	docker compose down

proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		internal/ipc/proto/hive.proto
