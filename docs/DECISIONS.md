# Registro de Decisões

Log incremental de decisões, problemas encontrados e soluções de contorno. É a fonte do `ARCHITECTURE.md` e do relatório de entrega.

Formato: **contexto** (com a referência ao README) → **opções e trade-offs** → **decisão** → **consequências**.

---

## D-001 — Emulador de SQS: LocalStack com versão fixada

- **Data:** 2026-10-05
- **Contexto:** o README (§4) exige AWS SQS executado localmente com LocalStack ou MiniStack.
- **Opções:**
  - LocalStack: mais conhecido e documentado, com módulo pronto no `testcontainers-go`. As versões recentes exigem token de conta, então precisamos fixar uma versão.
  - MiniStack: gratuito e sem token, porém menos maduro e menos documentado.
- **Decisão:** LocalStack com a imagem fixada em **`4.14.0`**. Testado na etapa 1.1: a `2026.09.0` (latest) encerra com "pro features can only be used with a valid license", e a `4.14.0` (última da linha 4.x, de 2026-02-26) sobe o SQS sem token. O MiniStack fica como plano B.
- **Consequências:** a aplicação de políticas IAM não deve estar disponível no emulador. As políticas de acesso ao broker (README §2) serão escritas e documentadas, e a limitação aparece em `ARCHITECTURE.md`.

## D-002 — Operação para carteira inexistente: 422 sem persistir, com log e métrica

- **Data:** 2026-10-05
- **Contexto:** o README (§7) pede que toda rejeição tenha um `failureCode` estável, distinguindo entradas corrigíveis de resultados definitivos. `wager_transactions.wallet_id` é uma FK obrigatória.
- **Opções:**
  - (a) Não persistir: schema simples, erro corrigível, mas sem auditoria durável.
  - (b) Persistir como `REJECTED`: auditável, mas exige FK opcional e "queima" a chave de idempotência.
- **Decisão:** responder com um erro de validação claro, sem gravar a transação:
  - HTTP: **422** com `{"error":{"code":"WALLET_NOT_FOUND", ...}}`. Esse corpo se distingue da rejeição de negócio, que traz `status: REJECTED` e `failureCode`;
  - registro seguro: log JSON (`correlationId`, `providerId`, `walletId`, sem payload financeiro nem credenciais, conforme README §12) + a métrica `wager_wallet_not_found_total{channel}`;
  - SQS: a mensagem vai para a DLQ, que é o registro durável e permite reprocessar depois que a carteira existir.
- **Consequências:** não há registro em banco da tentativa. O cliente pode reenviar a mesma chave depois de criar a carteira, porque a chave não fica consumida.

## D-003 — Reversões: no máximo uma reversão bem-sucedida por transação referenciada

- **Data:** 2026-10-05
- **Contexto:** o README (§7) diz que o ROLLBACK desfaz BET, WIN ou REFUND; que uma referência não pode receber "duas reversões bem-sucedidas do mesmo tipo"; e que é preciso impedir a devolução duplicada do mesmo débito.
- **Opções:**
  - (A) Uma reversão por transação, REFUND ou ROLLBACK: um único índice no banco garante toda a invariante.
  - (B) Uma por tipo: segue o texto literal, mas exige uma regra extra na aplicação ("a referência não pode estar revertida no momento") para evitar a devolução dupla.
  - (C) Proibir ROLLBACK de REFUND: contradiz o §7.
- **Decisão:** (A). Depois de avaliar (B), o usuário preferiu (A) pela simplicidade e porque a invariante fica inteira no banco (README §5.3).
- **Regras:**
  - índice `UNIQUE (reference_transaction_id) WHERE status='PROCESSED' AND kind IN ('REFUND','ROLLBACK')`;
  - BET reembolsada não pode sofrer ROLLBACK, e vice-versa: `ALREADY_REVERSED`;
  - ROLLBACK de REFUND é permitido, porque a referência é o REFUND; ele debita de volta o valor devolvido. A BET original continua com sua reversão consumida e não pode ser reembolsada de novo;
  - ROLLBACK de ROLLBACK ou de LOSS: `REFERENCE_KIND_NOT_REVERSIBLE`.
- **Consequências:** é mais restritivo que o texto literal ("do mesmo tipo"), já que bloqueia também tipos diferentes. Essa interpretação será explicitada em `ARCHITECTURE.md`.

## D-004 — Versão do Go: 1.27.1

- **Data:** 2026-10-05
- **Contexto:** o README (§4) pede que a versão seja declarada em `go.mod` e no Dockerfile. O plano previa 1.25, mas o ambiente local tem 1.27.1.
- **Opções:**
  - 1.27: mais recente, com suporte e igual à versão local;
  - 1.26: com suporte;
  - 1.25: fora de suporte, porque o Go mantém apenas as duas últimas versões.
- **Decisão:** 1.27.1 (`go.mod`: `go 1.27.1`; Dockerfile: `golang:1.27.1-alpine`).

## D-005 — Versões das imagens e organização do compose (etapa 1.1)

- **Imagens fixadas**, para reprodutibilidade: `postgres:16.15-alpine`, `quay.io/keycloak/keycloak:26.8.0`, `localstack/localstack:4.14.0`, `migrate/migrate:v4.20.1`, `golang:1.27.1-alpine` (build) e `alpine:3.24` (runtime).
- **Postgres 16** foi mantido como estava no plano aprovado (tem suporte até 2028).
- **Migrations como serviço one-shot (`migrate`):** a aplicação só sobe depois de `service_completed_successfully`. Com 3 instâncias, nenhuma disputa a migração, e o role da aplicação não precisa de DDL.
- **Dois roles no banco:**
  - `wallet_owner`: dono do banco e do schema, usado só pelas migrations;
  - `wallet_app`: só os DML concedidos tabela a tabela. Verificado: `DELETE FROM wallets` e `CREATE TABLE` retornam `permission denied`.
- **Variáveis:** um único `.env.example`, lido por todos os serviços via `env_file`, com um `.env` opcional (no `.gitignore`) por cima. O `docker compose up` funciona a partir de um checkout limpo sem copiar nada.
- **Runtime em `alpine` e não em distroless:** o healthcheck do compose precisa de `wget`. Ter um shell na imagem foi aceito pela simplicidade.

## D-006 — Configuração com `caarlos0/env` (etapa 1.2)

- **Contexto:** o README (§4) pede "inicialização com validação de configuração e dependências".
- **Opções:** biblioteca padrão (sem dependência, ~80 linhas de conversão manual) × `caarlos0/env` (tags na struct, menos código, um pouco "mágico").
- **Decisão:** `caarlos0/env/v11`.
  - Obrigatoriedade e tipos ficam nas tags (`required,notEmpty`, `envDefault`).
  - Regras extras ficam em `Config.Validate()`: `DB_MAX_CONNS >= 1`, filas terminando em `.fifo` e `INSTANCE_ID` não vazio (o padrão é o hostname).
- **Consequências:** configuração inválida derruba o processo antes de qualquer conexão (`config.Load` é um construtor do Fx).

## D-007 — Runner genérico para os workers (etapa 1.2)

- **Contexto:** o README (§4) pede "cancelamento, prazos de execução e término observável dos workers".
- **Opções:** Runner genérico (lógica de shutdown escrita e testada uma vez) × cada worker com o próprio ciclo de vida (lógica repetida três vezes).
- **Decisão:** `worker.Runner`. Cada worker implementa `Loop { Name(); RunOnce(ctx) (didWork bool, err error) }`.
  - `Start`: cria um contexto cancelável e N goroutines (`sync.WaitGroup.Go`).
  - Entre iterações: espera `idleDelay` quando não houve trabalho ou houve erro.
  - `Stop(ctx)`: cancela o contexto, o que significa parar de buscar trabalho novo, e espera as goroutines até o prazo do `OnStop`. Se o prazo estourar, retorna erro, e o término fica observável via log e Fx.
- **Contrato importante:** o `ctx` recebido por `RunOnce` é cancelado no início do shutdown. Para *buscar* trabalho, o worker usa esse `ctx`. Para *concluir ou liberar* o trabalho em andamento, usa `context.WithoutCancel(ctx)` com o próprio timeout. Assim o Runner continua simples e cada worker decide explicitamente o que conclui e o que libera (README §10: "conclua o processamento em andamento dentro do prazo, ou libere sua visibilidade").

## D-008 — Composição Fx e ordem do ciclo de vida (etapa 1.2)

- **Módulos:** `config`, `logger`, `metrics`, `postgres`, `sqs`, `http`, um `fx.Module` por pacote, reunidos em `bootstrap.Options()` e reaproveitados pelo `main` e pelos testes.
- **Ordem:** o Fx executa os `OnStop` na ordem inversa dos `OnStart`. Como um componente só é construído depois de suas dependências, a ordem sai do próprio grafo:
  - start: config → logger → pool (ping) → SQS (`GetQueueUrl` das duas filas) → servidor HTTP;
  - stop: servidor HTTP → SQS (fecha conexões ociosas) → pool.
  - Verificado com SIGTERM real no compose (logs na ordem acima).
- **`/health/ready` antecipado da etapa 2.9 para a 1.2:** o Fx só constrói o que alguém usa. O readiness é o consumidor natural do pool e do cliente SQS, o que evita um `fx.Invoke` artificial.
  - O readiness faz `Ping` no banco e `GetQueueAttributes` na fila de entrada, com timeout de 2s cada.
  - Responde 503 com `{"checks":{"postgres":"unavailable",...}}`. O detalhe do erro vai só para o log, para não expor hosts num endpoint público.
- **Servidor HTTP:** `net.Listen` síncrono no `OnStart`, para falhar cedo se a porta estiver ocupada. Se o `Serve` falhar depois, chama `fx.Shutdowner` com exit code 1, em vez de manter um processo sem HTTP.
- **Logs:** `slog` JSON com `instanceId`. Os eventos internos do Fx vão em nível DEBUG, para não poluir.
- **Métricas:** `prometheus.Registry` próprio, não o global, em `/metrics`, com os coletores de Go e de processo.
- **Timeouts:** `fx.StartTimeout` e `fx.StopTimeout` de 30s.
- **Inicialização só com as dependências saudáveis:** a ordem fica a cargo do orquestrador, usando os healthchecks das próprias dependências. A aplicação não tem lógica de espera.
  - Opções avaliadas: (A) só no compose × (B) compose + espera na aplicação, consultando o health das dependências por até 60s.
  - Decisão: (A). Sem código novo, e a aplicação não fica acoplada a um endpoint que só existe no LocalStack.
  - Compose: `app` depende de `postgres: service_healthy` (`pg_isready`), `localstack: service_healthy` (marcador criado só depois que as filas existem), `keycloak: service_healthy` e `migrate: service_completed_successfully`.
  - Fora de um orquestrador, se uma dependência estiver indisponível, a aplicação falha imediatamente no `OnStart` e o Fx desfaz o que já tinha iniciado (verificado com o Postgres parado).

## D-009 — Infraestrutura dos testes de integração (etapa 1.2)

- Pacote `test/testinfra` (build tag `integration`) sobe `postgres:16.15-alpine` e `localstack/localstack:4.14.0` via `testcontainers-go`, **reaproveitando os mesmos scripts do compose** (`init-roles.sh`, `init-queues.sh`) e aplicando as migrations com `golang-migrate` como biblioteca (role `wallet_owner`). A aplicação conecta com `wallet_app`.
- Comando: `make test-integration` (`go test -tags integration -race -count=1 ./test/...`).
- Primeiro teste: `TestFxApp_StartsServesAndStopsWithoutLeaks`. Sobe a aplicação completa com `fxtest`, verifica o readiness (Postgres e SQS ok), para e checa com `goleak` que nenhuma goroutine vazou (README §13: "verificação da composição Fx e de seu início e encerramento, incluindo liberação de recursos dos workers").

## D-010 — Domínio em pacote único `internal/domain` (etapa 1.3)

- **Opções:** pacote único × subpacotes (`money`, `wallet`, `wager`, `event`).
- **Decisão:** pacote único, mais simples de navegar, sem risco de import cycle e com os erros compartilhados no mesmo lugar (`domain.Money`, `domain.Wallet`).
- **Isolamento verificado:** `go list -deps ./internal/domain` não contém Fx, `net/http`, AWS nem pgx. Os imports são apenas a biblioteca padrão e `github.com/google/uuid` (README §4).

## D-011 — Money: `int64` em centavos, parsing estrito, moedas BRL/USD/EUR (etapa 1.3)

- **Representação:** `Money{minor int64, currency Currency}`, imutável (campos não exportados, operações retornam valores novos).
  - `Currency` também tem campo não exportado, então o valor zero (`Currency{}`/`Money{}`) é inválido e rejeitado por todas as operações.
- **Moedas:** lista curta BRL, USD, EUR, todas ISO 4217 com 2 casas.
  - Opções: lista curta × ISO 4217 completa (~150 moedas) × apenas o formato `^[A-Z]{3}$`, que aceitaria "XYZ" e moedas com outra escala.
  - Moedas como JPY (0 casas) e KWD (3 casas) não são suportadas, pela escala fixa (README §6.1).
- **Parsing de entrada externa (`ParseMoney`):** regex `^(0|[1-9][0-9]*)\.[0-9]{2}$` seguida de conversão inteira, sem float.
  - Rejeita vazio, sinal (`-`/`+`), `NaN`, `Infinity`, notação científica, escala diferente de 2, zeros à esquerda, espaços, separador `,`, dígitos não ASCII e valores acima do limite.
  - **Não há normalização:** só existe uma forma textual aceita para cada valor (ex.: `"25.00"`). O hash de idempotência (etapa 2.1) usa essa forma única, e entradas como `"25"` ou `"25.0"` são rejeitadas, nunca arredondadas.
  - O código da moeda é case-sensitive: `"brl"` é rejeitado, não normalizado.
- **Limites:** de `-92233720368547758.08` a `92233720368547758.07`. A entrada externa só aceita valores de `0.00` até o máximo.
- **Overflow:** checado antes de cada `Add`/`Sub`/`Neg` (e no parsing). Retorna `ErrMoneyOverflow` (`AMOUNT_OUT_OF_RANGE`).
- **Moedas diferentes:** `Add`, `Sub` e `Compare` retornam `ErrCurrencyMismatch`.
- **Serialização:** `MarshalJSON` produz `{"amount":"25.00","currency":"BRL"}`; negativos saem como `"-20.00"` (usados em diferenças, por exemplo na reconciliação).
  - Não há `UnmarshalJSON`. A entrada externa passa sempre por `ParseMoney`, que é estrito e não aceita negativos, e os valores do banco são reconstruídos por `NewMoney(minor, currency)`.

## D-012 — Erros de domínio: `*DomainError{Kind, Code}` (etapa 1.3)

- **Opções:** tipo único com código e categoria × `errors.New` com uma tabela de tradução na aplicação.
- **Decisão:** um `*DomainError` por erro (`var ErrInsufficientFunds = &DomainError{...}`), detalhado com `fmt.Errorf("%w: ...")`. Funciona com `errors.Is` e `errors.As`.
- **Categorias (`Kind`):**
  - `VALIDATION`: entrada corrigível (ex.: `INVALID_MONEY`, `INVALID_CURRENCY`, `INVALID_AMOUNT`, `INVALID_REQUEST`, `UNSUPPORTED_KIND`);
  - `BUSINESS`: rejeição definitiva, que vira `REJECTED` (ex.: `INSUFFICIENT_FUNDS`, `CURRENCY_MISMATCH`, `AMOUNT_OUT_OF_RANGE`);
  - `CONFLICT`: reservado para idempotência e unicidade;
  - `INTERNAL`: estado impossível, que indica bug ou dado corrompido (`INVALID_WALLET`, `INVALID_LEDGER_ENTRY`, `INVALID_TRANSITION`, `TERMINAL_STATE`, `INVALID_EVENT`).
- O domínio nunca usa `panic` para regra de negócio. O único `panic` possível é o `uuid.Must(uuid.NewV7())`, se o gerador aleatório do sistema operacional falhar.

## D-013 — Modelagem de Wallet, LedgerEntry e WagerTransaction (etapa 1.3)

- **Criação × reidratação:**
  - criação: `OpenWallet`, `NewExternalTransaction`, `NewLedgerEntry`, que geram ID (UUIDv7) e validam;
  - reidratação: `RehydrateWallet`, `RehydrateWagerTransaction`, `RehydrateLedgerEntry`, que **apenas validam** a consistência do estado persistido, sem movimentar saldo, transicionar estado ou emitir eventos (README §6).
- **Wallet:** `Debit`/`Credit` validam a carteira (inclusive a não inicializada), o valor positivo, a mesma moeda, overflow e saldo ≥ 0. Em seguida geram o `LedgerEntry` correspondente, atualizam o saldo e incrementam a versão.
  - Em caso de erro, nada muda: saldo, versão e `updatedAt` ficam intactos (testado).
  - Débito acima do saldo retorna `INSUFFICIENT_FUNDS`. O código diferente para a reversão (`REVERSAL_INSUFFICIENT_FUNDS`) é aplicado pela regra de reversão, na etapa 2.3.
- **Versão na abertura:** começa em 1, e o crédito de abertura **não** a incrementa (README §9: "a versão da carteira nessa abertura é 1"). Os movimentos seguintes incrementam.
- **LedgerEntry:** valida `balanceAfter = balanceBefore ± amount`, `amount > 0`, saldos ≥ 0, mesma moeda e direção conhecida.
- **WagerTransaction:** o estado interno é um `WagerTransactionSnapshot`. `Snapshot()` e `Rehydrate` fazem cópia profunda dos ponteiros, para que ninguém altere a entidade por fora (testado).
  - Os metadados externos ficam agrupados em `ExternalDetails`, que é `nil` para `OPENING`, e a validação de origem impõe isso nos dois sentidos.
  - **Política de valor:** LOSS = `0.00`; BET, WIN, REFUND, ROLLBACK e OPENING > 0.
  - **Referência:** obrigatória em REFUND/ROLLBACK; opcional em WIN; **rejeitada** em BET/LOSS, que não têm uso para ela, em vez de ser ignorada silenciosamente.
  - **Máquina de estados:**
    - `PENDING` → `PROCESSED` | `REJECTED` | `FAILED` | `PENDING_REFERENCE` (esta última só para reversões);
    - `PENDING_REFERENCE` → `PROCESSED` | `REJECTED` | `FAILED`;
    - estados terminais retornam `ErrTerminalState` sem alterar nada;
    - `PROCESSED` exige `balanceAfter` (o saldo observado, para o replay) e, em reversões, a referência resolvida; `REJECTED`/`FAILED` exigem `failureCode`.
  - **`OPENING`:** construtor interno separado (`newOpeningTransaction`, não exportado); `ParseExternalKind` e `NewExternalTransaction` rejeitam `OPENING` com `UNSUPPORTED_KIND`.
- **Tempo:** os construtores normalizam os instantes para UTC truncado em microssegundos, a precisão do `TIMESTAMPTZ`. Assim, o valor em memória é igual ao relido do banco.

## D-014 — Eventos: uma struct por tipo, embutindo `EventHeader` (etapa 1.3)

- **Opções:** struct por evento × envelope genérico `Envelope[T]` × `Data any`, que não seria tipado.
- **Decisão:** `WagerTransactionProcessed`, `WagerTransactionRejected`, `WalletBalanceChanged` e `WagerTransactionPendingReference`, cada um com `EventHeader` embutido e `Data` tipado. O JSON do envelope sai plano: `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId?`, `occurredAt`, `version`, `data`.
- **Construtores:** o tipo e a versão (1) são fixados pelo construtor (README §11). O construtor exige o status correspondente da transação, por exemplo `Processed` só para `PROCESSED`, e valida `correlationId` e `occurredAt`.
- **`aggregateId` = `walletId`** em todos os eventos: a carteira é a raiz do agregado, e esse valor vira o `MessageGroupId` na publicação, o que dá ordem por carteira.
- **Timestamps:** UTC em RFC 3339. **Dinheiro:** strings decimais. **`WalletBalanceChanged`** traz `walletId`, `transactionId`, `direction`, `money`, `balanceBefore`, `balanceAfter` e `walletVersion`.
- **`OpenWallet`** retorna `WalletOpening{Wallet, Transaction, LedgerEntry, Events}`. Com saldo zero, só a carteira, sem OPENING, ledger ou eventos (README §9). O payload é serializado uma vez, na criação, e será gravado na outbox como snapshot imutável (etapa 2.7).
- **Testes do domínio:** 94,7% de cobertura, com `-race`.

## D-015 — Referência em BET/LOSS e saldo observado na rejeição (etapa 1.3)

- **Referência em BET/LOSS:** é recusada como entrada inválida (`INVALID_REQUEST`, categoria VALIDATION).
  - Alternativa avaliada: aceitar e ignorar o campo.
  - Decisão: recusar, para que nada seja descartado em silêncio e o provedor descubra o erro.
- **Saldo observado na rejeição:** `REJECTED` passa a guardar o saldo da carteira no momento da rejeição, no mesmo campo `balanceAfter` (para uma rejeição, igual ao saldo anterior, porque não há movimento).
  - Alternativa avaliada: rejeição sem saldo. O README só exige o saldo no replay de operações concluídas.
  - Decisão: guardar o saldo. No teste das duas apostas de 80.00, a rejeitada registra 20.00, o que ajuda o provedor a entender a recusa, inclusive no replay.
  - `MarkRejected(code, observedBalance, now)` exige um saldo válido e não negativo. A moeda não é comparada com a da transação, porque uma rejeição por `CURRENCY_MISMATCH` observa justamente a moeda da carteira.
  - A reidratação de `REJECTED` sem `balanceAfter` falha. O evento `WagerTransactionRejected` traz `data.observedBalance`.
  - `FAILED` (falha de infraestrutura) continua sem saldo, porque pode acontecer sem que a carteira tenha sido lida.

## D-016 — Schema: `TEXT` + `CHECK` e moeda explícita do saldo observado (etapa 1.4)

- **Tipos fixos:** `kind`, `status`, `origin` e `direction` são `TEXT` com `CHECK (... IN (...))`, e não `ENUM`.
  - Opções: `TEXT` + `CHECK` (simples de evoluir e reverter, funciona direto com pgx) × `ENUM` (tipagem forte, mas `ALTER TYPE` limitado, sem como remover valores e com cast/registro no pgx).
- **Moeda do saldo observado:** `wager_transactions.balance_after BIGINT` + `balance_currency CHAR(3)`, com `CHECK` que exige os dois juntos ou nenhum.
  - Opções: coluna própria × usar a moeda da carteira via join.
  - Motivo: numa rejeição por `CURRENCY_MISMATCH`, o saldo observado está na moeda da carteira, diferente da moeda da transação (D-015). README §6.1: "a persistência deve preservar exatamente valor e moeda".

## D-017 — Invariantes impostas pelo banco (etapa 1.4)

Cada invariante do README §5.8 e §6 tem uma proteção no schema, verificada por `TestSchema_Invariants` (27 casos, que conferem o código do erro **e o nome da constraint**):

| Invariante | Proteção |
| --- | --- |
| Saldo nunca negativo | `wallets.balance CHECK (>= 0)`; no ledger, `balance_before/after >= 0` |
| Uma carteira por `(player, currency)` | `wallets_player_currency_uk` |
| Idempotência persistente por provedor | `wt_provider_idempotency_key_uk (provider_id, idempotency_key)` e `wt_provider_external_id_uk (provider_id, external_transaction_id)`, índices parciais para `origin = 'EXTERNAL'` |
| Crédito inicial único | `wt_single_opening_per_wallet_uk (wallet_id) WHERE kind = 'OPENING'` |
| Uma reversão bem-sucedida por transação (D-003) | `wt_single_successful_reversal_uk (reference_transaction_id) WHERE status = 'PROCESSED' AND kind IN ('REFUND','ROLLBACK')` |
| Interno × externo | `origin` explícito; `wt_origin_matches_kind_ck` (`INTERNAL` ⇔ `OPENING`); `wt_internal_has_no_external_data_ck`; `wt_external_has_required_data_ck` |
| Política de valor por tipo | `wt_amount_by_kind_ck`: LOSS ⇔ `amount = 0` |
| Regras de referência | `wt_reversal_has_reference_ck`; `wt_bet_loss_have_no_reference_ck` (D-015); `wt_processed_reversal_has_resolved_reference_ck` |
| Resultado de transação concluída | `wt_terminal_has_processed_at_ck`, `wt_concluded_has_balance_ck` (PROCESSED/REJECTED), `wt_unsuccessful_has_failure_code_ck` (REJECTED/FAILED), `wt_balance_pair_ck` |
| Estado terminal imutável | trigger `wager_transactions_terminal_guard` (BEFORE UPDATE) |
| Ledger: um lançamento por `(wallet, transaction)` | `ledger_wallet_transaction_uk` |
| Ledger: lançamento da mesma carteira da transação | FK composta `ledger_transaction_belongs_to_wallet_fk (transaction_id, wallet_id) → wager_transactions (id, wallet_id)`, apoiada por `wt_id_wallet_uk` |
| Ledger: aritmética | `ledger_balance_math_ck` (`after = before ± amount`) |
| Ledger append-only | duas camadas: (1) o role `wallet_app` só tem `SELECT, INSERT`, então UPDATE/DELETE/TRUNCATE dão *permission denied*; (2) triggers `BEFORE UPDATE OR DELETE` e `BEFORE TRUNCATE` bloqueiam **até o dono do schema** |
| Ausência de exclusões | o `wallet_app` não tem `DELETE` em nenhuma tabela |
| Inbox: `(consumer_name, message_id)` único | chave primária |
| Outbox: snapshot imutável | trigger `outbox_events_snapshot_guard` bloqueia mudança de `event_id`, `aggregate_*`, `event_type`, `event_version`, `payload`, `correlation_id` e `occurred_at`, e impede republicar algo com `published_at` já preenchido; só os campos de controle (`attempts`, `next_attempt_at`, `locked_*`, `published_at`, `last_error`) podem mudar |

- **Ausência de referência é `NULL`, nunca `''`:** os repositórios gravam campos opcionais vazios como `NULL`, e o domínio usa `""` para ausente.
- **Migrations:** `000001`–`000005`, uma por tabela, cada uma com seus grants. O `down` remove a tabela e a função de trigger. Comandos: `make migrate-up`, `make migrate-down` (um passo), `make migrate-down-all` e `make migrate-version`. Verificado: `down -all` → `up` limpo, no compose e em teste (`TestMigrations_UpDownUp`).

## Problemas encontrados

### P-001 — LocalStack recente exige licença (etapa 1.1)
A `localstack/localstack:latest` (`2026.09.0`) encerra na inicialização sem token. Resolvido fixando a `4.14.0` (D-001).

### P-002 — `migrate up` falha com a pasta de migrations vazia (etapa 1.1)
O `golang-migrate` retorna `error: first .: file does not exist` quando não há arquivos. Resolvido adiantando a migration `000001_create_wallets` (schema já aprovado no plano, etapa 1.4) em vez de criar uma migration vazia.

### P-003 — Imagem do Keycloak sem curl/wget para o healthcheck (etapa 1.1)
O healthcheck usa `/dev/tcp` do bash contra o endpoint de health da porta de gestão (9000), com `KC_HEALTH_ENABLED=true`.

### P-004 — Issuer do Keycloak depende de como ele é acessado (pendente, etapa 1.6)
Acessado pelo host, o `issuer` é `http://localhost:8081/realms/wagering`. De dentro da rede do compose, seria `http://keycloak:8080/...`. Tokens obtidos pelo host não validariam na aplicação. Será resolvido na etapa 1.6, com trade-offs apresentados.

### P-005 — Testes do Runner com condição de corrida no próprio teste (etapa 1.2)
Dois testes chamavam `Stop` logo depois de `Start`. A goroutine às vezes ainda não tinha entrado em `RunOnce`, via o contexto já cancelado e saía sem executá-lo, o que é o comportamento correto do Runner. Os testes passaram a esperar um sinal `started` antes do `Stop`. Rodados com `-race -count=3` sem falhas.

### P-006 — Mensagem de erro de parse da config cita o campo, não a variável (etapa 1.2)
Para `LOG_LEVEL=LOUD`, o `caarlos0/env` retorna `parse error on field "LogLevel"`. A mensagem continua clara e foi aceita sem código extra de tradução.

### P-007 — `CHECK (col <> '')` aceita `NULL` (etapa 1.4)
No SQL, `NULL <> ''` resulta em `NULL`, e um `CHECK` só rejeita quando o resultado é `false`. A primeira versão de `wt_external_has_required_data_ck` e de `wt_reversal_has_reference_ck` aceitava transação externa sem `provider_id` e REFUND sem referência. O caso do REFUND estava mascarado nos testes porque outra constraint falhava antes.
- **Correção:** `COALESCE(col, '') <> ''`.
- **Prevenção:** os testes passaram a verificar o **nome** da constraint violada, não só o código do erro, e cobrem `NULL` e `''` para cada campo obrigatório.
