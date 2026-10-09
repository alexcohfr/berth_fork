import type { Client } from "@/lib/api";

// Skills teach the agent tools on a box (Claude Code, Codex) to use berth.
// The box installs them for its user or inside one repository.

export type SkillAgent = "claude" | "codex" | "opencode";
export type SkillState = "installed" | "outdated" | "missing";
export type SkillTarget = "user" | "project";

export interface SkillRow {
  name: string;
  description: string;
  version: string;
  user: Record<SkillAgent, SkillState>;
  project?: Record<SkillAgent, SkillState>;
  // Per agent: whether the project copy is kept out of git.
  excluded?: Record<SkillAgent, boolean>;
}

export interface SkillsReport {
  agents: SkillAgent[];
  user_dirs: Record<SkillAgent, string>;
  project_dirs?: Record<SkillAgent, string>;
  location?: string;
  skills: SkillRow[];
}

export interface SkillsChange {
  skills: string[] | "all";
  agent: SkillAgent | "all";
  target: SkillTarget;
  location?: string;
  commit?: boolean;
}

export const skillsApi = {
  list: (c: Client, box: string, location?: string) => c.box<SkillsReport>(box, "GET", location ? `skills?location=${encodeURIComponent(location)}` : "skills"),
  install: (c: Client, box: string, req: SkillsChange) => c.box<SkillsReport>(box, "POST", "skills/install", req),
  uninstall: (c: Client, box: string, req: SkillsChange) => c.box<SkillsReport>(box, "POST", "skills/uninstall", req),
};

// What each skill is for, in a line; the SKILL.md descriptions are written
// for agents and run long.
export const skillSummaries: Record<string, string> = {
  berth: "Find their way around: repos, worktrees, tasks, ports and the repo's config",
  "berth-orchestrate": "Prompt, wait for, check and hand off to other agents",
  "berth-preview": "Run the dev server on the worktree's port and show it to you here",
  "berth-hooks": "Write hooks and gates for this box, a repo or the laptop",
  "berth-browser": "Open, click through and screenshot the worktree's page at any size",
  "berth-artifacts": "Show you charts, tables, diagrams and small pages here, live",
  "berth-visual-diff": "Screenshot pages before and after a change and show what moved",
};

export function skillSummary(s: Pick<SkillRow, "name" | "description">): string {
  return skillSummaries[s.name] ?? s.description.split(/(?<=\.)\s/)[0];
}
