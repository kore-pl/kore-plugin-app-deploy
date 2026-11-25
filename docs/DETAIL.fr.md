# Plugin de déploiement d’applications en un clic（kore-plugin-app-deploy）

## Aperçu

Ce plugin assure un déploiement réel d’applications en un clic : 9 modèles intégrés (dont Node.js / Python / PHP / MySQL+phpMyAdmin / Uptime Kuma) lancent des conteneurs via la CLI docker de l’hôte (argv fixe, sans shell). Les enregistrements sont stockés dans `<state>/deployments.json` (0600) ; `list` est croisé avec `docker ps` pour afficher l’état réel et un aperçu des ressources. Les applications déployées peuvent être arrêtées, démarrées et journalisées ; `remove` exige `confirm=true` explicite et ne supprime que les conteneurs créés par ce plugin.

## Surface du protocole

Protocole `kore.plugin.v1`, `lifecycle=lazy`, `depth_level=production`, capacité `system:admin`. Onze méthodes métier, dont le domaine doit être l’id du plugin `app-deploy` : déploiement par modèle `templates` / `deploy` / `list` / `remove`, artefact utilisateur `upload` / `deployFromArtifact` / `upgrade` / `jobs`, et gestion post-déploiement `stop` / `start` / `logs`. La sortie standard du handshake est réservée aux trames du protocole ; les journaux vont toujours sur stderr.

## Fonctionnalités clés

Les modèles sont des constantes locales, sans réseau ni téléchargement : depuis 0.4.0, `ports[]` porte `suggested_host_port`, qui recherche vers le haut à partir du port par défaut le premier disponible et ne lie que `127.0.0.1`. Un artefact utilisateur fait au plus 32 Mio et chaque entrée rejette les chemins absolus, `..`, les lettres de lecteur, les antislashs et les liens ; `upgrade` snapshotte l’ancien répertoire avant l’échange et revient automatiquement en arrière en cas d’échec. Le plan de contrôle `stop` / `start` / `logs` est une capacité optionnelle qui ne touche que les noms de conteneurs des enregistrements et renvoie `E_UNSUPPORTED` si indisponible.

## Périmètre de sécurité

Une liste blanche de modèles limite les images aux constantes du code et `deploy` n’accepte que les ids de modèle ; les noms doivent commencer par `kore-app-` et correspondre à `^[a-z][a-z0-9-]{0,30}$` ; seules les clés d’environnement déclarées par le modèle sont acceptées, les vrais secrets passant par un `--env-file` 0600 jamais dans argv ; la suppression exige `confirm:true` plus un enregistrement et la vérification de l’ID complet du conteneur, sans toucher aux conteneurs d’autrui ; chaque conteneur porte `--memory` et un `--memory-swap` équivalent pour éviter l’OOM ; les montages bind restent dans `<state>/volumes/`.

## Compilation et vérification

La vérification quotidienne utilise `make check` en local (gofmt → go vet → go test -timeout 200s) ; avant publication, exécutez `make dist` deux fois et vérifiez l’identité du sha256 (archive avec `--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` plus `gzip -9n`). L’E2E docker réel est ignoré par défaut ; une fois activé avec `make e2e`, l’absence de docker échoue au lieu d’être ignorée. Le répertoire d’état est défini par `KORE_APP_DEPLOY_STATE`, par défaut `<root>/state` ; les plateformes non Linux renvoient `E_UNSUPPORTED`.

## Liens connexes

- [源码](https://github.com/kore-pl/kore-plugin-app-deploy)
- [kore 内核与协议](https://github.com/kore-pl/kore)
- [插件索引](https://github.com/kore-pl/kore-plugins)
