.PHONY: build test test-local check clean web build-dev docker docker-up docker-down proto

BINARY := hive
PKG := github.com/nchapman/hivebot

build: web
	go build -o $(BINARY) ./cmd/hive

test:
	docker compose run --rm --build test

test-local:
	go test ./... -v -count=1

check:
	docker compose run --rm --build test sh -c "go test ./... -v -count=1 && go vet ./..."

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
