package integrations

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// The skills berth ships teach agent tools to use it. Each is a folder with a
// SKILL.md, installed where a tool discovers skills: for a user on the
// machine, or inside one repository.

//go:embed skills
var skillFiles embed.FS

// SkillInfo describes one skill berth ships.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Version identifies its content, so an installed copy can be told
	// current or outdated.
	Version string `json:"version"`
}

// SkillContent is a skill's SKILL.md.
func SkillContent(name string) ([]byte, error) {
	return skillFiles.ReadFile("skills/" + name + "/SKILL.md")
}

// Skills lists the skills berth ships, in name order.
func Skills() []SkillInfo {
	dirs, _ := fs.ReadDir(skillFiles, "skills")
	var out []SkillInfo
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		b, err := SkillContent(d.Name())
		if err != nil {
			continue
		}
		out = append(out, SkillInfo{Name: d.Name(), Description: frontmatter(b, "description"), Version: version(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func version(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}

// frontmatter reads one key from a SKILL.md's leading --- block.
func frontmatter(b []byte, key string) string {
	text := string(b)
	if !strings.HasPrefix(text, "---\n") {
		return ""
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return ""
	}
	for _, line := range strings.Split(text[4:4+end], "\n") {
		if v, ok := strings.CutPrefix(line, key+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Agent tools and where each discovers skills, relative to a home folder
// (user skills) or a repository (project skills).
var skillDirs = map[string]string{
	"opencode": filepath.Join(".opencode", "skills"),
	"claude":   filepath.Join(".claude", "skills"),
	// Codex reads $HOME/.agents/skills and <repo>/.agents/skills. It is
	// $HOME's, not $CODEX_HOME's: every Codex account on the machine reads
	// the same user skills, so they are installed once, not per account.
	"codex": filepath.Join(".agents", "skills"),
}

// legacyCodexDir is where earlier berth versions put the skill for Codex,
// which Codex no longer reads.
var legacyCodexDir = filepath.Join(".codex", "skills")

// SkillAgents are the tools skills install for.
func SkillAgents() []string { return []string{"claude", "codex", "opencode"} }

// UserSkillDir differs from the repository scope for OpenCode.
func UserSkillDir(home, agent string) (string, error) {
	if agent == "opencode" {
		config := os.Getenv("OPENCODE_CONFIG_DIR")
		if config == "" {
			config = filepath.Join(firstConfigHome(home), "opencode")
		}
		return filepath.Join(config, "skills"), nil
	}
	return SkillDir(home, agent)
}

func firstConfigHome(home string) string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	return filepath.Join(home, ".config")
}

// SkillState is an installed copy compared with what berth ships.
type SkillState string

const (
	SkillInstalled SkillState = "installed"
	SkillOutdated  SkillState = "outdated"
	SkillMissing   SkillState = "missing"
)

// SkillDir resolves repository skills. Use UserSkillDir for the user scope.
func SkillDir(root, agent string) (string, error) {
	rel, ok := skillDirs[agent]
	if !ok {
		return "", fmt.Errorf("unknown agent %q; use claude, codex or opencode", agent)
	}
	return filepath.Join(root, rel), nil
}

// SkillStatus reports one skill for one agent under root.
func SkillStatus(root, agent, name string) (SkillState, error) {
	dir, err := SkillDir(root, agent)
	if err != nil {
		return "", err
	}
	return skillStatusIn(dir, name)
}

// skillStatusIn reports one skill in a skills folder.
func skillStatusIn(dir, name string) (SkillState, error) {
	want, err := SkillContent(name)
	if err != nil {
		return "", fmt.Errorf("berth has no skill called %s", name)
	}
	have, err := os.ReadFile(filepath.Join(dir, name, "SKILL.md"))
	switch {
	case os.IsNotExist(err):
		return SkillMissing, nil
	case err != nil:
		return "", err
	case bytes.Equal(have, want):
		return SkillInstalled, nil
	}
	return SkillOutdated, nil
}

// InstallSkills writes the named skills for agent under root, replacing
// older copies, and returns the paths written. root is a home folder the
// user owns, so a skills folder they linked elsewhere is followed.
func InstallSkills(root, agent string, names []string) ([]string, error) {
	return installSkills(root, agent, names, false)
}

// InstallProjectSkills writes the named skills into a repository. A
// repository's files are not the user's own: one could commit
// .claude/skills/<skill>/SKILL.md as a symbolic link to ~/.ssh/authorized_keys,
// so no link below the repository is followed or replaced.
func InstallProjectSkills(repo, agent string, names []string) ([]string, error) {
	return installSkills(repo, agent, names, true)
}

func installSkills(root, agent string, names []string, strict bool) ([]string, error) {
	if agent == "opencode" && !strict {
		dir, err := UserSkillDir(root, agent)
		if err != nil {
			return nil, err
		}
		return installSkillsIn(dir, "", names, false)
	}
	rel, ok := skillDirs[agent]
	if !ok {
		return nil, fmt.Errorf("unknown agent %q; use claude or codex", agent)
	}
	written, err := installSkillsIn(root, rel, names, strict)
	if err == nil && agent == "codex" {
		removeLegacyCodex(root, names, strict)
	}
	return written, err
}

// installSkillsIn writes the named skills into the folder rel below root.
// strict refuses any symbolic link below root.
func installSkillsIn(root, rel string, names []string, strict bool) ([]string, error) {
	dir := filepath.Join(root, rel)
	var written []string
	for _, name := range names {
		b, err := SkillContent(name)
		if err != nil {
			return written, fmt.Errorf("berth has no skill called %s", name)
		}
		path := filepath.Join(dir, name, "SKILL.md")
		if strict {
			err = writeNoFollow(root, filepath.Join(rel, name, "SKILL.md"), b, 0o644)
		} else if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			err = os.WriteFile(path, b, 0o644)
		}
		if err != nil {
			return written, err
		}
		written = append(written, path)
	}
	return written, nil
}

// removeLegacyCodex deletes copies berth once wrote to ~/.codex/skills, so
// a Codex that still reads there does not load the skill twice.
func removeLegacyCodex(root string, names []string, strict bool) {
	for _, name := range names {
		dir := filepath.Join(root, legacyCodexDir, name)
		if strict && noLinks(root, filepath.Join(legacyCodexDir, name, "SKILL.md")) != nil {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, "SKILL.md")); err == nil && frontmatter(b, "name") == name {
			os.RemoveAll(dir)
		}
	}
}

// UninstallSkills removes the named skills for agent under root. Only a
// folder whose SKILL.md names that skill is removed, so a user's own skill
// of the same folder name is left alone.
func UninstallSkills(root, agent string, names []string) ([]string, error) {
	return uninstallSkills(root, agent, names, false)
}

// UninstallProjectSkills removes the named skills from a repository, refusing
// to remove anything reached through a symbolic link below it.
func UninstallProjectSkills(repo, agent string, names []string) ([]string, error) {
	return uninstallSkills(repo, agent, names, true)
}

func uninstallSkills(root, agent string, names []string, strict bool) ([]string, error) {
	if agent == "opencode" && !strict {
		dir, err := UserSkillDir(root, agent)
		if err != nil {
			return nil, err
		}
		return uninstallSkillsIn(dir, "", names, false)
	}
	rel, ok := skillDirs[agent]
	if !ok {
		return nil, fmt.Errorf("unknown agent %q; use claude or codex", agent)
	}
	return uninstallSkillsIn(root, rel, names, strict)
}

// uninstallSkillsIn removes the named skills from the folder rel below
// root. strict refuses anything reached through a symbolic link below root.
func uninstallSkillsIn(root, rel string, names []string, strict bool) ([]string, error) {
	dir := filepath.Join(root, rel)
	var removed []string
	for _, name := range names {
		if _, err := SkillContent(name); err != nil {
			return removed, fmt.Errorf("berth has no skill called %s", name)
		}
		skill := filepath.Join(dir, name)
		if strict {
			if err := noLinks(root, filepath.Join(rel, name, "SKILL.md")); err != nil {
				return removed, err
			}
		}
		b, err := os.ReadFile(filepath.Join(skill, "SKILL.md"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return removed, err
		}
		if frontmatter(b, "name") != name {
			return removed, fmt.Errorf("%s is not berth's %s skill; left it alone", skill, name)
		}
		if err := os.RemoveAll(skill); err != nil {
			return removed, err
		}
		removed = append(removed, skill)
	}
	return removed, nil
}

// SkillNames resolves "all" or a list against what berth ships.
func SkillNames(requested []string) ([]string, error) {
	all := Skills()
	if len(requested) == 0 || (len(requested) == 1 && requested[0] == "all") {
		names := make([]string, len(all))
		for i, s := range all {
			names[i] = s.Name
		}
		return names, nil
	}
	known := map[string]bool{}
	for _, s := range all {
		known[s.Name] = true
	}
	for _, n := range requested {
		if !known[n] {
			return nil, fmt.Errorf("berth has no skill called %s", n)
		}
	}
	return requested, nil
}

// excludeMarker heads the lines berth adds to a repository's
// .git/info/exclude, so it can find and remove them again.
const excludeMarker = "# berth skills (not committed; see berth skills)"

// ExcludeSkills keeps installed project skills out of git by listing them in
// the repository's .git/info/exclude; include=false removes them again.
func ExcludeSkills(repo string, agent string, names []string, include bool) error {
	dir, err := SkillDir("", agent)
	if err != nil {
		return err
	}
	common, err := gitCommonDir(repo)
	if err != nil {
		return err
	}
	path := filepath.Join(common, "info", "exclude")
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(b) == 0 {
		lines = nil
	}
	want := map[string]bool{}
	for _, n := range names {
		want["/"+filepath.ToSlash(filepath.Join(dir, n))+"/"] = true
	}
	var out []string
	have := map[string]bool{}
	for _, l := range lines {
		if want[l] {
			if !include {
				continue
			}
			have[l] = true
		}
		out = append(out, l)
	}
	if include {
		added := false
		for _, n := range names {
			l := "/" + filepath.ToSlash(filepath.Join(dir, n)) + "/"
			if have[l] {
				continue
			}
			if !added && !contains(out, excludeMarker) {
				out = append(out, excludeMarker)
			}
			out = append(out, l)
			added = true
		}
	}
	// Drop the marker once no berth lines follow it.
	if !hasSkillLines(out) {
		out = without(out, excludeMarker)
	}
	text := strings.Join(out, "\n")
	if text != "" {
		text += "\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// Excluded reports whether a project skill is kept out of git by berth.
func Excluded(repo, agent, name string) bool {
	dir, err := SkillDir("", agent)
	if err != nil {
		return false
	}
	common, err := gitCommonDir(repo)
	if err != nil {
		return false
	}
	b, _ := os.ReadFile(filepath.Join(common, "info", "exclude"))
	return contains(strings.Split(string(b), "\n"), "/"+filepath.ToSlash(filepath.Join(dir, name))+"/")
}

func gitCommonDir(repo string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", errors.New(repo + " is not a git repository")
	}
	dir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repo, dir)
	}
	return dir, nil
}

func hasSkillLines(lines []string) bool {
	for _, l := range lines {
		for _, rel := range skillDirs {
			if strings.HasPrefix(l, "/"+filepath.ToSlash(rel)+"/") {
				return true
			}
		}
	}
	return false
}

func contains(lines []string, s string) bool {
	for _, l := range lines {
		if l == s {
			return true
		}
	}
	return false
}

func without(lines []string, s string) []string {
	var out []string
	for _, l := range lines {
		if l != s {
			out = append(out, l)
		}
	}
	return out
}
