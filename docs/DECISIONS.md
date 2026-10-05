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
