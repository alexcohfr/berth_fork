# Plans d’évolution Shipyard

Planification du 2026-10-08, base `bc92f1c` + travail local décrit dans le plan.

## Plan 001 — Interface OpenCode complète

Fichier : `plans/001-opencode-native-workspace.md`.

Cible : utiliser le moteur OpenCode depuis Shipyard, avec ses prompts, modèles, skills et MCP, tout en conservant les machines, worktrees, terminaux, tâches et revues Shipyard.

Statut global : **IN PROGRESS — code des huit lots livré ; revue et recette finale le 2026-10-09**.

Worktree : `/Users/alexandrecohen/projets/berth_fork-opencode-native`.
Branche : `feat/opencode-native-workspace`. Baseline préservée : `a175393`.
Compte rendu : `plans/001-opencode-native-acceptance.md`. Le contrat 2.0.18 passe sur macOS et Linux arm64 ; le 2.0.26 passe sur Linux arm64. Le scénario complet et le démarrage macOS 2.0.26 restent à valider ; la parité quotidienne complète n'est pas encore déclarée.

## Ordre et suivi

- Lot 0 — Canal vers le runtime propriétaire : **DONE**. Serveur HTTP privé authentifié + Mini, contrat réel OpenCode 2.0.18 sur macOS et Linux arm64.
- Lot 1 — Envoi, arrêt et reconnexion natifs : **DONE**. Contrat et tests navigateur validés ; gates box/projet centralisées pour API, automatisations et rapports, avec test de non-régression et suite Go complète réussis.
- Lot 2 — Permissions et formulaires dans le chat : **DONE**. Demandes natives identifiées, formulaires typés et demandes des enfants.
- Lot 3 — Modèles/profils, commandes, skills et pièces jointes : **DONE**. Catalogues natifs, média au lancement simple et en conversation. Pour les boucles, essais multiples, handoffs et commandes de lancement personnalisées, joindre les fichiers après lancement.
- Lot 4 — Historique, reprise, contexte et sous-agents : **DONE**. Pagination 250+ messages, reprise d'identité, compaction et enfants vérifiés.
- Lot 5 — Réglages et continuité local/distant : **DONE**. Projections expurgées, OAuth synthétique, édition JSONC et transfert explicite avec aperçu.
- Lot 6 — Fork, retour arrière et workflow complet : **IN PROGRESS**. Fork et rewind conversationnel validés ; parcours unique tâche → tests → preview → revue → instruction restant. Restauration des fichiers désactivée conformément au plan.
- Lot 7 — Compatibilité et recette finale : **BLOCKED — macOS/OpenCode 2.0.26 et recette complète**. Contrôles locaux et contrat 2.0.18 validés sur macOS ; contrats 2.0.18 et 2.0.26 réussis sous Linux arm64 après correction de la disponibilité au démarrage. La 2.0.26 ne démarre pas dans les sondes isolées macOS. Le scénario complet reste ouvert.

Premier jalon quotidien : lots 0–3. Parité avancée : lots 4–7.

Statuts possibles : TODO, IN PROGRESS, DONE, BLOCKED (raison), REJECTED (raison). Le reviewer valide les statuts après lecture du diff et vérification des tests réellement exécutés. DONE se limite à la portée et à l'environnement indiqués, sans supposer une validation Linux.

## Décisions et périmètre

- Partir du chat OpenCode V2 déjà intégré et préserver le sélecteur dynamique de modèles en cours.
- OpenCode reste propriétaire de la conversation et de ses instructions ; Shipyard reste propriétaire des machines et worktrees.
- Le canal de lecture actuel n’est pas utilisé comme preuve d’un contrôle du runtime actif : ce contrat est la dépendance critique du lot 0.
- Pas de réécriture du moteur agent, de fork d’OpenCode ou de synchronisation automatique des secrets.
- Étude ciblée de l’intégration OpenCode, pas audit exhaustif du dépôt. Code, CI et documentation V2 consultés ; tests/builds non exécutés pendant la planification.
- Implémentation commitée dans le worktree dédié ; modifications locales préexistantes conservées dans le dépôt principal. Les preuves de la première exploration du pont plugin restent historiques, le contrat final porte sur HTTP.
