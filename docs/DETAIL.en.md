# One-click app deployment plugin（kore-plugin-app-deploy）

## Overview

This plugin provides real one-click application deployment: 9 built-in templates (including Node.js / Python / PHP / MySQL+phpMyAdmin / Uptime Kuma) start containers through the host’s docker CLI (fixed argv, no shell). Deployment records are stored in `<state>/deployments.json` (0600); `list` joins with `docker ps` to report real runtime state plus a resource usage overview. Deployed applications can be stopped, started, and logged; `remove` requires an explicit `confirm=true` and only deletes containers created by this plugin.

## Protocol surface

Protocol `kore.plugin.v1`, `lifecycle=lazy`, `depth_level=production`, capability `system:admin`. Eleven business methods, whose domain must equal the plugin id `app-deploy`: template deployment `templates` / `deploy` / `list` / `remove`, user artifact `upload` / `deployFromArtifact` / `upgrade` / `jobs`, and post-deploy management `stop` / `start` / `logs`. Handshake stdout is reserved for protocol frames; logs always go to stderr.

## Core capabilities

Templates are local constants with no network or downloads: since 0.4.0 `ports[]` carries `suggested_host_port`, scanning upward from the default port for the first free one and binding only to `127.0.0.1`. A user artifact is at most 32 MiB and each entry rejects absolute paths, `..`, drive letters, backslashes and links; `upgrade` snapshots the old directory before swapping in and rolls back automatically on failure. The control plane `stop` / `start` / `logs` is an optional capability that only touches container names from deployment records, returning `E_UNSUPPORTED` when unavailable.

## Security boundary

A template whitelist keeps images to source constants and `deploy` accepts only template ids; names must start with `kore-app-` and match `^[a-z][a-z0-9-]{0,30}$`; only template-declared environment keys are accepted, with real secrets passed through a 0600 `--env-file` and never in argv; deletion requires `confirm:true` plus a deployment record and re-check of the full container ID, never touching others’ containers; every container carries `--memory` and an equal `--memory-swap` to prevent OOM; bind mounts stay inside `<state>/volumes/`.

## Build and verification

Daily verification uses local `make check` (gofmt → go vet → go test -timeout 200s); before release run `make dist` twice and confirm identical sha256 (archive with `--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` plus `gzip -9n`). Real docker E2E is skipped by default; once enabled with `make e2e`, missing docker fails rather than skips. The state directory comes from `KORE_APP_DEPLOY_STATE`, defaulting to `<root>/state`; non-Linux platforms return `E_UNSUPPORTED`.

## Related links

- [源码](https://github.com/kore-pl/kore-plugin-app-deploy)
- [kore 内核与协议](https://github.com/kore-pl/kore)
- [插件索引](https://github.com/kore-pl/kore-plugins)
