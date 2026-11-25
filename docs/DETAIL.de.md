# Plugin für die Bereitstellung von Anwendungen mit einem Klick（kore-plugin-app-deploy）

## Überblick

Dieses Plugin bietet echte Bereitstellung von Anwendungen mit einem Klick: 9 integrierte Vorlagen (u. a. Node.js / Python / PHP / MySQL+phpMyAdmin / Uptime Kuma) starten Container über die docker-CLI des Hosts (festes argv, keine Shell). Bereitstellungsdatensätze liegen in `<state>/deployments.json` (0600); `list` wird mit `docker ps` verknüpft, um den echten Laufzeitstatus und eine Ressourcenübersicht zu liefern. Bereitgestellte Anwendungen können gestoppt, gestartet und protokolliert werden; `remove` erfordert ein explizites `confirm=true` und löscht nur von diesem Plugin erstellte Container.

## Protokollfläche

Protokoll `kore.plugin.v1`, `lifecycle=lazy`, `depth_level=production`, Fähigkeit `system:admin`. Elf Geschäftsmethoden, deren Domain der Plugin-ID `app-deploy` entsprechen muss: Vorlagenbereitstellung `templates` / `deploy` / `list` / `remove`, Benutzer-Artefakt `upload` / `deployFromArtifact` / `upgrade` / `jobs` sowie Verwaltung nach der Bereitstellung `stop` / `start` / `logs`. Die stdout des Handshakes ist für Protokollrahmen reserviert; Protokolle gehen immer nach stderr.

## Kernfunktionen

Vorlagen sind lokale Konstanten ohne Netzwerk oder Downloads: Seit 0.4.0 enthält `ports[]` `suggested_host_port`, das ab dem Standardport aufwärts den ersten freien Port sucht und nur an `127.0.0.1` bindet. Ein Benutzer-Artefakt ist höchstens 32 MiB groß; jeder Eintrag lehnt absolute Pfade, `..`, Laufwerksbuchstaben, Backslashes und Links ab; `upgrade` erstellt vor dem Austausch eine Momentaufnahme des alten Verzeichnisses und rollt bei Fehlern automatisch zurück. Die Steuerungsebene `stop` / `start` / `logs` ist eine optionale Fähigkeit, die nur Containernamen aus Bereitstellungsdatensätzen verwendet und bei Nichtverfügbarkeit `E_UNSUPPORTED` zurückgibt.

## Sicherheitsgrenze

Eine Vorlagen-Whitelist beschränkt Images auf Quellkonstanten, und `deploy` akzeptiert nur Vorlagen-IDs; Namen müssen mit `kore-app-` beginnen und `^[a-z][a-z0-9-]{0,30}$` entsprechen; es werden nur von der Vorlage deklarierte Umgebungsvariablen akzeptiert, echte Geheimnisse laufen über eine 0600-`--env-file` und nie in argv; Löschen erfordert `confirm:true` plus einen Bereitstellungsdatensatz und die Prüfung der vollständigen Container-ID, fremde Container werden nie berührt; jeder Container trägt `--memory` und ein gleichwertiges `--memory-swap` gegen OOM; Bind-Mounts bleiben innerhalb von `<state>/volumes/`.

## Build und Verifizierung

Die tägliche Prüfung nutzt lokales `make check` (gofmt → go vet → go test -timeout 200s); vor dem Release `make dist` zweimal ausführen und identische sha256 bestätigen (Archiv mit `--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` plus `gzip -9n`). Der echte docker-E2E ist standardmäßig übersprungen; nach Aktivierung mit `make e2e` führt fehlendes docker zu einem Fehler statt zu einem Skip. Das Statusverzeichnis kommt aus `KORE_APP_DEPLOY_STATE`, Standard `<root>/state`; nicht-Linux-Plattformen geben `E_UNSUPPORTED` zurück.

## Verwandte Links

- [源码](https://github.com/kore-pl/kore-plugin-app-deploy)
- [kore 内核与协议](https://github.com/kore-pl/kore)
- [插件索引](https://github.com/kore-pl/kore-plugins)
