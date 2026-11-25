# Plugin de implantação de aplicações em um clique（kore-plugin-app-deploy）

## Visão geral

Este plugin oferece implantação real de aplicações em um clique: 9 modelos integrados (incluindo Node.js / Python / PHP / MySQL+phpMyAdmin / Uptime Kuma) iniciam contêineres pela CLI do docker do host (argv fixo, sem shell). Os registros ficam em `<state>/deployments.json` (0600); `list` é cruzado com `docker ps` para mostrar o estado real e um resumo de recursos. As aplicações implantadas podem ser paradas, iniciadas e ter seus logs consultados; `remove` exige `confirm=true` explícito e só exclui contêineres criados por este plugin.

## Superfície do protocolo

Protocolo `kore.plugin.v1`, `lifecycle=lazy`, `depth_level=production`, capacidade `system:admin`. Onze métodos de negócio, cujo domínio deve ser igual ao id do plugin `app-deploy`: implantação por modelo `templates` / `deploy` / `list` / `remove`, artefato do usuário `upload` / `deployFromArtifact` / `upgrade` / `jobs` e gerenciamento pós-implantação `stop` / `start` / `logs`. O stdout do handshake é reservado para quadros do protocolo; os logs vão sempre para stderr.

## Funcionalidades principais

Os modelos são constantes locais, sem rede nem downloads: desde 0.4.0 `ports[]` traz `suggested_host_port`, que procura a partir da porta padrão a primeira livre e vincula apenas `127.0.0.1`. Um artefato de usuário tem no máximo 32 MiB e cada entrada rejeita caminhos absolutos, `..`, letras de unidade, barras invertidas e links; `upgrade` gera um snapshot do diretório antigo antes da troca e reverte automaticamente em caso de falha. O plano de controle `stop` / `start` / `logs` é uma capacidade opcional que só atua em nomes de contêiner dos registros e retorna `E_UNSUPPORTED` quando indisponível.

## Limite de segurança

Uma lista de modelos permitidos limita as imagens a constantes do código e `deploy` aceita apenas ids de modelo; os nomes devem começar com `kore-app-` e corresponder a `^[a-z][a-z0-9-]{0,30}$`; apenas chaves de ambiente declaradas pelo modelo são aceitas, com segredos reais via `--env-file` 0600 e nunca em argv; excluir exige `confirm:true`, um registro de implantação e a verificação do ID completo do contêiner, sem tocar em contêineres de terceiros; cada contêiner traz `--memory` e um `--memory-swap` equivalente para evitar OOM; montagens bind ficam dentro de `<state>/volumes/`.

## Build e verificação

A verificação diária usa `make check` local (gofmt → go vet → go test -timeout 200s); antes do lançamento, execute `make dist` duas vezes e confirme sha256 idêntico (arquivo com `--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` mais `gzip -9n`). O E2E real com docker é ignorado por padrão; ao ativá-lo com `make e2e`, a ausência do docker falha em vez de ser ignorada. O diretório de estado vem de `KORE_APP_DEPLOY_STATE`, com padrão `<root>/state`; plataformas não Linux retornam `E_UNSUPPORTED`.

## Links relacionados

- [源码](https://github.com/kore-pl/kore-plugin-app-deploy)
- [kore 内核与协议](https://github.com/kore-pl/kore)
- [插件索引](https://github.com/kore-pl/kore-plugins)
