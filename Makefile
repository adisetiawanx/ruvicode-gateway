.PHONY: build build-all build-pricing build-reconcile run run-pricing run-reconcile test migrate-up migrate-down docker-build docker-run lint tidy

build:
	@mkdir -p bin
	go build -o bin/gateway.exe cmd/gateway/main.go

build-pricing:
	@mkdir -p bin
	go build -o bin/pricing.exe cmd/pricing/main.go

build-reconcile:
	@mkdir -p bin
	go build -o bin/reconcile.exe cmd/reconcile/main.go

build-all: build build-pricing build-reconcile

run:
	go run cmd/gateway/main.go

run-pricing:
	go run cmd/pricing/main.go

run-reconcile:
	go run cmd/reconcile/main.go

test:
	go test ./... -v -race -cover

migrate-up:
	golang-migrate -path migrations -database $(DATABASE_URL) up

migrate-down:
	golang-migrate -path migrations -database $(DATABASE_URL) down

docker-build:
	docker build -t ruvicode-gateway .

docker-run:
	docker run --env-file .env -p 8080:8080 ruvicode-gateway

lint:
	# --allow-parallel-runners: skip the file lock golangci-lint takes on
	# start. On this Windows setup the lock can go stale when the temp dir
	# changes (MSYS path mangling), which makes every run fail with
	# "parallel golangci-lint is running" until the lock file is found and
	# removed. A solo dev does not run two lints at once.
	golangci-lint run ./... --allow-parallel-runners

tidy:
	go mod tidy
