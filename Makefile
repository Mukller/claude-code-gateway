.PHONY: build run test vet fmt tidy docker-build docker-up docker-down

build:
	go build -o bin/gateway$(shell go env GOEXE) ./cmd/gateway

run:
	go run ./cmd/gateway -config config.yaml

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

docker-build:
	docker compose build

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down
