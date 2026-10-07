# Arquitetura

Este documento registra as decisões que sustentam as garantias do serviço, as alternativas avaliadas e os testes que provam cada ponto. As referências "README §N" apontam para o enunciado, em [`docs/CHALLENGE.md`](docs/CHALLENGE.md).

## Visão geral

O mesmo caso de uso (`internal/app.Wagers`) atende três entradas:
- a **API HTTP**;
- o **consumidor SQS**;
- o **worker de referências pendentes**.

Tudo o que uma operação produz é gravado numa **única transação SQL**:
- a transação de aposta;
- o lançamento no ledger;
- o novo saldo;
- o registro da inbox, quando vem do SQS;
- os eventos da outbox.

O **publisher da outbox** envia os eventos para o SQS depois do commit. Todo estado vive no PostgreSQL, então qualquer instância pode continuar o trabalho de outra.

Camadas:
- `domain` não conhece infraestrutura (sem Fx, HTTP, AWS nem pgx; verificado por `go list -deps`);
- `app` orquestra os casos de uso;
- `store` concentra o SQL;
- `httpapi`, `consumer`, `publisher` e `worker` são os adaptadores;
- `platform` reúne config, logs, métricas, clientes e injeção de falhas.

## Dinheiro

- `Money{minor int64, currency}`, imutável, em **centavos**. Nenhum `float` existe no caminho do dinheiro.
- Moedas aceitas: **BRL, USD e EUR**, todas com 2 casas. Moedas de outra escala, como JPY e KWD, são recusadas.
- **Parsing estrito:** só a forma `^(0|[1-9][0-9]*)\.[0-9]{2}$` é aceita, em string. São recusados:
  - `"10"`, `"10.0"`, `"-1.00"`, `"1e3"`, `NaN`;
  - números JSON (`10.5`);
  - espaços;
  - moeda em minúsculas.

  Cada valor tem uma única forma textual, então não há normalização antes do hash.
- **Limites:** o `int64` em centavos vai de `-92233720368547758.08` a `92233720368547758.07`. A entrada externa aceita de `0.00` ao máximo; valores negativos só aparecem internamente, por exemplo na diferença da reconciliação.
- Overflow é checado em toda soma e subtração (`AMOUNT_OUT_OF_RANGE`). Operar moedas diferentes resulta em `CURRENCY_MISMATCH`.
- No banco: `BIGINT` mais `CHAR(3)`. O saldo observado numa rejeição guarda a própria moeda (`balance_currency`), porque numa rejeição por `CURRENCY_MISMATCH` ela difere da moeda da operação.
- Na API: sempre `{"amount":"25.00","currency":"BRL"}`.

## Transações SQL e Unit of Work

- `store.InTx(ctx, func(ctx, r *Repos) error)` abre **uma** transação `READ COMMITTED`. Todos os repositórios recebidos em `r` compartilham essa transação. Se a função retorna erro ou entra em panic, tudo é desfeito.
- A transação é sempre delimitada no **caso de uso**, nunca dentro de um repositório. O SQL é explícito (pgx, sem ORM).
- **Retry da transação inteira**, até 3 vezes, nos casos de:
  - conflito de versão;
  - `40001` (serialization failure);
  - `40P01` (deadlock).

  Por isso a função passada ao `InTx` não pode ter efeitos fora do banco: publicar no SQS, por exemplo, só acontece depois do commit, pela outbox.
- **Prazos:**
  - `lock_timeout` de 5s e `statement_timeout` de 10s, aplicados pelo Postgres;
  - `DB_TX_TIMEOUT` de 15s, aplicado **pela aplicação** a cada `InTx`, `ReadSnapshot` e `Query`, para continuar respondendo mesmo com o banco travado.
- **Classificação de erros:**
  - prazos estourados, falhas de conexão e os códigos `55P03`, `57014`, `53300`, `57P01`, `57P03` e a classe `08` viram `ErrUnavailable`;
  - no HTTP, `ErrUnavailable` vira **503**; no SQS, retry com backoff;
  - o cancelamento pelo próprio chamador passa sem classificação.
- A **reconciliação** usa `ReadSnapshot`: `REPEATABLE READ READ ONLY`, de modo que a carteira e a soma do ledger saem do mesmo snapshot e é impossível alterar o saldo por esse caminho.
- Abrir uma carteira grava, numa só transação:
  - a carteira;
  - a transação `OPENING`;
  - o crédito no ledger;
  - os eventos.

  O teste confere, pelo `xmin`, que todas as linhas vieram da mesma transação.

## Idempotência e hash

- A idempotência é **persistente** e escopada pelo **provedor do token**. Dois índices únicos parciais garantem isso: `(provider_id, idempotency_key)` e `(provider_id, external_transaction_id)`. Nada fica só em memória.
- **Hash do payload:** `SHA-256` do **JSON canônico** dos campos de negócio, com as chaves ordenadas, sem espaços e todos os valores como string:
  - entram `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money.amount`, `money.currency` e, só quando presente, `referenceExternalTransactionId`;
  - ficam fora a chave de idempotência e todo metadado de transporte;
  - um teste golden fixa o hash do exemplo do enunciado.
- **Normalização:** UUIDs são convertidos para minúsculas. Os demais identificadores usam o charset restrito `[A-Za-z0-9._:-]`, sem nada a normalizar. Valores fora do formato são recusados, nunca ajustados.
- HTTP e SQS decodificam a operação no **mesmo tipo** (`WagerRequestInput`) e a validam na **mesma função**, então o mesmo conteúdo gera o mesmo hash nos dois canais.
- **Fluxo:** `INSERT ... ON CONFLICT DO NOTHING`.

  | Situação | Resultado |
  | --- | --- |
  | não houve conflito | operação nova |
  | conflito, com a mesma chave e o mesmo hash | **replay**: o resultado persistido, com o **saldo original**, e `idempotentReplay: true` |
  | conflito, com a mesma chave e outro hash | `409 IDEMPOTENCY_KEY_CONFLICT` |
  | conflito, com o mesmo `externalTransactionId` e outra chave | `409 EXTERNAL_TRANSACTION_CONFLICT` |

- A chave enviada pelo cliente **nunca é substituída** por uma chave calculada.

## Concorrência e locks

É a parte central da solução.

**Estratégia:** lock **pessimista por carteira**, com uma checagem **otimista de versão** e as **constraints do banco** como barreiras finais. A escolha veio da comparação com otimista puro e com atualização condicional:
- o pessimista lida melhor com alta disputa numa mesma carteira;
- mantém a regra de saldo no agregado;
- serializa as regras que dependem do estado atual, como a reversão única.

**Fluxo de uma operação**, numa transação:
1. `INSERT` idempotente da transação. Se já existir, o resultado é replay ou conflito, **sem travar a carteira**.
2. `SELECT ... FROM wallets WHERE id = $1 FOR NO KEY UPDATE`: o **único** lock, e só na linha daquela carteira. Não há lock global: carteiras diferentes nunca se esperam (testado).
3. O domínio aplica a regra (débito, crédito ou rejeição) sobre o estado mais recente da linha.
4. `UPDATE wallets ... WHERE id = $1 AND version = $esperada`. Afetar 0 linhas gera `ErrConcurrentUpdate` e retry, nunca uma atualização perdida.
5. Ledger, transação, inbox e outbox são gravados, e então o commit.

**Por que `FOR NO KEY UPDATE`, e não `FOR UPDATE`:**
- O `INSERT` em `wager_transactions` valida a FK para `wallets` pegando `FOR KEY SHARE` na linha da carteira.
- `FOR UPDATE` conflita com `FOR KEY SHARE`. Duas operações na mesma carteira entravam num ciclo: cada uma segurava o KEY SHARE que a outra precisava liberar, o que gerou `deadlock detected` sob carga.
- `FOR NO KEY UPDATE` continua exclusivo entre escritores, mas é compatível com `FOR KEY SHARE`.
- Resultado: 0 deadlocks, e o bloco de testes de concorrência caiu de 57s para 3s.

**Barreiras em camadas:**
1. lock de linha por carteira;
2. versão otimista;
3. `CHECK (balance >= 0)`;
4. índices únicos: idempotência, um lançamento por `(carteira, transação)`, uma abertura por carteira e uma reversão bem-sucedida por referência.

Um bug numa camada é contido pela seguinte.

**Como os cenários do enunciado se resolvem:**

| Cenário | Mecanismo | Resultado |
| --- | --- | --- |
| A mesma BET 50× em paralelo | os outros 49 `INSERT`s esperam no índice único o primeiro terminar e, depois do commit, leem a linha confirmada como replay | 1× 201 e 49× 200, **um** débito |
| Duas BETs de 80.00 sobre 100.00 | as duas inserem a própria transação e disputam o lock; a segunda vê o saldo 20.00 | 1 `PROCESSED` e 1 `REJECTED INSUFFICIENT_FUNDS` (saldo observado 20.00), saldo final 20.00 |
| Carteiras distintas | nenhum lock compartilhado | processamento em paralelo |
| A mesma operação por HTTP e SQS ao mesmo tempo | o mesmo índice único decide qual canal grava; o outro vira replay, e a inbox aponta para a transação existente | um efeito |
| REFUND, ROLLBACK e REFUND da mesma BET em paralelo | o lock da carteira serializa; a checagem `ALREADY_REVERSED` mais o índice `wt_single_successful_reversal_uk` | exatamente uma reversão |

**Trabalho assíncrono entre instâncias:**
- **Worker de pendências:** `FOR UPDATE SKIP LOCKED`, uma pendência por transação. Cada instância pega uma diferente sem esperar as outras.
- **Outbox:** reserva com `SKIP LOCKED` mais **lease**, publicação fora da transação e confirmação condicionada ao dono da reserva.
- **Ordem de locks sem ciclos:** o caminho síncrono segura a carteira e **pula** pendências travadas ao acordá-las; o worker segura a pendência e depois espera a carteira.

**Prova com processos independentes:** três containers da aplicação, cada um com o próprio pool e memória, rodaram todos os cenários acima com as requisições distribuídas entre eles, inclusive 30 carteiras × 20 operações. Resultado:
- nenhum erro;
- `wallet_concurrency_conflicts_total` zerado, porque o lock serializa sem gerar retries;
- o saldo de todas as carteiras bate com o ledger.

## Máquina de estados e falhas transitórias × permanentes

```
PENDING ──► PROCESSED        (terminal)
   │   ──► REJECTED         (terminal, com failureCode e saldo observado)
   │   ──► FAILED           (terminal, com failureCode)
   └──► PENDING_REFERENCE ──► PROCESSED | REJECTED | FAILED
```

- O domínio valida cada transição, e um trigger no banco impede alterar uma transação terminal.
- Uma operação **sem dependências nunca é confirmada em `PENDING`**: ela termina na mesma transação em que foi aceita, sem commit intermediário de aceite (README §6.3).
- Só `PENDING_REFERENCE` é confirmado e retomado depois, por qualquer instância.
- **Falha transitória:** banco indisponível, prazo estourado, deadlock ou conflito esgotado. **Nada é gravado** e a operação pode ser repetida:
  - o HTTP responde `503 TEMPORARILY_UNAVAILABLE` com `Retry-After`;
  - o SQS deixa a mensagem voltar com backoff e, depois de 5 recebimentos, ela vai para a DLQ;
  - o worker tenta de novo.
- **Falha permanente de negócio:** `REJECTED` com `failureCode`. É definitiva e auditável, e o replay devolve a mesma rejeição.
- **Entrada inválida:** HTTP 400 ou a DLQ imediata no SQS. Nada é gravado e a chave não é consumida.
- **`FAILED`:** existe no domínio e no schema, mas **nenhum fluxo o produz**. Interpretação adotada: toda falha de infraestrutura é tratada como transitória e repetida até dar certo ou ir para a DLQ. Assim, uma operação legítima nunca é marcada como falha definitiva por causa de uma indisponibilidade passageira.

## Referências pendentes

- REFUND, ROLLBACK e WIN **com** referência dependem da transação referenciada, resolvida por `(providerId, referenceExternalTransactionId)` sempre **no escopo do próprio provedor**.

  | Estado da referência | Resultado |
  | --- | --- |
  | não chegou, ou ainda está pendente | `PENDING_REFERENCE`, HTTP 202 com `Location`, evento `WagerTransactionPendingReference`; nenhum lock, nenhum movimento |
  | `REJECTED` ou `FAILED` | `REJECTED REFERENCE_NOT_PROCESSED` |
  | `PROCESSED` | valida e aplica |

- **Worker:** `SELECT ... WHERE status = 'PENDING_REFERENCE' AND next_attempt_at <= agora ... FOR UPDATE SKIP LOCKED LIMIT 1`.
  - Backoff exponencial com teto e jitter de 10%.
  - Ao atingir `PENDING_MAX_ATTEMPTS` (10) ou `PENDING_TTL` (30m): `REJECTED REFERENCE_NOT_FOUND`.
  - Se o processo morre no meio, o rollback libera a pendência e outra instância a retoma (cenário de falha 5).
- **Acordar:** quando uma transação termina, na mesma transação SQL as pendências que a referenciam passam a `next_attempt_at = agora`. A resolução leva cerca de 0,5s, em vez de esperar o backoff. Isso também resolve cadeias: ROLLBACK → REFUND → BET.
- **WIN com referência espera** (interpretação): ela só é creditada depois que a BET existe e é validada. Uma WIN **sem** referência é creditada na hora.

## Reversões

| Operação | Referência permitida | Movimento | Sem saldo |
| --- | --- | --- | --- |
| REFUND | BET | crédito | — |
| ROLLBACK | BET | crédito | — |
| ROLLBACK | WIN | débito | `REVERSAL_INSUFFICIENT_FUNDS` |
| ROLLBACK | REFUND | débito (desfaz a devolução) | `REVERSAL_INSUFFICIENT_FUNDS` |
| qualquer outra combinação | — | `REFERENCE_KIND_NOT_REVERSIBLE` | — |

- O valor precisa ser **igual** ao da referência (`AMOUNT_MISMATCH`); não existe reversão parcial.
- Jogador, carteira, moeda, provedor e rodada precisam coincidir (`REFERENCE_MISMATCH`).
- **Política adotada:** no máximo **uma reversão bem-sucedida por transação**, seja REFUND ou ROLLBACK. É mais restritiva que o texto literal ("do mesmo tipo") e foi escolhida porque um único índice no banco garante toda a invariante. Uma BET reembolsada não aceita ROLLBACK, e vice-versa (`ALREADY_REVERSED`), então o débito nunca é devolvido duas vezes.
- `REVERSAL_INSUFFICIENT_FUNDS` é diferente do `INSUFFICIENT_FUNDS` da aposta, e a rejeição fica auditável: gravada com o saldo observado e com evento.

## Inbox

- `inbox_messages (consumer_name, message_id)` é a chave primária. O registro é inserido **na mesma transação** da operação, com `ON CONFLICT DO NOTHING`.
- Mensagem já registrada:
  - com o mesmo hash → **duplicata**, apagada sem nenhum efeito;
  - com outro hash → `MESSAGE_ID_CONFLICT`, que vai para a DLQ.
- O hash da inbox combina o hash de negócio com a chave de idempotência. Um reenvio com outra formatação continua sendo reconhecido como a mesma mensagem.
- O `DeleteMessage` só acontece **depois do commit**. Se o processo morre entre os dois, a mensagem volta, e a inbox a reconhece como duplicata, como provado no cenário de falha 1 com processo real.

## Outbox e contrato de eventos

- Os eventos são gravados na mesma transação da operação, como **snapshot JSON imutável** (um trigger impede alterá-los). Por construção, nada é publicado antes do commit.
- **Publicação:**
  1. **Reserva:** `UPDATE ... SET locked_by, locked_until = agora + lease, attempts + 1` sobre um `SELECT ... FOR UPDATE SKIP LOCKED`.
  2. **Envio** fora de qualquer transação SQL.
  3. **Confirmação** só se a reserva ainda pertence a quem publicou.
- **Falha:** backoff exponencial, **sem limite de tentativas**. Um evento confirmado nunca é descartado, e o atraso aparece nas métricas `outbox_pending_events` e `outbox_oldest_pending_age_seconds`.
- **Recuperação:** se o processo morre entre o commit e a publicação, ou entre a publicação e a confirmação, outra instância assume depois que o lease expira e republica o **mesmo `eventId`**. Os dois casos foram provados com processo real.
- **Ordem estrita por carteira:** só o evento mais antigo não publicado de cada carteira pode ser reservado. Com `MessageGroupId = walletId`, os consumidores recebem os eventos de cada carteira em ordem, mesmo com vários publishers. O custo é que um evento travado segura os seguintes **da mesma carteira**.
- **Contrato de saída:**

  | Item | Valor |
  | --- | --- |
  | Destino | `wallet-events.fifo` (DLQ `wallet-events-dlq.fifo`) |
  | Corpo | `{eventId, eventType, aggregateId (= walletId), correlationId, causationId?, occurredAt (UTC RFC 3339), version, data}`, com dinheiro em string decimal |
  | `MessageGroupId` / `MessageDeduplicationId` | `walletId` / `eventId` |
  | Atributos | `eventType`, `eventVersion` |
  | Tipos (v1) | `WagerTransactionProcessed`, `WagerTransactionRejected`, `WalletBalanceChanged`, `WagerTransactionPendingReference` |
  | Garantia | **at-least-once**, em ordem por carteira. O consumidor deve deduplicar por `eventId`; `WalletBalanceChanged.data.walletVersion` permite detectar lacunas |

## SQS

- **Contrato de entrada:**
  - envelope `{messageId, type: "WagerTransactionRequested", occurredAt, data}`, com `data` igual ao corpo HTTP mais `idempotencyKey`;
  - `MessageGroupId = walletId`, o que dá ordem por carteira e carteiras em paralelo;
  - `MessageDeduplicationId = messageId`, que é só uma otimização: a garantia vem da inbox e dos índices.
- As mensagens de um lote são processadas em sequência pela goroutine que as recebeu, o que preserva a ordem do grupo FIFO.

  | Resultado | Ação |
  | --- | --- |
  | processada, rejeitada, pendente, replay ou duplicata | `DeleteMessage` depois do commit |
  | envelope inválido, `UNKNOWN_PROVIDER`, erro de validação, `WALLET_NOT_FOUND` ou conflito | **direto para a DLQ**, com `failureReason` e o corpo original, e então apagada. Não segura o grupo FIFO com reprocessamentos inúteis |
  | transitório | `ChangeMessageVisibility` com backoff exponencial; depois de 5 recebimentos, o redrive do SQS leva a mensagem para a DLQ |

- **Prazos:** visibilidade de 30s, prazo de processamento de 20s (sempre menor que a visibilidade) e long polling de 10s.
- Como o SQS não carrega token, o provedor precisa estar em `KNOWN_PROVIDERS`. O acesso à fila em si é controlado pelas políticas IAM (ver Autenticação).

## Autenticação e autorização

- **IdP:** Keycloak, com o realm importado no boot. Tokens via `client_credentials`.
- **Validação do JWT**, com `coreos/go-oidc`:
  - assinatura RS256 com as chaves do JWKS (em cache, com rotação);
  - `iss` exato;
  - `aud = wagering-api`;
  - `exp`.
- **O issuer fixo:** `KC_HOSTNAME` fixa o `iss` em `http://localhost:8081/realms/wagering` qualquer que seja o caminho de acesso. As chaves são buscadas pela rede interna.
- **Roles do realm:**
  - `provider`: enviar e consultar as próprias operações. Exige a claim `provider_id`, que é separada do `client_id`;
  - `wallet-admin`: rotas de carteira e leitura de qualquer transação.

| Regra | Resposta |
| --- | --- |
| sem token, ou token inválido, expirado, de outro issuer ou de outra audiência | 401 `UNAUTHENTICATED` |
| role ausente; provedor em rota interna; serviço interno enviando operação | 403 `FORBIDDEN` |
| `providerId` do corpo diferente do token | 403, **antes de qualquer leitura ou escrita** |
| provedor consultando transação de outro pelo ID interno | 404, sem revelar a existência |
| provedor consultando a rota `/providers/{outro}/...` | 403 |
| outro provedor reutilizando a chave ou o ID externo no próprio escopo | uma transação nova dele, nunca o resultado do outro |

- Os testes rodam contra o Keycloak real e conferem que as tentativas negadas não deixam nenhum efeito: transações, ledger, outbox e saldo ficam iguais.
- **SQS:** as políticas de privilégio mínimo estão em [`deploy/aws/iam/`](deploy/aws/iam/):

  | Papel | Pode |
  | --- | --- |
  | provedor | só `SendMessage` na fila de entrada |
  | aplicação | consumir a entrada, enviar para a DLQ e publicar eventos |
  | consumidor de eventos | só ler `wallet-events` |

## Fx e ciclo de vida

- Há um `fx.Module` por pacote, reunidos em `bootstrap.Options()`. O mesmo grafo é usado pelo `main` e pelos testes, e um teste unitário valida o grafo completo (`fx.ValidateApp`).
- **Inicialização:**
  1. config validada;
  2. logger;
  3. pool do Postgres (com ping);
  4. cliente SQS (resolve as URLs das filas);
  5. verificador de JWT (busca o JWKS);
  6. servidor HTTP (`net.Listen` síncrono, para falhar cedo);
  7. workers.

  Qualquer falha desfaz o que já subiu. A espera pelas dependências fica com o orquestrador (healthchecks do compose).
- **Workers:** todos usam um `worker.Runner` genérico com o contrato `RunOnce(ctx) (didWork, err)`, que define N goroutines, o intervalo ocioso e a parada observável.
  - O `ctx` do loop é cancelado no início do shutdown e serve para **parar de buscar** trabalho.
  - O trabalho em andamento usa `context.WithoutCancel` com um prazo próprio, para **concluir ou liberar**.

## Shutdown

A ordem sai do grafo do Fx (inversa da inicialização):

1. O readiness passa a responder **503 `draining`**, o que tira a instância do balanceamento.
2. **Publisher da outbox:** para de reservar e **libera** os eventos reservados e ainda não publicados.
3. **Consumidor:** para de iniciar buscas. A busca em andamento termina, e o que ela trouxer é processado (se o shutdown ainda não começou) ou **liberado** com `ChangeMessageVisibility(0)`. A mensagem em processamento termina dentro do seu prazo.
4. **Worker de pendências:** a iteração em andamento termina ou é desfeita, e a pendência continua no banco.
5. **Servidor HTTP:** `Shutdown` gracioso, que conclui as requisições em andamento.
6. **Pool do Postgres:** fechado por último.

Medido no compose: 2,8s, sem erros. O teste de reinicialização envia uma mensagem **durante** o desligamento, e a próxima instância a consome em 0,11s.

## Observabilidade

| Sinal | Métrica |
| --- | --- |
| resultados por canal (`http`, `sqs`, `worker`), tipo e status | `wager_transactions_total` |
| replays e duplicatas | `wager_idempotent_replays_total`, `sqs_messages_total{result="duplicate"}` |
| carteira inexistente | `wager_wallet_not_found_total{channel}` |
| retries | `sqs_messages_total{result="retry"}`, `pending_reference_attempts_total`, `outbox_publish_total{result="failed"}` |
| DLQ | `sqs_messages_total{result="dead_letter"}`, `sqs_dead_letters_total{reason}` |
| conflitos de concorrência | `wallet_concurrency_conflicts_total{reason}` |
| atraso da outbox (lido do banco a cada coleta, igual em qualquer instância) | `outbox_pending_events`, `outbox_oldest_pending_age_seconds`, `pending_references_waiting` |
| latência | `wager_processing_duration_seconds{channel}`, `http_request_duration_seconds{route,method,status}` |
| reconciliação | `reconciliation_mismatches_total` |

- **Logs** em JSON (`slog`): `instanceId`, `correlationId` (`X-Correlation-Id`, propagado para as transações e os eventos), `transactionId`, `walletId`, `providerId`, `messageId`.
- Há um log de acesso por rota, sempre com o padrão da rota e nunca com IDs.
- **Nunca** são registrados tokens, o header `Authorization`, corpos de requisição ou payloads financeiros completos.

## Contrato HTTP e failure codes

| Situação | HTTP | Corpo |
| --- | --- | --- |
| Operação processada | 201 | `{transactionId, status: "PROCESSED", balance, idempotentReplay: false}` |
| Replay de operação processada | 200 | o mesmo corpo, com o saldo **original** e `idempotentReplay: true` |
| Rejeição de negócio (nova ou replay) | 422 | `{transactionId, status: "REJECTED", failureCode, balance (observado), idempotentReplay}` |
| Aguardando referência | 202 + `Location` | `{transactionId, status: "PENDING_REFERENCE", idempotentReplay}` |
| Carteira criada | 201 + `Location` | `{id, playerId, balance, version, createdAt, updatedAt}` |
| Entrada inválida | 400 | `{error: {code, message, correlationId}}` |
| Carteira inexistente | 422 | `{error: {code: "WALLET_NOT_FOUND"}}`, sem `status`; nada é gravado, e o evento fica em log e métrica |
| Sem autenticação / sem permissão | 401 / 403 | `UNAUTHENTICATED` / `FORBIDDEN` |
| Não encontrado | 404 | `NOT_FOUND` |
| Conflito de idempotência ou de unicidade | 409 | `IDEMPOTENCY_KEY_CONFLICT`, `EXTERNAL_TRANSACTION_CONFLICT`, `WALLET_ALREADY_EXISTS` |
| Indisponibilidade transitória | 503 + `Retry-After` | `TEMPORARILY_UNAVAILABLE` |
| Erro inesperado | 500 | `INTERNAL_ERROR` (detalhe só no log) |

| Código | Categoria | Significado |
| --- | --- | --- |
| `INVALID_MONEY`, `INVALID_CURRENCY`, `INVALID_AMOUNT`, `INVALID_REQUEST`, `UNSUPPORTED_KIND`, `MISSING_IDEMPOTENCY_KEY` | entrada (corrigível) | formato ou valor não aceito |
| `WALLET_NOT_FOUND` | entrada (corrigível) | a carteira não existe; a chave não é consumida |
| `IDEMPOTENCY_KEY_CONFLICT`, `EXTERNAL_TRANSACTION_CONFLICT`, `WALLET_ALREADY_EXISTS`, `MESSAGE_ID_CONFLICT` | conflito | reutilização com outro conteúdo |
| `INSUFFICIENT_FUNDS` | negócio (definitivo) | aposta sem saldo |
| `REVERSAL_INSUFFICIENT_FUNDS` | negócio (definitivo) | reversão que debitaria além do saldo |
| `CURRENCY_MISMATCH`, `PLAYER_WALLET_MISMATCH` | negócio (definitivo) | a operação não combina com a carteira |
| `REFERENCE_NOT_FOUND` | negócio (definitivo) | a referência não chegou dentro do limite de tentativas ou do TTL |
| `REFERENCE_NOT_PROCESSED` | negócio (definitivo) | a referência foi rejeitada ou falhou |
| `REFERENCE_MISMATCH`, `AMOUNT_MISMATCH`, `REFERENCE_KIND_NOT_REVERSIBLE`, `ALREADY_REVERSED` | negócio (definitivo) | violações das regras de referência e reversão |
| `AMOUNT_OUT_OF_RANGE` | negócio (definitivo) | o resultado estouraria o limite de `int64` |
| `UNKNOWN_PROVIDER`, `INVALID_MESSAGE` | só no SQS (vai para a DLQ) | provedor fora da allowlist ou envelope inválido |

## Limitações, interpretações e trabalho não concluído

**Interpretações adotadas:**
- **Uma reversão bem-sucedida por transação**, seja REFUND ou ROLLBACK, e não uma por tipo.
- **WIN com referência espera a BET**, em vez de ser recusada ou paga sem validação.
- **Carteira inexistente** responde 422 sem gravar nada, para que a chave não seja consumida. No SQS, a mensagem vai para a DLQ.
- **Referência em BET/LOSS** é recusada como entrada inválida, em vez de ignorada.
- **A rejeição grava o saldo observado**.
- **`FAILED` não é produzido:** toda falha de infraestrutura é tratada como transitória (ver Máquina de estados).
- **Lista curta de moedas** (BRL, USD, EUR) e **charset restrito** nos identificadores, para eliminar ambiguidades no hash.
- **Ordem estrita por carteira na outbox**, ao custo de um evento travado segurar os seguintes da mesma carteira.
- **Prazo de transação estourado no momento do commit** gera um resultado ambíguo: 503, mas talvez gravado. O reenvio cai no replay, sem duplicar.

**Limitações conhecidas:**
- **As políticas IAM não são aplicadas pelo LocalStack** na versão gratuita. As políticas estão escritas em `deploy/aws/iam/`, mas, no ambiente local, qualquer credencial acessa as filas. Numa conta AWS real elas seriam anexadas aos papéis.
- **LocalStack fixado em 4.14.0**, a última versão que roda sem licença.
- **Prazos menores no ambiente e2e:** a suíte e2e e a de falhas usam visibilidade e lease de 10s para acelerar a recuperação. Os valores padrão continuam sendo 30s.
- **Suíte de integração lenta** (cerca de 5 min): cada teste sobe seus próprios containers, inclusive um Keycloak.

**Não implementado** (opcional no enunciado ou fora do escopo):
- **Tracing distribuído** (OpenTelemetry) e dashboards; a correlação é feita pelo `correlationId` nos logs, transações e eventos.
- **Teste de carga** com throughput e percentis.
- **Partidas dobradas:** o ledger é de entrada simples por carteira, com saldos antes e depois encadeados.
- **Consumidor de exemplo** para `wallet-events.fifo`: o contrato está documentado, mas nenhum serviço o consome.
