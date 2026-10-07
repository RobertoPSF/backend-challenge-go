.PHONY: up down clean build fmt vet test test-race check queues test-integration e2e-up e2e-down test-e2e e2e migrate-up migrate-down migrate-down-all migrate-version

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

E2E_COMPOSE = docker compose -f docker-compose.yml -f docker-compose.e2e.yml

e2e-up: ## Sobe o ambiente com três instâncias da aplicação (portas 8080, 8082 e 8083)
	$(E2E_COMPOSE) up --build -d --wait

e2e-down: ## Derruba o ambiente multi-instância e apaga os volumes
	$(E2E_COMPOSE) down -v

test-e2e: ## Testes contra as três instâncias já em execução
	go test -tags e2e -race -count=1 ./test/e2e/...

e2e: e2e-up test-e2e

MIGRATE = docker compose run --rm -T --entrypoint /bin/sh migrate -c

migrate-up: ## Aplica todas as migrations pendentes
	$(MIGRATE) 'migrate -path /migrations -database "$$DATABASE_MIGRATE_URL" up'

migrate-down: ## Reverte a última migration
	$(MIGRATE) 'migrate -path /migrations -database "$$DATABASE_MIGRATE_URL" down 1'

migrate-down-all: ## Reverte todas as migrations
	$(MIGRATE) 'migrate -path /migrations -database "$$DATABASE_MIGRATE_URL" down -all'

migrate-version: ## Mostra a versão atual do schema
	$(MIGRATE) 'migrate -path /migrations -database "$$DATABASE_MIGRATE_URL" version'

KEYCLOAK_TOKEN_URL = http://localhost:8081/realms/wagering/protocol/openid-connect/token

token-%: ## Imprime um access token do client informado (ex.: make token-provider-a, make token-wallet-service)
	@response=$$(curl -sf -u "$*:$*-local-secret" -d grant_type=client_credentials $(KEYCLOAK_TOKEN_URL)) \
		&& echo "$$response" | sed -E 's/.*"access_token":"([^"]+)".*/\1/'
