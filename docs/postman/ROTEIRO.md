# Roteiro de testes via Postman

Collection: [`wallet-api.postman_collection.json`](wallet-api.postman_collection.json).

São **97 requisições em 11 pastas** e **275 asserções automáticas**, cobrindo o sistema inteiro: tokens, carteiras, validação, segurança, operações de aposta, **concorrência**, **worker de referências pendentes**, **entrada pela fila SQS** (inbox, duplicata e DLQ), **eventos publicados pela outbox** e **métricas**.

As pastas 06 a 10 também disparam requisições a partir dos scripts (paralelas e de leitura de filas), então o Runner mostra mais requisições executadas do que as 97 listadas.

## 1. Preparar o ambiente

```sh
docker compose up --build -d --wait
curl -s localhost:8080/health/ready
```

O retorno esperado é `{"checks":{"postgres":"ok","sqs":"ok"},"status":"ok"}`.

## 2. Importar e rodar

1. Postman → **Import** → selecione `wallet-api.postman_collection.json`. No Windows, use a cópia em `C:\Users\<você>\Downloads`, porque o Postman não lê arquivos direto do WSL.
2. As variáveis já vêm na collection: `baseUrl` (API, `localhost:8080`), `keycloakUrl` (`localhost:8081`) e `sqsUrl` (LocalStack, `localhost:4566`).
3. Na collection, clique em **⋯** e depois em **Run collection** → **Run**, **em ordem** e com o campo **Data** vazio. A pasta 00 obtém os tokens, e as seguintes reutilizam as carteiras que criam.
4. A execução completa leva cerca de 30s, porque algumas requisições esperam de propósito pelo worker e pelo consumidor (2 a 3s cada).

Para explorar uma pasta isolada, rode antes a **00 - Tokens**, cujos tokens valem 5 minutos.

Pela linha de comando:

```sh
docker run --rm --network host -v "$PWD/docs/postman:/etc/newman" postman/newman:6-alpine run wallet-api.postman_collection.json
```

O roteiro é **repetível**: pode ser rodado várias vezes sobre o mesmo ambiente, porque gera IDs novos a cada execução e limpa as filas que lê.

## 3. O que cada pasta demonstra

### Base

| Pasta | O que observar |
| --- | --- |
| **00 - Tokens** | Tokens reais emitidos pelo Keycloak via `client_credentials`: `provider-a`, `provider-b`, `wallet-service`, client sem roles, client com outra audiência e um token que expira em 2s. O **Console** mostra as claims de cada um. |
| **01 - Health** | `/health/live`, `/health/ready` (Postgres + SQS) e `/metrics`, todos públicos. |
| **02 - Carteiras** | Abertura com 1000.00 (201, `version: 1`, valor como string decimal), ledger com o crédito de abertura, 409 para o mesmo jogador e moeda, saldo zero sem lançamento. |
| **03 - Validação** | 20 entradas inválidas, todas 400 com o código específico (`"10"`, `"-1.00"`, `"1e3"`, `NaN`, número em vez de string, `"brl"`, `"JPY"`, campo desconhecido...). |
| **04 - Segurança** | 401 sem token, com token adulterado, de outra audiência ou expirado; 403 quando o provedor tenta rotas internas; nenhuma tentativa negada altera o saldo. |
| **05 - Operações de aposta** | BET (201) → reenvio (200 replay, mesmo saldo) → outra BET sem saldo (422 `INSUFFICIENT_FUNDS`) → conflito de chave (409) → LOSS → WIN com referência → REFUND → ROLLBACK da mesma BET (422 `ALREADY_REVERSED`) → REFUND antes da BET (202) → consultas e isolamento entre provedores → ledger, saldo final e **reconciliação**. |

### Funcionalidades novas

| Pasta | O que observar |
| --- | --- |
| **06 - Concorrência** | **Teste obrigatório do README:** carteira com 100.00 recebe **duas BETs distintas de 80.00 ao mesmo tempo** → exatamente uma 201 e uma 422 `INSUFFICIENT_FUNDS` (saldo observado 20.00), **um único débito** no ledger, saldo final 20.00. Depois, **a mesma BET 50× ao mesmo tempo** → 1× 201 e 49× 200 (`idempotentReplay`), todas apontando para a mesma transação, saldo 90.00 e versão 2. Fecha com a reconciliação consistente. O Console mostra os status recebidos. |
| **07 - Worker de pendências** | REFUND chega **antes** da BET → 202 `PENDING_REFERENCE`, com `nextAttemptAt`/`expiresAt` → a BET chega (201, saldo 70.00) → **sem nenhuma chamada extra**, o worker em segundo plano aplica o REFUND: a consulta mostra `PROCESSED`, saldo 100.00 e a referência resolvida. Uma WIN que referencia uma BET inexistente fica aguardando e não movimenta o ledger. |
| **08 - Fila SQS** | O Postman envia a operação **direto para a fila** `wager-transactions.fifo` (API SQS do LocalStack) e a aplicação consome sozinha: BET via fila → `PROCESSED`, saldo 75.00. **A mesma mensagem reentregue** → a inbox descarta, e o saldo continua 75.00. **A mesma operação pelo HTTP** → 200 replay do resultado do SQS. Mensagens inválidas (valor `1e3`, provedor desconhecido) → vão **direto para a DLQ** com o atributo `failureReason` (`INVALID_MONEY`, `UNKNOWN_PROVIDER`), sem criar transação nem mexer no saldo. |
| **09 - Eventos da outbox** | `outbox_pending_events = 0`: tudo o que foi confirmado foi publicado. Depois o Postman lê a `wallet-events.fifo` e confere os eventos da carteira do SQS: exatamente 4 (abertura + BET, cada uma com `Processed` e `BalanceChanged`), com `MessageGroupId = walletId`, atributo `eventType`, envelope completo, `eventId` único (a duplicata e o replay não publicaram nada), **versões em ordem** (v1, v2) e o evento da BET correlacionado ao `messageId` da mensagem SQS. |
| **10 - Métricas** | Confere em `/metrics` as séries exigidas pelo README: resultados por canal (`http`, `sqs`, `worker`), replays, latência, mensagens SQS por resultado, DLQ por motivo, tentativas de pendência, publicações da outbox, atraso da outbox, reconciliação e latência HTTP por rota. O Console imprime os valores. |

## 4. Testar manualmente pelo terminal

```sh
ADMIN=$(make -s token-wallet-service)
PROV=$(make -s token-provider-a)

curl -s -X POST localhost:8080/wallets \
  -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' \
  -d '{"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","initialBalance":{"amount":"1000.00","currency":"BRL"}}'
```

Para enviar uma operação pela fila, troque `<walletId>` e `<playerId>`:

```sh
docker compose exec localstack awslocal sqs send-message \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-group-id <walletId> --message-deduplication-id msg-001 \
  --message-body '{"messageId":"msg-001","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00.000Z","data":{"providerId":"provider-a","externalTransactionId":"sqs-tx-001","idempotencyKey":"provider-a:sqs-tx-001","playerId":"<playerId>","walletId":"<walletId>","roundId":"round-987","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}}'
```

## 5. Ver o efeito no banco

```sh
docker compose exec postgres psql -U postgres -d wallet
```

```sql
SELECT id, balance, version FROM wallets ORDER BY created_at DESC LIMIT 5;
SELECT kind, status, failure_code, amount, balance_after FROM wager_transactions ORDER BY created_at DESC LIMIT 10;
SELECT direction, amount, balance_before, balance_after FROM wallet_ledger_entries ORDER BY created_at DESC LIMIT 10;
SELECT consumer_name, message_id, completed_at FROM inbox_messages ORDER BY received_at DESC LIMIT 5;
SELECT event_type, attempts, published_at FROM outbox_events ORDER BY occurred_at DESC LIMIT 10;

-- O ledger é append-only, até para o superusuário:
UPDATE wallet_ledger_entries SET amount = 1;
```

Os valores estão em **centavos** (`100000` = `1000.00`). O `UPDATE` no ledger é recusado com `wallet_ledger_entries is append-only`.

**Para ver a reconciliação acusando divergência:** adultere um saldo e chame a reconciliação daquela carteira. A resposta traz `consistent: false`, a `difference` e o log WARN, e a métrica `reconciliation_mismatches_total` sobe. A reconciliação nunca corrige o saldo.

```sql
UPDATE wallets SET balance = balance + 1 WHERE id = '<walletId>';
```

## 6. Ver as filas

```sh
docker compose exec localstack awslocal sqs receive-message \
  --queue-url http://localhost:4566/000000000000/wallet-events.fifo \
  --max-number-of-messages 10 --message-attribute-names All --attribute-names MessageGroupId

docker compose exec localstack awslocal sqs receive-message \
  --queue-url http://localhost:4566/000000000000/wager-transactions-dlq.fifo \
  --max-number-of-messages 10 --message-attribute-names All
```

As pastas 08 e 09 **consomem e apagam** as mensagens que leem da DLQ e da fila de eventos, para que cada execução comece limpa.
