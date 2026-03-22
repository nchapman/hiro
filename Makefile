.PHONY: build test clean web build-dev docker docker-up docker-down

BINARY := hive
PKG := github.com/nchapman/hivebot

build: web
	go build -o $(BINARY) ./cmd/hive

test:
	go test ./... -v -count=1

check: test
	go vet ./...

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
