package box

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"

	"github.com/cosscom/shipyard/internal/statefile"
	"github.com/tailscale/hujson"
)

// Settings are projections, never raw config, provider headers or MCP errors.
type openCodeSettings struct {
	Sources []struct {
		Type string `json:"type"`
		Path string `json:"path,omitempty"`
	} `json:"sources"`
	MCP []struct {
		Name   string `json:"name"`
		Status struct {
			Status string `json:"status"`
		} `json:"status"`
		IntegrationID string `json:"integrationID,omitempty"`
	} `json:"mcp"`
	Plugins []struct {
		ID    string `json:"id"`
		State struct {
			Status string `json:"status"`
		} `json:"state"`
	} `json:"plugins"`
	Integrations []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Methods []struct {
			ID    string `json:"id,omitempty"`
			Type  string `json:"type"`
			Label string `json:"label,omitempty"`
			Form  []struct {
				Key      string `json:"key"`
				Type     string `json:"type"`
				Title    string `json:"title,omitempty"`
				Required bool   `json:"required,omitempty"`
			} `json:"form,omitempty"`
		} `json:"methods"`
		Connections []struct {
			Type   string `json:"type"`
			Method string `json:"method,omitempty"`
		} `json:"connections"`
	} `json:"integrations"`
	Attempts []openCodeAuthAttempt `json:"attempts"`
}
type openCodeAuthAttempt struct {
	Integration string `json:"integration"`
	ID          string `json:"attemptID"`
	URL         string `json:"url"`
	Mode        string `json:"mode"`
	Status      string `json:"status,omitempty"`
}

func (b *Box) openCodeSettings(w http.ResponseWriter, r *http.Request) error {
	sess, err := b.Sessions.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	ep, owner, err := b.openCodeRuntime(r.Context(), sess)
	if err != nil {
		return err
	}
	q := "?" + url.Values{"location[directory]": {owner.Directory}}.Encode()
	var out openCodeSettings
	for _, item := range []struct {
		path string
		out  any
	}{{"config", &out.Sources}, {"mcp", &struct {
		Data any `json:"data"`
	}{&out.MCP}}, {"plugin", &struct {
		Data any `json:"data"`
	}{&out.Plugins}}, {"integration", &struct {
		Data any `json:"data"`
	}{&out.Integrations}}} {
		if err := ep.call(r.Context(), "GET", "/api/"+item.path+q, nil, item.out); err != nil {
			return err
		}
	}
	path := filepath.Join(b.Sessions.EnvVar(r.Context(), sess, "BERTH_OPENCODE_RUNTIME"), "auth.json")
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &out.Attempts)
	}
	for i, a := range out.Attempts {
		var status struct {
			Data struct {
				Status string `json:"status"`
			} `json:"data"`
		}
		if err := ep.call(r.Context(), "GET", "/api/integration/"+url.PathEscape(a.Integration)+"/connect/oauth/"+url.PathEscape(a.ID)+q, nil, &status); err == nil {
			out.Attempts[i].Status = status.Data.Status
		} else {
			out.Attempts[i].Status = "unavailable"
		}
	}
	writeJSON(w, out)
	return nil
}

var openCodeSettingID = regexp.MustCompile(`^[A-Za-z0-9_./:@-]{1,256}$`)

func (b *Box) openCodeSettingAction(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Instance    string                     `json:"instance"`
		Action      string                     `json:"action"`
		Integration string                     `json:"integration"`
		Method      string                     `json:"method"`
		Attempt     string                     `json:"attempt"`
		Server      string                     `json:"server"`
		Key         string                     `json:"key"`
		Code        string                     `json:"code"`
		Answer      map[string]json.RawMessage `json:"answer"`
	}
	if err := decodeLimit(r, &req, 64<<10); err != nil {
		return err
	}
	sess, err := b.Sessions.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	if err := b.before(r, "session.send", map[string]any{"name": sess.Name, "path": sess.Dir, "action": "opencode.settings"}); err != nil {
		return err
	}
	ep, owner, err := b.openCodeRuntime(r.Context(), sess)
	if err != nil {
		return err
	}
	if ep.ID != req.Instance {
		return httpError{http.StatusConflict, "OpenCode runtime changed"}
	}
	q := "?" + url.Values{"location[directory]": {owner.Directory}}.Encode()
	if req.Action == "reload" {
		if err := ep.call(r.Context(), "POST", "/api/location/reload", nil, nil); err != nil {
			return err
		}
		writeJSON(w, map[string]bool{"accepted": true})
		return nil
	}
	if req.Action == "mcp-connect" || req.Action == "mcp-disconnect" {
		if !openCodeSettingID.MatchString(req.Server) {
			return badRequest("invalid MCP name")
		}
		action := "connect"
		if req.Action == "mcp-disconnect" {
			action = "disconnect"
		}
		if err := ep.call(r.Context(), "POST", "/api/experimental/mcp/"+url.PathEscape(req.Server)+"/"+action+q, nil, nil); err != nil {
			return err
		}
		writeJSON(w, map[string]bool{"accepted": true})
		return nil
	}
	if !openCodeSettingID.MatchString(req.Integration) {
		return badRequest("invalid integration")
	}
	base := "/api/integration/" + url.PathEscape(req.Integration) + "/connect/"
	path := filepath.Join(b.Sessions.EnvVar(r.Context(), sess, "BERTH_OPENCODE_RUNTIME"), "auth.json")
	defer lockFile(path)()
	attempts := []openCodeAuthAttempt{}
	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &attempts) != nil {
			return badRequest("invalid stored authentication attempts")
		}
	}
	switch req.Action {
	case "key":
		if req.Key == "" {
			return badRequest("key is required")
		}
		body := map[string]any{"key": req.Key}
		if req.Answer != nil {
			body["answer"] = req.Answer
		}
		if err := ep.call(r.Context(), "POST", base+"key"+q, body, nil); err != nil {
			return err
		}
	case "oauth":
		if !openCodeSettingID.MatchString(req.Method) {
			return badRequest("invalid authentication method")
		}
		var reply struct {
			Data openCodeAuthAttempt `json:"data"`
		}
		body := map[string]any{"methodID": req.Method}
		if req.Answer != nil {
			body["answer"] = req.Answer
		}
		if err := ep.call(r.Context(), "POST", base+"oauth"+q, body, &reply); err != nil {
			return err
		}
		u, err := url.Parse(reply.Data.URL)
		if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.User != nil {
			return badRequest("unsupported authentication URL; use OpenCode's terminal")
		}
		reply.Data.Integration = req.Integration
		attempts = append(attempts, reply.Data)
		if len(attempts) > 20 {
			attempts = attempts[len(attempts)-20:]
		}
	case "oauth-complete", "oauth-cancel":
		found := false
		for _, a := range attempts {
			if a.ID == req.Attempt && a.Integration == req.Integration {
				found = true
			}
		}
		if !found {
			return badRequest("authentication attempt is not owned by this task")
		}
		endpoint := base + "oauth/" + url.PathEscape(req.Attempt)
		if req.Action == "oauth-complete" {
			body := map[string]any{}
			if req.Code != "" {
				body["code"] = req.Code
			}
			if err := ep.call(r.Context(), "POST", endpoint+"/complete"+q, body, nil); err != nil {
				return err
			}
		} else {
			if err := ep.call(r.Context(), "DELETE", endpoint+q, nil, nil); err != nil {
				return err
			}
			keep := attempts[:0]
			for _, a := range attempts {
				if a.ID != req.Attempt {
					keep = append(keep, a)
				}
			}
			attempts = keep
		}
	default:
		return badRequest("unsupported OpenCode settings action")
	}
	data, _ := json.Marshal(attempts)
	if err := statefile.Write(path, data); err != nil {
		return err
	}
	writeJSON(w, map[string]bool{"accepted": true})
	return nil
}

func validateOpenCodeJSONC(path string, data []byte) error {
	if filepath.Base(path) != "opencode.json" && filepath.Base(path) != "opencode.jsonc" {
		return nil
	}
	v, err := hujson.Parse(bytes.Clone(data))
	if err != nil {
		return badRequest("OpenCode configuration is not valid JSONC")
	}
	v.Standardize()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(v.Pack(), &object); err != nil || object == nil {
		return badRequest("OpenCode configuration must be a JSON object")
	}
	return nil
}
