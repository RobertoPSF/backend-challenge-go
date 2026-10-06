.PHONY: up down clean build fmt vet test test-race check queues test-integration migrate-up migrate-down migrate-down-all migrate-version

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

MIGRATE = docker compose run --rm -T --entrypoint /bin/sh migrate -c

migrate-up: ## Aplica todas as migrations pendentes
	$(MIGRATE) 'migrate -path /migrations -database "$$DATABASE_MIGRATE_URL" up'

migrate-down: ## Reverte a última migration
	$(MIGRATE) 'migrate -path /migrations -database "$$DATABASE_MIGRATE_URL" down 1'

migrate-down-all: ## Reverte todas as migrations
	$(MIGRATE) 'migrate -path /migrations -database "$$DATABASE_MIGRATE_URL" down -all'

migrate-version: ## Mostra a versão atual do schema
	$(MIGRATE) 'migrate -path /migrations -database "$$DATABASE_MIGRATE_URL" version'
