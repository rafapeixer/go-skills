# go-skills

[English](README.md) | **Português**

**Skills opinativas que ensinam agentes de código a escrever Go idiomático e pronto para produção.**

Agentes de código conhecem a sintaxe de Go. O que eles não sabem é *qual* caminho seguir quando a linguagem oferece vários. Sozinhos, escrevem Go com cara de Java ou Python: camadas de serviço inchadas, erros engolidos ou embrulhados demais, goroutines sem dono, mock para tudo.

Cada skill aqui trata de **uma decisão de design**, toma uma posição clara sobre ela, mostra um exemplo bom e um ruim e dá ao agente um checklist para revisar o próprio resultado. Todo exemplo compila e é testado no CI.

Funciona com Claude Code, Cursor, Codex e qualquer agente que leia arquivos `SKILL.md`.

---

## Instalação

### Plugin do Claude Code (recomendado)

```
/plugin marketplace add rafapeixer/go-skills
/plugin install go-skills@go-skills
```

As skills carregam sozinhas quando a tarefa corresponde, ou pelo nome: `/go-skills:errors`. As atualizações chegam a cada release.

### Claude Code, copiando os arquivos

No projeto (versionado junto com o seu repositório):

```bash
git clone https://github.com/rafapeixer/go-skills.git /tmp/go-skills
mkdir -p .claude/skills
cp -r /tmp/go-skills/skills/* .claude/skills/
```

Pessoal (disponível em todos os projetos):

```bash
cp -r /tmp/go-skills/skills/* ~/.claude/skills/
```

### CLI universal de skills

Se você usa a CLI `skills`, que suporta a maioria dos agentes:

```bash
npx skills add rafapeixer/go-skills
```

### Qualquer outro agente

Copie a pasta `skills/` para o local de onde o seu agente carrega skills, ou aponte o agente para este repositório. Cada skill é uma pasta independente com um `SKILL.md` na raiz.

---

## Skills

> A v1, a v1.1 e a v2 estão prontas: 24 skills. Novas skills entram uma por PR.

### v1

| Skill | Use quando | Status |
|---|---|---|
| [project-layout](skills/project-layout/SKILL.md) | Criar um serviço, adicionar um pacote, decidir onde fica uma interface ou um helper, decidir se divide em serviços | Pronta |
| [errors](skills/errors/SKILL.md) | Criar, embrulhar, retornar ou inspecionar erros | Pronta |
| [context-and-deadlines](skills/context-and-deadlines/SKILL.md) | Passar ou criar um `context.Context`, definir timeouts de requisição ou de saída, rodar trabalho depois da resposta | Pronta |
| [concurrency](skills/concurrency/SKILL.md) | Iniciar goroutines, distribuir trabalho sobre uma lista, rodar workers em segundo plano, compartilhar estado entre goroutines | Pronta |
| [http-server](skills/http-server/SKILL.md) | Montar, proteger ou desligar um servidor net/http: timeouts, rotas, limite de corpo, health checks, CSRF | Pronta |
| [postgres-pgx-pool](skills/postgres-pgx-pool/SKILL.md) | Escolher o driver Postgres, dimensionar o pool do pgx, colocar o PgBouncer na frente, tratar o esgotamento do pool | Pronta |
| [secure-defaults](skills/secure-defaults/SKILL.md) | Gerar tokens, abrir caminhos vindos do usuário, configurar TLS, criptografar, renderizar HTML, definir cookies | Pronta |
| [testing](skills/testing/SKILL.md) | Escrever ou revisar testes: dublês, testes de banco, código dependente de tempo, HTTP end-to-end, fuzzing, benchmarks | Pronta |

### v1.1

| Skill | Use quando | Status |
|---|---|---|
| [sql-queries-and-transactions](skills/sql-queries-and-transactions/SKILL.md) | Escrever queries ou transações no Postgres com pgx: sqlc, paginação keyset, N+1, retry de falhas de serialização, outbox | Pronta |
| [db-migrations](skills/db-migrations/SKILL.md) | Criar migrations de schema, decidir onde rodam no deploy, alterar uma tabela com tráfego em produção | Pronta |
| [logging-slog](skills/logging-slog/SKILL.md) | Logar com log/slog: logger injetado, request ID via context, redação de segredos, chaves JSON, níveis e amostragem | Pronta |
| [ci-guardrails](skills/ci-guardrails/SKILL.md) | Escrever pipelines de CI ou hooks de agente: gofmt/vet/go fix/race, govulncheck, golangci-lint v2, fixação de ferramentas, builds de release | Pronta |
| [container-runtime-tuning](skills/container-runtime-tuning/SKILL.md) | Escrever Dockerfile ou manifesto Kubernetes, definir limites de CPU e memória, escolher GOMAXPROCS, GOMEMLIMIT ou GOGC | Pronta |
| [overload-protection](skills/overload-protection/SKILL.md) | Decidir o que o serviço faz acima da capacidade: limitar requisições em voo, rejeitar com 503, retries, bulkheads | Pronta |
| [outbound-http-client](skills/outbound-http-client/SKILL.md) | Chamar outro serviço por HTTP: montar client e transport, ler corpos de resposta, buscar URLs vindas do usuário | Pronta |
| [go-version-policy](skills/go-version-policy/SKILL.md) | Escolher as linhas `go` e `toolchain`, atualizar o Go, definir `GOTOOLCHAIN` no CI, fixar ferramentas | Pronta |

### v2

| Skill | Use quando | Status |
|---|---|---|
| [observability-otel](skills/observability-otel/SKILL.md) | Adicionar traces, métricas ou logs OpenTelemetry: setup e shutdown do SDK, otelhttp, nomes de span e cardinalidade, sampling, views, ponte com slog | Pronta |
| [performance-profiling](skills/performance-profiling/SKILL.md) | Medir antes de otimizar: pprof num listener interno, snapshots do flight recorder, default.pgo, escape analysis, sync.Pool, custo por requisição | Pronta |
| [sqlite-single-node](skills/sqlite-single-node/SKILL.md) | Usar SQLite como banco de um serviço de nó único: driver, pragmas por conexão, um writer e um pool de leitura, BEGIN IMMEDIATE, Litestream | Pronta |
| [background-jobs-outbox](skills/background-jobs-outbox/SKILL.md) | Executar trabalho depois de uma escrita: River InsertTx ou outbox, workers com SKIP LOCKED, LISTEN/NOTIFY, retries, shutdown de workers | Pronta |
| [scale-to-zero](skills/scale-to-zero/SKILL.md) | Rodar em Lambda, Cloud Run, Knative ou Fly Machines: custo de startup, dependências preguiçosas, SIGTERM dentro do prazo, build para Lambda | Pronta |
| [json-v2](skills/json-v2/SKILL.md) | Serializar ou decodificar JSON, omitzero ou omitempty, marshalers próprios, migrar para Go 1.27 e encoding/json/v2 | Pronta |
| [wasm-plugins](skills/wasm-plugins/SKILL.md) | Permitir que terceiros estendam o serviço: wazero em vez de plugin ou exec, guests wasip1, capacidades, limites de CPU e memória | Pronta |
| [mcp-server-go](skills/mcp-server-go/SKILL.md) | Expor um serviço Go a agentes via MCP: go-sdk oficial, ferramentas tipadas, streamable HTTP stateless, tokens OAuth bearer, limites por chamada | Pronta |

Uma skill por PR; veja [CONTRIBUTING.pt-BR.md](CONTRIBUTING.pt-BR.md).

---

## Muda o que os agentes escrevem?

Medido com `claude plugin eval` ([`evals/`](evals/)): o mesmo prompt roda com e sem o plugin, e graders por regex conferem o arquivo que o agente escreve. Duas execuções por lado, outubro de 2026.

| Tarefa | Sem plugin | Com plugin | O que mudou |
|---|---|---|---|
| Buscar 10.000 preços em paralelo, parar no primeiro erro | 0,33 | **1,00** | `errgroup` com `SetLimit` em vez de uma goroutine por ID |
| Criar o pool do pgx para 8 réplicas e orçamento de 200 conexões | 0,50 | **1,00** | `MaxConns` calculado pelo orçamento, jitter no tempo de vida, `pgxpool` sempre |
| Deployment Kubernetes para uma API Go | 0,67 | **1,00** | Limite de CPU definido, para o `GOMAXPROCS` acompanhar o container |
| Servidor HTTP de produção | 1,00 | 1,00 | O modelo já define timeouts e shutdown |
| Converter erros em status HTTP | 1,00 | 1,00 | O modelo já usa sentinelas e `errors.Is` |
| Token de redefinição de senha | 1,00 | 1,00 | O modelo já usa `crypto/rand` |

O plugin carregou a skill certa em todas as execuções. Onde o modelo já acerta, as skills mantêm o acerto; o ganho aparece nas decisões que exigem conhecimento atual e específico. A suíte cresce a cada skill.

---

## Como é uma skill

Todo `SKILL.md` tem o mesmo formato:

1. **The decision** — qual escolha a skill trata
2. **The rule** — a posição deste repositório, em regras numeradas de uma linha
3. **Why** — o raciocínio e o custo de fazer diferente
4. **Good example** — código executável em `examples/good/`
5. **Bad example** — o erro real, em `examples/bad/`, com cada problema comentado
6. **Checklist** — itens de sim/não que o agente aplica ao revisar
7. **When not to apply** — as exceções, porque toda regra tem

As skills em si são escritas em inglês, para um público global. Os títulos das seções ficam em inglês porque são verificados pelo CI.

Skills são curtas de propósito (100 a 400 linhas). Se uma passar disso, ela é dividida.

---

## Princípios

- **Uma decisão por skill.** Nada de "boas práticas" genéricas.
- **Uma posição, com motivos.** "Depende" só é aceito se vier seguido de "do quê, e o que fazer em cada caso".
- **Biblioteca padrão primeiro.** Pacotes de terceiros só aparecem onde a biblioteca padrão não tem resposta razoável (`pgx`, `errgroup`), e a skill explica por quê.
- **Go atual.** `log/slog`, generics, `errors.Join`, range-over-func. Os exemplos exigem Go 1.26 ou superior (a versão mais antiga que ainda recebe correções de segurança); o CI também testa contra a última versão estável.
- **Exemplos rodam.** O CI executa `gofmt`, `go vet` e `go test` em todo exemplo bom, e `go build` em todo exemplo ruim.

---

## Contribuindo

Discorda de uma regra? Abra uma issue e defenda o seu ponto. Regras mudam quando alguém mostra o que quebra, não quando alguém prefere outra coisa.

Quer adicionar uma skill? Comece pelo [templates/SKILL.template.md](templates/SKILL.template.md), siga o [CLAUDE.md](CLAUDE.md), uma skill por PR.

---

## Licença

MIT
