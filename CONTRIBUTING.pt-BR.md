# Contribuindo

[English](CONTRIBUTING.md) | **Português**

Obrigado por ajudar. Este repositório é opinativo de propósito, então o critério para uma mudança é um motivo, não uma preferência.

## Mudando uma regra

1. Abra uma issue primeiro. Diga qual regra, o que quebra quando as pessoas a seguem e o que a regra deveria dizer.
2. Com o acordo feito, abra um PR que atualiza a regra, os exemplos e o checklist juntos. Regra que muda sem os exemplos não é aceita.
3. Nunca enfraqueça uma regra para encaixar um exemplo ruim. Corrija o exemplo.

## Adicionando uma skill

1. Veja a lista de planejadas no README e abra uma issue dizendo qual decisão a skill trata. Uma decisão por skill.
2. Copie [templates/SKILL.template.md](templates/SKILL.template.md) para `skills/<nome>/SKILL.md`. O nome da pasta e o campo `name` precisam ser iguais.
3. Escreva a `description` como "Use when ..." para o agente saber quando carregá-la. A skill é escrita em inglês.
4. Adicione `examples/good/` e `examples/bad/`, cada um com o próprio `go.mod`:
   - module path `github.com/rafapeixer/go-skills/skills/<nome>/examples/<good|bad>`
   - diretiva `go 1.26.0`
   - menos de 100 linhas
5. Adicione a skill ao índice **nos dois** arquivos, `README.md` e `README.pt-BR.md`.

## Verificações locais

Rode as duas antes de dar push. O CI roda os mesmos scripts.

```bash
bash scripts/validate.sh       # formato: frontmatter, seções, tamanho, índice do README
bash scripts/test-examples.sh  # good: gofmt + go vet + go test; bad: go build
```

## Commits e PRs

- Uma skill por PR. Uma mudança lógica por commit.
- Mensagens de commit, em inglês: `skill(errors): add rule on wrapping with operation name`, `docs(readme): update skill index`, `ci: test against Go stable`.
- Qualquer mudança em um README ou CONTRIBUTING atualiza a versão em inglês ou português no mesmo commit.
- Preencha o template de pull request: o que muda, o que quebra sem a mudança e as fontes primárias.
- Quando conferir ou mudar os fatos de uma skill, coloque essa data em `metadata.verified` no frontmatter dela.
