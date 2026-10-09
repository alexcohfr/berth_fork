# Plan 001 — Utiliser Shipyard comme interface complète d’OpenCode

> Plan d’implémentation, pas implémentation réalisée. Lire entièrement avant de commencer.
> Exécuter un lot à la fois ; valider ses tests et les contrôles communs avant de passer au suivant.
> En cas de condition STOP, consigner le blocage dans `plans/README.md` plutôt que contourner le contrat.

## Statut et intention

- **Statut** : TODO.
- **Priorité** : P1 ; lots 0 à 3 indispensables à l’usage quotidien.
- **Effort global** : L, plusieurs PR ; estimations relatives par lot ci-dessous.
- **Risque** : élevé pour le transport, la livraison des messages et le retour arrière ; moyen ailleurs.
- **Catégorie** : direction / intégration OpenCode V2.
- **Dépendances externes** : version OpenCode V2 compatible sur chaque machine d’exécution.
- **Plan établi le** : 2026-10-08, commit `bc92f1c`, branche `feat/opencode-v2-harness`, avec modifications locales non commitées.
- **Confiance** : élevée sur les lacunes constatées dans le code ; transport de contrôle à valider au lot 0.

Demande : utiliser Berth, nommé Shipyard dans l’interface, avec les avantages d’OpenCode et de Shipyard réunis. Interprétation retenue : faire de Shipyard l’interface quotidienne d’un véritable moteur OpenCode, local ou distant, en conservant les autres agents déjà pris en charge.

**Résultat attendu :** choisir une machine et un worktree, retrouver ses agents/modèles/skills/MCP, discuter, autoriser les actions, joindre des fichiers, suivre les sous-agents, reprendre une ancienne conversation et revoir les changements depuis Shipyard. La fermeture de l’app ne coupe pas une tâche sur une box encore allumée.

## 1. Répartition des responsabilités

Chemin d’exécution cible : **UI Shipyard → agent laptop → berthd de la box → processus OpenCode propriétaire de la session → fournisseur du modèle**.

- **OpenCode** conserve la construction des prompts, `AGENTS.md`, les agents Build/Plan/personnalisés, les outils, MCP, skills, plugins serveur, permissions, contexte, compaction et historique de conversation.
- **Shipyard** conserve les machines, worktrees, ports, services, terminaux, files hors ligne, tâches, notifications, orchestration inter-tâches et revue Git.
- **Le plugin existant** relie les identités et événements ; il ne remplace pas le prompt système ou les outils d’OpenCode.
- **L’UI** est une vue. Elle ne devient ni le propriétaire du processus OpenCode ni une seconde base de conversations.
- L’identité opérationnelle est le tuple **box + session Shipyard + instance OpenCode + session OpenCode + répertoire**. Un chemin de dossier seul ne suffit jamais.
- Un sous-agent OpenCode reste un enfant de sa conversation. Une tâche Shipyard reste une unité de travail sur une machine/worktree. L’interface montre leur lien sans les confondre.

Cette direction suit `docs/concepts/architecture.mdx:10-24` : « The box serves, the laptop connects », peu de dépendances et sessions survivant au daemon. Elle ne promet pas de poursuivre du calcul sur un Mac local mis en veille : il faut une box distante qui reste active.

## 2. État actuel vérifié

### Déjà présent — conserver

- Lancement du vrai OpenCode V2 dans tmux : `internal/box/tasks.go:121-127`.
  ```go
  if p.ID == "opencode" && p.Command == "opencode" {
      cmd += " mini --standalone"
  }
  ```
- Plugin V2, identification des sessions et états de tours : `internal/integrations/adapters/opencode.js:7-41`. Il filtre les sessions d’autres répertoires et les enfants.
- Chat natif et détails d’outils à la demande : `internal/box/opencode.go:75-120`, `internal/transcript/opencode.go`.
- Choix dynamique des modèles et variantes avant lancement : `internal/box/opencode.go:30-64`, `app/src/components/conversation/composer-pickers.tsx`, `app/e2e/composer.spec.ts:10-70`. Ce travail comprend des modifications locales non commitées.
- Worktrees, revue, navigateur de prévisualisation, pièces jointes, orchestration et file hors ligne existent déjà. Les étendre plutôt que créer une seconde UI parallèle.

### Manques concrets — fondement des lots

1. **Contrôle du processus actif absent de cette intégration.** `openCodeRead` lance `opencode api --standalone` pour lire le stockage durable (`internal/box/opencode.go:124-135`). Cela ne fournit pas un canal vers le processus qui exécute le tour. L’API OpenCode documente explicitement que `session.interrupt` agit sur l’exécution détenue par le processus appelé.
2. **Envoi encore via terminal.** `internal/box/orchestrate.go:221` utilise `b.Sessions.Send(...)`, qui colle du texte puis presse Entrée. Les arrêts utilisent Échap et des heuristiques d’écran (`internal/box/controls.go:87-129`).
3. **Contrôles incomplets pour OpenCode.** `app/src/components/conversation/chat-controls.tsx:127` réserve les boutons modèle/mode/contexte et le bouton Stop visible à Claude/Codex :
   ```ts
   const chips = !ended && (agent === "claude" || agent === "codex");
   ```
4. **Questions et permissions principalement terminal.** Le formulaire répondable de `conversation-pane.tsx:131` est limité à Claude. `internal/box/answer.go:61` appelle `claudeRecord`. La documentation actuelle renvoie au terminal pour les demandes OpenCode (`docs/guides/agent-integrations.mdx:78-87`).
5. **Historique OpenCode borné à 100 messages.** `internal/box/opencode.go:90` demande `limit=100`; `internal/transcript/opencode.go:51-53` renvoie `Reset: true` et une notice. `transcriptapi.go:88-90` bifurque vers OpenCode avant le traitement de `before`.
6. **Catalogue de commandes et gestion des skills incomplets.** `internal/box/commands.go:338-364` découvre Claude/Codex uniquement. `internal/integrations/skills.go:82-95` et `app/src/lib/skills.ts:6` ne gèrent que ces deux agents ; `skills-panel.tsx:98,122` a deux colonnes codées en dur. Pourtant `integrations/command.go:143-147` installe déjà les skills Shipyard pour OpenCode.
7. **Pièces jointes transmises comme chemins dans le texte.** `app/src/lib/attachments.ts:150-155` utilise `withAttachments`. OpenCode expose des pièces jointes structurées ; la parité doit passer par ce contrat.
8. **Sous-agents, fork et rewind natifs limités à Claude.** `internal/box/history.go:52-79,157-169` dépend de `claudeRecord`; la projection OpenCode initialise une crew vide.
9. **Deux formats d’historique différents.** Shipyard utilise des offsets numériques (`app/src/lib/history.ts:48-57`), OpenCode des curseurs opaques. Une conversion implicite de curseur en nombre serait incorrecte.

Ces constats sont des lacunes fonctionnelles, pas un audit sécurité/performance complet. La restriction des détails d’outils au chargement à la demande et l’absence de contenu des prompts dans le journal d’événements sont des choix existants à conserver.

## 3. Contrat OpenCode vérifié et limites

Sources consultées le 2026-10-08 :

- API et schéma : https://opencode.ai/v2/docs/api et https://opencode.ai/v2/openapi.json
- Client et événements : https://opencode.ai/v2/docs/build/client
- Plugin : https://opencode.ai/v2/docs/build/plugins
- Instructions : https://opencode.ai/v2/docs/instructions
- Agents : https://opencode.ai/v2/docs/agents
- Skills : https://opencode.ai/v2/docs/skills
- Configuration : https://opencode.ai/v2/docs/config
- CLI : https://opencode.ai/v2/docs/cli

**Opérations HTTP confirmées** — vérifier à nouveau le schéma de la version installée avant d’écrire les types :

- `session.create`, `session.get`, `session.list`, `session.prompt`, `session.interrupt`.
- `session.switchAgent`, `session.switchModel`, `session.compact`.
- `session.permission.list/get/reply`, `session.form.list/get/reply`.
- `session.message.list/get`, `session.inbox.list/update/cancel`.
- `session.command`, `command.list`, `skill.list`, `agent.list`, `model.list`.
- `session.fork`, `session.revert.stage/clear/commit`.
- `mcp.list`, `integration.list`, `integration.oauth.connect/status/complete/cancel`, `plugin.list`, `config.get`, `location.reload`, `event.subscribe`.

Pièges à traiter explicitement :

- `session.prompt` admet une entrée durable ; son HTTP 200 ne signifie pas que le modèle a terminé. Corps : `text`, `id` optionnel préfixé `msg_`, `files`, `skills`, `agents`, `delivery: "steer" | "queue"`, `resume` et `metadata`.
- Le rejeu d’un même ID de prompt retourne l’admission originale selon la documentation des hooks. Prouver cette propriété sur la version supportée ; cela ne rend pas automatiquement toutes les commandes idempotentes.
- `session.interrupt` cible le **processus actif** ; les formulaires/permissions doivent eux aussi utiliser son runtime.
- `session.message.list` : page initiale avec `order`; pages suivantes avec le curseur opaque, **sans `order`**, en gardant le même filtre de type.
- `event.subscribe` est un flux **sans replay** ; coupures et consommateurs lents perdent des événements. Reconnexion = nouvelle souscription + réconciliation par lectures de l’état courant, pas simple reprise par `Last-Event-ID`.
- `session.environment` remplace l’environnement des commandes shell locales de la session. Ce n’est pas une garantie d’isolation de l’environnement des plugins/MCP/providers : préserver l’isolation existante par processus.
- `Session.Info.tokens` décrit l’usage ; ne pas l’assimiler sans preuve à l’occupation du contexte actif. `session.context` renvoie des messages, pas directement une jauge de tokens.
- L’HTTP demande `decision` pour `session.permission.reply` et `name/text` pour `session.command`. Les exemples du guide plugin utilisent parfois `reply` et `command/arguments`. Ne pas transposer ces champs à l’aveugle : contrat HTTP et contexte plugin doivent avoir chacun un test.
- `config.get` retourne des documents/sources de configuration, pas nécessairement un document effectif aplati et expurgé. La réponse brute ne doit pas être exposée comme écran de réglages.
- Les instructions V2 sont chargées par `AGENTS.md`. Le champ `instructions` est accepté mais non résolu selon la documentation actuelle. Ne pas construire un éditeur qui prétendrait activer ces entrées.
- Une sélection d’agent ne change pas automatiquement le modèle de la session. Un agent personnalisé avec `system` remplace le prompt de base ; Shipyard ne doit pas le faire implicitement.

Le schéma publié se présente comme expérimental. La parité s’annonce **par fonctionnalité vérifiée et version supportée**, pas uniquement parce que le numéro majeur est 2.

## 4. Lots d’exécution

### Lot 0 — Établir le canal vers le runtime propriétaire

**Priorité P0 · effort M · risque élevé · dépendance : aucune.**

But : prouver que Berth peut dialoguer avec le même moteur que son terminal sans toucher au service OpenCode personnel.

1. Étendre `TestOpenCodeLiveV2Contract` dans `internal/box/opencode_test.go`. Garder son HOME/XDG/store temporaires et son faux fournisseur HTTP local : aucun appel modèle payant.
2. Vérifier, sur le binaire V2 choisi, comment joindre le serveur privé démarré par Mini. Privilégier un transport natif existant s’il expose ce processus de façon authentifiée et stable.
3. Révision du 2026-10-09 : le contexte plugin 2.0.18 ne couvre pas le plan. Utiliser le **serveur HTTP natif privé** (`serve`), démarré dans l'environnement de la tâche, puis connecter Mini au même propriétaire par le mécanisme public de découverte/authentification vérifié sur le binaire. `serve --service --hostname 127.0.0.1 --port 0` avec registre XDG isolé a été sondé avec succès. Valider également Mini avant toute UI. Aucune dépendance envers les méthodes plugin absentes ; conserver le plugin pour les événements déjà exposés. Go `net/http`, opérations explicitement autorisées, pas de proxy générique exposé au frontend.
4. Conserver une instance privée par session Shipyard pour la première version. Le répertoire de socket/registre appartient à la box et à son utilisateur, hors dépôt. Parent privé, socket privée, validation des identités et du répertoire, budget de corps/réponse, nettoyage uniquement des ressources possédées. Aucune URL de contrôle arbitraire fournie par le frontend.
5. Enregistrer seulement le lien session/instance/emplacement du canal. Le reconstruire après redémarrage de berthd sans arrêter Mini. Détecter une instance périmée, y compris réutilisation de PID ou nom de session. Ne pas tuer une instance inconnue.
6. Documenter le transport, la version, l'authentification Mini/API et la survie sous tmux. Si le transport exige une API privée ou un fork d’OpenCode, STOP. L'absence de méthodes plugin n'est plus une condition bloquante lorsque leur équivalent HTTP est vérifié sur le runtime propriétaire. Le test réel doit valider ce contrat HTTP plutôt qu'exiger des méthodes plugin inutilisées.

**Preuves obligatoires :** deux sessions dans le même worktree ont des environnements distincts ; arrêter A laisse B active ; répondre à une permission débloque le bon processus ; redémarrer le client berthd de test permet de retrouver le lien ; le service partagé personnel reste hors du chemin de contrôle.

**Vérifier :**
```sh
go test -race ./internal/box ./internal/integrations/adapters -run OpenCode -count=1
OPENCODE_TEST_BIN="$(command -v opencode)" go test ./internal/box -run '^TestOpenCodeLiveV2Contract$' -count=1 -v
```
Résultat : exit 0 ; le second test doit réellement s’exécuter, pas être SKIP. Si le binaire manque, marquer le lot BLOCKED, pas validé. Lancer uniquement en environnement de test isolé.

### Lot 1 — Envoyer, interrompre et reconnecter nativement

**Priorité P1 · effort L · risque élevé · dépendance : lot 0.**

Fichiers principaux : `internal/box/opencode.go`, `orchestrate.go`, `controls.go`, `turns.go`, `api.go`, plugin OpenCode ; `app/src/lib/api.ts`, `chat-controls.ts`, `transcript-feed.ts`, composants de conversation.

1. Ajouter au backend les actions OpenCode natives et une description de capacités **par session**. Réutiliser les routes Shipyard existantes lorsque leur sémantique convient. Définir les nouvelles routes et champs dans `docs/reference/app-api.mdx` ; ce plan ne suppose pas qu’ils existent déjà.
2. Brancher l’envoi OpenCode dans `sendPrompt`, avant le chemin de saisie terminal, tout en conservant les validations, les gates `session.send`, l’identification du tour et les notifications existantes. L’UI et les flows passent par la même admission.
3. Affecter un ID OpenCode durable à chaque demande dès avant son premier envoi et relier `idem_key`, tour Shipyard et entrée inbox OpenCode. Un retry après réponse perdue réemploie cet ID. Un même ID avec contenu différent doit être refusé côté Shipyard.
4. Définir explicitement l’articulation des files : laptop = transport hors ligne ; OpenCode = entrées acceptées pendant l’exécution ; journal Shipyard = suivi. Éviter que `Turns.Queue` et l’inbox OpenCode délivrent chacun le même prompt. Préserver le comportement des autres agents.
5. Remonter les événements actifs via le canal authentifié Shipyard. Les deltas de texte sont éphémères ; le journal général garde seulement les identifiants/états. Une souscription par instance utile, pas un processus CLI par rafraîchissement et par hook React.
6. Après toute coupure : reconnecter le flux, lire messages/inbox/demandes en attente et réconcilier les IDs avec les événements arrivés pendant cette lecture. Prévoir un numéro de génération pour ignorer les anciennes réponses après changement de conversation.
7. Activer Stop pour OpenCode et traduire l’accusé natif correctement : requête d’arrêt acceptée ≠ arrêt observé. Supprimer uniquement pour OpenCode les décisions fondées sur le texte de l’écran quand une capacité native est disponible.
8. Le terminal reste accessible. Pas de bascule silencieuse vers un collage terminal après un envoi API dont le résultat est incertain : cela pourrait envoyer deux fois le prompt.

**Tests :** double envoi concurrent, perte d’ACK, reconnexion, deux fenêtres sur la même session, tour annulé, session disparue, mauvais runtime, client lent et queue existante Claude/Codex. Le test de reprise compare les IDs et l’état final, pas une impression visuelle.

**Vérifier :** `go test -race ./internal/box ./internal/agent ./internal/integrations/adapters` puis contrôles app communs et `app/e2e/opencode.spec.ts` (nouveau). Exit 0 et une seule admission dans le faux runtime pour un retry.

### Lot 2 — Permissions et questions directement dans le chat

**Priorité P1 · effort M · risque moyen · dépendance : lot 1.**

Fichiers : `internal/box/opencode.go`, `answer.go`, `api.go`, `internal/transcript/opencode.go`, `app/src/lib/transcript.ts`, `questions.ts`, `components/conversation/conversation-pane.tsx`, `conversation-view.tsx` ; créer un composant de formulaire OpenCode seulement si le modèle existant ne suffit pas.

1. Lire les permissions/formulaires pendants depuis le runtime. Leur identité reste `sessionID + requestID/formID`, indépendante de la position dans le chat.
2. Permissions : afficher action, ressources et message OpenCode ; proposer les décisions natives `once`, `always`, `reject` lorsque le contrat le permet. Expliquer la portée réellement persistée de « toujours ».
3. Questions : gérer tous les types déclarés par la version retenue (texte, nombre/entier, booléen, choix multiples, champs conditionnels). Respecter les options, contraintes et identifiants de champs ; ne pas les réduire à une liste de chaînes si cela perd de l’information.
4. Les champs externes et parcours d’authentification utilisent leur parcours officiel ; les types inconnus gardent un accès explicite au terminal plutôt qu’un formulaire incorrect.
5. Une réponse déjà soumise ailleurs, expirée ou annulée rafraîchit la carte ; elle ne part jamais vers une nouvelle demande à la place. Les changements de permissions traversent les gates Shipyard applicables et restent des décisions de l’utilisateur.
6. Refléter « attend ton autorisation » dans la tâche/inbox Shipyard, avec accès direct à la demande. Reconnexion = demandes toujours visibles.

**Tests :** autoriser/refuser, validation des nombres et multichoix, formulaire conditionnel, requête étrangère, réponse simultanée de deux clients, demande annulée puis remplacée, refus conservé après refresh.

**Vérifier :** `go test -race ./internal/box ./internal/transcript ./internal/integrations/adapters`, puis spec OpenCode ciblée et contrôles app. Exit 0 ; aucun `send-keys` pour une réponse native prise en charge.

### Lot 3 — Retrouver les commandes, agents, skills et fichiers d’OpenCode

**Priorité P1 · effort L · risque moyen · dépendance : lots 1–2.**

Fichiers : `internal/box/commands.go`, `opencode.go`, `tasks.go`, `skills.go`, `attachments.go`, `internal/integrations/{skills,accounts,command}.go` ; `app/src/lib/{commands,skills,attachments,api,chat-controls}.ts`, `components/conversation/{composer-pickers,task-composer,chat-controls}.tsx`, `components/skills/skills-panel.tsx`.

1. Alimenter le catalogue depuis `command.list`, `skill.list`, `agent.list` et le catalogue modèle du **répertoire/runtime choisi**. Garder les sections fournisseurs et variantes déjà réalisées. Différencier l’agent moteur « OpenCode » de son profil interne « Build / Plan / personnalisé ».
2. Exécuter une commande enregistrée via `session.command`, un skill via le champ structuré prévu et le modèle/profil via leurs opérations natives. Les commandes purement UI de la TUI ne sont pas nécessairement présentes dans `command.list` : traiter les équivalents Shipyard comme actions UI, pas comme faux prompts. Une commande inconnue ne doit pas être présentée comme exécutée.
3. Afficher le modèle/profil effectif, y compris après un changement depuis un autre client. Réserver les actions à ce que les capacités autorisent. Plan reste le véritable agent Plan d’OpenCode, pas une phrase ajoutée par Shipyard au prompt.
4. Réutiliser upload, drop/paste et chips. Envoyer à OpenCode `files: [{uri, name}]` après l’upload sur la box ; utiliser une construction d’URI correcte et tester les caractères spéciaux. Aucun chemin du Mac transmis comme s’il existait sur la box distante. Pour les mentions, préserver les offsets réels si l’API en attend.
5. Respecter la taille/type autorisés côté upload et les capacités média du modèle. Une image sur un modèle sans vision produit une explication claire ; le test vérifie la pièce jointe reçue par le faux fournisseur compatible.
6. Étendre le gestionnaire de skills Shipyard à OpenCode et générer les colonnes à partir de `report.agents`. Séparer **skills Shipyard installables** et **catalogue effectif OpenCode**, qui inclut déjà les skills personnels et compatibles.
7. Attention aux chemins : global OpenCode = répertoire config de l’utilisateur de la box + `skills`; projet = `.opencode/skills`. `SkillDir(root, agent)` traite aujourd’hui home et repo pareil : ajouter une résolution par portée et mettre à jour ses appelants plutôt que mettre une seule chaîne incorrecte dans `skillDirs`. Respecter les overrides config/XDG vérifiés au lot 0.
8. Conserver les règles d’installation existantes : contenu utilisateur préservé, protections anti-symlink en projet, exclusion Git sauf choix explicite, mise à jour idempotente. Ne pas copier les skills déjà découverts dans un second emplacement.

**Tests :** catalogue différent pour deux projets, modèle retiré, profil par défaut préservé, commande avec arguments, skill prioritaire projet, installation/désinstallation user/project, XDG, chemins avec espaces/accents, échec upload, image structurée et ancien agent inchangé.

**Vérifier :** `go test -race ./internal/box ./internal/integrations/...`, contrôles app, specs `e2e/opencode.spec.ts`, `e2e/composer.spec.ts`, `e2e/drop.spec.ts` si modifiées. Exit 0 ; le prompt utilisateur n’est pas réécrit par une couche système Shipyard.

### Lot 4 — Historique complet, reprise, contexte et sous-agents

**Priorité P2 · effort L · risque moyen · dépendance : lots 1–3.**

Fichiers : `internal/box/{opencode,transcriptapi,history}.go`, `internal/transcript/opencode.go`, `app/src/lib/{history,transcript,transcript-feed,conversation-store,chat-controls}.ts`, composants conversation/crew existants.

1. Charger l’historique par curseur OpenCode. Ajouter une branche de pagination explicite au contrat frontend/backend ; garder l’offset existant pour Claude/Codex. Réutiliser la virtualisation et les plafonds mémoire. Après reconnexion, les pages anciennes déjà chargées ne doivent pas être effacées par un `Reset` de la fenêtre récente.
2. Normaliser les messages de changement modèle/agent, compaction, skills et erreurs en plus de texte/outils. Utiliser les IDs natifs des messages/parties lorsque disponibles ; ne pas identifier durablement un outil par sa position dans un tableau.
3. Lister/rechercher les sessions du projet et permettre de **reprendre une conversation existante**. Reprendre signifie relier l’identité persistante, pas importer/copier la base. Avant une exécution, déterminer son runtime propriétaire ; refuser deux exécutions concurrentes accidentelles sur la même session. Les anciennes sessions déjà terminées restent lisibles.
4. Lister les enfants via `parentID`, les afficher dans la crew et ouvrir leurs conversations en lecture. Remonter leurs permissions sans faire terminer le tour du parent. Traiter également un fork, qui peut avoir un `parentID`, comme une conversation autonome lorsqu’il a été explicitement lié à une nouvelle tâche : le filtre actuel `if (session.parentID) continue` devra être raffiné.
5. Afficher tokens et coût remontés par OpenCode, avec unité et provenance. Le coût remonté n’est pas une facture d’abonnement. Ne pas utiliser `contextWindow` heuristique pour prétendre connaître le contexte d’un modèle OpenCode arbitraire. Jauge seulement si numérateur/dénominateur sont vérifiés ; sinon afficher « indisponible ».
6. Ajouter compaction native et suivi pending/running/completed/failed. La limite d’affichage de l’historique ne doit jamais modifier la mémoire du modèle.

**Tests :** plus de 250 messages répartis sur plusieurs pages, ordre stable, aucun doublon après reconnect/compaction, enfant finissant avant son parent, lecture après sortie du terminal, reprise d’une session inactive, refus de double propriétaire et métriques absentes.

**Vérifier :** `go test -race ./internal/box ./internal/transcript`, `pnpm -C app test`, specs OpenCode et chat-feed/crew concernées. Exit 0 ; le test paginé compare la séquence complète des IDs à la fixture.

### Lot 5 — Réglages OpenCode et continuité local/distant

**Priorité P2 · effort L · risque moyen · dépendance : lots 0–3.**

Fichiers : `internal/box/opencode.go`, routes explicites dans `api.go`, `internal/integrations` pour les chemins déjà utilisés ; `app/src/views/settings/agents-section.tsx`, panneau OpenCode dédié à créer si nécessaire, API/types dédiés sous `app/src/lib/`.

1. Ajouter « OpenCode sur cette machine » : version, runtime connecté, projet/répertoire, modèle/profil, MCP actifs/en erreur/en attente de connexion, plugins serveur chargés et sources de configuration. Lire l’état effectif du moteur ; ne pas reconstituer un moteur de fusion de config dans React.
2. Distinguer portée utilisateur de la box, projet, session. Un écran de contexte montre les sources de règles/skills effectivement connues et les sources non observables, sans prétendre exposer le prompt système intégral ou toutes les injections d’un plugin.
3. Les modifications passent par les opérations documentées avec leur portée réelle. Le patch de config publié est global/expérimental : ne pas l’utiliser comme éditeur projet. Pour une config projet, privilégier l’éditeur de fichier existant avec écriture atomique, validation JSONC et contrôle de version du contenu afin de ne pas écraser une modification concurrente.
4. Fournisseurs et MCP : connexion via les parcours OpenCode officiels, statut visible et reconnexion dans l’UI. Garder les secrets dans le stockage OpenCode de la machine concernée ; pas dans Zustand, localStorage, URLs de contrôle, événements ou logs. Expurger les réponses en allowlist de champs plutôt qu’afficher `config.get` brut.
5. Sur le même Mac et le même compte, réutiliser les réglages présents. Sur une box distante, proposer un **transfert explicite de configuration sélectionnée** : aperçu du diff, choix des règles/agents/commandes/skills et dépendances relatives, adaptation des chemins puis validation sur destination.
6. Ce transfert est ponctuel, pas une synchro bidirectionnelle de dossiers. Ne pas copier automatiquement bases de sessions, tokens OAuth, clés, historiques ou variables d’environnement secrètes. Les reconnexions s’effectuent sur la destination. Les fichiers référencés manquants sont signalés avant activation.
7. Les outils dépendant du Mac, d’une session graphique ou d’autorisations OS ne deviennent pas disponibles sur Linux par copie de config. Afficher leur localisation. Utiliser OpenCode sur le Mac pour ces usages ; prévoir un relais laptop explicite uniquement si un besoin concret le justifie, avec indisponibilité affichée quand le Mac dort.
8. La page affiche les capacités manquantes et conserve un accès au terminal pour les extensions TUI non portables. Les plugins **serveur** continuent à s’exécuter dans OpenCode ; leurs composants TUI ne sont pas automatiquement des composants React Shipyard.

**Tests :** portées distinctes, JSONC préservé, conflit de fichier détecté, MCP `needs_auth`, OAuth interrompu/repris, secret sentinelle absent des réponses publiques/logs, transfert sans credentials, chemins/dépendances invalides, outil Mac indisponible sur box distante.

**Vérifier :** tests Go ciblés `OpenCode|Skill`, contrôles app, spec OpenCode. Utiliser mocks et comptes synthétiques ; aucun appel à des intégrations personnelles pour la validation automatique.

### Lot 6 — Fork, retour arrière et workflow Shipyard complet

**Priorité P2 · effort L · risque élevé · dépendance : lots 1–4.**

Fichiers : `internal/box/history.go`, `opencode.go`, `tasks.go`, points d’orchestration existants `runhost.go`/`orchestrate.go` si nécessaires ; `app/src/lib/history.ts`, actions de conversation et revue existantes.

1. Brancher fork/revert sur les opérations OpenCode, avec sélection par ID de message. Un fork de conversation conserve le worktree par défaut ; créer un autre worktree est une action distincte prise en charge par Shipyard.
2. Relier le fork à sa propre session Shipyard et à son runtime autorisé. Le filtre des événements doit reconnaître les racines explicitement liées, même si OpenCode leur donne un `parentID`.
3. Retour arrière : prévisualiser la frontière conversationnelle et les fichiers affectés, appliquer le stage natif, permettre l’annulation et valider ensuite. Ne pas émuler cela par `git reset --hard`, suppression de fichiers ou écriture dans la base OpenCode.
4. Préserver les modifications utilisateur non concernées. Si OpenCode ne garantit pas une restauration sûre des fichiers avec la version/snapshot actifs, limiter la fonction à la conversation et désactiver le choix fichiers avec une raison précise. Vérifier aussi les changements produits par des outils shell.
5. Réutiliser l’inbox/review, les commentaires de diff, les previews et les skills `berth-*`. Vérifier un parcours complet « tâche → agent → tests → preview → revue → nouvelle instruction », avec fermeture/réouverture de l’app.
6. Les flows et tâches multi-agents continuent via `sendPrompt` et les états de tours natifs. Une attente utilisateur ne doit jamais être franchie automatiquement par une automation. Aucun nouveau planificateur n’est nécessaire.

**Tests :** fork depuis un message ancien, première instruction, parent inchangé, fork/enfant correctement attribués, stage/clear/commit, snapshots désactivés, fichier utilisateur modifié entre aperçu et confirmation, flow suspendu par permission puis repris.

**Vérifier :** `go test -race ./internal/box/... ./internal/transcript`, specs OpenCode et history/crew existantes réellement touchées, puis contrôles communs. Tous les scénarios de restauration travaillent dans des repos temporaires.

### Lot 7 — Compatibilité et activation

**Priorité P1 pour la livraison finale · effort M · risque moyen · dépendance : lots précédents.**

1. Faire tourner le contrat réel isolé contre la version minimale choisie et la version V2 courante supportée. Lister précisément les capacités vérifiées. Désactiver une fonction indisponible sans perdre l’historique ou bloquer le terminal.
2. Vérifier Mac local et box Linux, réseau coupé/revenu, app fermée, redémarrage berthd, runtime OpenCode arrêté, deux sessions partageant un worktree et session avec config projet différente.
3. Ajouter des assertions de parcours accessibles : navigation clavier du composeur/pickers, focus après réponse à un formulaire, nom accessible de Stop et annonces de connexion. Réutiliser composants et tokens existants, sans refonte visuelle.
4. Mettre à jour `docs/guides/agent-integrations.mdx`, la référence API et le changelog. Distinguer dans les docs lecture, contrôle natif et repli terminal ; documenter les limites spécifiques aux extensions non portables.
5. Ne déclarer la parité quotidienne acquise qu’après le scénario final de la section 8. Garder les autres agents fonctionnels.

**Vérifier :** tous les contrôles de la section suivante, contrat réel sans SKIP et specs modifiées. La validation macOS/Linux est rapportée séparément si l’un des environnements manque.

## 5. Conventions et vérifications communes

### Conventions à suivre

- Go : fonctions sur `*Box`, validation à l’entrée, `badRequest` / `httpError` / `writeJSON`, propagation du contexte. Exemple : `openCodeToolDetail` dans `internal/box/opencode.go:97-120`.
- Le frontend appelle `client.box`/`boxApi` ; jamais une URL OpenCode distante directement. Types dans `app/src/lib`, composants existants coss-ui/Base UI et état Zustand borné.
- Modèles de tests : `internal/box/opencode_test.go` (CLI simulée et contrat réel isolé), `internal/integrations/adapters/opencode_test.go` (plugin sous Node et assertions), `internal/transcript/opencode_test.go` (projection sans fuite), `app/e2e/composer.spec.ts` (fake agent et assertions accessibles).
- Chaque nouveau fichier de tests unitaires frontend doit être ajouté explicitement au script `test` de `app/package.json`.
- Pas de nouveaux attributs HTML `title`; utiliser `Tip`. Les avertissements déjà présents dans les fichiers existants se traitent seulement lorsqu’ils bloquent le contrôle du lot, sans refonte annexe.
- Ajouter une entrée simple sous `## Unreleased` pour chaque lot modifiant le comportement visible.
- Fixtures synthétiques `acme`. Aucune donnée personnelle, aucun secret ni appel modèle payant dans les tests.

### Commandes réelles du dépôt

Ces commandes proviennent de `AGENTS.md`, `Makefile`, `app/package.json` et `.github/workflows/ci.yml`. Elles n’ont pas été exécutées pendant cette planification. Une baseline rouge doit être identifiée avant d’attribuer un échec au lot.

À la racine, avant de déclarer un lot implémenté terminé :
```sh
go vet ./...
go test -race ./...
```
Dans `app/src-tauri` :
```sh
cargo check
```
Dans `app` :
```sh
CI=true pnpm install --frozen-lockfile
pnpm typecheck:plugins
pnpm check:titles
pnpm check:csp
pnpm check:themes
pnpm test
pnpm build
npx tsc -p e2e
```
Dans `docs-site`, lorsque `docs/` change :
```sh
pnpm build
```
Résultat attendu pour chaque commande : exit 0, pas de test de contrat requis ignoré.

Pour les tests navigateur, après le build, dans `app` :
```sh
E2E_PORT=1434 npx playwright test e2e/opencode.spec.ts
```
`e2e/opencode.spec.ts` est à créer au premier lot UI, pas un fichier déjà présent. Avant la commande, vérifier que 1434 est libre ; sinon choisir un port libre dans 1421–1439. Ajouter seulement les specs existantes effectivement modifiées. Ne pas utiliser/tuer 1420 ou 1377–1379. Invoquer le skill `e2e-tests` disponible avant d’écrire ou exécuter les specs.

## 6. Périmètre et discipline Git

**Fichiers autorisés, selon le lot :**

- `internal/box/opencode*.go`, `tasks*.go`, `api.go`, `orchestrate*.go`, `controls*.go`, `answer*.go`, `commands*.go`, `history*.go`, `attachments*.go`, `skills*.go`, `turns*.go`, `tmux.go`, `sessions*.go`, `runhost.go`, tests d’orchestration concernés.
- Extension explicite du 2026-10-09 : point d'entrée `cmd/berthd` et routage de commande existant si un petit lanceur natif est nécessaire pour superviser serveur privé + Mini dans tmux. Réutiliser le binaire Shipyard, pas de nouveau service installé. Tests du lanceur dans les mêmes packages. Les fichiers communs de types/API/notifications strictement nécessaires aux lots restent autorisés si leur rôle est consigné dans le compte rendu.
- `internal/integrations/adapters/opencode*`, `internal/integrations/{command,skills,accounts}.go` et leurs tests ; `internal/transcript/opencode*`, types communs de transcript si une extension additive est nécessaire, fixtures synthétiques associées.
- `internal/agent/queue*.go` et client de livraison uniquement pour la continuité d’ID/payload hors ligne ; ne pas refondre la queue générale.
- `app/src/lib/{api,commands,skills,attachments,chat-controls,transcript,transcript-feed,conversation-store,history,questions,queue}.ts`, nouveaux helpers/types `opencode*.ts` si nécessaires, tests associés.
- Composants de conversation/crew/formulaires/pickers existants concernés, panneau skills, `app/src/views/settings/agents-section.tsx`, nouveau panneau de configuration OpenCode éventuel ; mocks/fake-agent des scénarios concernés.
- `app/e2e/opencode.spec.ts` à créer, specs existantes effectivement affectées ; `app/package.json` seulement pour enregistrer de nouveaux tests.
- `docs/guides/agent-integrations.mdx`, `docs/reference/app-api.mdx`, `docs/changelog.mdx`, `plans/` ; CI seulement pour le contrat OpenCode isolé si son exécution automatisée est prête.

**Hors périmètre :** refonte de l’accueil ou du thème, `site/`, remplacement des autres moteurs, fork d’OpenCode, nouvelle marketplace, nouvelle infrastructure de synchronisation, lecture/écriture directe de sa base, migration des identifiants `berth*`, système de facturation et secrets personnels. Aucun besoin d’ajouter une bibliothèque pour des appels HTTP Go ou un pont Node standard.

Les instructions de dépôt protègent `~/.berth`, `~/Library/Application Support/berth` et le job launchd existant. Ne jamais les utiliser pour les essais. Les tests doivent injecter leurs répertoires temporaires et ne stopper que leurs propres PIDs.

Travailler par branches `feat/opencode-<lot>` et PRs contre `main` sur `cosscom/shipyard`, selon les consignes du dépôt ; commits conventionnels minuscules, par exemple `feat(app): answer opencode questions from chat`. Ce plan seul n’autorise aucun déploiement ni migration de configuration personnelle.

### Drift et travail local à préserver

Avant exécution :
```sh
git status --short --branch
git diff --stat bc92f1c..HEAD -- internal/box internal/integrations internal/transcript internal/agent/queue.go app/src app/e2e docs
git diff --stat -- internal/box internal/integrations internal/transcript internal/agent/queue.go app/src app/e2e docs
```

La comparaison au commit ne couvre pas les changements non commitées : relire également le diff local et les extraits ci-dessus. Ne pas revenir à `bc92f1c` en écrasant le sélecteur de modèles en cours.

Empreintes SHA-256 observées des fichiers locaux déjà modifiés et pertinents :
```text
899445bafd17d28c0f393a4e2a55078bee743be2034452c9ded5e57aea1e02b7  internal/box/opencode.go
b6b33bf3fc25e14e216655ffc81ab25718be42502909c72ef247194fb94b37f9  internal/box/tasks.go
ce7d1ca8716bb3c9945b7b4cd1f504d2fc60c97b6149c66ced612d7c86ec59b6  internal/box/api.go
ca96068c813734824ead9a0655ce7837d8c4e15a23ce742b72ffd95d7884bdff  internal/box/opencode_test.go
3d552d90a02258c8a38f3b54b530060caea7218598d548dcaed9670a57c94b03  internal/box/tasks_test.go
229b22901900b38300116c8b68f4b32e60bed464c111018689436cf27d9be5f4  app/src/components/conversation/composer-pickers.tsx
25bbb41d2f0e4344cba1ab54492f36e20a020c3901ba6888d889caa012a0e1b7  app/src/components/conversation/task-composer.tsx
21fbe5723f8a14b67315f74ac3fe9678351de3045c61438d101dd2a9eb55003b  app/e2e/composer.spec.ts
13b4e5f6438daf12fb7bd4c54155f7560e99345e171ccdad2985bd0be0fbcd88  docs/guides/agent-integrations.mdx
14d883f8579d9a9b8cc5fe0a85f02489052c6329591a65f78dec65e8ab2c761a  docs/changelog.mdx
```
Une différence signifie « relire et actualiser le plan », pas « restaurer ces versions ». Le travail local sur `site/` est indépendant et ne fait pas partie de ces lots.

## 7. Ordre, limites et conditions STOP

Ordre conseillé : **0 → 1 → 2 → 3 → 4 → 5 → 6 → 7**. Le lot 5 peut avancer après le lot 3 ; le lot 6 dépend de l’historique du lot 4. Les améliorations des skills installables peuvent être livrées séparément, mais ne suffisent pas à remplacer le terminal.

**Premier jalon utilisable : lots 0–3.** On travaille depuis le chat avec modèles/profils, commandes/skills, fichiers et réponses natives. **Parité de travail avancée : lots 4–7**, incluant reprise, sous-agents, réglages et retours arrière.

STOP si :

- La version installée ne permet pas de joindre de façon sûre le runtime propriétaire ; ne pas remplacer cela par des mutations dans un nouveau `--standalone`.
- Une méthode n’est pas disponible dans le contrat public effectivement testé, ou les champs diffèrent du plan : adapter la spécification avant l’UI.
- Les changements locaux recouvrent ceux d’un autre travail ; faire clarifier la base d’intégration plutôt que les écraser.
- La livraison idempotente ou la réconciliation ne peut pas être prouvée : conserver l’état « envoi incertain », jamais une seconde livraison silencieuse.
- Une restauration exige d’écraser des changements utilisateur non attribuables au tour : désactiver cette portée, pas de reset forcé.
- Il faut lire une base privée, modifier un service utilisateur, copier des secrets ou exposer un endpoint de contrôle public pour avancer.
- Un contrôle échoue deux fois après une correction raisonnable, ou un fichier hors périmètre devient nécessaire : noter la raison avant d’étendre le lot.

Directions écartées : réécrire les prompts/outils OpenCode dans Shipyard ; ajouter une orchestration LLM automatique au-dessus de tous les tours ; fusionner immédiatement les runtimes privés en service global ; copier silencieusement tout `~/.config/opencode` ; refaire tous les plugins TUI en React. Ces directions augmentent le risque sans être nécessaires au parcours demandé.

## 8. Critères de fin et scénario de recette

- [ ] Contrat réel isolé testé, version et transport retenus documentés ; aucun SKIP pour les fonctionnalités déclarées.
- [ ] Deux tâches OpenCode sur le même worktree restent indépendantes, y compris leurs demandes d’autorisation et environnements.
- [ ] Retry après coupure = un seul ID admis ; envoi/arrêt/questions natifs ne dépendent pas du texte de l’écran.
- [ ] Tous les prompts passent par les gates et le suivi de tours Shipyard sans remplacement du prompt système OpenCode.
- [ ] Choix modèle/profil, catalogue de commandes/skills et image jointe fonctionnent dans le chat ; la config réellement chargée est identifiable.
- [ ] Historique de 250+ messages, sous-agents et reprise d’une session existante passent les tests sans doublons ni trous.
- [ ] MCP/fournisseurs signalent les connexions manquantes et les différences local/distant ; secrets sentinelles absents des vues/journaux.
- [ ] Fork/revert validés sur repo jetable ; les limitations de snapshots sont explicites.
- [ ] Fermeture de l’app et redémarrage de berthd n’arrêtent pas la session distante encore active.
- [ ] Vérifications Go/Rust/app/docs et specs touchées passent, sans régression Claude/Codex.
- [ ] Diff borné au lot, changelog et docs à jour, statut du lot renseigné dans `plans/README.md`.

**Scénario final à automatiser autant que possible :** sur une box de test, créer un worktree via Shipyard ; lancer OpenCode avec un profil et une variante ; appeler une commande et un skill de test ; joindre une image ; répondre à un formulaire puis à une permission ; observer un sous-agent ; interrompre/reprendre ; couper/reconnecter le client pendant un envoi ; rouvrir l’historique ; afficher la preview et le diff ; créer un fork ; effectuer puis annuler un retour arrière. Le faux fournisseur permet une séquence déterministe sans dépense ni intégrations personnelles.

## 9. Maintenance

À chaque montée de version OpenCode : rejouer le contrat runtime, l’idempotence d’admission, les schémas de formulaires, la pagination et l’invalidation après revert. Les événements restent un accélérateur d’affichage ; l’état durable relu reste l’arbitre après une coupure.

En revue, porter l’attention sur l’identité runtime/session, la séparation file hors ligne/inbox, la portée de configuration et la préservation des changements utilisateur. Surveiller ensuite les coûts réels du flux et de la lecture à plusieurs chats avant d’introduire un cache ou un service mutualisé supplémentaire.

## 10. Preuves d’exécution — 2026-10-09

### Base et limite atteinte

- Worktree d’exécution : `/Users/alexandrecohen/projets/berth_fork-opencode-native`, branche `feat/opencode-native-workspace`.
- Base transférée : `a175393fbc010b2cc0af1253ead4a4a2d3d244fe`. Uniquement les diffs suivis `app/`, `internal/`, `docs/` et les fichiers ordinaires `plans/`; aucun diff `site/` ni wallpaper. Le dépôt initial n’a été ni réinitialisé ni modifié.
- **STOP au lot 0, condition « méthode absente du contrat public effectivement testé ».** Aucun lot 1–7 n’est implémenté. Le transport n’est pas retenu et la parité n’est pas déclarée.
- Le test réel existant (Mini, faux fournisseur local, hooks, lecture après sortie) passait avant l’ajout du contrôle de surface. Le test étendu échoue explicitement sur `typeof ctx.session.compact = undefined` dans **OpenCode 2.0.18**, alors que cette méthode figure dans le guide public des plugins. Ce n’est pas un test ignoré.
- L’inventaire du plugin montre aussi l’absence de `form`, `session.form`, `session.message`, `session.inbox`, `session.fork`, `session.revert` et `config`. Le pont `ctx` proposé ne peut donc pas être figé comme transport de l’ensemble du plan. Les noms HTTP ne sont pas des méthodes plugin implicites.

### Binaire et isolation du contrat

Le chemin fourni `.opencode/bin/opencode` est un script qui force `XDG_STATE_HOME="$HOME/.local/state"` et `OPENCODE_DB="opencode-v2.db"`, puis lance `$HOME/.local/share/opencode/bin/opencode`. Avec HOME temporaire, il échoue avant de démarrer. Le test a utilisé son exécutable réel : `/Users/alexandrecohen/.local/share/opencode/bin/opencode` (sortie isolée `opencode v2.0.18`). Aucun lanceur personnel n’a été modifié.

Le test renforcé efface l’environnement hérité sauf PATH, répertoire temporaire, locale et terminal; il reconstruit HOME, XDG, TMPDIR et la base dans des répertoires temporaires. Le seul fournisseur configuré est `acme`, local, avec une clé synthétique. Il journalise les noms/types de méthodes, jamais des identifiants de connexion. Le binaire doit être un exécutable autonome, pas un lanceur dépendant du HOME personnel.

### Options publiques recherchées et résultats exacts

Sources relues : [CLI](https://opencode.ai/v2/docs/cli), [API](https://opencode.ai/v2/docs/api), [client](https://opencode.ai/v2/docs/build/client), [plugins](https://opencode.ai/v2/docs/build/plugins), [plugins Effect](https://opencode.ai/v2/docs/build/plugins/effect), [RPC](https://opencode.ai/v2/docs/build/plugins/rpc), [Web et serveur dédié](https://opencode.ai/v2/docs/cli/web), [réseau](https://opencode.ai/v2/docs/network), [configuration CLI](https://opencode.ai/v2/docs/cli/config).

1. **Mini `--standalone`.** L’aide du binaire expose `--standalone`, `--server`, `--session`, `--fork`, `--model`, `--agent` et `--prompt`. Le plugin du processus propriétaire expose bien `session.prompt`, `session.interrupt`, `session.get`, `session.context`, `session.switchAgent`, `session.switchModel`, `permission.list/get/reply` et `event.subscribe`. `session.compact` est directement testé absent. Les espaces `ctx.form` et `ctx.session.form` sont directement testés `undefined`. Aucun mécanisme public de récupération des identifiants HTTP de ce Mini déjà lancé n’a été établi.
2. **`api --standalone get /openapi.json`.** Le schéma servi par 2.0.18 contient les routes de formulaires, inbox, messages, compaction, fork/revert, permissions et événements. Le sous-test `published_http_surface` les vérifie. Leur présence HTTP ne résout pas l’accès au processus qui détient l’exécution; une nouvelle commande standalone n’est pas ce processus.
3. **Serveur dédié natif : piste viable à spécifier.** Une sonde isolée a lancé `serve --service --hostname 127.0.0.1 --port 0` avec un `XDG_STATE_HOME` neuf. Le registre public de service contient `id`, `password`, `pid`, `url`, `version`; le PID correspond au seul enfant lancé. L’authentification Basic (`opencode`, mot de passe généré) donne HTTP 200 sur `/api/info`. `api server.info`, avec le même environnement isolé, retrouve le même PID. Le serveur a été arrêté par son handle de processus, sans appel à un service personnel. Sans authentification, `/api/info` et la création de session renvoient 401. Un Bearer contenant le mot de passe n’est pas accepté (401).
4. **Admission sur ce serveur HTTP dédié.** Deux POST concurrents avec `id: "msg_acme_retry_contract"`, `text: "Acme durable admission"`, `resume: false` ont renvoyé la même admission, même ID et même date. Un troisième POST a renvoyé cette admission; la lecture inbox a retourné exactement une entrée. Cela prouve uniquement l’admission en attente de cette sonde, pas la livraison de bout en bout, la reprise après ACK perdu ou l’idempotence des commandes.
5. **Client public et authentification.** Le paquet publié `@opencode/client@2.0.18`, lié par la documentation client, expose `Service.headers(endpoint)` et un endpoint Basic avec `username/password`. Son implémentation publique utilise le nom `opencode`. La documentation expose `Service.ensure({file, version, command, onStart})`. Elle permet de spécifier un service dédié; elle ne donne pas au plugin actuel toutes les méthodes de l’API HTTP.
6. **RPC plugin.** Le RPC public ajoute des méthodes implémentées par le plugin; il ne crée pas les méthodes absentes de son contexte. Aucun import privé Core, fork d’OpenCode, accès direct à sa base ou proxy arbitraire n’a été ajouté.

**Décision à reprendre avant l’UI :** réviser explicitement le lancement pour qu’un serveur HTTP privé précède Mini et en soit le propriétaire partagé avec Shipyard, ou choisir une version dont le contexte plugin couvre les opérations requises et le prouver. La première piste doit définir le passage d’authentification à Mini, la survie dans tmux, le registre par instance, le nettoyage et le traitement des lanceurs qui réécrivent XDG. Les preuves ci-dessus ne déclarent pas cette intégration réalisée. Une simple montée de version n’a pas été démontrée suffisante. Aucun binaire utilisateur n’a été mis à jour.

### Vérifications effectuées

Go 1.27.2 installé uniquement dans `app/node_modules/.shipyard-tools/go`, archive vérifiée par SHA-256 officiel. Dépendances app et docs installées normalement dans ce worktree, sans symlink.

- `go vet ./...` : exit 0.
- `go test -race ./...` : exit 0 après relance avec un délai suffisant; première exécution interrompue par la limite de 120 s. Le contrat opt-in est ignoré dans cette commande sans `OPENCODE_TEST_BIN`, il n’est donc pas validé par ce résultat.
- `go test -race ./internal/box ./internal/integrations/adapters -run OpenCode -count=1` : exit 0; même réserve pour le contrat opt-in.
- `OPENCODE_TEST_BIN=/Users/alexandrecohen/.local/share/opencode/bin/opencode go test ./internal/box -run '^TestOpenCodeLiveV2Contract$' -count=1 -v` : exécuté réellement, **exit 1 au contrôle de surface plugin**. Les hooks et réponses synthétiques du test historique ont fonctionné. Le lot 0 reste bloqué.
- `cargo check` dans `app/src-tauri` : exit 0.
- Dans `app`, `CI=true pnpm install --frozen-lockfile`, `pnpm typecheck:plugins`, `pnpm check:titles`, `pnpm check:csp`, `pnpm check:themes`, `pnpm test`, `pnpm build`, `npx tsc -p e2e` : chaque commande exit 0. Tests unitaires app : 271 réussis; thèmes : 24 réussis.
- `pnpm build` dans `docs-site` : exit 0. Avertissement préexistant de téléchargement de police dynamique pour `⌘` (HTTP 400), sans échec du build.
- Après contrôle que le port 1434 est libre, `E2E_PORT=1434 E2E_WORKERS=2 BERTH_E2E_LIVE=0 npx playwright test e2e/composer.spec.ts` : **3 réussis**, dont le choix fournisseur/modèle/variante préservé dans la baseline.
- `e2e/opencode.spec.ts` non créé/non exécuté : aucun lot UI engagé. Tests Linux, deux runtimes simultanés avec permissions, reprise berthd, parcours complet et version minimale/courante : **non validés**.

`plans/README.md` reste au reviewer. Aucun push, PR, déploiement ou changement des services personnels n’a été effectué.

### Révision après vérification du blocage

Le reviewer a relu les preuves et les guides publics CLI, Web, Troubleshooting et client le 2026-10-09. Le STOP sur le pont plugin est confirmé, mais une révision de transport permet de continuer l'objectif approuvé : serveur HTTP privé par tâche, client Mini et client Shipyard sur le même processus. La section lot 0 ci-dessus remplace la proposition de pont. Le résultat négatif du test plugin reste une preuve historique, pas une obligation d'ajouter une méthode au plugin ou de garder un test volontairement rouge.

Contrat de lancement à prouver : authentification par mécanisme public, refus non authentifié, session liée identique vue par Mini et API, arrêt/permission affectant le propriétaire attendu, survie aux déconnexions du client/daemon, arrêt limité aux processus possédés. Les mots de passe ne vont ni dans les arguments, ni les URL, ni le journal ; registre privé et env seulement lorsque le binaire les prend en charge. Ne pas modifier le service personnel. Le lanceur utilisateur connu réécrit XDG : détecter explicitement l'échec d'isolation ; ne pas déduire un exécutable en décodant arbitrairement un script. Un chemin explicite de binaire compatible ou un mécanisme de connexion explicite documenté peut résoudre ce cas. Les essais restent isolés avec l'exécutable réel déjà identifié.

### Reprise — contrôle de création avec image

Les contrôles Go/app/docs et le contrat HTTP étendu ont passé avant l'ajout du scénario de première image sur un nouveau worktree. Ce dernier a ensuite échoué plusieurs fois : configuration synthétique initialement non commitée dans le repo jetable, assertion de chemin non canonique sur macOS, puis délai sans requête image observée. Le lot n'est pas validé sur ces résultats. Diagnostic ciblé du lanceur en cours, sans élargissement de périmètre ; les statuts du README restent au reviewer.

## 11. Recette de l'implémentation révisée

Le compte rendu actuel est `plans/001-opencode-native-acceptance.md`. Il distingue l'implémentation des lots 0–7, les tests réellement exécutés et les limites de validation. La section 10 conserve les preuves historiques du pont plugin abandonné ; elle ne décrit pas l'état actuel du transport HTTP natif. Les statuts de `plans/README.md` restent ceux du reviewer.
