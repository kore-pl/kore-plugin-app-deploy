# Complemento de despliegue de aplicaciones en un clic（kore-plugin-app-deploy）

## Resumen

Este complemento ofrece despliegue real de aplicaciones en un clic: 9 plantillas integradas (Node.js / Python / PHP / MySQL+phpMyAdmin / Uptime Kuma) inician contenedores mediante la CLI de docker del host (argv fijo, sin shell). Los registros se guardan en `<state>/deployments.json` (0600); `list` se cruza con `docker ps` para mostrar el estado real y un resumen de recursos. Las aplicaciones desplegadas pueden detenerse, iniciarse y consultar sus registros; `remove` exige `confirm=true` explícito y solo elimina contenedores creados por este complemento.

## Superficie del protocolo

Protocolo `kore.plugin.v1`, `lifecycle=lazy`, `depth_level=production`, capacidad `system:admin`. Once métodos de negocio, cuyo dominio debe ser el id del complemento `app-deploy`: despliegue por plantilla `templates` / `deploy` / `list` / `remove`, paquete de usuario `upload` / `deployFromArtifact` / `upgrade` / `jobs` y gestión posterior `stop` / `start` / `logs`. La salida estándar del handshake se reserva para tramas del protocolo; los registros van siempre a stderr.

## Funcionalidades principales

Las plantillas son constantes locales, sin red ni descargas: desde 0.4.0 `ports[]` incluye `suggested_host_port`, que busca hacia arriba desde el puerto predeterminado el primero libre y solo enlaza `127.0.0.1`. Un paquete de usuario mide como máximo 32 MiB y cada entrada rechaza rutas absolutas, `..`, letras de unidad, barras invertidas y enlaces; `upgrade` instantánea el directorio antiguo antes de intercambiar y revierte automáticamente si falla. El plano de control `stop` / `start` / `logs` es una capacidad opcional que solo opera con nombres de contenedor de los registros y devuelve `E_UNSUPPORTED` cuando no está disponible.

## Límite de seguridad

Una lista blanca de plantillas limita las imágenes a constantes del código y `deploy` solo acepta ids de plantilla; los nombres deben empezar por `kore-app-` y coincidir con `^[a-z][a-z0-9-]{0,30}$`; solo se aceptan claves de entorno declaradas en la plantilla, y los secretos reales van en un `--env-file` 0600, nunca en argv; borrar exige `confirm:true` más un registro y la verificación del ID completo del contenedor, sin tocar contenedores ajenos; cada contenedor lleva `--memory` y un `--memory-swap` equivalente para evitar OOM; los montajes bind permanecen dentro de `<state>/volumes/`.

## Compilación y verificación

La verificación diaria usa `make check` local (gofmt → go vet → go test -timeout 200s); antes de publicar, ejecuta `make dist` dos veces y confirma un sha256 idéntico (archivo con `--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` más `gzip -9n`). El E2E real con docker se omite por defecto; al activarlo con `make e2e`, la ausencia de docker falla en lugar de omitirse. El directorio de estado se define con `KORE_APP_DEPLOY_STATE` (por defecto `<root>/state`); las plataformas no Linux devuelven `E_UNSUPPORTED`.

## Enlaces relacionados

- [源码](https://github.com/kore-pl/kore-plugin-app-deploy)
- [kore 内核与协议](https://github.com/kore-pl/kore)
- [插件索引](https://github.com/kore-pl/kore-plugins)
