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

## D-018 — Acesso a dados: store concreto com Unit of Work (etapa 1.5)

- **Contexto:** o README (§4) pede para documentar "a delimitação da transação SQL entre os repositórios" e para que "transações, locks e constraints permaneçam explícitos e verificáveis".
- **Opções:**
  - (A) interfaces na aplicação + Unit of Work: permite repositórios falsos, ao custo de mais código e indireção;
  - (B) funções do store recebendo `pgx.Tx`: o mínimo de código, mas a aplicação manipula `pgx.Tx`;
  - (C) store concreto + Unit of Work, sem interfaces.
- **Decisão:** (C), pacote `internal/store`.
  - `st.InTx(ctx, func(r *store.Repos) error)` abre **uma** transação `READ COMMITTED`. Todos os repositórios em `r` (`Wallets`, `Transactions`, `Ledger`, `Outbox`) usam essa mesma transação, que faz commit se `fn` retornar `nil` e rollback em qualquer erro, inclusive `panic`, via `defer Rollback` com `context.WithoutCancel`.
  - `st.Read()` devolve os mesmos repositórios ligados ao pool, para leituras fora de transação.
  - **A transação é delimitada sempre no caso de uso, nunca dentro de um repositório.** Os repositórios usam uma interface interna `querier` (`Exec`/`Query`/`QueryRow`), satisfeita tanto por `pgx.Tx` quanto por `*pgxpool.Pool`.
  - A aplicação não manipula tipos do pgx. Consequência: os casos de uso só são testados com Postgres real, o que o README já exige para as garantias.
- **Biblioteca:** `pgx/v5` com `pgxpool`, SQL escrito à mão em cada repositório, sem ORM nem gerador.
- **Mapeamento de `Money`:** `BIGINT` (centavos) + `CHAR(3)` (moeda), reconstruído por `domain.NewMoney` + `ParseCurrency`. No ledger, valor e saldos compartilham a coluna `currency`. Na transação, `amount`/`currency` e `balance_after`/`balance_currency` são pares independentes (D-016).
- **Campos opcionais:** `""` no domínio vira `NULL` no banco e volta como `""` (testado).

## D-019 — Retry de transação, timeouts e classificação de erros (etapa 1.5)

- **Retry automático da transação inteira:** até **3 tentativas**, com backoff curto (10ms × tentativa + jitter de até 10ms), para:
  - `ErrConcurrentUpdate`: `UPDATE wallets ... WHERE version = $expected` afetou 0 linhas;
  - `40001` (`serialization_failure`) e `40P01` (`deadlock_detected`).
  - Cada retry incrementa `wallet_concurrency_conflicts_total{reason="version|serialization|deadlock"}` e gera um log WARN.
  - Esgotadas as tentativas, retorna `ErrConcurrentUpdate`.
  - **Contrato:** `fn` pode ser executada mais de uma vez, então não pode ter efeitos fora da transação, como publicar no SQS ou chamar HTTP.
- **Timeouts na conexão** (parâmetros de runtime do pool, configuráveis): `lock_timeout = DB_LOCK_TIMEOUT` (padrão 5s) e `statement_timeout = DB_STATEMENT_TIMEOUT` (padrão 10s), com a validação `lock < statement`. Nenhuma transação espera indefinidamente por um lock de carteira.
- **Falhas transitórias** viram `store.ErrUnavailable`, que vai mapear para HTTP 503 e retry no SQS:
  - `55P03` (`lock_not_available`, estouro do `lock_timeout`), `57014` (`query_canceled`, `statement_timeout`), `53300` (`too_many_connections`), `57P01` (`admin_shutdown`), `57P03` (`cannot_connect_now`) e a classe `08` (conexão);
  - erros de conexão do pgconn, timeouts de rede e erros "seguros para repetir".
  - Se o próprio `ctx` foi cancelado (cliente desistiu ou shutdown), o erro do contexto é devolvido como está, e não como indisponibilidade.
- **Outros mapeamentos:** `pgx.ErrNoRows` vira `store.ErrNotFound`; o unique `wallets_player_currency_uk` vira `domain.ErrWalletAlreadyExists` (categoria CONFLICT).
- **Verificado em teste** (`TestStore`, 11 casos com Postgres real):
  - ida e volta do banco para abertura, transação externa e ledger;
  - rollback completo em erro;
  - versão desatualizada → `ErrConcurrentUpdate`;
  - retry com sucesso na 2ª tentativa e métrica incrementada; limite de 3 tentativas;
  - **lock de carteira além do `lock_timeout` → `ErrUnavailable` em cerca de 500ms;**
  - **lock de uma carteira não bloqueia outra carteira** (sem lock global, README §5.6);
  - paginação do ledger estável por `(created_at, id)`, com lançamentos encadeados.
- **Tempo:** a reidratação normaliza os instantes para UTC truncado em µs, porque o pgx devolve `TIMESTAMPTZ` no fuso local. O instante não muda, e o valor relido do banco fica idêntico ao criado em memória.

## D-020 — IdP Keycloak, `client_credentials` e validação de tokens (etapa 1.6)

- **IdP:** Keycloak 26.8 (recomendado pelo README §2), com o realm `wagering` importado no boot (`--import-realm`, `deploy/keycloak/realm-wagering.json`). Não há passo manual; o mesmo arquivo é usado no compose e nos testes.
- **Fluxo:** `client_credentials`, de serviço para serviço. Cada provedor e o serviço interno são clients confidenciais com conta de serviço, sem fluxo de navegador nem senha de usuário (README: "cadastro de senhas e emissão própria de tokens estão fora do escopo").
- **Identidades de teste** (secrets apenas locais, no formato `<client>-local-secret`):

  | Client | Roles | `provider_id` | Uso |
  | --- | --- | --- | --- |
  | `provider-a` / `provider-b` | `provider` | `provider-a` / `provider-b` | Provedores de jogos |
  | `wallet-service` | `wallet-admin` | — | Serviço interno (operações de carteira) |
  | `provider-a-short-lived` | `provider` | `provider-a` | Token de **2s**, para o teste de token expirado |
  | `no-role-client` | — | — | Autenticado, mas sem permissão (403) |
  | `other-api-client` | `provider` | — | Token **sem** `aud=wagering-api` (401) |

- **Endereço do Keycloak no token, problema P-004:**
  - Opções: (a) endereço público fixo + JWKS interno × (b) `/etc/hosts` (passo manual) × (c) aceitar vários issuers (validação mais fraca).
  - Decisão: (a). `KC_HOSTNAME=http://localhost:8081` faz **todo** token sair com `iss=http://localhost:8081/realms/wagering`, seja pedido pelo host ou pela rede interna (verificado). `KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true` mantém as chamadas internas por `keycloak:8080`.
  - A aplicação valida `OIDC_ISSUER` exatamente e busca as chaves em `OIDC_JWKS_URL` (`http://keycloak:8080/.../certs`), **sem discovery**, com `oidc.NewRemoteKeySet` + `oidc.NewVerifier`.
- **Validação do token** (`coreos/go-oidc/v3`):
  - assinatura **RS256** com as chaves do JWKS, em cache e com rotação automática quando aparece um `kid` novo;
  - `iss` exatamente igual a `OIDC_ISSUER`;
  - `aud` contendo `OIDC_AUDIENCE` (`wagering-api`, posto por um audience mapper em cada client que pode chamar a API);
  - `exp` no futuro.
- **Inicialização:** o `OnStart` do verificador busca o JWKS uma vez e falha se o endpoint estiver inacessível ou sem chaves, o que valida a dependência no boot (testado). O cliente HTTP é dedicado, e suas conexões são fechadas no `OnStop`.

## D-021 — Modelo de autorização (etapa 1.6)

- **`providerId` vem de uma claim própria `provider_id`**, posta por um mapper fixo em cada client de provedor.
  - Alternativa: usar o `client_id` (`azp`).
  - Motivo: separa a credencial da identidade de negócio, o que permite trocar o client, ter dois clients para o mesmo provedor ou renomear sem afetar as transações gravadas.
  - Isso dá ao token `provider-a-short-lived` o mesmo `provider_id=provider-a`.
- **Permissões por roles do realm**, lidas de `realm_access.roles`:
  - Alternativa: scopes OAuth, mais granulares e com mais configuração.
  - `provider`: enviar e consultar as **próprias** transações. Exige também uma `provider_id` não vazia; uma credencial com a role e sem a claim recebe 403.
  - `wallet-admin`: operações de carteira (abertura, consulta, ledger, reconciliação), restritas ao serviço interno (README §2).
- **Middlewares** (`internal/httpapi/auth.go`):
  - `Authenticate`: exige `Authorization: Bearer <jwt>`. Sem token, com outro esquema ou com token inválido, expirado, de outro issuer ou com outra audiência: **401** `{"error":{"code":"UNAUTHENTICATED"}}` com `WWW-Authenticate: Bearer realm="wagering", error="invalid_token"`. O `Principal` (`sub`, `azp`, `provider_id`, roles) vai para o contexto.
  - `RequireRole(role)`: sem a role, **403** `{"error":{"code":"FORBIDDEN"}}`.
  - Tokens nunca são logados; uma falha de autenticação loga só o caminho e o motivo.
- **Regras por recurso** (aplicadas nas etapas 1.7 e 2.4):
  - `providerId` do corpo igual ao do token, senão 403 **antes de qualquer escrita**;
  - transações de outro provedor → 404, para não revelar a existência;
  - idempotência sempre escopada pelo `provider_id` do token.
- **Mensageria:** sem token no SQS. O acesso será controlado por credenciais e políticas do broker, e as validações de domínio continuam no consumidor (etapa 2.6).
- **Verificado em teste** (`TestAuth_RealKeycloak`, 16 casos com Keycloak real):
  - 200 para provedor e serviço interno, com o `providerId` correto;
  - 401 para cabeçalho ausente, esquema `Basic`, bearer vazio, JWT malformado, assinatura adulterada, payload adulterado, audiência errada, outro issuer e **token expirado** (client de 2s);
  - 403 para provedor em rota interna, serviço interno em rota de provedor e client sem roles;
  - o verificador não inicia sem JWKS.
- **Comandos:** `make token-provider-a`, `make token-provider-b` e `make token-wallet-service` imprimem um access token; um client inexistente faz o comando falhar.

## D-022 — Endpoints de carteira, contrato HTTP e correlação (etapa 1.7)

- **Camada de aplicação** (`internal/app`): `Wallets.Open`, `Wallets.Get`, `Wallets.Ledger`.
  - A abertura chama `domain.OpenWallet` e persiste, em **um** `store.InTx`, a carteira, a transação `OPENING`, o lançamento e os 2 eventos da outbox.
  - Testado consultando o `xmin` das linhas: todas foram gravadas pela mesma transação do banco (README §9).
  - `app.ErrNotFound` e `app.ErrUnavailable` isolam o HTTP dos detalhes do store. As leituras fora de transação também passam por `store.Classify`, para que uma falha transitória vire 503.
- **Rotas** (todas exigem `wallet-admin`, README §2):

  | Rota | Sucesso | Erros |
  | --- | --- | --- |
  | `POST /wallets` | 201 + `Location` + `{id, playerId, balance, version, createdAt, updatedAt}` | 400 entrada inválida, 409 `WALLET_ALREADY_EXISTS` |
  | `GET /wallets/{walletId}` | 200 | 400 id inválido, 404 |
  | `GET /wallets/{walletId}/ledger?cursor=&limit=` | 200 `{items, nextCursor}` | 400 `limit`/`cursor` inválidos, 404 carteira inexistente |

- **Paginação do ledger:**
  - ordenação estável por `(created_at, id)` (o id é UUIDv7), com a consulta `(created_at, id) > cursor`;
  - o cursor é **opaco**: base64url do JSON `{t, id}`, e qualquer adulteração resulta em 400;
  - `limit` padrão 50, aceito de 1 a 200. A consulta busca `limit+1` para saber se há próxima página, e `nextCursor` é `null` na última.
- **Entrada estrita:**
  - `DisallowUnknownFields`, limite de 64 KiB, exatamente um objeto JSON;
  - `playerId` UUID não nulo, `initialBalance` obrigatório;
  - o dinheiro passa por `domain.ParseMoney`, então um valor numérico (`10.5`) em vez de string é rejeitado.
- **Mapeamento central de erros** (`writeAppError`):

  | Origem | HTTP | Código |
  | --- | --- | --- |
  | `DomainError` categoria VALIDATION | 400 | código do erro (`INVALID_MONEY`, `INVALID_CURRENCY`, ...) |
  | Requisição malformada | 400 | `INVALID_REQUEST` |
  | `DomainError` categoria CONFLICT | 409 | código do erro |
  | `DomainError` categoria BUSINESS | 422 | código do erro |
  | `app.ErrNotFound` | 404 | `NOT_FOUND` |
  | `app.ErrUnavailable` | 503 + `Retry-After: 1` | `TEMPORARILY_UNAVAILABLE` |
  | Qualquer outro erro | 500 (logado) | `INTERNAL_ERROR` |

  Formato: `{"error":{"code","message","correlationId"}}`.
- **Correlação (antecipada da etapa 2.9):** o middleware `CorrelationID` propaga `X-Correlation-Id`, aceito até 128 caracteres, ou gera um UUID novo. O valor é devolvido no cabeçalho da resposta, no corpo de erro e gravado em `wager_transactions.correlation_id` e nos eventos da outbox.
- **Fx:** `metrics.Module` passou a fornecer o registro também como `prometheus.Registerer`, usado pelo store.
- **Verificado:**
  - `TestWalletAPI`, 19 casos com a aplicação completa via Fx, Postgres, LocalStack e Keycloak reais;
  - fluxo manual com curl e token real no compose: 201, 409, 200, ledger, 403 para provedor e 401 sem token, com os eventos na outbox.

## D-023 — Hash canônico do payload e parsing único da operação (etapa 2.1)

- **Contexto:** o README (§9) pede um "hash determinístico dos campos de negócio, usando JSON canônico com ordenação de chaves", sem a chave de idempotência e sem metadados de transporte, com equivalência entre HTTP e SQS. Pede também: "caso aceite formas equivalentes, documente a normalização anterior ao hash".
- **Parsing único:** `domain.WagerRequestInput` define o formato JSON da operação, igual no corpo HTTP e no `data` da mensagem SQS, todos os campos como string. `domain.ParseWagerRequest(input, idempotencyKey)` valida e produz `domain.WagerRequest`, que calcula o hash.
  - O HTTP passa a chave do header `Idempotency-Key`; o SQS passa `data.idempotencyKey`. Os dois canais **não têm** código de validação próprio, então não podem divergir.
  - `money.amount` numérico (ex.: `25.00` sem aspas) não decodifica, porque não aceitamos float na entrada.
- **Algoritmo:** `hex(SHA-256(JSON canônico))`, gravado em `wager_transactions.payload_hash`.
  - **Campos incluídos:** `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money.amount`, `money.currency` e `referenceExternalTransactionId`, este **omitido** quando ausente, nunca `""` nem `null`.
  - **Excluídos:** a chave de idempotência e todo metadado de transporte (`messageId`, `type`, `occurredAt`, headers, correlation id).
  - **Forma canônica:** objeto com chaves em ordem lexicográfica, também no `money` aninhado (o `encoding/json` ordena as chaves de mapas), sem espaços, todos os valores como string.
  - Exemplo do README:
    ```
    {"externalTransactionId":"transaction-123","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"},"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","providerId":"provider-a","roundId":"round-987","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"}
    → 629836932b79106b99523d06a1e7fa80689b0ea1e1c47aa3f0a5a2c87d0c4344
    ```
    Esse valor foi conferido de forma independente com Python (`json.dumps(sort_keys=True, separators=(',',':'))` + `hashlib.sha256`) e está fixado num teste *golden*, que falha se o algoritmo mudar por acidente.
- **Normalizações e formas aceitas:**

  | Campo | Regra | Normalização antes do hash |
  | --- | --- | --- |
  | `playerId`, `walletId` | UUID canônico 8-4-4-4-12, sem `{}`, `urn:uuid:` ou forma sem hífens; UUID nulo recusado | **maiúsculas → minúsculas** (UUID não diferencia caixa) |
  | `providerId`, `externalTransactionId`, `roundId`, `gameId`, `referenceExternalTransactionId` | `^[A-Za-z0-9._:-]{1,128}$` | nenhuma |
  | Chave de idempotência | `^[A-Za-z0-9._:-]{1,256}$`; comporta `{providerId}:{externalTransactionId}` | nenhuma, e **nunca substituída** por uma chave calculada |
  | `kind` | `BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`, exatos e em maiúsculas; `OPENING` → `UNSUPPORTED_KIND` | nenhuma |
  | `money` | `ParseMoney` estrito (D-011): uma única forma textual por valor | nenhuma necessária |

- **Charset restrito nos identificadores:**
  - Opções: charset restrito × texto livre até 128 caracteres.
  - Motivo: elimina ambiguidades de Unicode. "é" composto e decomposto são bytes diferentes e gerariam dois hashes para o "mesmo" texto. Também mantém os IDs seguros em logs e em URLs (`/providers/{id}/...`), sem nada a normalizar.
  - Espaço nas pontas é recusado, nunca removido em silêncio.
- **Testes** (`wager_request_test.go`):
  - JSON canônico exato e hash golden;
  - referência só entra no hash quando presente;
  - a chave de idempotência não afeta o hash;
  - **mensagem SQS** (chaves em outra ordem, envelope com `messageId`/`occurredAt`) e **corpo HTTP** (UUID em maiúsculas, espaços) geram **o mesmo hash**;
  - cada um dos 10 campos de negócio altera o hash;
  - 22 entradas inválidas recusadas com o erro correto.
- **Uso:** as regras de replay (mesma chave e mesmo hash → resultado persistido com `idempotentReplay: true`; mesma chave e hash diferente → 409; mesmo `externalTransactionId` com outra chave → 409) são aplicadas pelo `ProcessWager`, na etapa 2.2.

## D-024 — `ProcessWager` e a estratégia de concorrência (etapa 2.2)

- **Estratégia (README §8):** lock pessimista por carteira, combinado com uma checagem otimista de versão e as constraints do banco. Foi confirmada pelo usuário depois de comparar com otimista puro e atualização atômica condicionada: o pessimista lida melhor com a alta disputa numa mesma carteira, mantém a regra de saldo no agregado (README §6.2) e serializa as regras que dependem do estado atual, como a reversão única (D-003).
- **Fluxo** (`internal/app/wagers.go`), em **uma** transação `READ COMMITTED` via `store.InTx`:
  1. Cria a `WagerTransaction` em `PENDING` a partir do `WagerRequest` validado (D-023).
  2. `INSERT ... ON CONFLICT DO NOTHING`, que vale contra **qualquer** índice único: chave de idempotência ou `externalTransactionId`.
     - FK de carteira violada → `WALLET_NOT_FOUND`, nada persistido (D-002).
     - Não inseriu → replay ou conflito:
       - busca por `(provider_id, idempotency_key)`: com o mesmo hash, **replay**, que devolve a transação persistida com `Replay=true`, inclusive o `balanceAfter` original; com hash diferente, `IDEMPOTENCY_KEY_CONFLICT` (409);
       - não achou pela chave, mas existe pelo `(provider_id, external_transaction_id)` → `EXTERNAL_TRANSACTION_CONFLICT` (409): a operação não pode ser reaplicada com outra chave.
  3. `SELECT ... FROM wallets WHERE id = $1 FOR NO KEY UPDATE`: **o único lock da operação, só da linha daquela carteira**.
  4. `domain.ApplyToWallet`:
     - BET → `Debit`; WIN → `Credit`; LOSS → sem movimento;
     - jogador diferente → `PLAYER_WALLET_MISMATCH`; moeda diferente → `CURRENCY_MISMATCH`; saldo insuficiente → `INSUFFICIENT_FUNDS`.
  5. Erro de negócio → `MarkRejected(code, saldo observado)` + `WagerTransactionRejected`. Sucesso → lançamento no ledger + `UPDATE wallets ... WHERE version = $esperada` (só se o saldo mudou) + `MarkProcessed(saldo)` + `WagerTransactionProcessed` (+ `WalletBalanceChanged`).
  6. `UPDATE wager_transactions` com o resultado, outbox e commit.
  - O `WagerTransaction` é recriado **dentro** de cada tentativa do `InTx`. Num retry, o estado em memória da tentativa anterior, por exemplo `PROCESSED`, não vaza para a nova.
- **Por que funciona entre processos:**
  - **50 envios idênticos:** o primeiro `INSERT` grava; os outros **esperam no índice único** até o primeiro terminar. Depois do commit, recebem "conflito", e o `SELECT` seguinte (snapshot novo em `READ COMMITTED`) vê a linha confirmada → replay. Se o primeiro der rollback, um dos outros insere. Resultado: **um** débito.
  - **Duas BETs distintas de 80.00 sobre 100.00:** as duas inserem a própria transação e disputam o lock da carteira. A segunda espera; quando pega o lock, o `FOR NO KEY UPDATE` devolve a **versão mais recente** da linha (saldo 20.00) → `REJECTED INSUFFICIENT_FUNDS`, com o saldo observado 20.00.
  - **Carteiras diferentes** não compartilham nenhum lock.
- **Camadas de defesa:**
  1. lock de linha por carteira;
  2. `UPDATE ... WHERE version = $esperada`: se algum caminho esquecer o lock, a atualização com versão antiga afeta 0 linhas → retry, nunca lost update (README §5.7);
  3. `CHECK (balance >= 0)`;
  4. `UNIQUE` de idempotência e de `(wallet, transaction)` no ledger.
- **`FOR NO KEY UPDATE`, e não `FOR UPDATE`:** ver P-014, que foi descoberto e provado em teste.
- **Testes** (`wagers_test.go`, Postgres real, com **3 pools de conexão independentes** simulando 3 processos; as 3 instâncias reais com o compose ficam para o Dia 3):
  - BET, WIN e LOSS (LOSS sem ledger, sem mudança de versão e sem `WalletBalanceChanged`);
  - as 3 rejeições com o saldo observado e o evento;
  - carteira inexistente não persiste;
  - replay devolve o saldo original mesmo depois de outras movimentações;
  - replay de rejeição; os dois tipos de conflito;
  - **concorrência:**
    - a mesma BET 50× em paralelo → 1 processada + 49 replays, 1 débito;
    - **80 + 80 sobre 100** → 1 `PROCESSED`, 1 `REJECTED INSUFFICIENT_FUNDS` (observado 20.00), saldo 20.00, **um único débito**, e o reenvio das duas não altera nada;
    - 30 BETs distintas na mesma carteira → saldo 70.00, versão 31, **0 deadlocks**;
    - 200 BETs em 20 carteiras em paralelo → todas corretas;
    - em todos os casos, saldo armazenado = créditos − débitos do ledger.
  - O bloco de concorrência rodou 5× seguidas com `-race`, sem falha.

## D-025 — Referências, reversões e espera por referência (etapa 2.3)

- **WIN com referência ainda indisponível:**
  - Opções: (a) esperar em `PENDING_REFERENCE` × (b) recusar na hora × (c) creditar sem validar.
  - Decisão: **(a)**. A WIN que informa `referenceExternalTransactionId` segue a mesma regra das reversões: só é creditada depois que a BET existe e é validada. Com entrega fora de ordem, (b) recusaria definitivamente uma WIN legítima, e (c) pagaria sem validar.
  - Custo: o crédito atrasa até a BET chegar. Uma WIN **sem** referência continua sendo creditada imediatamente.
- **Resolução da referência:** por `(providerId do operador, referenceExternalTransactionId)`. O escopo é sempre o próprio provedor, então a referência a uma transação de outro provedor não é encontrada e fica pendente até expirar (testado).

  | Estado da referência | Resultado |
  | --- | --- |
  | não existe | `PENDING_REFERENCE` + `next_attempt_at = agora + PENDING_BASE_BACKOFF`, `expires_at = agora + PENDING_TTL` + evento `WagerTransactionPendingReference`; nenhum lock de carteira, nenhum movimento |
  | `PENDING` / `PENDING_REFERENCE` | também espera (`PENDING_REFERENCE`) |
  | `REJECTED` / `FAILED` | `REJECTED REFERENCE_NOT_PROCESSED` (definitivo) |
  | `PROCESSED` | valida e aplica |

- **Validações** (`domain.ApplyToWallet`, depois do lock da carteira):
  - provedor, jogador, carteira, moeda e **rodada** iguais aos da referência → senão `REFERENCE_MISMATCH`;
  - WIN só pode referenciar uma BET (`REFERENCE_MISMATCH`), e o valor da WIN é livre;
  - reversões: valor **igual** ao da referência (`AMOUNT_MISMATCH`), sem reversão parcial (README §7).
- **Matriz de reversões:**

  | Operação | Referência | Movimento | Sem saldo |
  | --- | --- | --- | --- |
  | REFUND | BET | crédito | — |
  | REFUND | WIN, REFUND, ROLLBACK, LOSS | `REFERENCE_KIND_NOT_REVERSIBLE` | — |
  | ROLLBACK | BET | crédito | — |
  | ROLLBACK | WIN | débito | `REVERSAL_INSUFFICIENT_FUNDS` |
  | ROLLBACK | REFUND | débito | `REVERSAL_INSUFFICIENT_FUNDS` |
  | ROLLBACK | ROLLBACK, LOSS | `REFERENCE_KIND_NOT_REVERSIBLE` | — |

  `REVERSAL_INSUFFICIENT_FUNDS` é diferente do `INSUFFICIENT_FUNDS` da aposta (README §7), e a rejeição é auditável: fica gravada com o saldo observado e gera evento.
- **Reversão única (D-003):** com a carteira travada, `HasSuccessfulReversal(ref)` → `ALREADY_REVERSED`. Todas as reversões de uma referência são da **mesma carteira**, então o lock as serializa.
  - Barreira final no banco: `wt_single_successful_reversal_uk`. Se mesmo assim ocorrer a violação, o store a converte em `ErrConcurrentUpdate`, e o `InTx` refaz a operação, que então vê `ALREADY_REVERSED`.
  - Combinações: BET → REFUND → ROLLBACK(REFUND) → novo REFUND da BET = `ALREADY_REVERSED`. A reversão da BET fica "consumida" e o débito nunca é devolvido duas vezes (testado).
- **Banco:** a migration `000006` troca `wt_processed_reversal_has_resolved_reference_ck` por `wt_processed_reference_is_resolved_ck`: **qualquer** transação `PROCESSED` com `reference_external_transaction_id` precisa de `reference_transaction_id`, o que inclui a WIN. É uma migration nova, sem editar a `000002`, para não divergir de bancos já migrados. Upgrade testado: o compose aplicou a `000006` sobre um banco existente.
- **Estrutura do código:** `ProcessWager` = inserção idempotente + `settle`. O `settle` (resolver referência → travar carteira → aplicar → persistir) será reutilizado pelo worker de pendências (etapa 2.5). Se a transação já está `PENDING_REFERENCE` e a referência continua ausente, `settle` não faz nada, e o worker cuida do reagendamento.
- **Configuração:** `PENDING_BASE_BACKOFF` (1s), `PENDING_MAX_BACKOFF` (5m), `PENDING_MAX_ATTEMPTS` (10) e `PENDING_TTL` (30m), validadas no boot. O backoff e a expiração são aplicados na etapa 2.5.
- **Testes:**
  - unitários: 5 movimentos válidos e 13 rejeições (tipo, valor, reversão já feita, sem saldo, referência rejeitada, rodada, carteira, jogador e provedor diferentes);
  - integração (`TestProcessWager_References`): REFUND, reversão única, ROLLBACK de REFUND, `REVERSAL_INSUFFICIENT_FUNDS`, validações, WIN com referência, espera (REFUND, ROLLBACK e WIN) com agendamento e evento, replay de pendente, isolamento entre provedores;
  - **concorrência:** REFUND + ROLLBACK + REFUND da mesma BET em paralelo, por 3 instâncias, repetido 10× → sempre **exatamente uma** reversão bem-sucedida e o saldo correto. O bloco rodou 5× seguidas com `-race`.

## D-026 — Endpoints de transação e contrato HTTP (etapa 2.4)

- **Rotas:**

  | Rota | Quem | Regra |
  | --- | --- | --- |
  | `POST /wagering/transactions` | role `provider` | `Idempotency-Key` obrigatório; `providerId` do corpo = `provider_id` do token, senão **403 antes de qualquer leitura ou escrita** |
  | `GET /wagering/transactions/{transactionId}` | `provider` ou `wallet-admin` | o provedor só vê as próprias; as de outro provedor (e as internas, `OPENING`) respondem **404**, sem revelar a existência |
  | `GET /providers/{providerId}/wagering/transactions/{externalTransactionId}` | `provider` ou `wallet-admin` | o provedor só consulta o próprio `providerId` (403 para outro); o serviço interno consulta qualquer um |

  - **Serviço interno na rota do provedor:** opções (a) permitir leitura × (b) só provedores. Decisão: **(a)**, coerente com a consulta por ID interno e útil para suporte e auditoria. O serviço interno **não** envia operações (403).
  - `RequireRole` passou a aceitar uma lista de roles ("qualquer uma"). Um principal só com `provider` precisa de `provider_id` não vazio.
- **Entrada:**
  - o corpo é decodificado diretamente em `domain.WagerRequestInput`, o mesmo tipo do SQS (D-023), com `DisallowUnknownFields`;
  - a chave **só** pode vir no header: um `idempotencyKey` no corpo é campo desconhecido → 400;
  - o servidor nunca substitui a chave recebida.
- **Respostas** (README §9: "situações distinguíveis pelo contrato"):

  | Situação | HTTP | Corpo |
  | --- | --- | --- |
  | Processada (nova) | **201** | `{transactionId, status: PROCESSED, balance, idempotentReplay: false}` |
  | Processada (replay) | **200** | igual, com o `balance` **original** e `idempotentReplay: true` |
  | Rejeição de negócio (nova ou replay) | **422** | `{transactionId, status: REJECTED, failureCode, balance (observado), idempotentReplay}` |
  | Aguardando referência | **202** + `Location: /wagering/transactions/{id}` | `{transactionId, status: PENDING_REFERENCE, idempotentReplay}` |
  | Entrada inválida | 400 | `{error: {code}}`: `MISSING_IDEMPOTENCY_KEY`, `INVALID_REQUEST`, `INVALID_MONEY`, `INVALID_AMOUNT`, `INVALID_CURRENCY`, `UNSUPPORTED_KIND` |
  | Carteira inexistente (D-002) | 422 | `{error: {code: WALLET_NOT_FOUND}}`, sem `status` (o que distingue de uma rejeição persistida) |
  | Sem token / token inválido | 401 | `UNAUTHENTICATED` |
  | Sem permissão / `providerId` de outro | 403 | `FORBIDDEN` |
  | Não encontrada | 404 | `NOT_FOUND` |
  | Mesma chave com outro payload / mesmo ID externo com outra chave | 409 | `IDEMPOTENCY_KEY_CONFLICT` / `EXTERNAL_TRANSACTION_CONFLICT` |
  | Indisponibilidade transitória | 503 + `Retry-After` | `TEMPORARILY_UNAVAILABLE` |

  `FAILED` (falha permanente de infraestrutura) responde 500 com o corpo da transação. Nenhum fluxo produz esse estado ainda.
- **Consulta:** `GET` devolve a visão completa: IDs, provedor, rodada, jogo, tipo, valor, status, `failureCode`, saldo, referências, `attempts`, `createdAt`/`updatedAt`/`processedAt` e, enquanto não terminal, `nextAttemptAt`/`expiresAt`, para acompanhar pendências (README §9). A visão vem de `store.TransactionView`, que acrescenta as colunas de agendamento à transação de domínio.
- **Log:** cada operação gera um `INFO "wager transaction handled"` com `transactionId`, `walletId`, `providerId`, `kind`, `status`, `failureCode`, `idempotentReplay` e `correlationId`, sem payload financeiro completo (README §12).
- **Testes** (`TestWagerAPI`, aplicação completa e tokens reais, 10 cenários):
  - 201, depois 200 com o saldo original após outra movimentação; 422 com saldo observado (também no replay); LOSS; 202 e consulta da pendência pelo `Location`; REFUND e `ALREADY_REVERSED`; os dois 409;
  - 9 entradas inválidas, sem nenhuma transação persistida;
  - autorização: 401; 403 para o serviço interno enviando, para provider-b se passando por provider-a e para provider-b reenviando o pedido do provider-a; 404/403 nas consultas cruzadas; provider-a e serviço interno leem; **as requisições negadas não persistem nada e não mudam o saldo**;
  - **80 + 80 sobre 100 via HTTP** (um 201, um 422, saldo 20.00) e **50 envios idênticos via HTTP** (um 201 e 49 × 200, saldo 90.00).
- **Postman:** pasta `05 - Operações de aposta` (23 requisições); a collection passou a ter 70 requisições e 193 asserções, todas passando no newman.

## D-027 — Worker de referências pendentes (etapa 2.5)

- **Contexto:** o README (§7) pede que a operação fique `PENDING_REFERENCE` quando a referência ainda não chegou, que um worker tente de novo com backoff exponencial, inclusive depois de reiniciar, e que haja limite de tentativas ou TTL com `REJECTED` e código de referência não encontrada. O §6.3 exige que "todo `PENDING` confirmado" tenha retomada durável por outra instância.
- **Algoritmo** (`Wagers.ResumeNextPending`), **uma pendência por transação SQL**:
  1. `SELECT ... WHERE status = 'PENDING_REFERENCE' AND next_attempt_at <= agora ORDER BY next_attempt_at LIMIT 1 FOR UPDATE SKIP LOCKED`: cada goroutine e cada instância pega uma pendência diferente sem esperar as outras.
  2. A referência existe e terminou → o **mesmo `settle`** do `ProcessWager` (D-025): valida, trava a carteira e aplica (ou rejeita com `REFERENCE_NOT_PROCESSED`/`REFERENCE_MISMATCH`/...).
  3. A referência não existe ou ainda está pendente → `attempts + 1`:
     - se chegou a `PENDING_MAX_ATTEMPTS` ou passou de `expires_at` (`PENDING_TTL`): trava a carteira e grava `REJECTED REFERENCE_NOT_FOUND`, com o saldo observado e o evento `WagerTransactionRejected`;
     - senão, reagenda `next_attempt_at = agora + backoff(attempts)`.
  4. Commit. Se o processo morrer antes do commit, o rollback libera o lock e a pendência continua lá para outra instância: **retomada durável**, porque o estado vive no banco e não na memória.
- **Backoff:** `base × 2^tentativas`, limitado a `PENDING_MAX_BACKOFF`, mais um jitter de até 10%, para que instâncias não sincronizem. A duplicação é feita com teto, sem deslocamento de bits, então não estoura com bases grandes (testado). Padrões: base 1s, teto 5m, 10 tentativas, TTL 30m.
- **Acordar quando a referência chega:**
  - Opções: (a) só backoff × (b) backoff + acordar.
  - Decisão: **(b)**. Quando uma transação externa termina (`PROCESSED` ou `REJECTED`), na mesma transação SQL é feito `UPDATE ... SET next_attempt_at = agora WHERE id IN (SELECT ... WHERE status = 'PENDING_REFERENCE' AND provider_id = $1 AND reference_external_transaction_id = $2 FOR UPDATE SKIP LOCKED)`.
  - Sem deadlock: o `ProcessWager` segura a carteira e **pula** pendências travadas; o worker segura a pendência e depois espera a carteira. Não há espera circular.
  - Caso raro: se o worker estiver avaliando a pendência exatamente nesse instante, ela é pulada e resolvida no backoff seguinte.
  - Também resolve **cadeias**: ROLLBACK que espera um REFUND, que espera uma BET. A BET acorda o REFUND; o REFUND, ao terminar, acorda o ROLLBACK (testado).
- **`PENDING` sem referência nunca é confirmado:** as operações sem dependência são concluídas de forma síncrona na mesma transação (README §6.3: "sem commit intermediário de aceite"). Por isso o worker só busca `PENDING_REFERENCE`.
- **Ciclo de vida:** `worker.PendingReferences` implementa `Loop` e roda no `Runner` genérico (D-007), com `PENDING_WORKERS` goroutines (2) e `PENDING_POLL_INTERVAL` (500ms) quando não há trabalho.
  - Pode ser desligado com `ENABLE_REFERENCE_WORKER=false`, por exemplo para instâncias só de API.
  - No shutdown, o contexto é cancelado: uma iteração em andamento é desfeita (**liberada**) e retomada depois por qualquer instância.
  - A ordem verificada com SIGTERM no compose: worker → HTTP → SQS → pool.
- **Contagem de tentativas:** `attempts` registra quantas vezes a pendência foi reagendada. Com `MAX_ATTEMPTS = N`, a N-ésima avaliação sem referência rejeita.
- **Testes:**
  - unitário: backoff exponencial, teto, jitter ≤ 10% e ausência de overflow;
  - integração (`TestPendingReferences`, 3 instâncias):
    - REFUND aplicado pelo worker depois da BET;
    - sem a BET, continua pendente e as tentativas sobem;
    - **acordar** torna a pendência elegível na hora, mesmo com backoff de 1h;
    - expiração por tentativas e por TTL → `REFERENCE_NOT_FOUND` + evento;
    - referência rejeitada → `REFERENCE_NOT_PROCESSED`;
    - **cadeia** ROLLBACK → REFUND → BET;
    - **9 workers em 3 instâncias** resolvendo 20 pendências: cada uma exatamente uma vez (20 créditos, saldo e ledger corretos);
  - ponta a ponta (`TestWagerAPI`): com o worker real dentro da aplicação, um REFUND enviado antes da BET vira `PROCESSED` em cerca de 0,5s depois que a BET chega;
  - `TestFxApp` com `goleak`: o worker para sem vazar goroutines.

## D-028 — Consumidor SQS com inbox (etapa 2.6)

- **Mesmo caso de uso do HTTP (README §10):**
  - o `data` da mensagem é decodificado no mesmo `domain.WagerRequestInput`, que passa por `ParseWagerRequest` (D-023), com a chave vinda de `data.idempotencyKey`;
  - `Wagers.ProcessMessage` executa **o mesmo** fluxo de `Process` (inserção idempotente → replay/conflito → `settle`), com a inbox somada **na mesma transação SQL**: inbox, transação, ledger, saldo e outbox são confirmados juntos (README §6.5).
  - Uma pendência de referência conclui a mensagem depois que a pendência está persistida; o worker (D-027) assume a continuidade.
- **Inbox:**
  - `INSERT (consumer_name, message_id, payload_hash) ON CONFLICT DO NOTHING`;
  - se já existe com o mesmo hash → **duplicata**: a mensagem é apagada sem nenhum efeito;
  - se já existe com hash diferente → `MESSAGE_ID_CONFLICT` (DLQ);
  - ao concluir, grava `transaction_id` e `completed_at`.
  - O hash da inbox é `sha256(payloadHash + ":" + idempotencyKey)`: compara o conteúdo de negócio e a chave, e não os bytes do corpo. Um reenvio com a mesma informação e formatação diferente continua sendo a mesma mensagem.
- **Envelope:** JSON estrito (campos desconhecidos recusados), um único objeto, `messageId`, `type = WagerTransactionRequested`, `occurredAt` e `data` obrigatórios.
  - `correlationId` e `causationId` dos eventos = `messageId`.
  - `providerId` precisa estar em `KNOWN_PROVIDERS` (D-021: sem token no SQS, a validação de domínio continua no consumidor).
- **Classificação do resultado:**

  | Resultado | Ação |
  | --- | --- |
  | `PROCESSED`, `REJECTED` (negócio), `PENDING_REFERENCE`, replay ou duplicata | **`DeleteMessage` depois do commit** |
  | Envelope inválido (`INVALID_MESSAGE`), provedor desconhecido (`UNKNOWN_PROVIDER`), erros de validação (`INVALID_MONEY`, `UNSUPPORTED_KIND`, `WALLET_NOT_FOUND`, ...) e conflitos (`IDEMPOTENCY_KEY_CONFLICT`, `EXTERNAL_TRANSACTION_CONFLICT`, `MESSAGE_ID_CONFLICT`) | **direto para a DLQ** e `DeleteMessage` |
  | Transitório (`ErrUnavailable`, conflito de concorrência esgotado, timeout) ou erro inesperado | sem apagar: `ChangeMessageVisibility(min(base × 2^(receiveCount−1), máximo))`; depois de `maxReceiveCount = 5`, o **redrive** do SQS move para a DLQ |

- **Erro permanente direto para a DLQ:**
  - Opções: (a) direto, com motivo × (b) pelo redrive.
  - Decisão: **(a)**. A mensagem vai para a DLQ na hora, com os atributos `failureReason` e `sourceMessageId` e o corpo original, e **não segura o grupo FIFO da carteira** com 5 reprocessamentos inúteis.
  - `MessageDeduplicationId` na DLQ = o ID SQS original.
  - Publicar na DLQ e apagar não são atômicos: uma queda entre os dois pode duplicar a mensagem na DLQ, o que é inofensivo. Se a publicação falhar, a mensagem não é apagada e volta a ser recebida.
  - **Falhas transitórias continuam indo pelo redrive**, porque podem se resolver sozinhas.
- **Limites e prazos** (configuráveis e validados no boot):
  - `SQS_WAIT_TIME` 10s (long polling; era 20s, ver D-032);
  - `SQS_VISIBILITY_TIMEOUT` 30s;
  - `SQS_HANDLER_TIMEOUT` 20s, que precisa ser menor que a visibilidade para não processar uma mensagem que já ficou visível para outro consumidor;
  - backoff de retry com base de 2s e máximo de 5m;
  - `maxReceiveCount` 5 (redrive da fila, `SQS_MAX_RECEIVE_COUNT`);
  - `SQS_WORKERS` 2 goroutines por instância, cada uma recebendo até 10 mensagens;
  - `ENABLE_CONSUMER` permite desligar o consumidor.
- **Ordem e paralelismo:** as mensagens de um lote são processadas **em sequência** pela goroutine que as recebeu, o que preserva a ordem FIFO de um grupo.
  - Contrato para os produtores: `MessageGroupId = walletId` (ordem por carteira, carteiras em paralelo) e `MessageDeduplicationId = messageId` (deduplicação do SQS por 5 minutos, **só uma otimização**: a garantia vem da inbox e dos índices únicos).
- **Shutdown (README §10):** o consumidor para de iniciar novas buscas, e a busca em andamento termina e tem o resultado liberado (comportamento revisto na **D-032**). A mensagem em andamento termina com `context.WithoutCancel` + `SQS_HANDLER_TIMEOUT` (D-007), e as mensagens do lote ainda não iniciadas são **liberadas** com `ChangeMessageVisibility(0)`, para reentrega imediata.
  - Verificado no compose: `docker compose stop` levou 0,47s com a versão original; depois da D-032, 2,8s, e a ordem foi consumidor → worker de pendências → HTTP → pool.
- **Testes:**
  - unitários: envelope do README válido e 8 envelopes inválidos; classificação dos erros; backoff de retry;
  - integração (`TestSQSConsumer`, LocalStack e Postgres reais):
    - mensagem processada com inbox concluída e `correlationId = messageId`, e fila vazia;
    - **a mesma mensagem entregue 2×** (deduplicação do SQS contornada de propósito, para exercitar a inbox) → um único débito;
    - `messageId` com outro conteúdo → DLQ `MESSAGE_ID_CONFLICT`;
    - 6 tipos de mensagem inválida → DLQ com o motivo e o corpo original, nada persistido;
    - rejeição de negócio e pendência → apagadas, sem DLQ;
    - **a mesma operação por HTTP e por SQS** → um único efeito, e a mensagem vira replay e é apagada;
    - falha transitória (banco inacessível) → a mensagem fica e é processada depois de o banco "voltar" (outro consumidor);
    - transitório persistente → depois de 5 recebimentos, o **redrive** leva a mensagem para a DLQ, sem efeito financeiro;
  - compose: uma mensagem enviada com `awslocal` para a fila foi processada pela aplicação rodando (saldo 100.00 → 75.00).
- **Pendente para o Dia 3:** teste automatizado da liberação de mensagens no shutdown e de "processo morto entre o commit e o `DeleteMessage`" (cenário 5 do README §13), com injeção de falhas.

## D-029 — Publicação da outbox e contrato dos eventos de saída (etapa 2.7)

- **Contexto:** o README (§11) pede um worker separado que publique a outbox, suporte vários publishers, disputa por registros, backoff e recuperação de trabalho abandonado, preserve o `eventId` nas republicações e provisione o destino dos eventos, com contrato de roteamento e consumo documentado.
- **Ciclo de um evento:**
  1. **Gravado** na outbox no mesmo commit da operação (snapshot JSON imutável, D-014/D-017). Por construção, a publicação sempre acontece **depois** do commit (README §5.4).
  2. **Reservado** (`ClaimBatch`): `UPDATE ... SET locked_by = <instância/token>, locked_until = agora + OUTBOX_LEASE, attempts = attempts + 1` sobre um `SELECT ... FOR UPDATE SKIP LOCKED`. A reserva é feita fora de uma transação longa: o lock de linha dura só o tempo do `UPDATE`, e quem protege a posse durante a publicação é o **lease**.
  3. **Publicado** fora de qualquer transação SQL: `SendMessage` com timeout `OUTBOX_PUBLISH_TIMEOUT`.
  4. **Confirmado:** `published_at = agora`, mas só se `locked_by` ainda for o token desta reserva e `published_at IS NULL`.
  5. **Falha:** `next_attempt_at = agora + min(base × 2^(attempts−1), máximo)`, `last_error` e a reserva liberada. **Não há limite de tentativas**: um evento confirmado no banco nunca é descartado (README §3), e o atraso é exposto por métrica (etapa 2.9).
- **Recuperação de trabalho abandonado:**
  - queda **entre o commit e a publicação**: o evento continua `published_at IS NULL` e qualquer publisher o pega;
  - queda **entre a publicação e a confirmação**: o `locked_until` expira e outra instância republica **o mesmo payload com o mesmo `eventId`**. A entrega é **at-least-once**, e a deduplicação FIFO do SQS (`MessageDeduplicationId = eventId`, 5 min) costuma absorver a cópia.
  - Uma reserva **ativa** nunca é tomada por outra instância (testado).
- **Ordem por carteira:**
  - Opções: (a) ordem garantida × (b) melhor esforço.
  - Decisão: **(a)**. A reserva só considera o **evento mais antigo ainda não publicado de cada carteira** (`DISTINCT ON (aggregate_id) ... ORDER BY occurred_at, event_id`, apoiado pelo índice parcial `outbox_unpublished_by_aggregate_idx`, migration `000007`). O evento seguinte da mesma carteira só é elegível depois que o anterior foi publicado.
  - Com o grupo FIFO por carteira, os consumidores recebem os eventos de cada carteira **em ordem estrita**, inclusive com vários publishers concorrentes (testado com 3 publishers e 6 goroutines: versões 1..7 em sequência).
  - **Custo aceito:** uma carteira muito movimentada publica um evento por ciclo, e um evento com falha persistente retém os seguintes **da mesma carteira** até sair. As outras carteiras não são afetadas.
  - Dentro de uma mesma operação, a ordem é `WagerTransactionProcessed` → `WalletBalanceChanged`: o `occurred_at` é igual e o desempate é pelo `event_id` (UUIDv7, monotônico).
- **Shutdown:** o contexto cancelado interrompe o lote; os eventos reservados ainda não publicados são **liberados** (`locked_by = NULL`) para outra instância. Se a liberação falhar, o lease expira de qualquer forma.
- **Configuração:** `ENABLE_OUTBOX_PUBLISHER`, `OUTBOX_WORKERS` (1), `OUTBOX_BATCH_SIZE` (50), `OUTBOX_LEASE` (30s, precisa ser maior que `OUTBOX_PUBLISH_TIMEOUT` = 10s), `OUTBOX_POLL_INTERVAL` (500ms), `OUTBOX_RETRY_BASE_DELAY` (1s) e `OUTBOX_RETRY_MAX_DELAY` (5m).
- **Contrato de saída (roteamento e consumo):**

  | Item | Valor |
  | --- | --- |
  | Destino | fila SQS FIFO `wallet-events.fifo` (DLQ `wallet-events-dlq.fifo` para os consumidores, `maxReceiveCount` 5) |
  | Corpo | envelope JSON do evento: `eventId`, `eventType`, `aggregateId` (= `walletId`), `correlationId`, `causationId?`, `occurredAt` (UTC, RFC 3339), `version`, `data` tipado, com dinheiro em string decimal |
  | `MessageGroupId` | `walletId`: ordem estrita por carteira, carteiras em paralelo |
  | `MessageDeduplicationId` | `eventId` |
  | Atributos | `eventType` (String) e `eventVersion` (Number), para filtrar ou rotear sem abrir o corpo |
  | Tipos | `WagerTransactionProcessed`, `WagerTransactionRejected`, `WalletBalanceChanged`, `WagerTransactionPendingReference` (todos `version` 1) |
  | Garantia | at-least-once e em ordem por carteira. O consumidor **deve deduplicar por `eventId`**; `WalletBalanceChanged.data.walletVersion` permite detectar lacunas ou reprocessamentos |

- **Testes** (`TestOutboxPublisher`, LocalStack e Postgres reais, repetido 3×):
  - publicação depois do commit, com grupo = carteira, deduplicação = `eventId`, atributo `eventType` e marcação de `published_at`;
  - **ordem por carteira com 3 publishers concorrentes**;
  - **3 publishers e 9 goroutines → cada evento publicado exatamente uma vez** (`attempts = 1` em todos);
  - falha do broker → `attempts`, `last_error` e `next_attempt_at` futuro, retenção dos eventos seguintes da carteira, publicação depois da recuperação;
  - **queda entre a publicação e a confirmação**: outra instância respeita o lease ativo, republica depois da expiração com o **mesmo `eventId`** e confirma (`attempts = 2`);
  - **queda entre o commit e a publicação**: eventos pendentes assumidos por outro publisher.
  - Compose: depois de toda a collection do Postman, **83/83 eventos publicados**, visíveis na `wallet-events.fifo`.

## D-030 — Reconciliação (etapa 2.8)

- **Contexto:** o README (§9) pede reconstruir o saldo a partir do ledger, incluindo a abertura, e compará-lo "em uma visão consistente dos dados", com `difference` = armazenado − reconstruído. Pede também reportar divergências na resposta, nos logs e em métrica, sem alterar o saldo.
- **Rota:** `POST /wallets/{walletId}/reconciliation`, restrita a `wallet-admin` (provedor → 403).
- **Visão consistente:** `store.ReadSnapshot` abre uma transação **`REPEATABLE READ, READ ONLY`**. A leitura da carteira e a soma do ledger usam **o mesmo snapshot**: uma operação confirmada entre as duas leituras não aparece em nenhuma delas. Por ser `READ ONLY`, é impossível alterar o saldo por esse caminho.
- **Cálculo:**
  - `SUM(CASE direction WHEN 'CREDIT' THEN amount ELSE -amount END)` em `NUMERIC`, para não estourar durante a soma, convertido para `BIGINT` só se couber, senão `ErrSumOutOfRange`;
  - `count(*)` dá o `checkedEntries`;
  - `difference = stored.Sub(calculated)`, com a moeda da carteira e overflow checado.
- **Resposta:** `{walletId, storedBalance, calculatedBalance, difference, consistent, checkedEntries}`, com os valores em `Money` (string decimal + moeda). `difference` pode ser negativa.
- **Divergência:** log `WARN "reconciliation mismatch"` com `walletId`, os três valores e `checkedEntries`, mais a métrica `reconciliation_mismatches_total`. O saldo **não** é corrigido: a correção, se necessária, deve ser feita com lançamentos novos (ledger append-only, README §5.5).
- **Testes** (`TestWagerAPI`, aplicação completa):
  - exemplo do README: abertura de 1000.00 + BET de 25.00 → 975.00 / 975.00 / 0.00, `consistent: true`, `checkedEntries: 2`;
  - carteira com saldo zero → 0 lançamentos, consistente;
  - **saldo adulterado direto no banco** (+0.01) → `consistent: false`, `difference: 0.01`, métrica +1, log WARN, e a carteira **não** foi alterada (saldo e versão iguais);
  - 404 para carteira inexistente, 403 para provedor.
  - Postman: reconciliação da carteira do roteiro (150.00, 4 lançamentos, consistente).

## D-031 — Observabilidade: métricas, logs e health (etapa 2.9)

- **Métricas centralizadas** em `platform/metrics.Metrics`, um só lugar com todas as séries, injetado via Fx em store, casos de uso, consumidor, publisher, reconciliação e HTTP. O registro é próprio, não o global (D-008).
- **Cobertura do README (§12):**

  | Sinal exigido | Métrica |
  | --- | --- |
  | resultados por status | `wager_transactions_total{channel, kind, status}`, com channel = `http`, `sqs` ou `worker` |
  | duplicatas | `wager_idempotent_replays_total{channel}`; `sqs_messages_total{result="duplicate"}` (inbox) |
  | retries | `sqs_messages_total{result="retry"}`; `pending_reference_attempts_total{outcome="rescheduled|resolved|expired"}`; `outbox_publish_total{result="failed"}` |
  | DLQ | `sqs_messages_total{result="dead_letter"}` e `sqs_dead_letters_total{reason}`, com o código do motivo (cardinalidade baixa) |
  | conflitos de concorrência | `wallet_concurrency_conflicts_total{reason="version|serialization|deadlock"}` |
  | atraso da outbox | `outbox_pending_events`, `outbox_oldest_pending_age_seconds` (+ `pending_references_waiting`) |
  | latência de processamento | `wager_processing_duration_seconds{channel}` (histograma, inclui a transação SQL) e `http_request_duration_seconds{route, method, status}` |
  | divergências de reconciliação | `reconciliation_mismatches_total` |

- **Atraso da outbox lido do banco na coleta:**
  - Opções: (a) consulta a cada scrape × (b) gauges atualizados pelo publisher.
  - Decisão: **(a)**. Um coletor Prometheus executa, a cada `GET /metrics`, um `SELECT` leve (índice parcial de não publicados) com timeout de 2s.
  - O valor é exato e igual em qualquer instância, inclusive nas que não publicam e **mesmo com o publisher travado**, que é quando o atraso importa. Se a consulta falhar, as séries são omitidas naquela coleta e um WARN é logado.
- **Instrumentação por canal:** `WagerCommand.Channel` identifica a origem. O `Wagers` registra resultado, replay e latência num único ponto, e o worker de pendências registra as resoluções e expirações como canal `worker`.
- **Logs** (`slog` JSON, D-008):
  - **log de acesso** por requisição: `method`, `route` (o padrão da rota, como `/wallets/{walletId}`, nunca IDs, query ou corpo), `status`, `durationMs`, `correlationId`. Health e métricas vão em DEBUG, para não poluir com as sondas;
  - eventos de negócio já tinham `transactionId`, `walletId`, `providerId`, `messageId` e `correlationId` (D-026, D-028, D-027);
  - **não são registrados:** tokens, header `Authorization`, corpos de requisição nem payloads financeiros completos (README §12).
- **Health:** `/health/live` (processo) e `/health/ready` (Postgres + SQS, D-008). No início do shutdown, o readiness passa a responder **503 `draining`** antes de o servidor parar de aceitar conexões, o que tira a instância do balanceamento.
- **Tracing (OpenTelemetry) e dashboards:** opcionais no README, não implementados por prioridade. Ficam listados como trabalho futuro.
- **Testes:**
  - `TestWagerAPI` confere, depois dos fluxos reais, que **todas** as séries exigidas aparecem em `/metrics`, inclusive as do canal `worker`;
  - `TestSQSConsumer` confere processadas, duplicatas, DLQ por motivo e replays via SQS;
  - unitário do readiness em modo `draining`;
  - compose: amostra de `/metrics` e log de acesso verificados depois da collection do Postman.

## D-032 — Desligamento do consumidor sem mensagens presas (etapa 3.1)

- **Problema encontrado (P-016):** no shutdown, o long polling era cancelado **só do lado do cliente**. A requisição continuava aberta no broker, recebia mensagens que chegassem nesse intervalo e ninguém as liberava: elas ficavam invisíveis até o visibility timeout (30s). O teste de reinicialização mediu **30,78s** até a nova instância consumir uma mensagem enviada durante o desligamento. Não havia perda nem duplicidade, só atraso. A AWS real tem o mesmo comportamento.
- **Opções:**
  - (a) manter o cancelamento imediato e documentar o atraso;
  - (b) deixar a busca em andamento terminar e liberar o que ela trouxer.
- **Decisão: (b)**, que atende literalmente o README §10 ("libere sua visibilidade para reentrega segura"):
  - o `ReceiveMessage` usa `context.WithoutCancel(ctx)` com timeout de `SQS_WAIT_TIME + 5s`: o shutdown não aborta a busca em andamento, mas o Runner não inicia outra;
  - toda mensagem recebida é **processada** (se o shutdown ainda não começou) ou **liberada** com `ChangeMessageVisibility(0)` (se já começou);
  - `SQS_WAIT_TIME` padrão caiu de **20s para 10s**, para limitar o tempo de desligamento. O custo é um pouco mais de buscas vazias ao SQS;
  - o desligamento leva no máximo `SQS_WAIT_TIME`, bem dentro do `fx.StopTimeout` de 30s. Medido no compose: 2,8s.
- **Bug corrigido junto:** na versão antiga, se a busca retornasse mensagens no instante em que o shutdown começava, o código descartava o lote (`if ctx.Err() != nil { return }`) **sem processar nem liberar**. Agora isso não acontece.
- **Prova:** `TestRestart_*` envia a mensagem **durante** o desligamento da instância A; a instância B a consome em **0,11s** (antes: 30,78s). O teste passou 2 vezes seguidas.

## D-033 — Testes de integração: lacunas cobertas na etapa 3.1

- **Infraestrutura dos testes:**
  - Opções: (a) um conjunto de containers por teste × (b) containers compartilhados por pacote (`TestMain`).
  - Decisão: **(a)**: isolamento total, sem refatorar. Custo: a suíte leva cerca de 5 min. A otimização fica como melhoria futura.
- **Lacunas encontradas na comparação com o README §13, e os testes novos:**
  1. **Atomicidade financeira com falha no meio** (`TestProcessWager/failure_after_ledger_and_balance_were_written_rolls_everything_back`):
     - o `INSERT` da outbox é revogado do `wallet_app`, então o `ProcessWager` (e o `ProcessMessage`, com a inbox) falha **depois** de gravar a transação, o lançamento e o novo saldo;
     - resultado: nada persistido (transação, débito, inbox, saldo e versão intactos);
     - depois de restaurar a permissão, a mesma operação é processada pela primeira vez, sem replay.
  2. **Recuperação após reinicialização** (`TestRestart_PreservesIdempotencyPendingWorkAndConsistency`, README §13.8):
     - a instância A cria uma BET e uma pendência e é desligada; durante o desligamento chega uma mensagem SQS; a instância B sobe sobre o mesmo banco e as mesmas filas;
     - resultado: o replay devolve o resultado e o saldo originais; a mensagem é consumida por B; a pendência criada por A é resolvida pelo worker de B; a reconciliação fica consistente (60.00, 5 lançamentos); a outbox é esvaziada; a invariante do ledger vale.
  3. **A mesma operação, ao mesmo tempo, pelo HTTP e pelo SQS**, com o consumidor real rodando na aplicação (`TestWagerAPI/the_same_operation_at_the_same_time_over_HTTP_and_SQS_has_a_single_effect`, 5 rodadas): uma única transação, a inbox apontando para ela e um único débito.
  4. **Invariante global** (`assertAllWalletsReconcile`): para **todas** as carteiras do banco, saldo armazenado = créditos − débitos do ledger. Roda ao final de `TestWagerAPI` e de `TestRestart`, e acusou corretamente a carteira adulterada de propósito pelo teste de reconciliação, que agora desfaz a adulteração ao terminar.
- **Harness:** dividido em `startInfra` (containers + variáveis) e `startInstance` (uma instância Fx da aplicação, com `INSTANCE_ID` próprio), o que permite várias instâncias e reinícios sobre a mesma infraestrutura.

## D-034 — Testes de autenticação e autorização (etapa 3.2)

Todos rodam contra o **Keycloak real**, com tokens `client_credentials`, em dois níveis:
- **Middleware** (`TestAuth_RealKeycloak`):
  - 401 para cabeçalho ausente ou malformado, assinatura ou payload adulterados, `aud` errado, issuer de outro realm e token expirado;
  - 403 para role errada ou ausente.
- **Aplicação completa** (`TestWalletAPI`, `TestWagerAPI`):
  - provider em `POST /wallets`, `GET /wallets/:id`, ledger e reconciliação → 403 sem criar linhas;
  - `provider-b` se passando pelo `provider-a` → 403;
  - leitura cruzada por ID interno → 404, e pela rota externa → 403;
  - admin lê qualquer transação → 200.

Lacunas fechadas nesta etapa:
1. **Sem efeito financeiro nas negações:** `financialState` compara transações, lançamentos, eventos da outbox e saldo da carteira antes e depois de todas as tentativas negadas (sem token, token adulterado, outra audiência, admin enviando operação, provedor se passando por outro). Antes, só as transações e o saldo eram conferidos.
2. **401 com tokens reais na aplicação completa**, não só no middleware isolado.
3. **Chave do `provider-a` reutilizada pelo `provider-b` no próprio escopo** (mesmo `externalTransactionId` e mesmo `Idempotency-Key`, mas `providerId=provider-b`): cria uma transação **nova** do `provider-b` (201, outro `transactionId`, sem `idempotentReplay`). Isso confirma que a idempotência é escopada pelo provedor do token e não vaza o resultado de outro provedor.

## D-035 — Três instâncias independentes e suíte multi-instância (etapa 3.3)

- **Por que containers, e não três instâncias Fx no mesmo processo de teste:** o README (linha 200) exige "pelo menos três **processos** independentes, cada um com suas próprias conexões e memória". Três apps Fx dentro do `go test` compartilhariam o processo, então não bastam como prova.
- **Ambiente** (`docker-compose.e2e.yml`, override do compose principal):
  - `app2` e `app3` herdam o serviço `app` por `extends`. Dentro de um override, o `extends` precisa apontar `file: docker-compose.yml`, senão o Compose não enxerga o serviço base;
  - cada uma tem `INSTANCE_ID` próprio e porta própria (8080, 8082 e 8083; a 8081 é do Keycloak);
  - todas rodam HTTP, consumidor SQS, worker de pendências e publisher da outbox, com pools de conexão separados.
- **Comandos:**
  - `make e2e-up` sobe tudo com `--wait`;
  - `make test-e2e` roda `go test -tags e2e ./test/e2e/...` contra as instâncias já em execução;
  - `make e2e` faz os dois;
  - `make e2e-down` derruba e apaga os volumes.
- **Configuração:** a suíte lê as URLs por variável de ambiente (`E2E_APP_URLS`, `E2E_KEYCLOAK_URL`, `E2E_DATABASE_URL`, `E2E_SQS_URL`), com os padrões do compose. Usa tokens reais do Keycloak, o Postgres para as asserções e o LocalStack para enviar mensagens.
- **Cenários** (`test/e2e/multi_instance_test.go`). As requisições são distribuídas em round-robin entre as instâncias e disparadas juntas, atrás de uma barreira (`concurrently`):
  1. **A mesma BET 50×**: exatamente 1× 201 e 49× 200, todas com o mesmo `transactionId` e o saldo 90.00; um único débito.
  2. **80 + 80 sobre 100 em instâncias diferentes**, 10 rodadas: uma 201 e uma 422 `INSUFFICIENT_FUNDS`, um débito, saldo 20.00. O reenvio de cada uma por outra instância devolve a mesma transação, com o mesmo status e saldo, marcada como `idempotentReplay`.
  3. **30 carteiras × 20 operações** (BET 10.00 e WIN 5.00 alternadas, 600 requisições com até 30 simultâneas): todas 201; cada carteira termina com 950.00, versão 21 e 21 lançamentos. O reenvio das 600 devolve 200, sem mudar nada.
  4. **As mesmas operações pelo HTTP (3 instâncias) e pelo SQS (3 consumidores)**, 6 carteiras × 10 operações, cada uma enviada pelos dois canais ao mesmo tempo:
     - uma única transação e um único débito por operação;
     - a inbox registra todas as mensagens;
     - o reenvio HTTP devolve 200;
     - o teste imprime quantas mensagens cada instância consumiu (numa rodada: 20, 22 e 18).
  5. **Pendências retomadas por qualquer instância**: 9 REFUNDs chegam antes das BETs, que entram por outras instâncias; todos terminam `PROCESSED` e as carteiras voltam a 100.00.
- **Convergência ao fim de cada cenário** (`assertConverged`): a outbox é totalmente publicada, e o saldo de **todas** as carteiras do banco é igual a créditos − débitos do ledger.
- **Resultado:** a suíte passa em cerca de 6,5s, com 3 rodadas seguidas verdes.
  - Nos logs das três instâncias: nenhum ERROR ou WARN.
  - `wallet_concurrency_conflicts_total` ficou zerada: o lock pessimista `FOR NO KEY UPDATE` serializa as escritas da mesma carteira entre processos sem gerar conflito de versão nem deadlock.
  - O trabalho assíncrono se distribuiu entre as instâncias. Pendências resolvidas: 24, 26 e 22. Mensagens SQS: 20, 22 e 18.
- **Fora do escopo:** o teste de carga (k6/vegeta), opcional no plano, não foi feito.

## Problemas encontrados

### P-001 — LocalStack recente exige licença (etapa 1.1)
A `localstack/localstack:latest` (`2026.09.0`) encerra na inicialização sem token. Resolvido fixando a `4.14.0` (D-001).

### P-002 — `migrate up` falha com a pasta de migrations vazia (etapa 1.1)
O `golang-migrate` retorna `error: first .: file does not exist` quando não há arquivos. Resolvido adiantando a migration `000001_create_wallets` (schema já aprovado no plano, etapa 1.4) em vez de criar uma migration vazia.

### P-003 — Imagem do Keycloak sem curl/wget para o healthcheck (etapa 1.1)
O healthcheck usa `/dev/tcp` do bash contra o endpoint de health da porta de gestão (9000), com `KC_HEALTH_ENABLED=true`.

### P-004 — Issuer do Keycloak depende de como ele é acessado (resolvido na etapa 1.6)
Acessado pelo host, o `issuer` era `http://localhost:8081/realms/wagering`; de dentro da rede do compose, seria `http://keycloak:8080/...`. Tokens obtidos pelo host não validariam na aplicação.
- **Resolução (D-020):** `KC_HOSTNAME` fixa o `iss`; a aplicação valida `OIDC_ISSUER` e busca as chaves em `OIDC_JWKS_URL`, pela rede interna.

### P-005 — Testes do Runner com condição de corrida no próprio teste (etapa 1.2)
Dois testes chamavam `Stop` logo depois de `Start`. A goroutine às vezes ainda não tinha entrado em `RunOnce`, via o contexto já cancelado e saía sem executá-lo, o que é o comportamento correto do Runner. Os testes passaram a esperar um sinal `started` antes do `Stop`. Rodados com `-race -count=3` sem falhas.

### P-006 — Mensagem de erro de parse da config cita o campo, não a variável (etapa 1.2)
Para `LOG_LEVEL=LOUD`, o `caarlos0/env` retorna `parse error on field "LogLevel"`. A mensagem continua clara e foi aceita sem código extra de tradução.

### P-007 — `CHECK (col <> '')` aceita `NULL` (etapa 1.4)
No SQL, `NULL <> ''` resulta em `NULL`, e um `CHECK` só rejeita quando o resultado é `false`. A primeira versão de `wt_external_has_required_data_ck` e de `wt_reversal_has_reference_ck` aceitava transação externa sem `provider_id` e REFUND sem referência. O caso do REFUND estava mascarado nos testes porque outra constraint falhava antes.
- **Correção:** `COALESCE(col, '') <> ''`.
- **Prevenção:** os testes passaram a verificar o **nome** da constraint violada, não só o código do erro, e cobrem `NULL` e `''` para cada campo obrigatório.

### P-008 — Alvo `make token-%` terminava com sucesso para client inexistente (etapa 1.6)
No pipe `curl | sed`, o status de saída é o do `sed`, então uma falha do `curl` não aparecia. O alvo passou a guardar a resposta numa variável e só segue com `&&`; agora retorna erro (`Error 22`).

### P-009 — Teste do Fx quebrou com as variáveis de OIDC obrigatórias (etapa 1.6)
Regressão esperada, pega pela verificação de fim de etapa. O teste agora sobe também o Keycloak real e define `OIDC_ISSUER`/`OIDC_JWKS_URL`. O tempo da suíte de integração subiu para cerca de 80s; se crescer demais, considerar compartilhar os containers por pacote.

### P-010 — Dependência ausente no grafo do Fx (etapa 1.7)
O store pedia `prometheus.Registerer`, mas o módulo de métricas só fornecia `*prometheus.Registry`. O teste unitário `TestOptions_DependencyGraphIsComplete` (`fx.ValidateApp`) pegou o problema antes de qualquer execução. Correção: fornecer também o `Registerer`.

### P-011 — `count(DISTINCT xmin)` não funciona em Postgres (etapa 1.7, só no teste)
O tipo `xid` não tem operador de ordenação, e o teste ignorava o erro do `Scan`, o que produziu um falso negativo na verificação de atomicidade. Correção: `xmin::text` e checagem do erro.

### P-012 — Tempo da suíte de integração (observação, etapa 1.7)
A suíte leva cerca de 110s, com três testes subindo Postgres, LocalStack e Keycloak. Ainda é aceitável. Se crescer no Dia 2/3, avaliar compartilhar a stack entre os testes de um pacote.

### P-013 — Docker indisponível no WSL durante a verificação (etapa 2.1, ambiente)
Depois que a suíte de integração passou (190s), o Docker Desktop deixou de responder no WSL ("The command 'docker' could not be found in this WSL 2 distro"), o que impediu subir o compose e rodar a collection do Postman. É um problema do ambiente local, não do projeto. Depois que o Docker Desktop foi reaberto, a verificação foi refeita por completo: unitários com `-race`, integração (108s), compose do zero com `/health/ready` ok e a collection do Postman (47 requisições, 128 asserções). Tudo passou.

### P-014 — Deadlock entre a FK de `wager_transactions` e o `SELECT ... FOR UPDATE` da carteira (etapa 2.2)
- **Sintoma:** com `FOR UPDATE`, o teste "30 BETs distintas na mesma carteira" falhou com `deadlock detected (40P01)` e `canceling statement due to statement timeout (57014)`, e o bloco de concorrência levou 57s. Nenhum valor foi corrompido, porque o banco aborta uma das transações, mas operações legítimas falhavam sob carga. O teste 80 + 80 passou só por sorte de timing.
- **Causa:** o `INSERT` em `wager_transactions` tem FK para `wallets`, e o Postgres a valida pegando um lock **`FOR KEY SHARE`** na linha da carteira. `FOR UPDATE` **conflita** com `FOR KEY SHARE`. Então:
  1. A insere (KEY SHARE);
  2. B insere (KEY SHARE, compatível);
  3. A pede `FOR UPDATE` e espera o KEY SHARE de B;
  4. B pede `FOR UPDATE` e espera o KEY SHARE de A → ciclo.
- **Correção:** `SELECT ... FOR NO KEY UPDATE`, o mesmo lock que o Postgres usa num `UPDATE` que não altera colunas de chave. Ele continua exclusivo entre escritores da mesma carteira (serializa as operações), mas é **compatível com `FOR KEY SHARE`**, então os inserts com FK não entram no ciclo.
- **Resultado:** o mesmo teste passa sem nenhum deadlock (verificado pela métrica `wallet_concurrency_conflicts_total{reason="deadlock"} = 0`), e o bloco de concorrência caiu de 57s para cerca de 3s.
- **Alternativa considerada:** travar a carteira **antes** de inserir a transação. Também evita o ciclo, mas faria até os replays esperarem pelo lock da carteira.

### P-015 — Falso positivo de "transação persistida" no teste da API (etapa 2.4, só no teste)
Os casos de entrada inválida e de autorização contavam **todas** as transações da carteira e acharam 1 a mais. Inspecionando as linhas, era a `OPENING` da abertura, que existe por design. A contagem passou a filtrar `origin = 'EXTERNAL'`. Nenhuma alteração no código da aplicação.

### P-016 — Mensagem presa numa busca de long polling abandonada no shutdown (etapa 3.1)
Detectado pelo teste de reinicialização: uma mensagem enviada durante o desligamento levava cerca de 30s (o visibility timeout) para ser consumida pela nova instância, porque foi entregue a uma requisição de long polling cancelada só do lado do cliente. Resolvido pela D-032, que também corrigiu o descarte silencioso de um lote recebido no instante do shutdown.

### P-017 — Instabilidade do Docker Desktop durante os testes (etapa 3.1, ambiente)
Durante a etapa, o Docker Desktop reiniciou sozinho uma vez. O compose caiu, e um container do testcontainers sumiu durante a subida (`No such container`). Numa rodada seguinte, o socket do Docker deu timeout (`context deadline exceeded`) ao subir o Postgres. Nenhum dos casos tem relação com o código; rodadas seguintes passaram completas (suíte de integração em 5 min). Com cerca de 7,7 GB de memória na VM do Docker e um Keycloak por teste, a máquina fica perto do limite. Se as falhas voltarem, a mitigação é a opção (b) da D-033.

### P-018 — Ajustes na primeira execução da suíte multi-instância (etapa 3.3, só no teste)
- Duas asserções do teste estavam erradas em relação ao contrato da API, e a aplicação estava certa nos dois casos:
  - a rejeição de negócio traz o código em `failureCode`, não em `error.code`;
  - o replay de uma operação processada responde **200** com `idempotentReplay: true`, não 201.
- O Postman, rodado logo depois da suíte e2e, falhou na pasta 09. Causa: a suíte deixou cerca de 10 mil eventos na `wallet-events.fifo`, que não tem consumidor, e a coleção lê só um lote. Depois de limpar a fila, as 275 asserções passaram. O procedimento ficou documentado no `ROTEIRO.md`.
- Uma execução da suíte de integração falhou durante esta etapa, enquanto o Postman rodava ao mesmo tempo contra o ambiente de três instâncias. A saída ficou truncada e não mostra qual teste falhou. As duas execuções seguintes, ainda com o ambiente de três instâncias no ar, passaram completas (290s e 297s). A falha fica registrada como **não reproduzida**; se voltar a aparecer, a saída completa deve ser guardada para investigação.
