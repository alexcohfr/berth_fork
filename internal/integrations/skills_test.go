package integrations

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBerthShipsItsSkillsWithFrontmatter(t *testing.T) {
	skills := Skills()
	names := []string{}
	for _, s := range skills {
		names = append(names, s.Name)
		b, _ := SkillContent(s.Name)
		if frontmatter(b, "name") != s.Name || s.Description == "" || len(s.Version) != 12 {
			t.Errorf("%s: name %q, description %q, version %q", s.Name, frontmatter(b, "name"), s.Description, s.Version)
		}
	}
	if strings.Join(names, ",") != "berth,berth-artifacts,berth-browser,berth-hooks,berth-orchestrate,berth-preview,berth-visual-diff" {
		t.Fatalf("skills = %v", names)
	}
}

func TestSkillsInstallReportAndUninstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	names, _ := SkillNames([]string{"all"})
	for _, agent := range SkillAgents() {
		if st, _ := UserSkillStatus(home, agent, "berth"); st != SkillMissing {
			t.Fatalf("%s before install: %s", agent, st)
		}
		paths, err := InstallSkills(home, agent, names)
		if err != nil || len(paths) != len(names) {
			t.Fatalf("%s install: %v %v", agent, paths, err)
		}
		if st, _ := UserSkillStatus(home, agent, "berth-preview"); st != SkillInstalled {
			t.Fatalf("%s after install: %s", agent, st)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "berth", "SKILL.md")); err != nil {
		t.Fatalf("codex skills are not where Codex reads them: %v", err)
	}
	// An older copy is outdated until installed again.
	path := filepath.Join(home, ".claude", "skills", "berth", "SKILL.md")
	os.WriteFile(path, []byte("---\nname: berth\ndescription: old\n---\n"), 0o644)
	if st, _ := SkillStatus(home, "claude", "berth"); st != SkillOutdated {
		t.Fatalf("an old copy is %s", st)
	}
	// Someone else's skill in the same folder is never removed.
	other := filepath.Join(home, ".claude", "skills", "berth-hooks", "SKILL.md")
	os.WriteFile(other, []byte("---\nname: my-hooks\n---\n"), 0o644)
	if _, err := UninstallSkills(home, "claude", []string{"berth-hooks"}); err == nil {
		t.Fatal("removed a skill berth did not write")
	}
	removed, err := UninstallSkills(home, "claude", []string{"berth", "berth-preview"})
	if err != nil || len(removed) != 2 {
		t.Fatalf("uninstall: %v %v", removed, err)
	}
	if st, _ := SkillStatus(home, "claude", "berth"); st != SkillMissing {
		t.Fatalf("after uninstall: %s", st)
	}
	if _, err := SkillNames([]string{"nope"}); err == nil {
		t.Fatal("an unknown skill was accepted")
	}
}

func TestInstallingForCodexRemovesTheOldCopyCodexNoLongerReads(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".codex", "skills", "berth")
	os.MkdirAll(legacy, 0o755)
	os.WriteFile(filepath.Join(legacy, "SKILL.md"), []byte("---\nname: berth\n---\nold"), 0o644)
	mine := filepath.Join(home, ".codex", "skills", "mine")
	os.MkdirAll(mine, 0o755)
	os.WriteFile(filepath.Join(mine, "SKILL.md"), []byte("---\nname: mine\n---\n"), 0o644)
	if _, err := InstallSkills(home, "codex", []string{"berth"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("the legacy copy is still there")
	}
	if _, err := os.Stat(mine); err != nil {
		t.Fatal("a user's own Codex skill was removed")
	}
}

func TestOpenCodeSkillsKeepUserOverridesSeparateFromProject(t *testing.T) {
	home, repo, xdg, override := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	if dir, err := UserSkillDir(home, "opencode"); err != nil || dir != filepath.Join(xdg, "opencode", "skills") {
		t.Fatalf("XDG skill scope: %s %v", dir, err)
	}
	t.Setenv("OPENCODE_CONFIG_DIR", override)
	if _, err := InstallSkills(home, "opencode", []string{"berth"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(override, "skills", "berth", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallProjectSkills(repo, "opencode", []string{"berth"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".opencode", "skills", "berth", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallProjectSkills(repo, "opencode", []string{"berth"}); err != nil {
		t.Fatal(err)
	}
	if state, err := UserSkillStatus(home, "opencode", "berth"); err != nil || state != SkillInstalled {
		t.Fatalf("project removal affected user scope: %s %v", state, err)
	}
	if _, err := UninstallSkills(home, "opencode", []string{"berth"}); err != nil {
		t.Fatal(err)
	}
}

func TestProjectSkillsStayOutOfGitUntilCommitted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	git("init", "-q", "-b", "main")
	exclude := filepath.Join(repo, ".git", "info", "exclude")
	os.MkdirAll(filepath.Dir(exclude), 0o755)
	os.WriteFile(exclude, []byte("# mine\n*.log\n"), 0o644)
	git("commit", "-q", "--allow-empty", "-m", "init")

	names := []string{"berth", "berth-preview"}
	InstallSkills(repo, "claude", names)
	if err := ExcludeSkills(repo, "claude", names, true); err != nil {
		t.Fatal(err)
	}
	ExcludeSkills(repo, "claude", names, true) // twice is the same as once
	out, _ := exec.Command("git", "-C", repo, "status", "--porcelain").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("installed skills show in git status: %s", out)
	}
	if !Excluded(repo, "claude", "berth") {
		t.Fatal("Excluded does not see the skill")
	}
	b, _ := os.ReadFile(exclude)
	if strings.Count(string(b), "/.claude/skills/berth/") != 1 || !strings.Contains(string(b), "*.log") {
		t.Fatalf("exclude = %q", b)
	}
	// From a worktree, the shared exclude file is the one edited.
	wt := filepath.Join(t.TempDir(), "wt")
	git("worktree", "add", "-q", wt)
	if err := ExcludeSkills(wt, "codex", []string{"berth"}, true); err != nil {
		t.Fatal(err)
	}
	ExcludeSkills(repo, "claude", names, false)
	ExcludeSkills(wt, "codex", []string{"berth"}, false)
	b, _ = os.ReadFile(exclude)
	if string(b) != "# mine\n*.log\n" {
		t.Fatalf("exclude after removing = %q", b)
	}
}

// A repository can commit .claude/skills/<skill>/SKILL.md, or a folder on
// the way to it, as a symbolic link. Installing berth's project skills must
// not write through it (security audit M-5).
func TestProjectSkillInstallRefusesSymlinks(t *testing.T) {
	names, err := SkillNames(nil)
	if err != nil || len(names) == 0 {
		t.Fatal("no skills", err)
	}
	const keys = "ssh-ed25519 AAAA... me@laptop\n"
	for _, tc := range []struct{ name, link string }{
		{"file", filepath.Join(".claude", "skills", names[0], "SKILL.md")},
		{"skill folder", filepath.Join(".claude", "skills", names[0])},
		{"skills folder", filepath.Join(".claude", "skills")},
		{"in-repo target", filepath.Join(".claude", "skills", names[0], "SKILL.md")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			victim := filepath.Join(tmp, "home", ".ssh", "authorized_keys")
			os.MkdirAll(filepath.Dir(victim), 0o700)
			os.WriteFile(victim, []byte(keys), 0o600)
			repo := filepath.Join(tmp, "repo")
			link := filepath.Join(repo, tc.link)
			os.MkdirAll(filepath.Dir(link), 0o755)
			target := victim
			switch {
			case tc.name == "in-repo target":
				target = filepath.Join(repo, ".git", "hooks", "pre-commit")
				os.MkdirAll(filepath.Dir(target), 0o755)
				os.WriteFile(target, []byte(keys), 0o755)
				victim = target
			case filepath.Base(tc.link) != "SKILL.md":
				// A folder link: point it at a folder holding the victim's
				// stand-in where SKILL.md would land.
				dir := filepath.Join(tmp, "elsewhere")
				sub := dir
				if tc.name == "skills folder" {
					sub = filepath.Join(dir, names[0])
				}
				os.MkdirAll(sub, 0o700)
				victim = filepath.Join(sub, "SKILL.md")
				os.WriteFile(victim, []byte(keys), 0o600)
				target = dir
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if _, err := InstallProjectSkills(repo, "claude", names[:1]); err == nil {
				t.Fatal("install wrote through a symbolic link")
			}
			if got, _ := os.ReadFile(victim); string(got) != keys {
				t.Fatalf("%s was overwritten: %q", victim, got)
			}
			if _, err := UninstallProjectSkills(repo, "claude", names[:1]); err == nil {
				t.Fatal("uninstall followed a symbolic link")
			}
			if _, err := os.Stat(victim); err != nil {
				t.Fatalf("uninstall removed %s through a link", victim)
			}
		})
	}
	// A plain repository still installs.
	repo := t.TempDir()
	if _, err := InstallProjectSkills(repo, "claude", names[:1]); err != nil {
		t.Fatal(err)
	}
	if st, _ := SkillStatus(repo, "claude", names[0]); st != SkillInstalled {
		t.Fatalf("status = %s", st)
	}
	if _, err := InstallProjectSkills(repo, "codex", names[:1]); err != nil {
		t.Fatal(err)
	}
	if removed, err := UninstallProjectSkills(repo, "claude", names[:1]); err != nil || len(removed) != 1 {
		t.Fatal(removed, err)
	}
}
