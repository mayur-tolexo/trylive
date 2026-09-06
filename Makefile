.PHONY: build web test lint run docker

## build: build the web app and the Go binary that embeds it
build: web
	go build -o bin/trylive ./cmd/trylive

## web: build the app into web/dist
web:
	cd web && npm ci && npm run build

## test: Go tests (Postgres conformance when TRYLIVE_TEST_DATABASE_URL is set) and web tests
test:
	go test ./...
	cd web && npm test -- --run

## lint: gofmt and vet
lint:
	test -z "$$(gofmt -l cmd internal web)"
	go vet ./...

## run: start the server from .env against the in-memory store
run:
	set -a && . ./.env && set +a && go run ./cmd/trylive

## docker: build the production image
docker:
	docker build -f deploy/Dockerfile -t trylive:local .
