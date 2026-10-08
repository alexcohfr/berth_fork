package box

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cosscom/shipyard/internal/transcript"
)

var openCodeID = regexp.MustCompile(`^ses[A-Za-z0-9_-]{1,120}$`)
var openCodeMessageID = regexp.MustCompile(`^msg_[A-Za-z0-9_-]{1,120}$`)

type openCodeModel struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Variants []string `json:"variants"`
}

func (b *Box) openCodeModels(w http.ResponseWriter, r *http.Request) error {
	dir, err := b.Locations.Dir(r.Context(), r.URL.Query().Get("at"))
	if err != nil {
		return err
	}
	var page struct {
		Data []struct {
			ID, ProviderID, Name string
			Enabled              bool
			Variants             []struct{ ID string }
		} `json:"data"`
	}
	// Catalog plugins live in the shared service. A one-shot standalone API
	// process exits before they finish populating model.list in OpenCode 2.
	if err := openCodeAPI(r.Context(), dir, b.envForDir(r.Context(), dir), []string{"model.list", "--param", "location[directory]=" + dir}, &page); err != nil {
		return err
	}
	models := []openCodeModel{}
	for _, m := range page.Data {
		id := m.ProviderID + "/" + m.ID
		if !m.Enabled || !modelWord.MatchString(id) || strings.HasPrefix(id, "-") {
			continue
		}
		item := openCodeModel{ID: id, Name: m.Name, Variants: []string{}}
		for _, v := range m.Variants {
			if modelWord.MatchString(v.ID) {
				item.Variants = append(item.Variants, v.ID)
			}
		}
		models = append(models, item)
	}
	sort.SliceStable(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	writeJSON(w, models)
	return nil
}

func (b *Box) openCodeSession(sess Session) string {
	if b.Turns != nil {
		if st, ok := b.Turns.State(sess.Name); ok && openCodeID.MatchString(st.AgentSessionID) {
			return st.AgentSessionID
		}
	}
	return ""
}

func (b *Box) openCodeTranscript(w http.ResponseWriter, r *http.Request, sess Session) error {
	id := b.openCodeSession(sess)
	if id == "" {
		writeJSON(w, transcript.Result{Source: "none", Items: []transcript.Item{}, Crew: []transcript.CrewMember{},
			Reason: "Waiting for OpenCode's first prompt. If it has already started, install the OpenCode integration and restart the terminal."})
		return nil
	}
	// ponytail: one bounded API page per refresh; use a persistent API client
	// if many simultaneously visible OpenCode chats make CLI startup costly.
	var page struct {
		Data   []transcript.OpenCodeMessage `json:"data"`
		Cursor struct {
			Next *string `json:"next"`
		} `json:"cursor"`
	}
	if err := b.openCodeRead(r.Context(), sess, []string{"session.message.list", "--param", "sessionID=" + id, "--param", "limit=100", "--param", "order=desc"}, &page); err != nil {
		return err
	}
	writeJSON(w, transcript.OpenCode(id, sess.Dir, page.Data, page.Cursor.Next != nil && *page.Cursor.Next != ""))
	return nil
}

func (b *Box) openCodeToolDetail(w http.ResponseWriter, r *http.Request, sess Session) error {
	id := r.PathValue("id")
	message, tool, ok := strings.Cut(id, ":")
	if !ok || !openCodeMessageID.MatchString(message) || tool == "" || len(tool) > 512 {
		return badRequest("invalid OpenCode tool id")
	}
	session := b.openCodeSession(sess)
	if session == "" {
		return httpError{http.StatusNotFound, "OpenCode's session is not linked yet"}
	}
	var result struct {
		Data transcript.OpenCodeMessage `json:"data"`
	}
	if err := b.openCodeRead(r.Context(), sess, []string{"session.message.get", "--param", "sessionID=" + session, "--param", "messageID=" + message}, &result); err != nil {
		return err
	}
	d, err := transcript.OpenCodeDetail(result.Data, sess.Dir, id)
	if errors.Is(err, transcript.ErrNoTool) {
		return httpError{http.StatusNotFound, "that step is no longer in the OpenCode conversation"}
	}
	if err != nil {
		return err
	}
	writeJSON(w, d)
	return nil
}

// Read the public API with OpenCode's own CLI. --standalone reads the same
// durable sessions without starting, reconfiguring or depending on the user's
// shared service. It also works after the original terminal has exited.
func (b *Box) openCodeRead(ctx context.Context, sess Session, args []string, out any) error {
	var env []string
	// These select the same store as the terminal, including project overrides.
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "OPENCODE_DB", "OPENCODE_CONFIG_DIR"} {
		if value := b.Sessions.EnvVar(ctx, sess, key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return openCodeAPI(ctx, sess.Dir, env, append([]string{"--standalone"}, args...), out)
}

func openCodeAPI(ctx context.Context, dir string, env, args []string, out any) error {
	found, ok := agentFound("opencode")
	if !ok {
		return errors.New("OpenCode is not installed on this box")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, found.Path, append([]string{"api"}, args...)...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if found.PATH != "" {
		cmd.Env = append(cmd.Env, "PATH="+found.PATH)
	}
	cmd.Env = append(cmd.Env, env...)
	cmd.Env = append(cmd.Env, "BERTH_SESSION=", "BERTH_AGENT=")
	var buf bytes.Buffer
	cmd.Stdout = &openCodeOutput{Buffer: &buf, remaining: 16 << 20}
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("reading OpenCode: %w", ctx.Err())
		}
		return fmt.Errorf("could not read OpenCode's V2 API: %w", err)
	}
	if err := json.Unmarshal(buf.Bytes(), out); err != nil {
		return errors.New("OpenCode returned an invalid V2 API response")
	}
	return nil
}

type openCodeOutput struct {
	*bytes.Buffer
	remaining int
}

func (w *openCodeOutput) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, io.ErrShortBuffer
	}
	w.remaining -= len(p)
	return w.Buffer.Write(p)
}
