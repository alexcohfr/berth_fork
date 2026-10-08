# Plans d’évolution Shipyard

Planification du 2026-10-08, base `bc92f1c` + travail local décrit dans le plan.

## Plan 001 — Interface OpenCode complète

Fichier : `plans/001-opencode-native-workspace.md`.

Cible : utiliser le moteur OpenCode depuis Shipyard, avec ses prompts, modèles, skills et MCP, tout en conservant les machines, worktrees, terminaux, tâches et revues Shipyard.

Statut global : **TODO — planifié, non implémenté**.

## Ordre et suivi

- Lot 0 — Canal vers le runtime propriétaire : **TODO**. Priorité P0, effort M, aucune dépendance.
- Lot 1 — Envoi, arrêt et reconnexion natifs : **TODO**. Priorité P1, effort L, dépend du lot 0.
- Lot 2 — Permissions et formulaires dans le chat : **TODO**. Priorité P1, effort M, dépend du lot 1.
- Lot 3 — Modèles/profils, commandes, skills et pièces jointes : **TODO**. Priorité P1, effort L, dépend des lots 1–2.
- Lot 4 — Historique, reprise, contexte et sous-agents : **TODO**. Priorité P2, effort L, dépend des lots 1–3.
- Lot 5 — Réglages et continuité local/distant : **TODO**. Priorité P2, effort L, dépend des lots 0–3.
- Lot 6 — Fork, retour arrière et workflow complet : **TODO**. Priorité P2, effort L, dépend des lots 1–4.
- Lot 7 — Compatibilité et recette finale : **TODO**. Priorité P1 pour livraison, effort M, dépend des lots précédents.

Premier jalon quotidien : lots 0–3. Parité avancée : lots 4–7.

Statuts possibles : TODO, IN PROGRESS, DONE, BLOCKED (raison), REJECTED (raison). L’exécuteur met à jour le lot avec les tests réellement exécutés et la version OpenCode validée.

## Décisions et périmètre

- Partir du chat OpenCode V2 déjà intégré et préserver le sélecteur dynamique de modèles en cours.
- OpenCode reste propriétaire de la conversation et de ses instructions ; Shipyard reste propriétaire des machines et worktrees.
- Le canal de lecture actuel n’est pas utilisé comme preuve d’un contrôle du runtime actif : ce contrat est la dépendance critique du lot 0.
- Pas de réécriture du moteur agent, de fork d’OpenCode ou de synchronisation automatique des secrets.
- Étude ciblée de l’intégration OpenCode, pas audit exhaustif du dépôt. Code, CI et documentation V2 consultés ; tests/builds non exécutés pendant la planification.
- Aucun changement de code applicatif réalisé pour ce plan. Les modifications locales préexistantes restent hors de cette livraison.
