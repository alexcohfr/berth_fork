package box

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"

	"github.com/cosscom/shipyard/internal/transcript"
)

type openCodeConversation struct {
	ID       string `json:"id"`
	ParentID string `json:"parentID,omitempty"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
	Time struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

func openCodeRelated(ctx context.Context, ep openCodeEndpoint, owner openCodeOwner, id string) error {
	for n := 0; n < 16; n++ {
		if id == owner.SessionID {
			return nil
		}
		if !openCodeID.MatchString(id) {
			break
		}
		var v struct {
			Data openCodeConversation `json:"data"`
		}
		if err := ep.call(ctx, "GET", "/api/session/"+id, nil, &v); err != nil {
			return err
		}
		if v.Data.Location.Directory != owner.Directory {
			break
		}
		id = v.Data.ParentID
	}
	return badRequest("this conversation is not a child of the task")
}

func (b *Box) openCodeChildren(ctx context.Context, sess Session) ([]openCodeConversation, error) {
	ep, owner, err := b.openCodeRuntime(ctx, sess)
	if err != nil {
		return nil, err
	}
	var page struct {
		Data []openCodeConversation `json:"data"`
	}
	if err := ep.call(ctx, "GET", "/api/session?"+url.Values{"parentID": {owner.SessionID}, "limit": {"100"}, "directory": {owner.Directory}}.Encode(), nil, &page); err != nil {
		return nil, err
	}
	var active struct {
		Data map[string]any `json:"data"`
	}
	if err := ep.call(ctx, "GET", "/api/session/active", nil, &active); err != nil {
		return nil, err
	}
	// A linked fork has its own runtime, not a worker owned by its parent.
	owned := map[string]bool{}
	all, err := b.Sessions.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range all {
		if !s.Exited && s.Name != sess.Name && controlAgent(s) == "opencode" {
			owned[b.openCodeSession(s)] = true
		}
	}
	children := page.Data[:0]
	for _, child := range page.Data {
		if !owned[child.ID] {
			children = append(children, child)
		}
	}
	page.Data = children
	for i := range page.Data {
		page.Data[i].State = "finished"
		if active.Data[page.Data[i].ID] != nil {
			page.Data[i].State = "running"
		}
	}
	return page.Data, nil
}

func (b *Box) openCodeHelpers(w http.ResponseWriter, r *http.Request, sess Session) error {
	children, err := b.openCodeChildren(r.Context(), sess)
	if err != nil {
		return err
	}
	out := []transcript.Helper{}
	for _, c := range children {
		out = append(out, transcript.Helper{ID: c.ID, Name: firstNonEmpty(c.Title, c.ID), State: c.State, Started: c.Time.Created, Updated: c.Time.Updated})
	}
	writeJSON(w, map[string]any{"helpers": out})
	return nil
}

func (b *Box) openCodeConversations(w http.ResponseWriter, r *http.Request) error {
	sess, err := b.Sessions.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	ep, owner, err := b.openCodeRuntime(r.Context(), sess)
	if err != nil {
		return err
	}
	q := url.Values{"directory": {owner.Directory}, "limit": {"50"}}
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		if len(cursor) > 8192 {
			return badRequest("invalid cursor")
		}
		q.Set("cursor", cursor)
	} else {
		q.Set("order", "desc")
	}
	if term := r.URL.Query().Get("search"); term != "" {
		if len(term) > 256 {
			return badRequest("search is too long")
		}
		q.Set("search", term)
	}
	var page struct {
		Data   []openCodeConversation `json:"data"`
		Cursor struct {
			Next *string `json:"next"`
		} `json:"cursor"`
	}
	if err := ep.call(r.Context(), "GET", "/api/session?"+q.Encode(), nil, &page); err != nil {
		return err
	}
	writeJSON(w, page)
	return nil
}

func (b *Box) openCodeResume(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		ID       string `json:"id"`
		Instance string `json:"instance"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if !openCodeID.MatchString(req.ID) {
		return badRequest("invalid OpenCode conversation")
	}
	sess, err := b.Sessions.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	ep, owner, err := b.openCodeRuntime(r.Context(), sess)
	if err != nil {
		return err
	}
	if ep.ID != req.Instance {
		return httpError{http.StatusConflict, "OpenCode runtime changed"}
	}
	var info struct {
		Data openCodeConversation `json:"data"`
	}
	if err := ep.call(r.Context(), "GET", "/api/session/"+req.ID, nil, &info); err != nil {
		return err
	}
	dir, err := filepath.EvalSymlinks(info.Data.Location.Directory)
	if err != nil || dir != owner.Directory {
		return badRequest("conversation belongs to another worktree")
	}
	if req.ID == owner.SessionID {
		return httpError{http.StatusConflict, "This conversation is already open in this task"}
	}
	var active struct {
		Data map[string]any `json:"data"`
	}
	if err := ep.call(r.Context(), "GET", "/api/session/active", nil, &active); err != nil {
		return err
	}
	if active.Data[req.ID] != nil {
		return httpError{http.StatusConflict, "This conversation is running; interrupt its owner first"}
	}
	ns, err := b.startOpenCodeConversation(r, sess, req.ID, info.Data.Title)
	if err != nil {
		return err
	}
	writeJSON(w, ns)
	return nil
}

func (b *Box) startOpenCodeConversation(r *http.Request, source Session, id, title string) (Session, error) {
	defer b.lockSend("opencode-owner:" + id)()
	all, err := b.Sessions.List(r.Context())
	if err != nil {
		return Session{}, err
	}
	for _, s := range all {
		if !s.Exited && controlAgent(s) == "opencode" && b.openCodeSession(s) == id {
			return Session{}, httpError{http.StatusConflict, "This conversation already has a Shipyard owner"}
		}
	}
	name := "opencode-" + openCodeNonce()[:10]
	if err := b.before(r, "session.start", map[string]any{"name": name, "location": source.Location, "path": source.Dir}); err != nil {
		return Session{}, err
	}
	env, wrap := b.sessionEnv(r.Context(), source.Dir)
	// Preserve the source's store selection, not process credentials.
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "OPENCODE_CONFIG_DIR", "OPENCODE_DB", "BERTH_OPENCODE_BIN"} {
		if v := b.Sessions.EnvVar(r.Context(), source, k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, "BERTH_OPENCODE_SESSION="+id, "BERTH_OPENCODE_RESUME=1")
	ns, err := b.Sessions.create(r.Context(), name, source.Location, source.Dir, "opencode mini --standalone", "opencode", env, wrap)
	if err != nil {
		return Session{}, err
	}
	if b.Events != nil {
		b.publish(r, "session.started", map[string]any{"name": ns.Name, "location": ns.Location, "path": ns.Dir, "agent": "opencode"})
	}
	if title != "" {
		ns = b.titleNew(r.Context(), ns, title, "")
	}
	b.announceOpen(r, ns, "tab")
	return ns, nil
}

var errOpenCodeOwned = errors.New("this OpenCode conversation already has a native owner")
