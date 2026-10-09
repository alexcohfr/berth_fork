package box

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/cosscom/shipyard/internal/events"
	"github.com/cosscom/shipyard/internal/statefile"
)

type openCodeFile struct {
	URI  string `json:"uri"`
	Name string `json:"name,omitempty"`
}
type openCodeSkill struct {
	ID string `json:"id"`
}

type openCodeReceipt struct {
	Hash    string      `json:"hash"`
	Result  *SendResult `json:"result,omitempty"`
	Command bool        `json:"command,omitempty"`
}

type openCodePermission struct {
	ID        string   `json:"id"`
	SessionID string   `json:"sessionID"`
	Action    string   `json:"action"`
	Resources []string `json:"resources"`
	Save      []string `json:"save,omitempty"`
	Message   string   `json:"message,omitempty"`
}
type openCodeForm struct {
	ID        string            `json:"id"`
	SessionID string            `json:"sessionID"`
	Title     string            `json:"title"`
	Fields    []json.RawMessage `json:"fields"`
}

func openCodePending(ctx context.Context, ep openCodeEndpoint, id string) ([]openCodePermission, []openCodeForm, error) {
	caps, err := openCodeCapabilities(ctx, ep)
	if err != nil {
		return nil, nil, err
	}
	var p struct {
		Data []openCodePermission `json:"data"`
	}
	var f struct {
		Data []openCodeForm `json:"data"`
	}
	if slices.Contains(caps, "permission") {
		if err := ep.call(ctx, "GET", "/api/session/"+id+"/permission", nil, &p); err != nil {
			return nil, nil, err
		}
	}
	if slices.Contains(caps, "form") {
		if err := ep.call(ctx, "GET", "/api/session/"+id+"/form", nil, &f); err != nil {
			return nil, nil, err
		}
	}
	return p.Data, f.Data, nil
}

var openCodeRequestID = regexp.MustCompile(`^(per|frm_)[A-Za-z0-9_-]{1,120}$`)

func (b *Box) openCodeAction(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Instance  string                     `json:"instance"`
		SessionID string                     `json:"session_id"`
		ID        string                     `json:"id"`
		Decision  string                     `json:"decision"`
		Target    string                     `json:"target,omitempty"`
		Answer    map[string]json.RawMessage `json:"answer"`
		Agent     string                     `json:"agent"`
		Model     struct {
			ID         string `json:"id"`
			ProviderID string `json:"providerID"`
			Variant    string `json:"variant,omitempty"`
		} `json:"model"`
	}
	if err := decodeLimit(r, &req, 64<<10); err != nil {
		return err
	}
	sess, err := b.Sessions.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	if controlAgent(sess) != "opencode" {
		return badRequest("this task does not run OpenCode")
	}
	if err := b.before(r, "session.send", map[string]any{"name": sess.Name, "path": sess.Dir, "action": r.PathValue("action")}); err != nil {
		return err
	}
	ep, owner, err := b.openCodeRuntime(r.Context(), sess)
	if err != nil {
		return err
	}
	if req.Instance != ep.ID || req.SessionID != owner.SessionID {
		return httpError{http.StatusConflict, "OpenCode conversation changed; refresh before replying"}
	}
	caps, err := openCodeCapabilities(r.Context(), ep)
	if err != nil {
		return err
	}
	feature := r.PathValue("action")
	if strings.HasPrefix(feature, "revert-") {
		feature = "revert"
	}
	if !slices.Contains(caps, feature) {
		return httpError{http.StatusNotImplemented, "This OpenCode runtime does not advertise this native capability; use its terminal"}
	}
	if strings.HasPrefix(r.PathValue("action"), "revert-") {
		defer b.lockSend(sess.Name)()
		return b.openCodeRevert(w, r, sess, ep, owner, req.ID)
	}
	if r.PathValue("action") == "compact" {
		if !openCodeMessageID.MatchString(req.ID) {
			return badRequest("compaction needs a stable message ID")
		}
		if err := ep.call(r.Context(), "POST", "/api/session/"+owner.SessionID+"/compact", map[string]any{"id": req.ID, "delivery": "queue"}, nil); err != nil {
			return err
		}
		writeJSON(w, map[string]bool{"accepted": true})
		return nil
	}
	if !openCodeRequestID.MatchString(req.ID) {
		if action := r.PathValue("action"); action == "agent" || action == "model" {
			catalog, err := openCodeCatalogAt(r.Context(), ep, owner.Directory)
			if err != nil {
				return err
			}
			var body any
			valid := false
			if action == "agent" {
				for _, a := range catalog.Agents {
					if a.ID == req.Agent && !a.Hidden && a.Mode != "subagent" {
						valid = true
					}
				}
				body = map[string]any{"agent": req.Agent}
			} else {
				for _, m := range catalog.Models {
					if m.ID == req.Model.ID && m.ProviderID == req.Model.ProviderID && m.Enabled {
						valid = req.Model.Variant == ""
						for _, v := range m.Variants {
							if v.ID == req.Model.Variant {
								valid = true
							}
						}
					}
				}
				body = map[string]any{"model": req.Model}
			}
			if !valid {
				return badRequest("selection is no longer in this runtime's catalog")
			}
			if err := ep.call(r.Context(), "POST", "/api/session/"+owner.SessionID+"/"+action, body, nil); err != nil {
				return err
			}
			writeJSON(w, map[string]bool{"accepted": true})
			return nil
		}
		return badRequest("invalid OpenCode request ID")
	}
	target := owner.SessionID
	if req.Target != "" {
		if err := openCodeRelated(r.Context(), ep, owner, req.Target); err != nil {
			return err
		}
		target = req.Target
	}
	permissions, forms, err := openCodePending(r.Context(), ep, target)
	if err != nil {
		return err
	}
	path := "/api/session/" + target
	var body any
	found := false
	switch r.PathValue("action") {
	case "permission":
		if req.Decision != "once" && req.Decision != "always" && req.Decision != "reject" {
			return badRequest("invalid permission decision")
		}
		for _, p := range permissions {
			if p.ID == req.ID && p.SessionID == target {
				found = true
			}
		}
		path += "/permission/" + req.ID + "/reply"
		body = map[string]any{"decision": req.Decision}
	case "form":
		for _, f := range forms {
			if f.ID == req.ID && f.SessionID == target {
				found = true
			}
		}
		if req.Answer == nil {
			return badRequest("form answer required")
		}
		path += "/form/" + req.ID + "/reply"
		body = map[string]any{"answer": req.Answer}
	default:
		return badRequest("unsupported OpenCode action")
	}
	if !found {
		return httpError{http.StatusConflict, "This request was answered or cancelled; refresh the conversation"}
	}
	if err := ep.call(r.Context(), "POST", path, body, nil); err != nil {
		return err
	}
	writeJSON(w, map[string]bool{"accepted": true})
	return nil
}

func (b *Box) openCodeSend(ctx context.Context, sess Session, req SendRequest, origin, from string) (SendResult, error) {
	if req.Enter != nil && !*req.Enter {
		return SendResult{}, badRequest("OpenCode native sends submit a prompt; use the terminal for keys")
	}
	if len(req.Text) > 64<<10 || len(req.IdemKey) > 256 || len(req.Files) > 16 || len(req.Skills) > 16 {
		return SendResult{}, badRequest("OpenCode prompt exceeds its budget")
	}
	ep, owner, err := b.openCodeRuntime(ctx, sess)
	if err != nil {
		return SendResult{}, err
	}
	if err := openCodeFiles(ctx, ep, owner, req.Files); err != nil {
		return SendResult{}, err
	}
	key := req.IdemKey
	if key == "" {
		key = openCodeNonce()
	}
	sum := sha256.Sum256([]byte(owner.SessionID + "\x00" + key))
	id := "msg_" + hex.EncodeToString(sum[:])
	delivery := "steer"
	if req.When == "idle" {
		delivery = "queue"
	}
	body := map[string]any{"id": id, "text": req.Text, "delivery": delivery, "files": req.Files, "skills": req.Skills}
	command := ""
	if strings.HasPrefix(strings.TrimSpace(req.Text), "/") {
		name, text, _ := strings.Cut(strings.TrimSpace(req.Text), " ")
		name = strings.TrimPrefix(name, "/")
		catalog, err := openCodeCatalogAt(ctx, ep, owner.Directory)
		if err != nil {
			return SendResult{}, err
		}
		for _, c := range catalog.Commands {
			if c.Name == name {
				command = name
			}
		}
		if command == "" {
			return SendResult{}, badRequest("OpenCode has no registered /%s command; use the native controls or terminal", name)
		}
		delete(body, "id")
		body["name"], body["text"] = name, text
	}
	// Omit absent lists: the native schema accepts arrays, not JSON null.
	if req.Files == nil {
		delete(body, "files")
	}
	if req.Skills == nil {
		delete(body, "skills")
	}
	raw, _ := json.Marshal(body)
	hash := sha256.Sum256(raw)
	receipt := openCodeReceipt{Hash: hex.EncodeToString(hash[:]), Command: command != ""}
	path := filepath.Join(filepath.Dir(b.Sessions.EnvVar(ctx, sess, "BERTH_OPENCODE_RUNTIME")), "requests", owner.SessionID, id+".json")
	if data, readErr := os.ReadFile(path); readErr == nil {
		var old openCodeReceipt
		if json.Unmarshal(data, &old) != nil || old.Hash != receipt.Hash {
			return SendResult{}, httpError{http.StatusConflict, "This request ID already belongs to a different prompt"}
		}
		if old.Result != nil {
			result := *old.Result
			result.Duplicate = true
			return result, nil
		}
		if old.Command {
			return SendResult{}, httpError{http.StatusConflict, "Command delivery is uncertain; inspect the conversation before issuing it again"}
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return SendResult{}, readErr
	}
	data, _ := json.Marshal(receipt)
	if req.When != "idle" && !req.Force {
		permissions, forms, err := openCodePending(ctx, ep, owner.SessionID)
		if err != nil {
			return SendResult{}, err
		}
		if len(permissions)+len(forms) > 0 {
			return SendResult{}, httpError{http.StatusConflict, ErrAgentWaiting{sess.Name}.Error()}
		}
		children, err := b.openCodeChildren(ctx, sess)
		if err != nil {
			return SendResult{}, err
		}
		for _, child := range children {
			p, f, err := openCodePending(ctx, ep, child.ID)
			if err != nil {
				return SendResult{}, err
			}
			if len(p)+len(f) > 0 {
				return SendResult{}, httpError{http.StatusConflict, ErrAgentWaiting{sess.Name}.Error()}
			}
		}
	}
	if err := statefile.Write(path, data); err != nil {
		return SendResult{}, err
	}
	operation := "prompt"
	if command != "" {
		operation = "command"
	}
	if err := ep.call(ctx, "POST", "/api/session/"+owner.SessionID+"/"+operation, body, nil); err != nil {
		return SendResult{}, err
	}
	result := SendResult{Sent: true, At: time.Now().UTC(), NativeID: id}
	if command != "" {
		result.NativeID = ""
	}
	if b.Events != nil {
		var prior Turn
		var known bool
		if b.Turns != nil {
			prior, known = b.Turns.ByIdem(sess.Name, key)
		}
		if known {
			result.Turn, result.Seq = prior.ID, prior.SentSeq
		} else {
			e := b.Events.Publish(events.Event{Type: "session.sent", Box: b.Name, Origin: origin, Data: map[string]any{"name": sess.Name, "agent": "opencode", "from": from, "idem_key": key, "native_id": result.NativeID}})
			result.Seq, result.At = e.Seq, e.Time.UTC()
			if b.Turns != nil {
				if tr, ok := b.Turns.ForSent(sess.Name, e.Seq); ok {
					result.Turn = tr.ID
				}
			}
		}
	}
	b.openCodeWatch(sess, ep, owner)
	receipt.Result = &result
	data, _ = json.Marshal(receipt)
	if err := statefile.Write(path, data); err != nil {
		return SendResult{}, err
	}
	return result, nil
}

func (b *Box) openCodeState(w http.ResponseWriter, r *http.Request) error {
	sess, err := b.Sessions.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	if controlAgent(sess) != "opencode" {
		return badRequest("this task does not run OpenCode")
	}
	ep, owner, err := b.openCodeRuntime(r.Context(), sess)
	if err != nil {
		return err
	}
	var info struct {
		Data struct {
			ID      string          `json:"id"`
			Agent   string          `json:"agent"`
			Model   json.RawMessage `json:"model"`
			Cost    float64         `json:"cost"`
			Tokens  json.RawMessage `json:"tokens"`
			Outcome string          `json:"outcome"`
			Revert  *struct {
				MessageID string `json:"messageID"`
			} `json:"revert,omitempty"`
		} `json:"data"`
	}
	if err := ep.call(r.Context(), "GET", "/api/session/"+owner.SessionID, nil, &info); err != nil {
		return err
	}
	var active struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := ep.call(r.Context(), "GET", "/api/session/active", nil, &active); err != nil {
		return err
	}
	b.openCodeWatch(sess, ep, owner)
	permissions, forms, err := openCodePending(r.Context(), ep, owner.SessionID)
	if err != nil {
		return err
	}
	if children, err := b.openCodeChildren(r.Context(), sess); err == nil {
		for _, child := range children {
			p, f, err := openCodePending(r.Context(), ep, child.ID)
			if err != nil {
				return err
			}
			permissions = append(permissions, p...)
			forms = append(forms, f...)
		}
	}
	var inbox struct {
		Data []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"data"`
	}
	if err := ep.call(r.Context(), "GET", "/api/session/"+owner.SessionID+"/inbox", nil, &inbox); err != nil {
		return err
	}
	caps, err := openCodeCapabilities(r.Context(), ep)
	if err != nil {
		return err
	}
	type compaction struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	var latest *compaction
	if slices.Contains(caps, "compact") {
		var page struct {
			Data []compaction `json:"data"`
		}
		if err := ep.call(r.Context(), "GET", "/api/session/"+owner.SessionID+"/message?type=compaction&order=desc&limit=1", nil, &page); err != nil {
			return err
		}
		if len(page.Data) > 0 {
			latest = &page.Data[0]
		}
		for _, item := range inbox.Data {
			if item.Type == "compaction" {
				latest = &compaction{ID: item.ID, Status: "pending"}
			}
		}
	}
	writeJSON(w, map[string]any{"version": ep.Version, "instance": ep.ID, "session": info.Data, "running": active.Data[owner.SessionID] != nil, "directory": owner.Directory, "permissions": permissions, "forms": forms, "inbox": inbox.Data, "compaction": latest, "capabilities": caps})
	return nil
}
