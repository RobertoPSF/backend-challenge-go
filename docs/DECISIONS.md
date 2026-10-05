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

## Problemas encontrados

### P-001 — LocalStack recente exige licença (etapa 1.1)
A `localstack/localstack:latest` (`2026.09.0`) encerra na inicialização sem token. Resolvido fixando a `4.14.0` (D-001).

### P-002 — `migrate up` falha com a pasta de migrations vazia (etapa 1.1)
O `golang-migrate` retorna `error: first .: file does not exist` quando não há arquivos. Resolvido adiantando a migration `000001_create_wallets` (schema já aprovado no plano, etapa 1.4) em vez de criar uma migration vazia.

### P-003 — Imagem do Keycloak sem curl/wget para o healthcheck (etapa 1.1)
O healthcheck usa `/dev/tcp` do bash contra o endpoint de health da porta de gestão (9000), com `KC_HEALTH_ENABLED=true`.

### P-004 — Issuer do Keycloak depende de como ele é acessado (pendente, etapa 1.6)
Acessado pelo host, o `issuer` é `http://localhost:8081/realms/wagering`. De dentro da rede do compose, seria `http://keycloak:8080/...`. Tokens obtidos pelo host não validariam na aplicação. Será resolvido na etapa 1.6, com trade-offs apresentados.
