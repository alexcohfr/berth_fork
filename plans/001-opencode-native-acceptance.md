# Recette OpenCode natif — 2026-10-09

**STATUS: COMPLETE** — implémentation des lots 0–7 livrée et contrôles exécutés réussis, avec les limites fonctionnelles et de recette détaillées ci-dessous.

## Périmètre et identité Git

- Worktree : `/Users/alexandrecohen/projets/berth_fork-opencode-native`.
- Branche : `feat/opencode-native-workspace`.
- Baseline conservée : `a175393fbc010b2cc0af1253ead4a4a2d3d244fe`.
- Investigation plugin conservée : `b3a3aab8f39d7893f5a906284d6a102952e97062`.
- Le SHA final est celui du commit contenant ce compte rendu, donné dans la réponse de livraison (`git rev-parse HEAD`).
- Les modifications du reviewer dans le plan et son README sont conservées. Aucun changement de leurs statuts par l'implémenteur.
- Le dépôt initial, `site/`, les wallpapers, les services personnels et les données durables personnelles restent hors de cette livraison.
- Commit local uniquement ; aucun push, aucune PR ou publication.

## Lots et preuves

### Lot 0 — Propriétaire et transport

Implémenté : un lanceur `berthd opencode mini` dans tmux supervise un serveur `serve --service --hostname 127.0.0.1 --port 0`, puis Mini. Seul `XDG_STATE_HOME` est isolé par runtime en production ; les emplacements utilisateur de configuration et de données sont conservés. Le registre privé fournit les identifiants Basic utilisés par Mini et le client Go. Le plugin reste un producteur de hooks.

Preuves réelles : PID du registre, refus HTTP non authentifié, même session vue par Mini et API, reconstruction par un nouveau client Box, deux propriétaires dans le même répertoire avec environnements distincts, interruption/permission ciblées, arrêt du serveur possédé. Identité vérifiée : tâche + instance + session native + répertoire. Verrou de fichier contre deux propriétaires Shipyard de la même conversation.

Limite : la reconstruction simule le remplacement du client backend ; elle ne redémarre pas un service personnel. Un lanceur shell réécrivant HOME/XDG est refusé ; `BERTH_OPENCODE_BIN` sélectionne explicitement l'exécutable compatible.

### Lot 1 — Envoi, arrêt, reconnexion

Implémenté : `sendPrompt` branche vers HTTP avant toute saisie terminal ; IDs dérivés de la session native et de la clé de retry ; reçus avec empreinte du payload conservés hors du dossier de runtime ; attente native, interruption et réconciliation des tours par messages/inbox. La file laptop conserve les pièces jointes/skills/IDs et le mode de livraison à travers son redémarrage. Une livraison tentée ne peut plus être réécrite ou redirigée. Une commande à résultat incertain n'est pas répétée automatiquement.

Preuves : admission concurrente et retry, réponse perdue après admission, événement SSE réel sans contenu de prompt dans le journal, invalidation/relecture, queue persistante synthétique, reconnexion navigateur et absence de `/keys`. Le watcher valide la continuité du propriétaire et expire quand il n'est plus utile. Les hooks de création et d'envoi restent appliqués ; le refus de la première instruction OpenCode est testé aux portées box et projet, avec et sans pièce jointe.

### Lot 2 — Permissions et formulaires

Implémenté : décisions natives `once`, `always`, `reject` ; IDs session/demande vérifiés ; champs texte, nombre, entier, booléen, multichoix et conditions. Les types externes/inconnus renvoient explicitement au terminal. Les enfants peuvent rendre leur parent « attente utilisateur » sans le terminer.

Preuves : formulaire réel, permissions sur deux runtimes réels, requête étrangère refusée, réponses UI avec identité, contraintes natives/HTML et retour de focus, réconciliation enfant en attente → parent actif → terminé. Les réponses natives ne passent pas par `send-keys`.

### Lot 3 — Catalogues, skills et fichiers

Implémenté : profils internes et modèles/variantes du runtime ; commandes enregistrées via `/command` ; skills structurés ; URI de fichiers validées sur la box et support média déclaré par le modèle. Première image d'une tâche copiée dans le nouveau worktree avant admission avec un ID stable. Le composeur des agents actifs transmet aussi les fichiers structurés lorsque les agents partagent le worktree.

Preuves : fournisseur/variante au lancement ; profil effectif ; commande avec arguments et rejet d'une commande inconnue ; skill de projet réellement transmis au faux fournisseur ; image réellement reçue par celui-ci ; accents/espaces/`#`/`%` littéral, fichier absent, sortie du worktree et modèle sans support média ; installation générale, distinction utilisateur/projet, XDG et override OpenCode.

Limite explicite : pour les boucles, essais multiples, handoffs et commandes de lancement personnalisées, joindre les fichiers depuis la conversation après lancement. Ces composeurs ne transforment plus silencieusement une pièce jointe OpenCode en simple texte. Une diffusion de fichiers entre worktrees différents est refusée.

### Lot 4 — Historique, reprise, contexte et enfants

Implémenté : curseurs opaques, chargement des pages antérieures, comblement borné des pages manquées, génération invalidant les réponses anciennes ; enfants consultables avec contrôle de parenté ; recherche et reprise de l'identité durable ; usage/coût natifs et compaction suivie.

Preuves : fixture réelle de plus de 250 messages comparée par IDs, pagination enfant, lecture après sortie du terminal, reprise par route Shipyard, refus du second propriétaire, reconnexion navigateur comblant deux pages et remplacement après changement de génération. La jauge d'occupation de contexte reste indisponible plutôt que calculée avec une heuristique de modèle.

### Lot 5 — Réglages et transfert

Implémenté : projections expurgées des sources de configuration/MCP/plugins/connexions, recharge explicite, connexion par clé ou OAuth public, suivi persistant des tentatives OAuth, édition projet existante avec validation JSONC et contrôle de version. Transfert choisi utilisateur/projet vers un projet destinataire, chemins/contenus éditables, dépendances relatives vérifiées, aperçu avant écriture et conservation du bit exécutable des scripts.

Preuves synthétiques : `needs_auth`, reprise/annulation OAuth par un nouveau client backend, sentinelle secrète absente des réponses, code OAuth absent du stockage navigateur, JSONC préservé, fichiers concurrents refusés, symlinks/chemins traversants/credentials/dépendances manquantes rejetés ; parcours UI export → aperçu destination → application.

Limites : comptes et OAuth synthétiques uniquement ; aucun fournisseur/MCP personnel interrogé. Le transfert exclut les configurations de fournisseurs, credentials et bases de sessions. Atomicité par fichier, pas transaction multi-fichiers. Les outils Mac ne sont pas rendus portables par copie de fichiers. Les formulaires d'authentification externes/commandes et extensions TUI restent dans le terminal.

### Lot 6 — Fork, rewind et suivi Shipyard

Implémenté : fork par ID de message, lié à sa propre tâche/runtime, première instruction native et reçu anti-duplication ; prévisualisation, stage, annulation et confirmation de rewind. Les forks explicitement liés sont exclus des workers attendus par le parent. Les flows utilisent la même admission et les états natifs.

Preuves réelles : route Shipyard de fork avec première instruction et retry, refus du second propriétaire, parent conservé, compaction, stage/clear/commit ; fichier utilisateur préservé. Preuve UI : frontière native sélectionnée et annulation du rewind.

Limite prévue au plan : restauration des fichiers désactivée, car la préservation des modifications externes et des outils shell n'a pas été démontrée. Seule la conversation est modifiée ; aucun reset Git ou accès direct à la base OpenCode.

### Lot 7 — Compatibilité et activation

Implémenté : capacités par schéma du runtime, terminal conservé, noms accessibles, navigation/focus des contrôles, documentation API/guide et changelog. Version effectivement exécutée : OpenCode **2.0.18**.

La recette est répartie entre le contrat réel isolé et les parcours navigateur contre fake-agent. Ce n'est pas une exécution monolithique de tous les écrans contre une box Linux distante. Les outils preview/review existants sont réutilisés ; les tests Go généraux et les specs chat/feed/crew/drop vérifient leur voisinage sans modifier ces fonctionnalités.

## Environnements et commandes

macOS 27.0 arm64 ; OpenCode 2.0.18 ; Go 1.27.2 ; Rust/Cargo 1.93.0 ; Node 22.23.1 ; pnpm 10.28.1 ; tmux 3.7c. Go et ses caches sont locaux au worktree :

```sh
export PATH="$PWD/app/node_modules/.shipyard-tools/go/bin:$PATH"
export GOPATH="$PWD/app/node_modules/.shipyard-tools/gopath"
export GOCACHE="$PWD/app/node_modules/.shipyard-tools/gocache"
```

Contrat réel : HOME/XDG/TMPDIR/config/base isolés ; environnement hérité nettoyé ; fournisseur HTTP local `acme` ; aucune dépense modèle. Exécutable utilisé : `/Users/alexandrecohen/.local/share/opencode/bin/opencode`.

```sh
go vet ./...
go test -race ./...
OPENCODE_TEST_BIN=/Users/alexandrecohen/.local/share/opencode/bin/opencode go test -race ./internal/box -run '^TestOpenCodeLiveV2Contract$' -count=1 -v
go test -race ./internal/box -run 'OpenCode|Gate' -count=1
```

Le contrat opt-in est ignoré par le test général sans variable ; il est exécuté séparément, sans SKIP. Une répétition réelle `-count=5` a également passé avant l'ajout de l'assertion de transmission du skill au fournisseur ; cette assertion passe dans la dernière exécution.

Contrôles Rust/app/docs :

```sh
(cd app/src-tauri && cargo check)
(cd app && CI=true pnpm install --frozen-lockfile)
(cd app && pnpm typecheck:plugins && pnpm check:titles && pnpm check:csp && pnpm check:themes && pnpm test && pnpm build)
(cd app && npx tsc -p e2e)
(cd docs-site && pnpm build)
```

Chaque contrôle a passé. App : 271 tests unitaires et 24 contrôles de thèmes. Le build docs produit 93 pages et conserve l'avertissement préexistant de police dynamique `⌘` (HTTP 400), sans échec.

Navigateur, après vérification que le port 1434 est libre, dans `app` :

```sh
E2E_PORT=1434 E2E_WORKERS=2 BERTH_E2E_LIVE=0 npx playwright test e2e/opencode.spec.ts e2e/composer.spec.ts --repeat-each=5
E2E_PORT=1434 E2E_WORKERS=2 BERTH_E2E_LIVE=0 npx playwright test e2e/opencode.spec.ts e2e/composer.spec.ts e2e/drop.spec.ts e2e/chat-feed.spec.ts e2e/crew.spec.ts
```

Première commande : **50/50 réussis**. Deuxième commande : **24/24 réussis**. Dernier contrôle Go général : `go vet ./...` et `go test -race ./...` réussis.

Après les derniers correctifs de retry et d'encodage d'URI : nouveau build app, compilation TypeScript e2e et `E2E_PORT=1434 E2E_WORKERS=2 BERTH_E2E_LIVE=0 npx playwright test e2e/opencode.spec.ts e2e/composer.spec.ts` : **10/10 réussis**, dont le nom de fichier contenant un `%20` littéral. Le contrat réel et la suite Go générale ont aussi été rejoués après les modifications backend : exit 0.

## Écarts, incidents et limites de validation

- Le transport HTTP public remplace le pont plugin conformément à la révision approuvée. Les méthodes absentes du plugin restent documentées comme preuve historique.
- `cmd/berthd` et son routage existant hébergent le lanceur ; aucun nouveau service installé. `start-work.ts` et `broadcast.ts` portent uniquement les données de pièces jointes jusqu'aux routes existantes ; `main_test.go` expose ce lanceur au binaire de tests.
- Les passes communes ont été refaites sur l'intégration finale ; leur passage strict à chaque frontière de lot n'est pas revendiqué rétrospectivement.
- Les premières recettes d'image ont révélé une fixture non commitée, puis une comparaison de chemins non canoniques et un délai transitoire non reproduit dans les cinq répétitions suivantes. Le diagnostic du terminal est conservé en cas de récidive.
- Les premières assertions navigateur confondaient plusieurs messages assistant regroupés et leurs libellés visibles. La fixture de pagination et les sélecteurs de messages ont été corrigés ; cinq répétitions passent.
- La revue finale a corrigé le changement involontaire de mode de livraison lors d'un passage en queue après un envoi média incertain, la portée projet des nouveaux appels de gates et l'encodage des `%` littéraux dans les URI. Les tests de queue, de refus de première instruction et de pièces jointes navigateur couvrent ces cas.
- Linux et une autre version V2 restent non exécutés. Ce sont des limites de matrice, pas une validation implicite.
- Pas de test réel de comptes OAuth/MCP personnels, ni de migration de données, ni de restauration de fichiers. Les composants serveur OpenCode continuent à exécuter leurs propres extensions.
- Artefacts générés de compilation TypeScript/Vite identifiés puis retirés du diff ; dépendances et journaux de validation restent dans `node_modules`, non commités.

Journaux locaux : `app/node_modules/.shipyard-tools/` (`full-go.log`, `targeted-final.log`, `live-contract.log`, `live-repeat.log`, `cargo-check.log`, `app-checks-final.log`, `app-tests-final.log`, `app-build.log`, `docs-build.log`, `browser-repeat.log`, `browser-tests.log`, `browser-final.log`).

## Fichiers de livraison

La liste exhaustive est celle de `git show --name-only` sur le commit final. Zones modifiées :

- `cmd/berthd/main.go`.
- `internal/agent/queue.go`, `queue_test.go`.
- `internal/box/api.go`, `commands.go`, `controls.go`, `history.go`, `main_test.go`, `opencode.go`, `opencode_test.go`, `orchestrate.go`, `runhost.go`, `sessions.go`, `skills.go`, `tasks.go`, `turns.go`, `worktreefiles.go`.
- `internal/box/opencode_catalog.go`, `opencode_events.go`, `opencode_history.go`, `opencode_native.go`, `opencode_native_test.go`, `opencode_revert.go`, `opencode_runtime.go`, `opencode_settings.go`, `opencode_transfer.go`.
- `internal/integrations/accounts.go`, `skills.go`, `skills_test.go`, `adapters/opencode.js`.
- `internal/transcript/opencode.go`, `opencode_test.go`, `transcript.go`.
- `app/src/lib/api.ts`, `broadcast.ts`, `history.ts`, `opencode.ts`, `queue.ts`, `skills.ts`, `start-work.ts`, `transcript-feed.ts`.
- `app/src/components/conversation/chat-controls.tsx`, `conversation-pane.tsx`, `conversation-view.tsx`, `opencode-controls.tsx`, `opencode-settings.tsx`, `prompt-actions.tsx`, `subagent-view.tsx`, `task-composer.tsx`.
- `app/src/components/skills/skills-panel.tsx`, `app/src/views/settings/agents-section.tsx`.
- `app/e2e/composer.spec.ts`, `opencode.spec.ts`.
- `docs/changelog.mdx`, `docs/guides/agent-integrations.mdx`, `docs/reference/app-api.mdx`.
- `plans/001-opencode-native-workspace.md`, `plans/README.md` (révision reviewer préservée), et le présent compte rendu.
