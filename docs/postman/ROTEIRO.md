# Roteiro de testes via Postman

Collection: [`wallet-api.postman_collection.json`](wallet-api.postman_collection.json), com 47 requisições e 128 asserções automáticas.

Cobre o que existe até o fim do Dia 1: tokens, health, abertura e consulta de carteiras, ledger, validação de entrada e segurança. Operações de aposta (BET/WIN/LOSS/REFUND/ROLLBACK), SQS, outbox publisher e reconciliação entram no Dia 2, e a collection será ampliada.

## 1. Preparar o ambiente

```sh
docker compose up --build -d --wait
curl -s localhost:8080/health/ready
```

O retorno esperado é `{"checks":{"postgres":"ok","sqs":"ok"},"status":"ok"}`.

## 2. Importar e rodar

1. Postman → **Import** → selecione `docs/postman/wallet-api.postman_collection.json`.
2. As variáveis já vêm na collection: `baseUrl = http://localhost:8080` e `keycloakUrl = http://localhost:8081`. Não é preciso criar um *environment*.
3. Clique com o botão direito na collection → **Run collection** → **Run**. As pastas dependem umas das outras e precisam rodar **em ordem**: a pasta 00 obtém os tokens e a 02 cria a carteira usada depois.
4. Para explorar uma requisição isolada, rode antes a pasta **00 - Tokens**. Os tokens valem 5 minutos.

Pela linha de comando, sem o Postman instalado:

```sh
docker run --rm --network host -v "$PWD/docs/postman:/etc/newman" postman/newman:6-alpine run wallet-api.postman_collection.json
```

## 3. O que cada pasta demonstra

| Pasta | O que observar |
| --- | --- |
| **00 - Tokens** | Tokens reais emitidos pelo Keycloak via `client_credentials`. O **Console** do Postman mostra as claims de cada token: `iss`, `aud`, `provider_id`, roles e validade. O `provider-a-short-lived` vale só 2s. Secret errado → 401 do próprio Keycloak. |
| **01 - Health** | `/health/live` (o processo está de pé), `/health/ready` (Postgres e SQS acessíveis) e `/metrics` (Prometheus). Os três são públicos. |
| **02 - Carteiras** | Abertura com 1000.00 BRL: 201, `version: 1`, valor como **string decimal**, `Location` e `X-Correlation-Id` devolvido. O ledger mostra o crédito de abertura 0.00 → 1000.00. O mesmo jogador e moeda dá **409**; outra moeda é aceita. Saldo zero não cria lançamento. |
| **03 - Validação** | 20 entradas inválidas, todas **400** com o código específico (`INVALID_MONEY`, `INVALID_CURRENCY`, `INVALID_REQUEST`): `"10"`, `"10.5"`, `"-1.00"`, `"1e3"`, `NaN`, valor acima do limite, número em vez de string, `"brl"`, `"JPY"`, campo desconhecido, JSON quebrado, `limit` e `cursor` inválidos, carteira inexistente (404). |
| **04 - Segurança** | 401 para requisição sem token, esquema `Basic`, token adulterado, outra audiência e **token expirado**. 403 quando o provedor tenta operações de carteira ou quando o client não tem roles. A última requisição confirma que **nenhuma tentativa negada alterou o saldo**. |

## 4. Testar manualmente pelo terminal

```sh
ADMIN=$(make -s token-wallet-service)
PROV=$(make -s token-provider-a)

curl -s -X POST localhost:8080/wallets \
  -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' \
  -d '{"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","initialBalance":{"amount":"1000.00","currency":"BRL"}}'
```

## 5. Ver o efeito no banco

Depois de abrir carteiras, confira o que foi gravado **na mesma transação**:

```sh
docker compose exec postgres psql -U postgres -d wallet
```

```sql
SELECT id, player_id, currency, balance, version FROM wallets ORDER BY created_at DESC LIMIT 5;

SELECT kind, status, origin, amount, balance_after FROM wager_transactions ORDER BY created_at DESC LIMIT 5;

SELECT direction, amount, balance_before, balance_after FROM wallet_ledger_entries ORDER BY created_at DESC LIMIT 5;

SELECT event_type, correlation_id, published_at, payload->'data'->'balanceAfter'
FROM outbox_events ORDER BY occurred_at DESC LIMIT 6;

-- O ledger é append-only, até para o superusuário:
UPDATE wallet_ledger_entries SET amount = 1;
```

Os valores no banco estão em **centavos** (`BIGINT`): `100000` = `1000.00`. O `UPDATE` no ledger é recusado com `wallet_ledger_entries is append-only`. Os eventos da outbox ficam com `published_at` nulo até o publisher ser implementado (Dia 2).
