.PHONY: build run run-pricing run-reconcile test migrate-up migrate-down docker-build docker-run lint tidy

build:
	go build -o bin/gateway cmd/gateway/main.go

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
	golangci-lint run ./...

tidy:
	go mod tidy
