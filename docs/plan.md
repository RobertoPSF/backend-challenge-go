# Plano de Execução Detalhado — 3 dias

Este documento detalha cada etapa da implementação do desafio descrito no [README.md](../README.md). Cada etapa traz:

- **Objetivo**: o que precisa existir ao final;
- **Tarefas**: o trabalho concreto;
- **Detalhes técnicos**: SQL, assinaturas e fluxos de referência;
- **Testes**: o que comprova a etapa;
- **Pronto quando**: o critério objetivo para encerrá-la;
- **Registrar**: o que anotar em `docs/DECISIONS.md`.

O `docs/DECISIONS.md` é um log incremental de decisões, problemas encontrados e soluções de contorno. No fim, ele é a fonte do `ARCHITECTURE.md` e do relatório de entrega.

---

## Sumário

- [0. Decisões de base e convenções](#0-decisões-de-base-e-convenções)
- [Dia 1 — Fundação, domínio e persistência](#dia-1--fundação-domínio-e-persistência)
  - [1.1 Esqueleto do projeto e infraestrutura](#11-esqueleto-do-projeto-e-infraestrutura)
  - [1.2 Composição com Uber Fx](#12-composição-com-uber-fx)
  - [1.3 Domínio](#13-domínio)
  - [1.4 Schema e migrations](#14-schema-e-migrations)
  - [1.5 Repositórios e Unit of Work](#15-repositórios-e-unit-of-work)
  - [1.6 Autenticação e autorização](#16-autenticação-e-autorização)
  - [1.7 Endpoints de carteira](#17-endpoints-de-carteira)
- [Dia 2 — Núcleo transacional, mensageria e workers](#dia-2--núcleo-transacional-mensageria-e-workers)
  - [2.1 Hash canônico e idempotência](#21-hash-canônico-e-idempotência)
  - [2.2 Caso de uso ProcessWager](#22-caso-de-uso-processwager)
  - [2.3 Regras por tipo e reversões](#23-regras-por-tipo-e-reversões)
  - [2.4 Endpoints de transação](#24-endpoints-de-transação)
  - [2.5 Worker de referências pendentes](#25-worker-de-referências-pendentes)
  - [2.6 Consumidor SQS com inbox](#26-consumidor-sqs-com-inbox)
  - [2.7 Outbox publisher](#27-outbox-publisher)
  - [2.8 Reconciliação](#28-reconciliação)
  - [2.9 Observabilidade e health checks](#29-observabilidade-e-health-checks)
- [Dia 3 — Prova das garantias, falhas e documentação](#dia-3--prova-das-garantias-falhas-e-documentação)
  - [3.1 Testes de integração](#31-testes-de-integração)
  - [3.2 Testes de autenticação e autorização](#32-testes-de-autenticação-e-autorização)
  - [3.3 Multi-instância e concorrência](#33-multi-instância-e-concorrência)
  - [3.4 Simulação de falhas e recuperação](#34-simulação-de-falhas-e-recuperação)
  - [3.5 Documentação](#35-documentação)
  - [3.6 Checklist final de entrega](#36-checklist-final-de-entrega)
- [Apêndice A — Failure codes](#apêndice-a--failure-codes)
- [Apêndice B — Contrato de status HTTP](#apêndice-b--contrato-de-status-http)
- [Apêndice C — Máquina de estados](#apêndice-c--máquina-de-estados)
- [Apêndice D — Variáveis de ambiente](#apêndice-d--variáveis-de-ambiente)
- [Apêndice E — Riscos e plano de contingência](#apêndice-e--riscos-e-plano-de-contingência)

---

## 0. Decisões de base e convenções

### 0.1 Stack

| Tema | Escolha | Justificativa |
| --- | --- | --- |
| Go | 1.27.1 (em `go.mod` e na imagem `golang:1.27.1-alpine` do Dockerfile, D-004) | Versão mais recente e com suporte |
| Composição | `go.uber.org/fx` | Obrigatório |
| HTTP | `net/http` + `github.com/go-chi/chi/v5` | Roteamento com parâmetros, middlewares compatíveis com `net/http` |
| Banco | `github.com/jackc/pgx/v5` (`pgxpool`) com SQL explícito | Preferência do enunciado; locks e transações visíveis |
| Migrations | `github.com/golang-migrate/migrate/v4` | Arquivos `NNNN_nome.up.sql`/`.down.sql`, CLI e uso como biblioteca |
| SQS | `github.com/aws/aws-sdk-go-v2/service/sqs` | SDK oficial; endpoint configurável para o emulador |
| Emulador AWS | LocalStack com versão fixada (D-001) | Mais conhecido; versões recentes exigem token |
| IdP | Keycloak (realm importado via `--import-realm`) | Recomendado; provisionamento automático |
| OIDC/JWT | `github.com/coreos/go-oidc/v3` | Discovery, cache de JWKS, validação de `iss`, `aud`, `exp` e assinatura |
| IDs | UUIDv7 (`github.com/google/uuid`) | Ordenáveis no tempo, bons para índices e cursores |
| Logs | `log/slog` com `JSONHandler` | Biblioteca padrão |
| Métricas | `github.com/prometheus/client_golang` | Padrão de mercado; endpoint `/metrics` |
| Testes | `testing`, `go.uber.org/fx/fxtest`, `go.uber.org/goleak`, `testcontainers-go` | Infraestrutura real nos testes de integração |

### 0.2 Convenções

- **Timestamps:** `TIMESTAMPTZ` no banco, `time.Time` em UTC no código e RFC 3339 (com milissegundos) na serialização.
- **Dinheiro:** `int64` em unidades mínimas (centavos) e `CHAR(3)` para a moeda. **Nenhum** `float` no caminho.
- **Erros:** sentinelas de domínio e um tipo `*DomainError{Code, Kind}` classificável com `errors.Is`/`errors.As`.
- **Contexto:** toda função de I/O recebe `context.Context` como primeiro parâmetro.
- **Build tags:** `integration` (testcontainers) e `e2e` (contra o compose com 3 instâncias).
- **Binário único:** os componentes são ligados por variável de ambiente (`ENABLE_HTTP`, `ENABLE_CONSUMER`, `ENABLE_OUTBOX_WORKER`, `ENABLE_REFERENCE_WORKER`). Por padrão, todos rodam.

### 0.3 Estrutura de pacotes

```
cmd/
  api/main.go                 # fx.New(platform.Module, store.Module, app.Module, httpapi.Module, ...).Run()
internal/
  domain/                     # puro: sem imports de fx, net/http, aws, pgx
    money/                    # Money, Currency, parsing, aritmética
    wallet/                   # Wallet, LedgerEntry, Direction
    wager/                    # WagerTransaction, Kind, Status, FailureCode
    event/                    # Envelope + payloads tipados
    errors.go                 # DomainError, sentinelas
  app/                        # casos de uso: OpenWallet, ProcessWager, ResolvePending, Reconcile, queries
    ports.go                  # interfaces de repositório e UnitOfWork (definidas pelo consumidor)
  store/postgres/             # implementação pgx dos ports, UnitOfWork, mapeamentos
  httpapi/                    # router, handlers, DTOs, middlewares (auth, log, recover, correlation)
  auth/                       # verificador OIDC, Principal, políticas
  messaging/sqs/              # consumer, publisher, cliente
  worker/                     # outbox publisher, reference resolver, runner genérico
  platform/                   # config, logger, metrics, health, fault injection
migrations/                   # SQL versionado
deploy/
  keycloak/realm-wagering.json
  aws/init-queues.sh
  postgres/init-roles.sql
test/
  integration/                # //go:build integration
  e2e/                        # //go:build e2e
docs/
  plan.md, DECISIONS.md
```

**Registrar:** escolha de bibliotecas, estrutura de pacotes e motivo de manter o domínio isolado.

---

# Dia 1 — Fundação, domínio e persistência

**Meta do dia:** `docker compose up --build` sobe toda a infraestrutura; o domínio está completo e coberto por testes unitários; as migrations aplicam e revertem; é possível abrir e consultar carteiras com token real do Keycloak.

**Distribuição sugerida:** 1.1–1.2 (≈2h) · 1.3 (≈3h) · 1.4–1.5 (≈2h) · 1.6–1.7 (≈2h).

---

## 1.1 Esqueleto do projeto e infraestrutura

### Objetivo
Ter o repositório inicializado e um ambiente local reproduzível com Postgres, Keycloak, o emulador SQS e a aplicação.

### Tarefas
1. `go mod init github.com/RobertoPSF/backend-challenge-go` com `go 1.27.1`.
2. Criar a árvore de diretórios da seção 0.3.
3. `Dockerfile` multi-stage:
   - estágio `build`: `golang:1.27.1-alpine`, `go mod download`, `CGO_ENABLED=0 go build -o /out/api ./cmd/api`;
   - estágio final: `gcr.io/distroless/static` ou `alpine`, usuário não-root, `ENTRYPOINT ["/api"]`.
4. `docker-compose.yml` com os serviços:
   - `postgres` (16-alpine), com healthcheck `pg_isready` e o volume de init `deploy/postgres/init-roles.sql`;
   - `keycloak` (`quay.io/keycloak/keycloak:<versão fixa>`) com `start-dev --import-realm`, o volume `deploy/keycloak` e um healthcheck;
   - `aws` (LocalStack com versão fixada, D-001), com o script de init montado e um healthcheck que verifica se as filas existem;
   - `migrate`: serviço *one-shot* (`migrate/migrate`) que aplica `migrations/` com o usuário dono do schema e depende do Postgres saudável;
   - `app`: depende de `migrate` (`service_completed_successfully`), `keycloak` e `aws` saudáveis.
5. `deploy/aws/init-queues.sh` cria:
   - `wager-transactions-dlq.fifo` (`FifoQueue=true`);
   - `wager-transactions.fifo` (`FifoQueue=true`, `ContentBasedDeduplication=false`, `VisibilityTimeout=30`, `RedrivePolicy={"deadLetterTargetArn":"<dlq>","maxReceiveCount":"5"}`);
   - `wallet-events.fifo`, o destino dos eventos de saída, e sua DLQ `wallet-events-dlq.fifo`.
6. `deploy/postgres/init-roles.sql` cria dois roles:
   - `wallet_owner`: dono do schema, usado só pelas migrations;
   - `wallet_app`: DML apenas, **sem** `UPDATE`/`DELETE`/`TRUNCATE` em `wallet_ledger_entries`. Os grants são dados na própria migration.
7. `.env.example` com todas as variáveis do Apêndice D.
8. `Makefile` com os alvos `up`, `down`, `build`, `test`, `test-race`, `test-integration`, `test-e2e`, `migrate-up`, `migrate-down`, `lint` (`gofmt -l` + `go vet`) e `token-provider-a`, `token-admin` (curl no Keycloak).

### Detalhes técnicos
- As migrations rodam em um serviço separado, e não no boot de cada instância. Assim, as 3 réplicas não disputam a migração e o role da aplicação não precisa de DDL. O `golang-migrate` já usa advisory lock, mas a separação também resolve o privilégio.
- Cada instância da aplicação tem processo, pool de conexões e memória próprios. Isso é requisito da seção 8.

### Pronto quando
- `docker compose up --build` sobe todos os serviços como *healthy*.
- `aws sqs list-queues --endpoint-url ...` lista as 4 filas.
- `curl :8080/health/live` → `200`.

### Registrar
Versões fixadas das imagens, escolha do emulador AWS, separação de roles do banco e migrations como serviço one-shot.

---

## 1.2 Composição com Uber Fx

### Objetivo
Montar a aplicação inteira por injeção de construtores, com ciclo de vida observável e shutdown ordenado.

### Tarefas
1. `platform/config`: struct `Config` carregada do ambiente, com `Validate()` que falha o boot se algo obrigatório faltar ou estiver inválido (URLs, durações, limites).
2. `platform/logger`: `*slog.Logger` JSON com o nível configurável.
3. `platform/metrics`: um `prometheus.Registry` próprio, sem o registry global, para isolar os testes.
4. Módulos `fx.Module("postgres", ...)`, `fx.Module("sqs", ...)`, `fx.Module("auth", ...)`, `fx.Module("store", ...)`, `fx.Module("app", ...)`, `fx.Module("http", ...)`, `fx.Module("workers", ...)`.
5. Um **runner genérico de workers** (`worker.Runner`), reutilizado por consumer, outbox e reference worker:
   ```go
   type Loop interface {
       Name() string
       RunOnce(ctx context.Context) (didWork bool, err error)
   }
   // Runner: OnStart cria ctx cancelável e N goroutines; OnStop cancela,
   // aguarda WaitGroup até o deadline do ctx do OnStop e reporta goroutines que não terminaram.
   ```
6. Servidor HTTP com `OnStart` (`net.Listen` síncrono, para falhar cedo se a porta estiver ocupada, e depois `Serve` em goroutine) e `OnStop` (`srv.Shutdown(ctx)`).
7. `fx.StopTimeout(30 * time.Second)` e `fx.StartTimeout(60 * time.Second)`.

### Detalhes técnicos — ordem do ciclo de vida
O Fx executa os `OnStop` na ordem **inversa** dos `OnStart`. Como os recursos são construídos antes de quem depende deles, o fluxo natural é:

```
Start: config → logger → pgxpool (ping) → sqs client (GetQueueUrl) → oidc verifier (discovery)
       → workers → http server
Stop:  http server (para de aceitar e drena requisições) → workers (cancelam a busca, terminam
       ou liberam o trabalho em andamento) → oidc → sqs → pgxpool.Close()
```

Isso cumpre a regra de fechar as dependências só depois que os componentes que as usam terminaram.

### Testes
- `fxtest.New(t, modules..., fx.Populate(...))` com `app.RequireStart().RequireStop()`, contra infraestrutura real (tag `integration`).
- `goleak.VerifyNone(t)` depois do Stop, para provar que os workers liberaram as goroutines.
- `fx.ValidateApp(...)` em teste unitário, para checar o grafo sem subir nada.

### Pronto quando
A aplicação sobe e desce sem vazamento; um SIGTERM gera logs `worker stopped` de cada componente antes de `postgres pool closed`.

### Registrar
Organização dos módulos, contrato do `Runner` e ordem de shutdown.

---

## 1.3 Domínio

### Objetivo
Modelar as regras financeiras com estado encapsulado, construtores validados, reidratação separada e erros classificáveis, sem nenhuma dependência de infraestrutura.

### 1.3.1 Money (`domain/money`)

```go
type Currency struct{ code string }           // não exportado → zero value inválido
func ParseCurrency(s string) (Currency, error) // ISO 4217 (allowlist: BRL, USD, EUR, ...)

type Money struct {
    units    int64     // centavos
    currency Currency
}
func Parse(amount string, currency string) (Money, error) // entrada externa: não negativo
func Zero(c Currency) Money
func FromMinorUnits(units int64, c Currency) (Money, error) // reidratação a partir do banco
func (m Money) Add(o Money) (Money, error)
func (m Money) Sub(o Money) (Money, error)
func (m Money) Neg() (Money, error)            // falha para math.MinInt64
func (m Money) Cmp(o Money) (int, error)
func (m Money) IsZero() bool; IsNegative() bool; IsValid() bool
func (m Money) String() string                 // "25.00"
func (m Money) MarshalJSON() / UnmarshalJSON   // {"amount":"25.00","currency":"BRL"}
```

Regras do parsing (`Parse`):
- regex `^(0|[1-9]\d*)\.\d{2}$`, que rejeita vazio, sinal, `NaN`, `Infinity`, `1e3`, `.5`, `1.5`, `1.555`, espaços e zeros à esquerda;
- a conversão é feita **dígito a dígito** com checagem de overflow. Não usar `strconv.ParseFloat`; `strconv.ParseInt` pode ser usado na parte inteira, com verificação da multiplicação por 100;
- a moeda passa por `ParseCurrency`, em maiúsculas estritas. `brl` é rejeitado, sem normalização silenciosa;
- `Money{}` (zero value) é inválido e qualquer operação com ele retorna `ErrInvalidMoney`.

Aritmética: `Add`/`Sub` checam overflow (`a > 0 && b > MaxInt64 - a`, etc.) e moeda igual (`ErrCurrencyMismatch`).

**Limites documentados:** o máximo é `92.233.720.368.547.758,07` unidades. A escala é fixa em 2, então moedas com 0 ou 3 casas decimais (JPY, KWD) não são suportadas. Isso vai para as limitações.

**Testes (tabela):** formatos válidos e inválidos, limites (`MaxInt64` em centavos, +1 dando overflow), soma, subtração e negação com overflow, moedas incompatíveis, zero value, ida e volta do JSON e garantia de que `MarshalJSON` sempre devolve 2 casas.

### 1.3.2 Wallet e LedgerEntry (`domain/wallet`)

```go
type Wallet struct { id, playerID uuid.UUID; balance money.Money; version int64; createdAt, updatedAt time.Time }

func Open(id, playerID uuid.UUID, initial money.Money, now time.Time) (*Wallet, error) // version = 1
func Rehydrate(id, playerID uuid.UUID, balance money.Money, version int64, createdAt, updatedAt time.Time) (*Wallet, error)
// Rehydrate só valida a consistência (balance >= 0, version >= 1); não gera eventos nem ledger.

func (w *Wallet) Debit(txID uuid.UUID, amount money.Money, now time.Time) (LedgerEntry, error)
func (w *Wallet) Credit(txID uuid.UUID, amount money.Money, now time.Time) (LedgerEntry, error)
// ambos: amount > 0, mesma moeda, débito não deixa saldo < 0 (ErrInsufficientFunds),
// incrementam version e atualizam updatedAt, retornam o lançamento correspondente.

type LedgerEntry struct { id, walletID, transactionID uuid.UUID; direction Direction; amount, balanceBefore, balanceAfter money.Money; createdAt time.Time }
func NewLedgerEntry(...) (LedgerEntry, error) // valida after = before ± amount conforme direction
```

A abertura com saldo inicial > 0 gera um `Credit` de abertura: o `balanceBefore` é 0, o `balanceAfter` é o inicial e a **versão permanece 1**. A versão inicial é 1 por regra; o crédito de abertura faz parte da criação, então não incrementa.

**Testes:** abertura com zero e com positivo, débito exato até zerar, débito acima do saldo, moeda divergente, versão incrementada só quando há mudança e reidratação que não altera a versão nem gera lançamento.

### 1.3.3 WagerTransaction (`domain/wager`)

```go
type Kind string   // OPENING, BET, WIN, LOSS, REFUND, ROLLBACK
type Status string // PENDING, PENDING_REFERENCE, PROCESSED, REJECTED, FAILED

type ExternalRequest struct { ProviderID, ExternalTransactionID, IdempotencyKey, PayloadHash, PlayerID, WalletID, RoundID, GameID, Kind, Money, ReferenceExternalTransactionID }

func NewExternal(id uuid.UUID, req ExternalRequest, now time.Time) (*Transaction, error) // rejeita OPENING
func NewOpening(id uuid.UUID, walletID, playerID uuid.UUID, amount money.Money, now time.Time) (*Transaction, error) // já PROCESSED
func Rehydrate(...) (*Transaction, error)

func (t *Transaction) MarkProcessed(balanceAfter money.Money, referenceID *uuid.UUID, now time.Time) error
func (t *Transaction) MarkRejected(code FailureCode, now time.Time) error
func (t *Transaction) MarkPendingReference(nextAttempt time.Time, now time.Time) error
func (t *Transaction) MarkFailed(code FailureCode, now time.Time) error
func (t *Transaction) ScheduleRetry(next time.Time) error // só em PENDING_REFERENCE
```

- As transições inválidas retornam `ErrInvalidTransition`. Os estados terminais retornam `ErrTerminalState`.
- **Política de valor zero:** `LOSS` exige exatamente `0.00`; `BET`, `WIN`, `REFUND` e `ROLLBACK` exigem `> 0`; `OPENING` aceita `> 0`, e o zero nem cria a transação.
- `REFUND`/`ROLLBACK` sem `ReferenceExternalTransactionID` são rejeitados como entrada inválida.

**Testes:** todas as transições válidas e inválidas, terminais imutáveis, política de zero por tipo, OPENING recusado em `NewExternal` e OPENING sem metadados externos.

### 1.3.4 Eventos (`domain/event`)

```go
type Envelope[T any] struct {
    EventID       uuid.UUID  `json:"eventId"`
    EventType     string     `json:"eventType"`
    AggregateID   uuid.UUID  `json:"aggregateId"`
    CorrelationID string     `json:"correlationId"`
    CausationID   *string    `json:"causationId,omitempty"`
    OccurredAt    time.Time  `json:"occurredAt"`
    Version       int        `json:"version"`
    Data          T          `json:"data"`
}
func NewWagerTransactionProcessed(...) Envelope[WagerTransactionProcessedData]   // type e version fixados no construtor
func NewWagerTransactionRejected(...) Envelope[WagerTransactionRejectedData]
func NewWalletBalanceChanged(...) Envelope[WalletBalanceChangedData]             // walletId, transactionId, direction, money, balanceBefore, balanceAfter, walletVersion
func NewWagerTransactionPendingReference(...) Envelope[WagerTransactionPendingReferenceData]
```

O payload é serializado **uma única vez**, na criação, e gravado na outbox como snapshot (`JSONB`). Os publishers nunca o recalculam.

### 1.3.5 Erros
```go
type ErrorKind int // KindValidation (corrigível), KindBusiness (terminal), KindConflict, KindNotFound, KindTransient, KindPermanent
type DomainError struct { Kind ErrorKind; Code FailureCode; Msg string }
var ErrInsufficientFunds = &DomainError{Kind: KindBusiness, Code: "INSUFFICIENT_FUNDS"} // etc.
```
O mapeamento de erro para status HTTP e para a ação no SQS fica **fora** do domínio (Apêndice B).

### Pronto quando
`go test -race ./internal/domain/...` passa, com cobertura acima de 90% no pacote `domain`.

### Registrar
Representação do `Money`, limites, allowlist de moedas, estratégia de versão na abertura e a taxonomia de erros.

---

## 1.4 Schema e migrations

### Objetivo
Impor no banco as invariantes de unicidade, não negatividade, imutabilidade do ledger e coerência das transações, de forma independente do código.

### Migrations planejadas

| Arquivo | Conteúdo |
| --- | --- |
| `0001_wallets` | tabela `wallets` |
| `0002_wager_transactions` | tabela, CHECKs de origem, índices únicos, trigger de estado terminal |
| `0003_ledger` | `wallet_ledger_entries`, triggers de imutabilidade, grants restritos |
| `0004_inbox_outbox` | `inbox_messages`, `outbox_events` |
| `0005_grants` | grants do `wallet_app` |

### Esboço de schema

```sql
CREATE TABLE wallets (
  id          UUID PRIMARY KEY,
  player_id   UUID NOT NULL,
  currency    CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  balance     BIGINT NOT NULL CHECK (balance >= 0),
  version     BIGINT NOT NULL CHECK (version >= 1),
  created_at  TIMESTAMPTZ NOT NULL,
  updated_at  TIMESTAMPTZ NOT NULL,
  CONSTRAINT wallets_player_currency_uk UNIQUE (player_id, currency)
);

CREATE TABLE wager_transactions (
  id                                 UUID PRIMARY KEY,
  origin                             TEXT NOT NULL CHECK (origin IN ('INTERNAL','EXTERNAL')),
  kind                               TEXT NOT NULL CHECK (kind IN ('OPENING','BET','WIN','LOSS','REFUND','ROLLBACK')),
  status                             TEXT NOT NULL CHECK (status IN ('PENDING','PENDING_REFERENCE','PROCESSED','REJECTED','FAILED')),
  wallet_id                          UUID NOT NULL REFERENCES wallets(id),
  player_id                          UUID NOT NULL,
  currency                           CHAR(3) NOT NULL,
  amount                             BIGINT NOT NULL CHECK (amount >= 0),
  provider_id                        TEXT,
  external_transaction_id            TEXT,
  idempotency_key                    TEXT,
  payload_hash                       TEXT,
  round_id                           TEXT,
  game_id                            TEXT,
  reference_external_transaction_id  TEXT,
  reference_transaction_id           UUID REFERENCES wager_transactions(id),
  failure_code                       TEXT,
  balance_after                      BIGINT,          -- saldo observado no processamento (replay)
  attempts                           INT NOT NULL DEFAULT 0,
  next_attempt_at                    TIMESTAMPTZ,
  expires_at                         TIMESTAMPTZ,
  correlation_id                     TEXT,
  created_at                         TIMESTAMPTZ NOT NULL,
  updated_at                         TIMESTAMPTZ NOT NULL,
  processed_at                       TIMESTAMPTZ,

  -- separação interna x externa
  CONSTRAINT origin_internal_ck CHECK (origin <> 'INTERNAL' OR (
      kind = 'OPENING' AND provider_id IS NULL AND external_transaction_id IS NULL
      AND idempotency_key IS NULL AND payload_hash IS NULL AND round_id IS NULL
      AND game_id IS NULL AND reference_external_transaction_id IS NULL)),
  CONSTRAINT origin_external_ck CHECK (origin <> 'EXTERNAL' OR (
      kind <> 'OPENING' AND provider_id IS NOT NULL AND external_transaction_id IS NOT NULL
      AND idempotency_key IS NOT NULL AND payload_hash IS NOT NULL
      AND round_id IS NOT NULL AND game_id IS NOT NULL)),
  CONSTRAINT reversal_requires_ref_ck CHECK (kind NOT IN ('REFUND','ROLLBACK') OR reference_external_transaction_id IS NOT NULL),
  CONSTRAINT loss_zero_ck CHECK (kind <> 'LOSS' OR amount = 0),
  CONSTRAINT positive_amount_ck CHECK (kind IN ('LOSS') OR amount > 0),
  CONSTRAINT rejected_has_code_ck CHECK (status NOT IN ('REJECTED','FAILED') OR failure_code IS NOT NULL)
);

-- idempotência persistente
CREATE UNIQUE INDEX wt_provider_idem_uk  ON wager_transactions (provider_id, idempotency_key)          WHERE origin = 'EXTERNAL';
CREATE UNIQUE INDEX wt_provider_ext_uk   ON wager_transactions (provider_id, external_transaction_id)  WHERE origin = 'EXTERNAL';
-- impede crédito inicial duplicado
CREATE UNIQUE INDEX wt_opening_uk        ON wager_transactions (wallet_id) WHERE kind = 'OPENING';
-- no máximo uma reversão bem-sucedida por transação referenciada
CREATE UNIQUE INDEX wt_single_reversal_uk ON wager_transactions (reference_transaction_id)
  WHERE status = 'PROCESSED' AND kind IN ('REFUND','ROLLBACK');
-- fila de pendências
CREATE INDEX wt_pending_idx ON wager_transactions (next_attempt_at) WHERE status IN ('PENDING','PENDING_REFERENCE');
-- busca de pendentes que aguardam uma referência recém-chegada
CREATE INDEX wt_waiting_ref_idx ON wager_transactions (provider_id, reference_external_transaction_id) WHERE status = 'PENDING_REFERENCE';

-- trigger: estado terminal é imutável
CREATE FUNCTION forbid_terminal_update() RETURNS trigger AS $$
BEGIN
  IF OLD.status IN ('PROCESSED','REJECTED','FAILED') THEN
    RAISE EXCEPTION 'transaction % is terminal (%)', OLD.id, OLD.status USING ERRCODE = 'P0001';
  END IF;
  RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER wt_terminal_guard BEFORE UPDATE ON wager_transactions FOR EACH ROW EXECUTE FUNCTION forbid_terminal_update();

CREATE TABLE wallet_ledger_entries (
  id              UUID PRIMARY KEY,
  wallet_id       UUID NOT NULL REFERENCES wallets(id),
  transaction_id  UUID NOT NULL REFERENCES wager_transactions(id),
  direction       TEXT NOT NULL CHECK (direction IN ('DEBIT','CREDIT')),
  amount          BIGINT NOT NULL CHECK (amount > 0),
  currency        CHAR(3) NOT NULL,
  balance_before  BIGINT NOT NULL CHECK (balance_before >= 0),
  balance_after   BIGINT NOT NULL CHECK (balance_after >= 0),
  created_at      TIMESTAMPTZ NOT NULL,
  CONSTRAINT ledger_wallet_tx_uk UNIQUE (wallet_id, transaction_id),
  CONSTRAINT ledger_math_ck CHECK (
     (direction = 'CREDIT' AND balance_after = balance_before + amount) OR
     (direction = 'DEBIT'  AND balance_after = balance_before - amount))
);
CREATE INDEX ledger_wallet_cursor_idx ON wallet_ledger_entries (wallet_id, created_at, id);

-- imutabilidade: trigger para UPDATE/DELETE e TRUNCATE + grants
CREATE FUNCTION forbid_ledger_mutation() RETURNS trigger AS $$
BEGIN RAISE EXCEPTION 'wallet_ledger_entries is append-only' USING ERRCODE = 'P0001'; END $$ LANGUAGE plpgsql;
CREATE TRIGGER ledger_no_update_delete BEFORE UPDATE OR DELETE ON wallet_ledger_entries FOR EACH ROW EXECUTE FUNCTION forbid_ledger_mutation();
CREATE TRIGGER ledger_no_truncate BEFORE TRUNCATE ON wallet_ledger_entries FOR EACH STATEMENT EXECUTE FUNCTION forbid_ledger_mutation();
REVOKE UPDATE, DELETE, TRUNCATE ON wallet_ledger_entries FROM wallet_app;

CREATE TABLE inbox_messages (
  consumer_name   TEXT NOT NULL,
  message_id      TEXT NOT NULL,
  payload_hash    TEXT NOT NULL,
  transaction_id  UUID REFERENCES wager_transactions(id),
  received_at     TIMESTAMPTZ NOT NULL,
  completed_at    TIMESTAMPTZ,
  PRIMARY KEY (consumer_name, message_id)
);

CREATE TABLE outbox_events (
  event_id        UUID PRIMARY KEY,           -- estável: reaproveitado em toda republicação
  aggregate_type  TEXT NOT NULL,
  aggregate_id    UUID NOT NULL,
  event_type      TEXT NOT NULL,
  event_version   INT  NOT NULL,
  payload         JSONB NOT NULL,             -- envelope completo, snapshot imutável
  correlation_id  TEXT,
  occurred_at     TIMESTAMPTZ NOT NULL,
  attempts        INT NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ NOT NULL,
  locked_by       TEXT,
  locked_until    TIMESTAMPTZ,
  published_at    TIMESTAMPTZ,
  last_error      TEXT
);
CREATE INDEX outbox_pending_idx ON outbox_events (next_attempt_at) WHERE published_at IS NULL;
-- trigger: payload/event_id/event_type imutáveis após insert
```

### Tarefas
1. Escrever as migrations `up`/`down`. O `down` do ledger precisa remover os triggers antes do `DROP TABLE`.
2. Documentar `make migrate-up`, `make migrate-down` (um passo) e `migrate ... down -all`.

### Testes (integração)
- Up → down → up sem erro.
- `UPDATE`, `DELETE` e `TRUNCATE` no ledger falham, tanto com `wallet_app` (permission denied) quanto com o dono (trigger).
- `balance < 0` viola o CHECK; uma segunda carteira para o mesmo `(player, currency)` viola o UNIQUE.
- Um segundo `OPENING` para a mesma carteira viola o UNIQUE.
- Uma segunda reversão `PROCESSED` da mesma referência viola o UNIQUE.
- `UPDATE` em transação terminal falha.

### Pronto quando
Todas as invariantes da seção 5.8 do enunciado são verificáveis com SQL puro, sem a aplicação.

### Registrar
Cada constraint e a invariante que ela protege, as duas camadas de imutabilidade e por que o saldo de replay é gravado na transação.

---

## 1.5 Repositórios e Unit of Work

### Objetivo
Repositórios que operam dentro de uma transação SQL delimitada explicitamente pelo caso de uso.

### Detalhes técnicos
```go
// app/ports.go
type UnitOfWork interface {
    Do(ctx context.Context, opts TxOptions, fn func(ctx context.Context, r Repos) error) error
}
type Repos interface {
    Wallets() WalletRepository
    Transactions() TransactionRepository
    Ledger() LedgerRepository
    Inbox() InboxRepository
    Outbox() OutboxRepository
}
type WalletRepository interface {
    Insert(ctx, *wallet.Wallet) error                      // mapeia 23505 → ErrWalletAlreadyExists
    GetForUpdate(ctx, id uuid.UUID) (*wallet.Wallet, error) // SELECT ... FOR UPDATE
    Get(ctx, id uuid.UUID) (*wallet.Wallet, error)
    UpdateBalance(ctx, w *wallet.Wallet, expectedVersion int64) error // WHERE id=$1 AND version=$2; 0 linhas → ErrConcurrentUpdate
}
```
- O `UnitOfWork` em Postgres faz `pool.BeginTx`; `fn` recebe repositórios ligados ao `pgx.Tx`; commit se não houver erro, rollback caso contrário.
- **Retry de transação:** erros `40001` (serialization_failure), `40P01` (deadlock_detected) e `ErrConcurrentUpdate` são retentados até 3 vezes com backoff curto e jitter. Cada retry incrementa a métrica `wallet_concurrency_conflicts_total`.
- Erros de conexão ou timeout viram `KindTransient`, e o handler responde 503.
- Um `statement_timeout` e um `lock_timeout` (ex.: 5s) configurados na conexão evitam espera infinita em lock.

### Testes (integração)
- Rollback em erro não deixa nenhum rastro (carteira, ledger ou outbox).
- `UpdateBalance` com versão antiga retorna `ErrConcurrentUpdate`.

### Registrar
Onde a transação é delimitada (sempre no caso de uso, nunca no repositório), a política de retry e os timeouts.

---

## 1.6 Autenticação e autorização

### Objetivo
Toda rota de negócio exige um JWT válido emitido pelo Keycloak; a identidade determina o `providerId`; operações de carteira são restritas ao serviço interno.

### Provisionamento do Keycloak (`deploy/keycloak/realm-wagering.json`)
- Realm `wagering`.
- Client scope `wagering-api` com mapper de **audience** (`aud: wagering-api`).
- Clients confidenciais com `serviceAccountsEnabled: true` e só `client_credentials`:
  - `provider-a`: mapper *hardcoded claim* `provider_id = provider-a` e realm role `provider`;
  - `provider-b`: idem com `provider-b`;
  - `wallet-service`: realm role `wallet-admin`;
  - `provider-a-short`: `access.token.lifespan = 5` segundos, para o teste de token expirado.
- Os secrets são fixos de desenvolvimento e ficam no `.env.example`, marcados como **apenas locais**.

### Validação
- `oidc.NewProvider(ctx, issuer)` no `OnStart`, com retry até o Keycloak responder; `verifier := provider.Verifier(&oidc.Config{ClientID: "wagering-api"})`, que valida assinatura (JWKS com cache e rotação), `iss`, `aud` e `exp`.
- Docker: o issuer visto pelo container (`http://keycloak:8080`) é diferente do visto pelo host (`localhost:8081`). Solução: `KC_HOSTNAME` fixo para que o `iss` seja único, ou config separada `OIDC_ISSUER` (validação) / `OIDC_DISCOVERY_URL` (busca). Fica registrado como problema conhecido.

### Modelo de permissões
```go
type Principal struct { Subject string; ProviderID string; Roles []string }
```
| Rota | Regra |
| --- | --- |
| `POST /wallets`, `GET /wallets/*`, `POST /wallets/:id/reconciliation` | role `wallet-admin` |
| `POST /wagering/transactions` | role `provider`; `body.providerId == principal.ProviderID`, senão **403 antes de qualquer escrita** |
| `GET /wagering/transactions/:id` | `wallet-admin` vê tudo; `provider` só vê as próprias, e as de outro provedor dão **404** para não revelar a existência |
| `GET /providers/:providerId/wagering/transactions/:ext` | `path.providerId == principal.ProviderID`, senão 403 |
| `/health/*`, `/metrics` | públicos |

- **Replay entre provedores:** a busca de idempotência é sempre escopada por `provider_id` do token. O `provider-b` reenviando a chave do `provider-a` não encontra nada; e, como o body precisa ter `providerId = provider-b`, os registros também ficam separados.
- **SQS:** não há token. O acesso é controlado por credenciais e políticas do broker (usuário/role IAM com `sqs:SendMessage` só na fila de entrada; o app com `ReceiveMessage`/`DeleteMessage`/`ChangeMessageVisibility`). O consumidor mantém as validações de domínio: `providerId` precisa estar na allowlist configurada (`KNOWN_PROVIDERS`) e `idempotencyKey` não pode ser vazia. Se o emulador não aplicar IAM, a política é escrita mesmo assim e a limitação fica documentada.

### Testes
Ficam na etapa 3.2.

### Pronto quando
`curl` sem token → 401; token do provider em `POST /wallets` → 403; token admin → 201.

### Registrar
Escolha do Keycloak, `client_credentials`, claims usadas, modelo de roles, 404 vs 403 e a limitação do IAM no emulador.

---

## 1.7 Endpoints de carteira

### Objetivo
Abrir e consultar carteiras, com abertura atômica.

### `POST /wallets` — caso de uso `OpenWallet`
Em uma única transação:
1. `wallet.Open(...)`.
2. `INSERT wallets`; um conflito `23505` em `wallets_player_currency_uk` vira **409 `WALLET_ALREADY_EXISTS`**.
3. Se o saldo inicial for > 0:
   - `wager.NewOpening(...)` (`PROCESSED`, `origin = INTERNAL`);
   - lançamento `CREDIT` de 0 para o inicial;
   - outbox com `WagerTransactionProcessed` + `WalletBalanceChanged` (`walletVersion = 1`).
4. Commit e resposta `201` com `{id, playerId, balance, version: 1}`.

### `GET /wallets/:walletId`
Retorna a carteira, ou 404.

### `GET /wallets/:walletId/ledger?cursor=&limit=`
- Ordenação estável `(created_at, id)`.
- O cursor é `base64url(json{"t": created_at, "id": id})`, opaco para o cliente. Um cursor inválido dá 400.
- `limit` padrão 50, máximo 200.
- Resposta: `{ "items": [...], "nextCursor": "..." | null }`.
- Query: `WHERE wallet_id=$1 AND (created_at, id) > ($2, $3) ORDER BY created_at, id LIMIT $4+1`.

### Testes
- Unitário do handler (parse, validação e mapeamento de erros) e integração da abertura atômica (carteira + transação + ledger + 2 eventos).
- Saldo inicial zero não cria OPENING, ledger nem eventos.
- Abertura duplicada → 409.

### Pronto quando
O fluxo de abertura e leitura funciona via curl com token admin.

---

# Dia 2 — Núcleo transacional, mensageria e workers

**Meta do dia:** as operações BET, WIN, LOSS, REFUND e ROLLBACK funcionam por HTTP e por SQS, com as mesmas garantias; referências pendentes são resolvidas pelo worker; os eventos saem pela outbox; existem reconciliação, métricas e readiness.

**Distribuição sugerida:** 2.1–2.4 (≈4h) · 2.5 (≈1h) · 2.6 (≈1h30) · 2.7 (≈1h) · 2.8–2.9 (≈1h).

---

## 2.1 Hash canônico e idempotência

### Objetivo
Um hash determinístico e idêntico para HTTP e SQS, que permita detectar replay e conflito de payload.

### Algoritmo
1. O DTO de entrada (HTTP body ou `data` da mensagem SQS) é convertido em um `wager.CanonicalRequest` **depois** da validação. O `Money` já sai normalizado pelo próprio parse estrito, porque a forma aceita é única.
2. Campos incluídos: `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money.amount`, `money.currency` e `referenceExternalTransactionId` (omitido quando ausente).
3. Campos excluídos: `idempotencyKey`, `messageId`, `occurredAt`, `type`, headers, correlation id e qualquer outro metadado de transporte.
4. Normalização: UUIDs em minúsculas canônicas (`uuid.UUID.String()`); os demais textos sem trim implícito (espaço é rejeitado na validação).
5. Serialização: `map[string]any` com valores string e objeto aninhado para `money`. `encoding/json` ordena as chaves de mapas, o que dá JSON canônico sem espaços e com chaves ordenadas.
6. `hex(sha256(bytes))`, gravado como `payload_hash`.

### Regras de idempotência
| Situação | Resultado |
| --- | --- |
| Mesma `(providerId, idempotencyKey)` e mesmo hash | replay: devolve o resultado persistido com `idempotentReplay: true` |
| Mesma `(providerId, idempotencyKey)` e hash diferente | **409 `IDEMPOTENCY_KEY_CONFLICT`** |
| Chave nova, mas `(providerId, externalTransactionId)` já existente | **409 `EXTERNAL_TRANSACTION_CONFLICT`**: não reaplica com outra chave |
| `Idempotency-Key` ausente (HTTP) | 400 |

O servidor **nunca** substitui a chave recebida por uma calculada.

### Testes (unitários)
- O mesmo pedido vindo de um DTO HTTP e de um DTO SQS gera o mesmo hash.
- A ordem dos campos no JSON de entrada não altera o hash.
- Mudar um único campo de negócio altera o hash; mudar a chave ou o `messageId` não altera.

### Registrar
Algoritmo, campos, normalizações e a equivalência HTTP/SQS.

---

## 2.2 Caso de uso `ProcessWager`

### Objetivo
Um único caso de uso, usado por HTTP, SQS e pelo worker de pendências, que aplica a operação com exatamente um efeito financeiro, mesmo com concorrência entre processos.

### Fluxo (transação `READ COMMITTED`)
```
ProcessWager(ctx, cmd) → Result{Transaction, Balance, Replay bool}

UoW.Do:
 1. txRow := INSERT INTO wager_transactions (... status='PENDING' ...)
             ON CONFLICT DO NOTHING RETURNING id
    1a. se não inseriu → SELECT por (provider_id, idempotency_key), depois por (provider_id, external_transaction_id)
        - mesmo hash → return replay (resultado persistido: status, failure_code, balance_after)
        - hash diferente / outra chave → conflito 409
 2. se kind ∈ {REFUND, ROLLBACK}:
        ref := SELECT ... WHERE provider_id=$1 AND external_transaction_id=$2
        - inexistente → MarkPendingReference(next=now+backoff(0)), outbox PendingReference, commit, return 202
 3. w := SELECT * FROM wallets WHERE id=$1 FOR UPDATE     ← único lock da operação, por carteira
        - inexistente → 422 WALLET_NOT_FOUND sem persistir (rollback; D-002)
        - player diferente / moeda diferente → REJECTED com o código respectivo
 4. regra de negócio (seção 2.3) → decide entre crédito, débito, nada ou rejeição
 5a. sucesso: entry := w.Debit/Credit
              INSERT ledger; UPDATE wallets ... WHERE id=$1 AND version=$expected
              t.MarkProcessed(balanceAfter, refID)
              outbox: WagerTransactionProcessed (+ WalletBalanceChanged se o saldo mudou)
 5b. rejeição: t.MarkRejected(code); outbox: WagerTransactionRejected
 6. UPDATE wager_transactions SET status=..., ... WHERE id=$1
 7. (SQS) inbox.Complete na mesma tx
 8. depois de processar uma BET/WIN/REFUND: UPDATE wager_transactions SET next_attempt_at=now()
    WHERE status='PENDING_REFERENCE' AND provider_id=$1 AND reference_external_transaction_id=$2
    → acorda as reversões que esperavam por esta transação
COMMIT
```

### Por que funciona sob concorrência (o texto que vai para o ARCHITECTURE.md)

**Cenário 1 — 50 envios idênticos em paralelo (mesma chave):**
- As 50 transações executam o `INSERT … ON CONFLICT DO NOTHING`. A primeira insere. As outras 49 **bloqueiam no índice único** até a primeira terminar; esse é o comportamento do Postgres para conflito com uma tupla ainda não confirmada.
- Quando a primeira faz commit, as 49 recebem "conflito" (nenhuma linha inserida). O `SELECT` seguinte, em `READ COMMITTED`, usa um snapshot novo e enxerga a linha confirmada, que tem o mesmo hash. Resultado: replay.
- Se a primeira fizer rollback (crash), uma das outras passa a inserir e segue normalmente.
- Resultado: **um** ledger, **um** débito e 49 `idempotentReplay: true`.

**Cenário 2 — duas BETs distintas de 80.00 sobre um saldo de 100.00:**
- As duas inserem a própria transação (chaves diferentes) e disputam o `SELECT … FOR UPDATE` da carteira. Uma obtém o lock e a outra espera.
- A primeira debita (100 → 20), incrementa a versão e faz commit.
- A segunda obtém o lock e, em `READ COMMITTED`, o `FOR UPDATE` devolve a **versão mais recente** da linha (saldo 20). Como 80 > 20, `REJECTED INSUFFICIENT_FUNDS`.
- Resultado: um PROCESSED, um REJECTED, saldo 20.00 e um único débito no ledger.

**Camadas de defesa** (cada uma sozinha já impediria o erro financeiro):
1. **Lock pessimista por linha de carteira.** Serializa só a mesma carteira; carteiras distintas não compartilham lock, então avançam em paralelo. Não existe mutex em memória nem lock global.
2. **`UPDATE … WHERE version = $expected`.** Se, por bug, alguém atualizasse sem o lock, a atualização com versão antiga afetaria 0 linhas e geraria um retry, o que impede lost update.
3. **`CHECK (balance >= 0)`.** Mesmo com bug na aplicação, o banco recusa saldo negativo.
4. **`UNIQUE` de idempotência, `ledger_wallet_tx_uk` e `wt_single_reversal_uk`.** Mesmo com bug, o banco recusa efeito duplicado.

**Por que lock pessimista e não otimista puro:** carteiras de apostas têm alta contenção (muitas operações na mesma carteira em sequência rápida). Com otimismo puro, muitos retries seriam abortados. O pessimista serializa sem desperdício e mantém a versão como verificação.

**Deadlocks:** cada operação trava **uma única** linha de carteira, e a reversão trava a mesma carteira da referência (exigido pela regra de coerência). Sem duas carteiras travadas na mesma transação, não há ciclo de espera. O `deadlock_detected` ainda é tratado como transitório, com retry.

**Lock timeout:** `SET LOCAL lock_timeout = '5s'`. Se estourar, o erro é transitório: HTTP 503 com `Retry-After`, e no SQS a mensagem volta para a fila.

**Operações síncronas:** uma operação sem dependência pendente é concluída na mesma transação em que é registrada; não há commit intermediário de `PENDING`. Se o processo morrer antes do commit, nada foi persistido e o cliente reenvia com segurança. O `PENDING` só seria confirmado num aceite assíncrono, e o worker da etapa 2.5 também varre `PENDING` antigos como rede de segurança.

### Testes
- Integração (com goroutines e pools distintos): 50 réplicas, 80 + 80 sobre 100, e 20 carteiras com 10 operações cada em paralelo, conferindo os saldos e o tempo total (paralelismo efetivo).
- Unitário com repositórios em memória **apenas** para cobrir os ramos do fluxo. A garantia de concorrência é provada só com Postgres real.

### Registrar
Todo o texto de concorrência acima, mais os problemas encontrados ao testar.

---

## 2.3 Regras por tipo e reversões

### Regras
| Tipo | Efeito | Validações |
| --- | --- | --- |
| BET | débito | `amount > 0`; saldo suficiente, senão `INSUFFICIENT_FUNDS` |
| WIN | crédito | `amount > 0`; com referência informada, ela deve ser uma BET da mesma rodada (`REFERENCE_MISMATCH`). A referência é opcional e, se ausente, a WIN não fica pendente |
| LOSS | nenhum | `amount == 0.00`; moeda = moeda da carteira; sem ledger, sem mudança de versão; evento só `WagerTransactionProcessed` |
| REFUND | crédito = valor da BET | referência obrigatória, do tipo BET (`REFERENCE_KIND_NOT_REFUNDABLE`) e `PROCESSED` |
| ROLLBACK | movimento contrário | referência BET (gera crédito), WIN (gera débito) ou REFUND (gera débito); débito sem saldo dá `REVERSAL_INSUFFICIENT_FUNDS` |

### Coerência com a referência
Provedor (implícito na busca), jogador, carteira, moeda e rodada iguais (`REFERENCE_MISMATCH`), e valor igual (`AMOUNT_MISMATCH`).

### Política REFUND × ROLLBACK (D-003)
- Uma transação pode ter **no máximo uma reversão bem-sucedida**, seja REFUND ou ROLLBACK. Uma BET já reembolsada não pode sofrer ROLLBACK, e vice-versa: `ALREADY_REVERSED`.
- Um ROLLBACK de um REFUND é permitido, porque a referência é o REFUND e não a BET: debita de volta o valor devolvido. Depois disso, a BET original continua com a reversão "consumida" pelo REFUND, então **não pode** ser reembolsada de novo. Isso impede a devolução duplicada do mesmo débito.
- ROLLBACK de ROLLBACK e de LOSS: `REFERENCE_KIND_NOT_REVERSIBLE`.
- A checagem `ALREADY_REVERSED` acontece **depois** do lock da carteira, e todas as reversões de uma referência estão na mesma carteira, então ficam serializadas. O índice `wt_single_reversal_uk` é a barreira final.

### Referência existente, mas não concluída
| Estado da referência | Comportamento |
| --- | --- |
| `PENDING` / `PENDING_REFERENCE` | a reversão fica `PENDING_REFERENCE` e aguarda |
| `REJECTED` / `FAILED` | `REJECTED REFERENCE_NOT_PROCESSED` (definitivo) |
| `PROCESSED` | segue o fluxo |

### Testes (unitários + integração)
Tabela com cada tipo × cada condição, cobrindo todos os failure codes do Apêndice A.

---

## 2.4 Endpoints de transação

### `POST /wagering/transactions`
1. Autenticação + role `provider`.
2. Header `Idempotency-Key` obrigatório (400).
3. Decode estrito (`DisallowUnknownFields`, limite de tamanho de body), validação e `kind != OPENING`.
4. `body.providerId == principal.ProviderID` (403, sem escrita).
5. `ProcessWager` e mapeamento de status segundo o Apêndice B.

Resposta padrão:
```json
{ "transactionId": "...", "status": "PROCESSED|REJECTED|PENDING_REFERENCE",
  "balance": {"amount":"975.00","currency":"BRL"},   // ausente se não processada
  "failureCode": "INSUFFICIENT_FUNDS",                // só quando REJECTED/FAILED
  "idempotentReplay": false }
```

### `GET /wagering/transactions/:transactionId` e `GET /providers/:providerId/wagering/transactions/:externalTransactionId`
Visão completa da transação: status, failure code, referências resolvidas, tentativas e próxima tentativa, para acompanhar pendências.

### Formato de erro
```json
{ "error": { "code": "IDEMPOTENCY_KEY_CONFLICT", "message": "...", "correlationId": "..." } }
```

---

## 2.5 Worker de referências pendentes

### Objetivo
Retomar de forma durável qualquer `PENDING_REFERENCE` (e qualquer `PENDING` órfão) por qualquer instância, com backoff exponencial e expiração.

### Algoritmo (`RunOnce`)
```sql
BEGIN;
SELECT id FROM wager_transactions
 WHERE status IN ('PENDING_REFERENCE','PENDING')
   AND next_attempt_at <= now()
 ORDER BY next_attempt_at
 LIMIT $batch
 FOR UPDATE SKIP LOCKED;          -- instâncias pegam lotes distintos sem esperar umas pelas outras
```
Para cada item, chamar `ProcessWager.Resume(ctx, tx, id)`, que reutiliza os passos 2–8 do fluxo:
- a referência apareceu e está PROCESSED: aplica;
- ainda ausente e `attempts + 1 < max` (e `now < expires_at`): `attempts++`, `next_attempt_at = now + min(base * 2^attempts, cap) ± jitter`;
- esgotado: `REJECTED REFERENCE_NOT_FOUND` + evento `WagerTransactionRejected`.

Política padrão: `base = 1s`, `cap = 5min`, `max = 10` tentativas **ou** TTL de 30 min (o que vier primeiro). Tudo configurável; nos testes, valores curtos.

Um crash no meio deixa a transação SQL sem commit. O lock é liberado e outra instância pega o item no próximo ciclo.

### Testes (integração)
- REFUND antes da BET; depois a BET chega e o REFUND é aplicado (via "acordar" do passo 8 ou pelo backoff).
- REFUND sem BET até expirar: `REJECTED REFERENCE_NOT_FOUND` + evento.
- Dois workers concorrentes nunca processam o mesmo item (contagem de ledger = 1).

---

## 2.6 Consumidor SQS com inbox

### Objetivo
Consumir `wager-transactions.fifo` com as mesmas garantias do HTTP, removendo a mensagem só depois do commit.

### Algoritmo
1. `ReceiveMessage(MaxNumberOfMessages=10, WaitTimeSeconds=20, VisibilityTimeout=30, AttributeNames=[ApproximateReceiveCount, MessageGroupId])`.
2. Para cada mensagem (pool de N goroutines, com contexto de prazo `handlerTimeout = 20s` < visibility):
   1. Parse do envelope. Se for inválido (JSON quebrado, tipo desconhecido, campos ausentes ou malformados), é **erro permanente**: envio direto para a DLQ com o atributo `failureReason`, depois `DeleteMessage`, depois a métrica `sqs_dlq_total{reason="invalid"}`.
   2. Hash da mensagem (o mesmo algoritmo canônico da etapa 2.1).
   3. Na transação: `INSERT inbox (consumer, messageId, hash) ON CONFLICT DO NOTHING`.
      - já existe e `completed_at` está preenchido: duplicata (métrica). Se o hash for igual, apenas `DeleteMessage`; se for diferente, erro permanente e DLQ;
      - novo: `ProcessWager` na **mesma tx**, depois `inbox.completed_at = now()` + `transaction_id`, depois commit.
   4. Depois do commit, `DeleteMessage`.
      - Rejeição de negócio confirmada: também apaga, porque é terminal.
      - `PENDING_REFERENCE` persistido: apaga, porque o worker assume a continuidade.
   5. Erro transitório (DB fora, lock timeout): **não apaga**. `ChangeMessageVisibility(min(2^receiveCount * 2s, 300s))` faz o backoff; ao passar de `maxReceiveCount = 5`, o redrive manda para a DLQ (métrica `sqs_retries_total`).
3. **Shutdown:** cancelar o loop de `Receive` (para de buscar), aguardar os handlers até o deadline e, para as mensagens que não começaram ou não terminaram, `ChangeMessageVisibility(0)` para reentrega imediata e segura. Se a transação não fez commit, nada persistiu; se fez, a inbox deduplica.

### `MessageGroupId` e `MessageDeduplicationId` (contrato para os produtores)
- `MessageGroupId = walletId`: ordem FIFO por carteira e paralelismo entre carteiras. Efeito colateral: uma mensagem em backoff segura o seu grupo. Isso é aceitável, porque preserva a ordem da carteira, e fica documentado.
- `MessageDeduplicationId = messageId`: dedup de 5 minutos do SQS. É **apenas otimização**; a garantia vem da inbox e dos UNIQUEs.

### Concorrência HTTP × SQS
A mesma operação chegando pelos dois canais converge pelo UNIQUE `(provider_id, idempotency_key)`. Um deles processa e o outro vira replay. Isso é testado na etapa 3.1.

### Testes (integração com SQS real no emulador)
- Mensagem processada e removida, com a inbox concluída.
- A mesma mensagem enviada 2 vezes com `MessageDeduplicationId` diferentes (para furar o dedup do SQS) gera **um** efeito, e a inbox registra a duplicata.
- Mesmo `messageId` com payload diferente vai para a DLQ.
- Mensagem inválida vai para a DLQ.
- Banco indisponível (pausar o container): a mensagem volta e é processada quando o banco retorna; depois de N falhas, vai para a DLQ.

### Registrar
Limites de tentativas, visibility, timeout do handler, tratamento de inválidas, grupos e deduplicação.

---

## 2.7 Outbox publisher

### Objetivo
Publicar eventos somente depois do commit, com vários publishers concorrentes, retry com backoff e recuperação de trabalho abandonado.

### Algoritmo
```sql
-- claim (transação curta)
UPDATE outbox_events SET locked_by = $instance, locked_until = now() + $lease
 WHERE event_id IN (
   SELECT event_id FROM outbox_events
    WHERE published_at IS NULL
      AND next_attempt_at <= now()
      AND (locked_until IS NULL OR locked_until < now())   -- lease expirado = trabalho abandonado
    ORDER BY occurred_at
    LIMIT $batch
    FOR UPDATE SKIP LOCKED)
RETURNING event_id, aggregate_id, payload, attempts;
```
Para cada evento:
- `SendMessage(QueueUrl=wallet-events.fifo, Body=payload, MessageGroupId=aggregate_id, MessageDeduplicationId=event_id, MessageAttributes{eventType, eventVersion})`;
- sucesso: `UPDATE ... SET published_at=now(), locked_by=NULL WHERE event_id=$1 AND locked_by=$instance`;
- falha: `attempts++`, `next_attempt_at = now + backoff(attempts)`, `last_error`, liberação do lock.

Se o processo morrer entre a publicação e a marcação, o lease expira e outra instância republica **o mesmo payload com o mesmo `eventId`**. O consumidor deduplica por `eventId`, e o dedup FIFO de 5 min também ajuda. A entrega é **at-least-once**, documentada.

### Contrato de saída (documentar)
- Fila `wallet-events.fifo`; corpo = envelope JSON; atributos `eventType`/`eventVersion` para roteamento.
- Ordem garantida por carteira dentro do grupo FIFO. Os consumidores devem ser idempotentes por `eventId` e podem usar `walletVersion` para ordenar.

### Métricas
`outbox_pending_events` (gauge), `outbox_oldest_pending_age_seconds` (lag), `outbox_publish_total{result}` e `outbox_publish_attempts`.

### Testes
Ficam nas etapas 3.1 e 3.4.

---

## 2.8 Reconciliação

### `POST /wallets/:walletId/reconciliation`
- Transação `REPEATABLE READ, READ ONLY`, para que o saldo e o ledger sejam lidos do **mesmo snapshot**.
- `stored := SELECT balance, currency FROM wallets WHERE id=$1`.
- `calc := SELECT COALESCE(SUM(CASE direction WHEN 'CREDIT' THEN amount ELSE -amount END),0), COUNT(*) FROM wallet_ledger_entries WHERE wallet_id=$1`.
- A soma é feita em `NUMERIC` no SQL e convertida para `int64` com verificação, sem overflow silencioso.
- `difference = stored - calculated`; `consistent = difference == 0`.
- Divergência gera log `WARN reconciliation mismatch` e a métrica `reconciliation_mismatches_total`.
- **Nunca** altera o saldo.

### Testes
- Consistência depois de abertura + BET (`checkedEntries = 2`, igual ao exemplo do enunciado).
- Divergência forçada via superusuário no teste: `consistent = false` e métrica incrementada.

---

## 2.9 Observabilidade e health checks

### Logs
- Middleware HTTP que gera ou propaga o `X-Correlation-Id` e injeta um logger no `ctx` com `correlationId`.
- Campos ao longo do fluxo: `transactionId`, `walletId`, `providerId`, `messageId`, `kind`, `status`, `failureCode`, `durationMs`.
- **Nunca** logar tokens, secrets, header `Authorization` ou payload completo.

### Métricas (Prometheus, `/metrics`)
| Métrica | Tipo | Labels |
| --- | --- | --- |
| `wager_transactions_total` | counter | `kind`, `status`, `channel` (http/sqs/worker) |
| `wager_idempotent_replays_total` | counter | `channel` |
| `wager_processing_duration_seconds` | histogram | `kind`, `channel` |
| `wallet_concurrency_conflicts_total` | counter | `reason` (version, deadlock, lock_timeout) |
| `sqs_messages_total` | counter | `result` (processed, duplicate, rejected, retry, dlq) |
| `sqs_dlq_total` | counter | `reason` |
| `pending_reference_retries_total` | counter | — |
| `outbox_pending_events` / `outbox_oldest_pending_age_seconds` | gauge | — |
| `outbox_publish_total` | counter | `result` |
| `reconciliation_mismatches_total` | counter | — |

### Health (implementado antecipadamente na etapa 1.2, D-008)
- `/health/live`: 200 se o processo responde.
- `/health/ready`: `pool.Ping` + `GetQueueAttributes` na fila de entrada, com timeout de 2s cada; 503 com o detalhe de qual dependência falhou. Durante o shutdown, passa a devolver 503 imediatamente, para tirar a instância do balanceamento.

---

# Dia 3 — Prova das garantias, falhas e documentação

**Meta do dia:** todos os testes obrigatórios da seção 13 estão automatizados e passando; a solução roda com 3 instâncias; a documentação permite reproduzir tudo a partir de um checkout limpo.

**Distribuição sugerida:** 3.1–3.2 (≈3h) · 3.3–3.4 (≈3h) · 3.5–3.6 (≈2h).

---

## 3.1 Testes de integração

**Infra:** `test/integration` com `//go:build integration`. Um `TestMain` sobe Postgres, Keycloak e o emulador AWS via `testcontainers-go` (uma vez por pacote), aplica as migrations e importa o realm. Entre testes, `TRUNCATE` (com superusuário, desabilitando o trigger de TRUNCATE só no setup de teste) ou um schema novo por teste.

| Teste | Verifica |
| --- | --- |
| `TestMigrations_UpDownUp` | reversibilidade |
| `TestLedger_IsAppendOnly` | UPDATE, DELETE e TRUNCATE recusados |
| `TestConstraints_*` | UNIQUEs, CHECKs, OPENING único, reversão única |
| `TestOpenWallet_Atomic` | carteira + transação + ledger + 2 eventos, ou nada |
| `TestProcessWager_Atomicity` | erro injetado depois do ledger: rollback total |
| `TestConcurrency_SameBet50x` | 50 goroutines, um débito, 49 replays |
| `TestConcurrency_TwoBets80On100` | 1 PROCESSED, 1 REJECTED, saldo 20, um débito |
| `TestConcurrency_IndependentWallets` | N carteiras em paralelo, saldos corretos |
| `TestIdempotency_PayloadConflict` / `_OtherKeySameExternalId` | 409s |
| `TestIdempotency_ReplayReturnsOriginalBalance` | replay devolve o saldo original depois de outras movimentações |
| `TestReversals_*` | todas as combinações REFUND/ROLLBACK, `ALREADY_REVERSED`, `REVERSAL_INSUFFICIENT_FUNDS` |
| `TestPendingReference_ResolvesLater` / `_Expires` | worker resolve ou rejeita |
| `TestSQS_ProcessAndDelete` / `_Redelivery` / `_InvalidToDLQ` / `_TransientRetryThenDLQ` | consumidor |
| `TestCrossChannel_HTTPAndSQSSameOperation` | um efeito, um replay |
| `TestOutbox_TwoPublishersCompete` | cada evento publicado uma vez (sem crash) e todos publicados |
| `TestOutbox_RetryWithBackoff` | publisher com SQS indisponível volta a publicar |
| `TestFx_StartStop_NoLeaks` | `fxtest` + `goleak` |
| `TestReconciliation_*` | consistente e divergente |

Ao final de cada teste financeiro, um helper `assertLedgerMatchesBalance(t, walletID)` confere o saldo contra créditos menos débitos.

---

## 3.2 Testes de autenticação e autorização

Contra o **Keycloak real**, com tokens obtidos via `client_credentials`:

| Teste | Esperado |
| --- | --- |
| sem `Authorization` | 401 |
| token com assinatura adulterada / emitido por outro realm | 401 |
| token expirado (client `provider-a-short`, aguardando a expiração) | 401 |
| `aud` errado | 401 |
| provider em `POST /wallets` / `GET /wallets/:id` / reconciliação | 403, nenhuma linha criada |
| `provider-a` enviando body com `providerId=provider-b` | 403, nenhuma linha em `wager_transactions` |
| `provider-b` consultando uma transação do `provider-a` (por ID interno e por rota externa) | 404/403, sem vazamento de dados |
| `provider-b` reenviando chave e payload do `provider-a` | não retorna o resultado do `provider-a` |
| admin consultando qualquer transação | 200 |

Cada teste de acesso negado confere também que **não houve efeito financeiro**: a contagem de ledger, o saldo e a outbox ficam iguais.

---

## 3.3 Multi-instância e concorrência

### Ambiente
- `docker-compose.e2e.yml` (override) com `app1`, `app2` e `app3` (âncora YAML, portas 8081–8083), cada uma com todos os componentes ligados, inclusive consumer e workers. Isso cumpre o requisito de três processos independentes, cada um com suas próprias conexões e memória.
- `make e2e` sobe o ambiente e roda `go test -tags e2e ./test/e2e/...`.

### Testes (`test/e2e`)
- 50 envios da mesma BET distribuídos em round-robin pelas 3 instâncias: um débito.
- 80 + 80 sobre 100 em instâncias diferentes: 1 PROCESSED, 1 REJECTED, saldo 20.
- 30 carteiras × 20 operações espalhadas pelas instâncias: todos os saldos batem com o ledger.
- Mistura HTTP (3 instâncias) + SQS (3 consumidores) para as mesmas operações.
- Reenvio dos cenários acima: resultado inalterado.

### Teste de carga (opcional, se sobrar tempo)
`k6` ou `vegeta` com um script versionado, relatando throughput, p50/p95/p99, erros, conflitos e atraso da outbox.

---

## 3.4 Simulação de falhas e recuperação

### Mecanismo de injeção de falhas
`platform/fault`: a variável `FAULT_POINT` (desligada por padrão; **só tem efeito se `FAULT_INJECTION_ENABLED=true`**) faz o processo chamar `os.Exit(137)` em pontos nomeados:
- `consumer.after_commit_before_delete`
- `outbox.after_publish_before_mark`
- `outbox.after_claim_before_publish`
- `worker.after_claim`
- `wager.before_commit`

### Cenários
| # | Cenário | Validação |
| --- | --- | --- |
| 1 | Consumidor morre depois do commit e antes do `DeleteMessage` | a mensagem reaparece após o visibility timeout; outra instância a recebe; a inbox reconhece a duplicata; um único ledger; `ApproximateReceiveCount = 2` |
| 2 | Publisher morre depois de publicar e antes de marcar | outra instância republica depois do lease; a fila de eventos tem 2 mensagens com o **mesmo `eventId`** (ou 1, se o dedup FIFO absorveu); a outbox fica `published` |
| 3 | Publisher morre entre o commit da operação e a publicação | o evento fica pendente; outra instância publica |
| 4 | Processo morre antes do commit da operação | nada persistido; o reenvio processa normalmente |
| 5 | Restart total (`docker compose restart`) com pendências | os `PENDING_REFERENCE` são retomados; replays continuam devolvendo os resultados originais; a reconciliação fica consistente |
| 6 | Postgres pausado (`docker pause`) durante a carga | HTTP devolve 503 e readiness 503; o SQS reentrega; depois do `unpause`, tudo converge sem duplicidade |
| 7 | SQS indisponível | o outbox acumula e o lag cresce; depois de voltar, drena |
| 8 | Dois publishers disputando a outbox com kill de um deles | todos os eventos publicados e nenhum perdido |

O controle de containers nos testes usa a API Docker via `testcontainers-go` ou `exec docker compose kill/start`.

---

## 3.5 Documentação

### `README.md` da solução (substitui o enunciado; o enunciado vai para `docs/CHALLENGE.md`)
1. Visão geral e diagrama.
2. Pré-requisitos (Docker, Go 1.27, make, aws cli opcional).
3. `cp .env.example .env` + tabela de variáveis.
4. `docker compose up --build`: o que sobe e as portas.
5. Filas: como são criadas e como inspecioná-las.
6. Migrations: aplicação e reversão.
7. Como obter tokens (`make token-provider-a`, `make token-admin`) e as identidades de teste.
8. Exemplos curl de cada endpoint, incluindo replay, conflito, rejeição, pendência e reconciliação.
9. Exemplo de envio via SQS.
10. Testes: `go test ./...`, `go test -race ./...`, `go vet ./...`, `make test-integration`, `make test-e2e` e os cenários de falha.

### `ARCHITECTURE.md`
Seções obrigatórias: Dinheiro · Transações SQL e Unit of Work · Idempotência e hash · **Concorrência e locks** · Máquina de estados e falhas transitórias × permanentes · Referências pendentes · Reversões (política REFUND/ROLLBACK) · Inbox · Outbox e contrato de eventos · SQS (grupos, dedup, visibility, DLQ) · Autenticação e autorização · Fx e ciclo de vida · Shutdown · Observabilidade · Failure codes e contrato HTTP · **Limitações, interpretações e trabalho não concluído**.

### Relatório de entrega
Gerado a partir de `docs/DECISIONS.md`: tudo que foi feito, decisões, tecnologias, problemas encontrados e como foram resolvidos, com destaque para a concorrência.

---

## 3.6 Checklist final de entrega

- [ ] `git clone` limpo + `docker compose up --build` funcionando sem passos manuais
- [ ] `gofmt -l .` vazio
- [ ] `go vet ./...` sem avisos
- [ ] `go test ./...` e `go test -race ./...` passando
- [ ] `make test-integration` e `make test-e2e` passando
- [ ] `go.mod` e `go.sum` versionados; versão do Go igual em `go.mod` e no Dockerfile
- [ ] `.env.example` sem segredos reais
- [ ] Nenhum `float32`/`float64` no código de dinheiro (`grep -rn "float" internal/`)
- [ ] Todas as rotas de negócio exigem auth (teste que percorre o router)
- [ ] README, ARCHITECTURE e relatório revisados
- [ ] Revisão dos critérios eliminatórios da seção 14, item a item

---

## Apêndice A — Failure codes

| Código | Tipo | Onde aparece | Corrigível? |
| --- | --- | --- | --- |
| `INVALID_REQUEST` | validação | 400, não persistido | sim |
| `INVALID_MONEY` | validação | 400 | sim |
| `UNSUPPORTED_KIND` (OPENING externo) | validação | 400 | sim |
| `MISSING_IDEMPOTENCY_KEY` | validação | 400 | sim |
| `IDEMPOTENCY_KEY_CONFLICT` | conflito | 409 | sim (usar nova chave) |
| `EXTERNAL_TRANSACTION_CONFLICT` | conflito | 409 | não |
| `WALLET_ALREADY_EXISTS` | conflito | 409 | não |
| `WALLET_NOT_FOUND` | validação | 422 com `error.code`, não persistido; log + métrica; no SQS vai para a DLQ (D-002) | sim |
| `PLAYER_WALLET_MISMATCH` | negócio | `REJECTED` | definitivo |
| `CURRENCY_MISMATCH` | negócio | `REJECTED` | definitivo |
| `INSUFFICIENT_FUNDS` | negócio | `REJECTED` (BET) | definitivo |
| `REVERSAL_INSUFFICIENT_FUNDS` | negócio | `REJECTED` (ROLLBACK de WIN/REFUND) | definitivo |
| `REFERENCE_NOT_FOUND` | negócio | `REJECTED` após expiração | definitivo |
| `REFERENCE_NOT_PROCESSED` | negócio | `REJECTED` | definitivo |
| `REFERENCE_MISMATCH` | negócio | `REJECTED` | definitivo |
| `REFERENCE_KIND_NOT_REVERSIBLE` | negócio | `REJECTED` | definitivo |
| `AMOUNT_MISMATCH` | negócio | `REJECTED` | definitivo |
| `ALREADY_REVERSED` | negócio | `REJECTED` | definitivo |
| `INFRASTRUCTURE_PERMANENT_FAILURE` | permanente | `FAILED` | definitivo |


## Apêndice B — Contrato de status HTTP

| Situação | HTTP | Corpo |
| --- | --- | --- |
| Processada (nova) | 201 | `status: PROCESSED`, `balance`, `idempotentReplay: false` |
| Processada (replay) | 200 | `status: PROCESSED`, `balance` original, `idempotentReplay: true` |
| Rejeição de negócio (nova ou replay) | 422 | `status: REJECTED`, `failureCode`, `idempotentReplay` |
| Aguardando referência | 202 | `status: PENDING_REFERENCE`, `Location` da transação |
| Entrada inválida | 400 | `error.code` |
| Carteira inexistente (D-002) | 422 | `error.code = WALLET_NOT_FOUND` (sem `status`, o que o distingue da rejeição) |
| Sem autenticação / token inválido | 401 | `error.code = UNAUTHENTICATED` |
| Sem permissão | 403 | `error.code = FORBIDDEN` |
| Não encontrado | 404 | `error.code = NOT_FOUND` |
| Conflito de idempotência / carteira existente | 409 | `error.code` |
| Indisponibilidade transitória | 503 | `error.code = TEMPORARILY_UNAVAILABLE` + `Retry-After` |

No SQS: processado, rejeitado ou pendente persistido → `DeleteMessage`; transitório → backoff de visibilidade; inválido ou permanente → DLQ.

## Apêndice C — Máquina de estados

```
             ┌──────────────► PROCESSED (terminal)
             │
 PENDING ────┼──────────────► REJECTED  (terminal)
   │         │
   │         └──────────────► FAILED    (terminal)
   ▼
 PENDING_REFERENCE ──(ref ok)──► PROCESSED
        │  ▲   └──(ref inválida/expirou)──► REJECTED
        └──┘ retry (attempts++, next_attempt_at)
        └──(falha permanente)──► FAILED
```
- **Transitório:** conexão perdida, timeout, `lock_timeout`, `40001`, `40P01`, SQS throttling. Retry; a transação não muda de estado.
- **Permanente:** erro que se repetiria de forma determinística (dado corrompido na reidratação, violação inesperada de constraint). Vira `FAILED` com código, para auditoria.
- O banco reforça a imutabilidade terminal com o trigger `wt_terminal_guard`.

## Apêndice D — Variáveis de ambiente

| Variável | Exemplo | Descrição |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | endereço do servidor |
| `DATABASE_URL` | `postgres://wallet_app:...@postgres:5432/wallet` | conexão da aplicação |
| `DATABASE_MIGRATE_URL` | `postgres://wallet_owner:...@postgres:5432/wallet` | usada só pelas migrations |
| `DB_MAX_CONNS` | `20` | tamanho do pool |
| `DB_LOCK_TIMEOUT` | `5s` | `lock_timeout` |
| `AWS_ENDPOINT_URL` | `http://aws:4566` | emulador |
| `AWS_REGION` / `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | `us-east-1` / `test` / `test` | credenciais locais |
| `SQS_INPUT_QUEUE` / `SQS_EVENTS_QUEUE` | `wager-transactions.fifo` / `wallet-events.fifo` | filas |
| `SQS_CONSUMER_NAME` | `wallet-service` | nome na inbox |
| `SQS_WORKERS` / `SQS_VISIBILITY_TIMEOUT` / `SQS_HANDLER_TIMEOUT` | `4` / `30s` / `20s` | consumidor |
| `OIDC_ISSUER` / `OIDC_DISCOVERY_URL` / `OIDC_AUDIENCE` | ... / ... / `wagering-api` | auth |
| `KNOWN_PROVIDERS` | `provider-a,provider-b` | allowlist do consumidor |
| `OUTBOX_BATCH` / `OUTBOX_LEASE` / `OUTBOX_POLL_INTERVAL` | `50` / `30s` / `500ms` | publisher |
| `PENDING_BASE_BACKOFF` / `PENDING_MAX_BACKOFF` / `PENDING_MAX_ATTEMPTS` / `PENDING_TTL` | `1s` / `5m` / `10` / `30m` | worker de referências |
| `ENABLE_HTTP` / `ENABLE_CONSUMER` / `ENABLE_OUTBOX_WORKER` / `ENABLE_REFERENCE_WORKER` | `true` | componentes |
| `INSTANCE_ID` | hostname | `locked_by` e logs |
| `LOG_LEVEL` | `info` | |
| `FAULT_INJECTION_ENABLED` / `FAULT_POINT` | `false` / vazio | testes de falha |

## Apêndice E — Riscos e plano de contingência

| Risco | Impacto | Mitigação |
| --- | --- | --- |
| LocalStack recente exige auth token; IAM enforcement é pago | infra/auth do broker | LocalStack com versão fixada (D-001), com MiniStack como plano B; política IAM escrita e documentada mesmo sem enforcement |
| Issuer do Keycloak diferente entre host e container | tokens recusados | `KC_HOSTNAME` fixo, ou issuer e discovery separados |
| Dia 2 é o mais denso | atraso | ordem de prioridade: 2.1–2.4 → 2.6 → 2.7 → 2.5 → 2.8–2.9; métricas e reconciliação podem ir para a manhã do Dia 3. **O núcleo de concorrência nunca é cortado** |
| Testes de integração lentos (Keycloak demora para subir) | ciclo de feedback | containers compartilhados por pacote; `-run` focado durante o desenvolvimento |
| Flakiness em testes de falha (tempo de visibility) | CI instável | visibility curta nos testes (5s) e polling com timeout, em vez de sleep fixo |
| Dedup FIFO do SQS mascarando o teste de duplicidade | falso positivo | enviar duplicatas com `MessageDeduplicationId` diferentes, para que a deduplicação comprovada seja a da aplicação |

### Ordem de corte, se faltar tempo (do último para o primeiro)
1. Teste de carga (opcional).
2. Tracing (opcional, não planejado).
3. Cenários de falha 6–7 automatizados; nesse caso, ficam como roteiro manual documentado.
4. Paginação com filtros extras.

**Nunca cortar:** autenticação, idempotência persistente, lock por carteira, ledger imutável, outbox, inbox, os testes 80 + 80 e 50×, e 3 instâncias.
