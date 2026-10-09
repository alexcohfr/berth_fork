package box

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/cosscom/shipyard/internal/statefile"
	"github.com/cosscom/shipyard/internal/transcript"
)

func (b *Box) openCodeFork(w http.ResponseWriter, r *http.Request, sess Session, req ForkRequest) error {
	if !openCodeMessageID.MatchString(req.At) || !openCodeSettingID.MatchString(req.IdemKey) || len(req.IdemKey) > 128 {
		return badRequest("fork needs a native message ID and a stable request ID")
	}
	if err := b.before(r, "session.start", map[string]any{"name": sess.Name, "path": sess.Dir, "action": "opencode.fork"}); err != nil {
		return err
	}
	ep, owner, err := b.openCodeRuntime(r.Context(), sess)
	if err != nil {
		return err
	}
	if err := openCodeFiles(r.Context(), ep, owner, req.Files); err != nil {
		return err
	}
	var message struct {
		Data transcript.OpenCodeMessage `json:"data"`
	}
	if err := ep.call(r.Context(), "GET", "/api/session/"+owner.SessionID+"/message/"+req.At, nil, &message); err != nil {
		return err
	}
	path := filepath.Join(b.Sessions.EnvVar(r.Context(), sess, "BERTH_OPENCODE_RUNTIME"), "fork-"+etagOf([]byte(req.IdemKey))+".json")
	defer lockFile(path)()
	body, _ := json.Marshal(req)
	var receipt struct {
		Hash    string   `json:"hash"`
		Session *Session `json:"session,omitempty"`
	}
	hash := etagOf(body)
	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &receipt) != nil || receipt.Hash != hash {
			return httpError{http.StatusConflict, "Fork request ID already used"}
		}
		if receipt.Session == nil {
			return httpError{http.StatusConflict, "Fork result is uncertain; search the OpenCode conversations before trying again"}
		}
		writeJSON(w, receipt.Session)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	receipt.Hash = hash
	data, _ := json.Marshal(receipt)
	if err := statefile.Write(path, data); err != nil {
		return err
	}
	var fork struct {
		Data openCodeConversation `json:"data"`
	}
	if err := ep.call(r.Context(), "POST", "/api/session/"+owner.SessionID+"/fork", map[string]string{"before": req.At}, &fork); err != nil {
		return err
	}
	ns, err := b.startOpenCodeConversation(r, sess, fork.Data.ID, firstNonEmpty(req.Title, "Fork of "+fork.Data.Title))
	if err != nil {
		return err
	}
	if req.Text != "" || len(req.Files) > 0 {
		deadline := time.NewTimer(15 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			if _, _, err := b.openCodeRuntime(r.Context(), ns); err == nil {
				break
			}
			select {
			case <-r.Context().Done():
				return r.Context().Err()
			case <-deadline.C:
				return httpError{http.StatusServiceUnavailable, "Fork saved; runtime startup not confirmed. Open its terminal before sending"}
			case <-tick.C:
			}
		}
		if _, err := b.sendPrompt(r.Context(), ns.Name, SendRequest{Text: req.Text, Files: req.Files, When: "idle", IdemKey: "fork-first-" + req.IdemKey}, origin(r), gateOrigin(r)); err != nil {
			return err
		}
	}
	receipt.Session = &ns
	data, _ = json.Marshal(receipt)
	if err := statefile.Write(path, data); err != nil {
		return err
	}
	writeJSON(w, ns)
	return nil
}

func (b *Box) openCodeRevert(w http.ResponseWriter, r *http.Request, sess Session, ep openCodeEndpoint, owner openCodeOwner, id string) error {
	if !openCodeMessageID.MatchString(id) {
		return badRequest("choose a native message boundary")
	}
	var active struct {
		Data map[string]any `json:"data"`
	}
	if err := ep.call(r.Context(), "GET", "/api/session/active", nil, &active); err != nil {
		return err
	}
	if active.Data[owner.SessionID] != nil {
		return httpError{http.StatusConflict, "Wait for OpenCode to be idle before rewinding"}
	}
	var info struct {
		Data struct {
			Revert *struct {
				MessageID string `json:"messageID"`
			} `json:"revert"`
		} `json:"data"`
	}
	if err := ep.call(r.Context(), "GET", "/api/session/"+owner.SessionID, nil, &info); err != nil {
		return err
	}
	base := "/api/session/" + owner.SessionID
	switch r.PathValue("action") {
	case "revert-preview":
		var message struct {
			Data transcript.OpenCodeMessage `json:"data"`
		}
		if err := ep.call(r.Context(), "GET", base+"/message/"+id, nil, &message); err != nil {
			return err
		}
		writeJSON(w, map[string]any{"message_id": id, "text": message.Data.Text, "files": false, "reason": "File restoration is disabled: preservation of external and shell edits has not been verified on this OpenCode version"})
		return nil
	case "revert-stage":
		if info.Data.Revert != nil && info.Data.Revert.MessageID != id {
			return httpError{http.StatusConflict, "Another rewind is staged; clear it before changing the boundary"}
		}
		if err := ep.call(r.Context(), "POST", base+"/revert/stage", map[string]any{"messageID": id, "files": false}, nil); err != nil {
			return err
		}
	case "revert-clear", "revert-commit":
		if info.Data.Revert == nil || info.Data.Revert.MessageID != id {
			return httpError{http.StatusConflict, "The staged boundary changed; refresh before confirming"}
		}
		method, path := "DELETE", base+"/revert"
		if r.PathValue("action") == "revert-commit" {
			method, path = "POST", base+"/revert/commit"
		}
		if err := ep.call(r.Context(), method, path, nil, nil); err != nil {
			return err
		}
	default:
		return badRequest("unsupported revert action")
	}
	root := b.Sessions.EnvVar(r.Context(), sess, "BERTH_OPENCODE_RUNTIME")
	if err := statefile.Write(filepath.Join(root, "history-generation"), []byte(openCodeNonce())); err != nil {
		return err
	}
	writeJSON(w, map[string]bool{"accepted": true})
	return nil
}
