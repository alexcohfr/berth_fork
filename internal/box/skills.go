package box

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cosscom/shipyard/internal/integrations"
)

// Skills teach agent tools on this box to use berth. They install for the
// box's user, or inside one location's repository for just that project.

// SkillTargets is a skill's state for each agent tool in one place.
type SkillTargets map[string]integrations.SkillState

// SkillRow is one skill and where it is installed.
type SkillRow struct {
	integrations.SkillInfo
	User    SkillTargets `json:"user"`
	Project SkillTargets `json:"project,omitempty"`
	// Excluded is, per agent, whether the project copy is kept out of git.
	Excluded map[string]bool `json:"excluded,omitempty"`
}

type SkillsReport struct {
	Agents []string `json:"agents"`
	// UserDirs and ProjectDirs are where each agent's skills go.
	UserDirs    map[string]string `json:"user_dirs"`
	ProjectDirs map[string]string `json:"project_dirs,omitempty"`
	Location    string            `json:"location,omitempty"`
	Skills      []SkillRow        `json:"skills"`
}

// SkillsRequest installs or removes skills.
type SkillsRequest struct {
	// Skills are names, or ["all"].
	Skills []string `json:"skills"`
	// Agent is claude, codex, or all.
	Agent string `json:"agent"`
	// Target is user or project; project needs Location.
	Target   string `json:"target"`
	Location string `json:"location,omitempty"`
	// Commit leaves project skills visible to git, to be committed and
	// shared; by default they are listed in .git/info/exclude.
	Commit bool `json:"commit,omitempty"`
}

// UnmarshalJSON accepts "skills": "all" as well as a list.
func (r *SkillsRequest) UnmarshalJSON(b []byte) error {
	type plain SkillsRequest
	var raw struct {
		plain
		Skills json.RawMessage `json:"skills"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*r = SkillsRequest(raw.plain)
	if len(raw.Skills) > 0 {
		var one string
		if json.Unmarshal(raw.Skills, &one) == nil {
			r.Skills = []string{one}
		} else if err := json.Unmarshal(raw.Skills, &r.Skills); err != nil {
			return err
		}
	}
	return nil
}

// SkillsReport lists the skills berth ships and where they are installed,
// including inside location's repository when location is set.
func (b *Box) SkillsReport(r *http.Request, location string) (SkillsReport, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return SkillsReport{}, err
	}
	out := SkillsReport{Agents: integrations.SkillAgents(), UserDirs: map[string]string{}, Location: location, Skills: []SkillRow{}}
	repo := ""
	if location != "" {
		loc, err := b.Locations.Get(r.Context(), location)
		if err != nil {
			return SkillsReport{}, err
		}
		if !loc.Repo {
			return SkillsReport{}, badRequest("%s is not a git repository", location)
		}
		repo = loc.Path
		out.ProjectDirs = map[string]string{}
	}
	for _, agent := range out.Agents {
		out.UserDirs[agent], _ = integrations.UserSkillDir(home, agent)
		if repo != "" {
			out.ProjectDirs[agent], _ = integrations.SkillDir(repo, agent)
		}
	}
	for _, s := range integrations.Skills() {
		row := SkillRow{SkillInfo: s, User: SkillTargets{}}
		if repo != "" {
			row.Project, row.Excluded = SkillTargets{}, map[string]bool{}
		}
		for _, agent := range out.Agents {
			row.User[agent], _ = integrations.UserSkillStatus(home, agent, s.Name)
			if repo != "" {
				row.Project[agent], _ = integrations.SkillStatus(repo, agent, s.Name)
				row.Excluded[agent] = integrations.Excluded(repo, agent, s.Name)
			}
		}
		out.Skills = append(out.Skills, row)
	}
	return out, nil
}

func (b *Box) listSkills(w http.ResponseWriter, r *http.Request) error {
	rep, err := b.SkillsReport(r, r.URL.Query().Get("location"))
	if err != nil {
		return err
	}
	writeJSON(w, rep)
	return nil
}

func (b *Box) installSkills(w http.ResponseWriter, r *http.Request) error {
	return b.changeSkills(w, r, true)
}

func (b *Box) uninstallSkills(w http.ResponseWriter, r *http.Request) error {
	return b.changeSkills(w, r, false)
}

func (b *Box) changeSkills(w http.ResponseWriter, r *http.Request, install bool) error {
	var req SkillsRequest
	if err := decode(r, &req); err != nil {
		return err
	}
	names, err := integrations.SkillNames(req.Skills)
	if err != nil {
		return badRequest("%v", err)
	}
	agents := []string{req.Agent}
	if req.Agent == "" || req.Agent == "all" {
		agents = integrations.SkillAgents()
	}
	root, repo := "", ""
	switch req.Target {
	case "", "user":
		req.Target = "user"
		if root, err = os.UserHomeDir(); err != nil {
			return err
		}
	case "project":
		if req.Location == "" {
			return badRequest("a project install needs a location")
		}
		loc, err := b.Locations.Get(r.Context(), req.Location)
		if err != nil {
			return err
		}
		if !loc.Repo {
			return badRequest("%s is not a git repository", req.Location)
		}
		root, repo = loc.Path, loc.Path
	default:
		return badRequest("target is user or project")
	}
	typ, action := "skills.installed", "skills.install"
	if !install {
		typ, action = "skills.removed", "skills.uninstall"
	}
	data := map[string]any{"skills": names, "agents": agents, "target": req.Target, "location": req.Location}
	if err := b.before(r, action, data); err != nil {
		return err
	}
	var paths []string
	for _, agent := range agents {
		var done []string
		switch {
		case install && repo != "":
			done, err = integrations.InstallProjectSkills(root, agent, names)
		case install:
			// Every Claude Code account folder gets them (integrations/accounts.go).
			done, err = integrations.InstallUserSkills(root, agent, names)
		case repo != "":
			done, err = integrations.UninstallProjectSkills(root, agent, names)
		default:
			done, err = integrations.UninstallUserSkills(root, agent, names)
		}
		paths = append(paths, done...)
		if err != nil {
			return badRequest("%v", err)
		}
		if repo != "" {
			// Committed skills are visible to git; otherwise they stay local.
			if err := integrations.ExcludeSkills(repo, agent, names, install && !req.Commit); err != nil {
				return err
			}
		}
	}
	for i, p := range paths {
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
			paths[i] = rel
		}
	}
	data["paths"] = paths
	b.publish(r, typ, data)
	rep, err := b.SkillsReport(r, req.Location)
	if err != nil {
		return err
	}
	writeJSON(w, rep)
	return nil
}
