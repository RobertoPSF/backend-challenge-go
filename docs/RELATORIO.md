# Relatório de entrega

Resumo de tudo o que foi feito para entregar o desafio ([`CHALLENGE.md`](CHALLENGE.md)): o que foi construído, as tecnologias escolhidas, as decisões tomadas (com as alternativas descartadas), os problemas encontrados e como foram resolvidos. A **concorrência** tem uma seção própria.

O detalhe de cada decisão está em [`DECISIONS.md`](DECISIONS.md) (`D-001`…`D-038` são decisões e `P-001`…`P-019` são problemas). A arquitetura consolidada está em [`../ARCHITECTURE.md`](../ARCHITECTURE.md), e o passo a passo para rodar está no [`../README.md`](../README.md).

## 1. O que foi entregue

- **Serviço de carteiras e apostas** com API HTTP e consumidor SQS que compartilham o mesmo caso de uso, o mesmo parsing e o mesmo hash de idempotência:
  - operações `BET`, `WIN`, `LOSS`, `REFUND` e `ROLLBACK`;
  - abertura de carteira com crédito inicial;
  - consulta de saldo e ledger paginado;
  - consulta de transações;
  - reconciliação.
- **Ledger append-only** com saldo antes e depois em cada lançamento. Ele é protegido por permissões do role da aplicação e por triggers, que bloqueiam alterações até para o dono do schema.
- **Idempotência persistente** por provedor, com replay do resultado original (inclusive o saldo da época) e conflito 409 para reutilização com outro conteúdo.
- **Referências fora de ordem:** REFUND, ROLLBACK e WIN com referência esperam em `PENDING_REFERENCE`. Um worker com backoff exponencial resolve ou expira, e a pendência é "acordada" na hora em que a referência chega.
- **Inbox** (deduplicação de mensagens) e **outbox** (publicação dos eventos depois do commit, com lease, recuperação e ordem por carteira).
- **Autenticação real** com Keycloak (JWT validado por JWKS) e autorização por role e por provedor.
- **Observabilidade:** métricas Prometheus para todos os sinais pedidos, logs JSON correlacionados e health com `draining` no shutdown.
- **Ambiente reproduzível:** `docker compose up --build` sobe Postgres, Keycloak (realm importado), LocalStack (filas criadas), migrations e aplicação, sem nenhum passo manual.
- **Testes** em quatro níveis (ver §5), com containers reais, três processos independentes e falhas injetadas com morte real do processo.
- **Documentação:** README, ARCHITECTURE, registro de decisões, plano, este relatório e uma collection do Postman (97 requisições, 275 asserções) com roteiro.

**Números:**
- cerca de 4.500 linhas de Go de produção e 6.100 de testes;
- 7 migrations (up e down);
- 24 commits de implementação ao longo de 3 dias, seguindo o [plano](plan.md).

## 2. Tecnologias e por quê

| Tecnologia | Uso | Por que (alternativa descartada) |
| --- | --- | --- |
| **Go 1.27.1** | linguagem | a versão mais recente com suporte, igual à local (1.25 já estava fora de suporte) — D-004 |
| **Uber Fx** | composição e ciclo de vida | exigido pelo enunciado; um `fx.Module` por pacote, com o grafo validado em teste — D-008 |
| **chi** | roteador HTTP | leve, compatível com `net/http` |
| **pgx/v5 + pgxpool** | acesso ao Postgres | SQL explícito, sem ORM, para que locks e constraints fiquem visíveis e verificáveis — D-018 |
| **golang-migrate** | migrations | roda como serviço one-shot no compose e como biblioteca nos testes — D-005 |
| **PostgreSQL 16** | banco | constraints, locks de linha, `SKIP LOCKED`, snapshots `REPEATABLE READ` |
| **LocalStack 4.14.0** | SQS local | a versão mais recente que roda sem licença; a `latest` exige token (P-001) — D-001 |
| **Keycloak 26.8** | IdP | indicado pelo enunciado, com o realm importado no boot — D-020 |
| **coreos/go-oidc** | validação do JWT | RS256, JWKS com cache e rotação, `iss`, `aud` e `exp` |
| **aws-sdk-go-v2** | SQS | cliente oficial |
| **caarlos0/env** | configuração | tags na struct mais validação no boot, em vez de cerca de 80 linhas de parsing manual — D-006 |
| **Prometheus + slog** | métricas e logs | registro próprio (não o global) e logs JSON — D-008, D-031 |
| **testcontainers-go, goleak, fxtest** | testes | containers reais por teste, verificação de vazamento de goroutines e do ciclo de vida do Fx |
| **google/uuid (v7)** | IDs | ordenáveis no tempo, o que dá uma ordenação estável no ledger e na outbox |

## 3. Concorrência

O enunciado pesa a concorrência (20 pontos) e lista como eliminatórios o saldo negativo, a movimentação duplicada e a dependência de uma única instância. Esta é a parte central da solução.

### 3.1 Estratégia escolhida

Lock **pessimista por carteira**, combinado com uma checagem **otimista de versão** e com as **constraints do banco** como barreiras finais (D-024). Antes de implementar, comparamos três estratégias:

| Estratégia | Por que não / por que sim |
| --- | --- |
| Otimista puro (versão + retry) | em alta disputa numa mesma carteira, gera retries em cascata; regras que dependem do estado atual (reversão única) ficam frágeis |
| Atualização atômica condicionada (`UPDATE ... WHERE balance >= x`) | rápida, mas tira a regra de saldo do domínio e não cobre reversões nem referências |
| **Pessimista por carteira (escolhida)** | serializa só as escritas da mesma carteira; carteiras diferentes seguem em paralelo; a regra continua no agregado |

### 3.2 Como uma operação é processada

Numa única transação `READ COMMITTED`:
1. `INSERT ... ON CONFLICT DO NOTHING` da transação. Os índices únicos por provedor decidem quem é o primeiro; quem perde vira replay ou 409, **sem travar a carteira**.
2. `SELECT ... FOR NO KEY UPDATE` na linha da carteira, o único lock.
3. O domínio aplica a regra sobre o estado mais recente da linha.
4. `UPDATE wallets ... WHERE version = $esperada`. Afetar 0 linhas causa retry da transação inteira, nunca uma atualização perdida.
5. Ledger, transação, inbox e outbox são gravados, e então o commit.

Barreiras em camadas:
1. lock;
2. versão;
3. `CHECK (balance >= 0)`;
4. índices únicos.

Mesmo que uma camada falhasse, a seguinte impediria saldo negativo ou movimento duplicado.

### 3.3 O problema mais importante encontrado: deadlock com `FOR UPDATE` (P-014)

- **Sintoma:** a primeira versão usava `SELECT ... FOR UPDATE`. O teste "30 BETs distintas na mesma carteira" falhou com `deadlock detected`, e o bloco de concorrência levou 57s. Não houve corrupção, porque o banco aborta uma transação, mas operações legítimas falhavam sob carga. O teste 80 + 80 só passava por sorte de timing.
- **Causa:** o `INSERT` da transação tem FK para `wallets`, e o Postgres a valida com um lock `FOR KEY SHARE` na linha da carteira. `FOR UPDATE` conflita com `FOR KEY SHARE`:
  1. A insere (KEY SHARE);
  2. B insere (KEY SHARE);
  3. A pede `FOR UPDATE` e espera B;
  4. B pede `FOR UPDATE` e espera A. Ciclo.
- **Correção:** `FOR NO KEY UPDATE`, o lock que o próprio Postgres usa num `UPDATE` sem mudança de chave. Ele continua exclusivo entre escritores, mas é compatível com o `KEY SHARE` da FK.
- **Resultado:** 0 deadlocks (medido pela métrica), e o bloco caiu de 57s para cerca de 3s.

Esse problema só apareceu porque o teste exercitava muitas operações concorrentes na **mesma** carteira. Um teste só com 80 + 80 não teria pego.

### 3.4 Cenários exigidos e como foram provados

| Cenário | Mecanismo | Provado em |
| --- | --- | --- |
| Mesma BET 50× em paralelo | os 49 seguintes esperam no índice único e leem a linha confirmada como replay | 3 pools (integração), HTTP (integração) e **3 processos** (e2e): 1× 201 e 49× 200, **1 débito** |
| BETs de 80.00 + 80.00 sobre 100.00 | as duas inserem; o lock serializa; a segunda vê o saldo 20.00 | integração, HTTP e **3 processos, 10 rodadas**: 1 `PROCESSED` e 1 `REJECTED INSUFFICIENT_FUNDS`, saldo 20.00 |
| Carteiras distintas em paralelo | nenhum lock compartilhado | 200 BETs em 20 carteiras (integração); **600 operações em 30 carteiras por 3 processos** (e2e) |
| Mesma operação por HTTP e SQS ao mesmo tempo | o índice único decide e a inbox aponta para a transação existente | integração (5 rodadas) e **3 processos e 3 consumidores** (e2e) |
| Reversões concorrentes da mesma BET | lock da carteira mais `ALREADY_REVERSED` mais índice único | 10 repetições: sempre exatamente uma reversão |
| Pendências disputadas por vários workers | `FOR UPDATE SKIP LOCKED` | 9 workers em 3 instâncias resolvendo 20 pendências: cada uma exatamente uma vez |
| Publishers disputando a outbox | reserva com `SKIP LOCKED` mais lease | 3 publishers e 9 goroutines: cada evento publicado uma vez, em ordem por carteira |

**Resultado nos 3 processos reais** (D-035): nenhum ERROR ou WARN nos logs, `wallet_concurrency_conflicts_total` zerado (o lock serializa sem gerar retries) e o trabalho assíncrono distribuído entre as instâncias. Todos os cenários terminam conferindo que o saldo de **todas** as carteiras do banco é igual a créditos − débitos do ledger.

### 3.5 Concorrência no trabalho assíncrono

- **Worker de pendências:** uma pendência por transação com `SKIP LOCKED`. Se o processo morre no meio, o rollback a devolve e outra instância a retoma, como provado com morte real do processo.
- **Acordar sem deadlock:** o caminho síncrono segura a carteira e **pula** pendências travadas; o worker segura a pendência e depois espera a carteira. Não há espera circular (D-027).
- **Outbox:**
  - a reserva com lease é feita fora de uma transação longa;
  - a confirmação só vale para o dono da reserva;
  - a ordem estrita por carteira vem de só o evento mais antigo de cada carteira ser elegível (D-029).

## 4. Principais decisões

Cada decisão de design foi apresentada com as alternativas e os trade-offs e tomada em conjunto. As principais:

| Tema | Decisão | Alternativa descartada |
| --- | --- | --- |
| Dinheiro | `int64` em centavos, parsing estrito de string, BRL/USD/EUR | float; ISO 4217 completa; aceitar `"25"` e normalizar |
| Reversões | uma reversão bem-sucedida **por transação** (REFUND ou ROLLBACK), garantida por um índice | uma por tipo, que exigiria uma regra extra contra devolução dupla |
| WIN com referência ausente | espera em `PENDING_REFERENCE` | recusar na hora; creditar sem validar |
| Carteira inexistente | 422 sem gravar nada, com log e métrica; no SQS, DLQ | gravar como `REJECTED`, consumindo a chave |
| Referência em BET/LOSS | recusada como entrada inválida | ignorar em silêncio |
| Saldo na rejeição | gravado (saldo observado) | rejeição sem saldo |
| Acesso a dados | store concreto com Unit of Work (`InTx`) | interfaces na aplicação; `pgx.Tx` exposto |
| Hash de idempotência | SHA-256 de JSON canônico, charset restrito nos IDs | texto livre com normalização Unicode |
| Identidade do provedor | claim própria `provider_id` | usar o `client_id` |
| Autorização | roles do realm (`provider`, `wallet-admin`) | scopes OAuth |
| Consulta de transação de outro provedor | 404 (não revela a existência) | 403 |
| Erro permanente no SQS | direto para a DLQ, com motivo | esperar 5 recebimentos inúteis pelo redrive |
| Ordem dos eventos | estrita por carteira | melhor esforço |
| Atraso da outbox | lido do banco a cada coleta | gauge mantido pelo publisher |
| Shutdown do consumidor | a busca termina e o que ela trouxer é liberado | cancelar e deixar a mensagem presa por 30s |
| Banco travado | prazo de transação do lado da aplicação → 503 | timeout só no HTTP; não fazer nada |
| Três instâncias | três containers (processos reais) | três apps Fx no mesmo processo de teste |
| Injeção de falhas | pontos nomeados no código (desligados por padrão) com `os.Exit(137)` | só `docker kill` em momentos aleatórios |
| Inicialização | ordem pelos healthchecks do compose | espera embutida na aplicação |

## 5. Testes

| Nível | Comando | O que cobre |
| --- | --- | --- |
| Unitários | `go test -race ./...` | domínio (Money, máquina de estados, reversões, hash golden), config, Runner, backoff, classificação de mensagens, grafo do Fx |
| Integração | `make test-integration` (cerca de 5 min) | Postgres, LocalStack e Keycloak reais via testcontainers; schema (27 invariantes com o nome da constraint), store, concorrência com 3 pools, API completa, consumidor, publisher, worker, autenticação, reinicialização, Fx sem vazamentos |
| Multi-instância | `make e2e-up && make test-e2e` (cerca de 7s) | três processos reais da aplicação nos cenários de concorrência |
| Falhas | `make test-faults` (cerca de 3 min) | 8 cenários com morte real do processo (código 137), `kill -9` das três instâncias e pausa do Postgres e do SQS |
| Manual | Postman ou newman | 275 asserções sobre o sistema inteiro |

**Lacunas encontradas e fechadas pelos próprios testes:**
- atomicidade com falha forçada no meio da transação;
- invariante global do ledger;
- tentativas negadas sem nenhum efeito (transações, ledger, outbox, saldo);
- isolamento de chaves entre provedores;
- a mesma operação por HTTP e SQS com o consumidor real rodando.

Ao final de cada etapa foi feita uma verificação completa:
- `gofmt`, `vet` e unitários com `-race`;
- a suíte de integração inteira;
- o compose subindo do zero;
- o Postman;
- o desligamento ordenado.

## 6. Problemas encontrados

| # | Problema | Resolução |
| --- | --- | --- |
| P-001 | LocalStack `latest` exige licença | versão fixada em 4.14.0 |
| P-002 | `migrate up` falha com a pasta vazia | antecipada a primeira migration |
| P-003 | imagem do Keycloak sem curl/wget para o healthcheck | `/dev/tcp` do bash na porta de gestão |
| P-004 | `iss` do token variava conforme o acesso (host × rede interna) | `KC_HOSTNAME` fixo + JWKS pela rede interna |
| P-005 | condição de corrida no próprio teste do Runner | o teste espera o worker iniciar |
| P-007 | `CHECK (col <> '')` aceitava `NULL` | `COALESCE`; os testes passaram a conferir o nome da constraint |
| P-008 | `make token-%` "passava" com client inexistente | o status do `curl` passou a ser checado |
| P-010 | dependência ausente no grafo do Fx | pega pelo teste `fx.ValidateApp` antes de rodar |
| P-011 | `count(DISTINCT xmin)` não funciona | `xmin::text` e checagem do erro |
| **P-014** | **deadlock entre o lock da FK e o `FOR UPDATE`** | **`FOR NO KEY UPDATE`** (ver §3.3) |
| P-015 | falso positivo num teste (contava a transação de abertura) | filtro por origem |
| **P-016** | **mensagem presa por 30s numa busca abandonada no shutdown, e um lote descartado sem processar** | **a busca termina e o resultado é liberado** (de 30,78s para 0,11s) |
| P-018 | asserções do e2e erradas em relação ao contrato; fila de eventos cheia afetando o Postman | testes corrigidos; procedimento documentado |
| **P-019** | **requisição pendurada para sempre com o Postgres pausado** | **`DB_TX_TIMEOUT`: 503 em 15s** |
| P-013, P-017 | instabilidades do Docker Desktop no WSL | ambiente; refeito e documentado |

Os problemas em negrito eram defeitos reais da aplicação e foram descobertos pelos testes de concorrência, de reinicialização e de falha. Nenhum deles teria aparecido com testes só de caminho feliz.

## 7. Atendimento aos critérios eliminatórios

| Critério | Como é atendido |
| --- | --- |
| Autenticação efetiva nos endpoints de negócio | JWT validado (assinatura, `iss`, `aud`, `exp`) em todas as rotas, exceto health e métricas; testado contra o Keycloak real |
| Acesso não autorizado | roles mais vínculo do `providerId` do corpo com o token; 404 para transações de outro provedor; negações sem efeito (testado) |
| Dinheiro em ponto flutuante | `int64` em centavos; nenhum `float` no caminho do dinheiro |
| Saldo negativo por concorrência | lock por carteira, versão e `CHECK (balance >= 0)`; provado com 3 processos |
| Movimentação duplicada | índices únicos de idempotência, inbox e um lançamento por transação; provado com 50 envios, HTTP + SQS e reentrega depois de morte do processo |
| Idempotência só em memória | toda a idempotência está no banco; provado depois de reinício e `kill -9` |
| Dependência de uma única instância | todo estado no banco; trabalho assíncrono com `SKIP LOCKED` e lease; provado com 3 processos e recuperação por outra instância |
| Publicação antes do commit | outbox gravada no commit e publicada depois; provado com morte entre o commit e a publicação |
| Ledger auditável | append-only (permissões + triggers), saldo antes e depois, reconciliação |
| Mocks no lugar de Postgres, SQS ou IdP | todos os testes de integração, e2e e falhas usam os serviços reais |

## 8. O que ficou de fora

- **Opcionais do enunciado não implementados:** tracing distribuído (OpenTelemetry), teste de carga e partidas dobradas.
- **Limitação do ambiente:** o LocalStack gratuito não aplica as políticas IAM, que estão escritas em `deploy/aws/iam/`.
- **Melhoria possível:** compartilhar os containers entre os testes de integração, para reduzir os cerca de 5 minutos da suíte.
