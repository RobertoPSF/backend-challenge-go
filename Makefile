.PHONY: up down clean build fmt vet test test-race check queues test-integration

up:
	docker compose up --build -d

down:
	docker compose down

clean:
	docker compose down -v

build:
	go build -o bin/ ./...

fmt: 
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "rode: gofmt -w ." && exit 1)

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

check: fmt vet test-race

queues:
	docker compose exec localstack awslocal sqs list-queues

test-integration: ## Testes de integração com containers reais (testcontainers)
	go test -tags integration -race -count=1 ./test/...
