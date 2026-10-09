package box

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

var openCodeCapabilitiesCache sync.Map

func openCodeCapabilities(ctx context.Context, ep openCodeEndpoint) ([]string, error) {
	if cached, ok := openCodeCapabilitiesCache.Load(ep.ID); ok {
		return cached.([]string), nil
	}
	var schema struct {
		Paths map[string]map[string]struct {
			ID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := ep.call(ctx, "GET", "/openapi.json", nil, &schema); err != nil {
		return nil, err
	}
	ops := map[string]bool{}
	for _, methods := range schema.Paths {
		for _, method := range methods {
			ops[method.ID] = true
		}
	}
	features := map[string][]string{
		"prompt": {"session.prompt", "session.inbox.list"}, "interrupt": {"session.interrupt"}, "permission": {"session.permission.list", "session.permission.reply"}, "form": {"session.form.list", "session.form.reply"}, "agent": {"agent.list", "session.switchAgent"}, "model": {"model.list", "session.switchModel"}, "command": {"command.list", "session.command"}, "skill": {"skill.list", "session.prompt"}, "files": {"session.prompt"}, "history": {"session.message.list", "session.message.get"}, "compact": {"session.compact"}, "resume": {"session.list", "session.get"}, "fork": {"session.fork"}, "revert": {"session.revert.stage", "session.revert.clear", "session.revert.commit"}, "settings": {"config.get", "mcp.list", "integration.list", "plugin.list", "location.reload"},
	}
	result := []string{}
	for name, required := range features {
		ok := true
		for _, op := range required {
			ok = ok && ops[op]
		}
		if ok {
			result = append(result, name)
		}
	}
	// Registrations are per launch; keep this tiny capability cache bounded.
	n := 0
	openCodeCapabilitiesCache.Range(func(key, value any) bool {
		n++
		if n > 128 {
			openCodeCapabilitiesCache.Delete(key)
		}
		return true
	})
	openCodeCapabilitiesCache.Store(ep.ID, result)
	return result, nil
}

type openCodeCatalog struct {
	Agents []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Mode   string `json:"mode"`
		Hidden bool   `json:"hidden"`
	} `json:"agents"`
	Models []struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
		Name       string `json:"name"`
		Enabled    bool   `json:"enabled"`
		Variants   []struct {
			ID string `json:"id"`
		} `json:"variants"`
		Capabilities struct {
			Input []string `json:"input"`
		} `json:"capabilities"`
	} `json:"models"`
	Commands []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"commands"`
	Skills []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Path        string `json:"path"`
	} `json:"skills"`
}

func openCodeCatalogAt(ctx context.Context, ep openCodeEndpoint, dir string) (openCodeCatalog, error) {
	var c openCodeCatalog
	caps, err := openCodeCapabilities(ctx, ep)
	if err != nil {
		return c, err
	}
	q := "?" + url.Values{"location[directory]": {dir}}.Encode()
	for _, read := range []struct {
		path string
		out  any
	}{
		{"agent", &struct {
			Data any `json:"data"`
		}{&c.Agents}}, {"model", &struct {
			Data any `json:"data"`
		}{&c.Models}}, {"command", &struct {
			Data any `json:"data"`
		}{&c.Commands}}, {"skill", &struct {
			Data any `json:"data"`
		}{&c.Skills}},
	} {
		if !slices.Contains(caps, read.path) {
			continue
		}
		if err := ep.call(ctx, "GET", "/api/"+read.path+q, nil, read.out); err != nil {
			return c, err
		}
	}
	return c, nil
}

func (b *Box) openCodeCatalog(w http.ResponseWriter, r *http.Request) error {
	sess, err := b.Sessions.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	ep, owner, err := b.openCodeRuntime(r.Context(), sess)
	if err != nil {
		return err
	}
	cat, err := openCodeCatalogAt(r.Context(), ep, owner.Directory)
	if err != nil {
		return err
	}
	writeJSON(w, cat)
	return nil
}

func openCodeFiles(ctx context.Context, ep openCodeEndpoint, owner openCodeOwner, files []openCodeFile) error {
	for i, file := range files {
		u, err := url.Parse(file.URI)
		if err != nil || u.Scheme != "file" || u.Host != "" || u.RawQuery != "" || u.Fragment != "" {
			return badRequest("attach a file uploaded to this box")
		}
		path, err := filepath.EvalSymlinks(u.Path)
		if err != nil {
			return badRequest("attached file is unavailable on this box")
		}
		rel, err := filepath.Rel(owner.Directory, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return badRequest("attached file must be inside this worktree")
		}
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() || st.Size() > MaxAttachment {
			return badRequest("invalid attachment or file larger than 20 MB")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		head := make([]byte, 512)
		n, _ := f.Read(head)
		f.Close()
		kind, _, err := sniffAttachment(path, head[:n])
		if err != nil {
			return err
		}
		if strings.HasPrefix(kind, "image/") || kind == "application/pdf" {
			var info struct {
				Data struct {
					Model struct {
						ID         string `json:"id"`
						ProviderID string `json:"providerID"`
					} `json:"model"`
				} `json:"data"`
			}
			if err := ep.call(ctx, "GET", "/api/session/"+owner.SessionID, nil, &info); err != nil {
				return err
			}
			if info.Data.Model.ID == "" {
				if err := ep.call(ctx, "GET", "/api/model/default?"+url.Values{"location[directory]": {owner.Directory}}.Encode(), nil, &struct {
					Data any `json:"data"`
				}{&info.Data.Model}); err != nil {
					return err
				}
			}
			catalog, err := openCodeCatalogAt(ctx, ep, owner.Directory)
			if err != nil {
				return err
			}
			media := "image"
			if kind == "application/pdf" {
				media = "pdf"
			}
			supported := false
			for _, m := range catalog.Models {
				if m.ID == info.Data.Model.ID && m.ProviderID == info.Data.Model.ProviderID && m.Enabled {
					supported = slices.Contains(m.Capabilities.Input, media)
				}
			}
			if !supported {
				return badRequest("the current OpenCode model does not declare %s input support; select a compatible model", media)
			}
		}
		files[i].URI = (&url.URL{Scheme: "file", Path: path}).String()
	}
	return nil
}

// New-worktree uploads initially live in the source checkout. Copy only this
// project's uploaded attachments into the new worktree before launching Mini.
func (b *Box) openCodeInitialFiles(ctx context.Context, location, destination string, files []openCodeFile) ([]openCodeFile, error) {
	if len(files) > 16 {
		return nil, badRequest("at most 16 native attachments are allowed")
	}
	project, _, _ := strings.Cut(location, "/")
	for i, file := range files {
		u, err := url.Parse(file.URI)
		if err != nil || u.Scheme != "file" || u.Host != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, badRequest("attach a file uploaded to this box")
		}
		path, err := filepath.EvalSymlinks(u.Path)
		if err != nil {
			return nil, badRequest("uploaded attachment is unavailable")
		}
		loc, wt, ok := b.worktreeAt(ctx, path)
		if !ok || loc.Name != project || !strings.HasPrefix(path, filepath.Join(wt.Path, ".berth", "attachments")+string(filepath.Separator)) {
			return nil, badRequest("initial attachments must be uploaded to this project")
		}
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() || st.Size() > MaxAttachment {
			return nil, badRequest("initial attachment must be a regular file smaller than 20 MB")
		}
		if sameDir(wt.Path, destination) {
			files[i].URI = (&url.URL{Scheme: "file", Path: path}).String()
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(f, MaxAttachment+1))
		f.Close()
		if readErr != nil {
			return nil, readErr
		}
		saved, err := SaveAttachment(ctx, destination, firstNonEmpty(file.Name, filepath.Base(path)), data)
		if err != nil {
			return nil, err
		}
		files[i] = openCodeFile{URI: (&url.URL{Scheme: "file", Path: saved.Path}).String(), Name: saved.Name}
	}
	return files, nil
}
