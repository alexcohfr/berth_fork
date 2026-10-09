package box

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cosscom/shipyard/internal/events"
	"github.com/cosscom/shipyard/internal/hooks"
)

func syntheticOpenCode(t *testing.T, handler http.HandlerFunc) (*Box, Session, openCodeEndpoint) {
	t.Helper()
	s := testSessions(t)
	base := filepath.Join(filepath.Dir(s.Commands), "opencode")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "runtime-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "opencode" || password != "synthetic-private" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/api/info" {
			writeJSON(w, map[string]int{"pid": 1})
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	ep := openCodeEndpoint{ID: "acme-" + openCodeNonce(), URL: server.URL, Password: "synthetic-private", PID: 1, Version: "2.0.18"}
	if err := os.MkdirAll(filepath.Join(root, "state", "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(ep)
	if err := os.WriteFile(filepath.Join(root, "state", "opencode", "service.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	sess, err := s.create(context.Background(), "acme-native", "acme", dir, "cat", "opencode", []string{"BERTH_OPENCODE_RUNTIME=" + root, "BERTH_OPENCODE_SESSION=ses_acme"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(openCodeOwner{Name: sess.Name, Directory: dir, SessionID: "ses_acme", Instance: ep.ID})
	if err := os.WriteFile(filepath.Join(root, "owner.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return &Box{Sessions: s, Locations: NewLocations(filepath.Join(t.TempDir(), "locations.json"))}, sess, ep
}

func TestOpenCodeSettingsSecretsAndOAuthRecovery(t *testing.T) {
	const secret = "acme-sentinel-secret-never-public"
	status := "pending"
	b, sess, ep := syntheticOpenCode(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			writeJSON(w, []any{map[string]any{"type": "document", "path": "/acme/opencode.jsonc", "info": map[string]any{"providers": map[string]string{"apiKey": secret}}}})
		case "/api/mcp":
			writeJSON(w, map[string]any{"data": []any{map[string]any{"name": "acme", "status": map[string]string{"status": "needs_auth", "error": secret}}}})
		case "/api/plugin":
			writeJSON(w, map[string]any{"data": []any{map[string]any{"id": "acme", "state": map[string]string{"status": "failed", "error": secret}}}})
		case "/api/integration":
			writeJSON(w, map[string]any{"data": []any{map[string]any{"id": "acme", "name": "Acme", "metadata": map[string]string{"token": secret}, "methods": []any{map[string]any{"type": "oauth", "id": "web", "form": []any{map[string]string{"type": "string", "key": "account", "default": secret}}}}, "connections": []any{map[string]string{"type": "credential", "label": secret}}}}})
		case "/api/integration/acme/connect/oauth":
			writeJSON(w, map[string]any{"data": map[string]string{"attemptID": "attempt_acme", "url": "https://example.com/authorize", "mode": "code", "instructions": secret}})
		case "/api/integration/acme/connect/oauth/attempt_acme":
			if r.Method == "DELETE" {
				status = "cancelled"
			}
			writeJSON(w, map[string]any{"data": map[string]string{"status": status, "message": secret}})
		case "/api/integration/acme/connect/oauth/attempt_acme/complete":
			status = "complete"
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected native endpoint: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	act := func(action string) error {
		body, _ := json.Marshal(map[string]string{"instance": ep.ID, "action": action, "integration": "acme", "method": "web", "attempt": "attempt_acme", "code": "synthetic-code"})
		r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		r.SetPathValue("name", sess.Name)
		return b.openCodeSettingAction(httptest.NewRecorder(), r)
	}
	if err := act("oauth"); err != nil {
		t.Fatal(err)
	}
	read := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/", nil)
		r.SetPathValue("name", sess.Name)
		w := httptest.NewRecorder()
		if err := (&Box{Sessions: b.Sessions}).openCodeSettings(w, r); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), ep.Password) {
			t.Fatal("settings leaked a credential")
		}
		return w
	}
	if w := read(); !strings.Contains(w.Body.String(), "attempt_acme") || !strings.Contains(w.Body.String(), "needs_auth") {
		t.Fatal("authentication cannot be resumed")
	}
	if err := act("oauth-complete"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read().Body.String(), "complete") {
		t.Fatal("completed sign-in is not visible")
	}
	if err := act("oauth-cancel"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read().Body.String(), "attempt_acme") {
		t.Fatal("cancelled sign-in retained")
	}
}

func TestOpenCodeTransferPreviewConflictsAndBoundaries(t *testing.T) {
	b, sess, ep := syntheticOpenCode(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	file := openCodeTransferFile{Path: ".opencode/skills/acme/SKILL.md", Content: "---\nname: acme\ndescription: Acme\n---\nUse ./check.sh via [check](./check.sh).\n"}
	call := func(action string, files []openCodeTransferFile) ([]openCodeTransferFile, error) {
		body, _ := json.Marshal(map[string]any{"instance": ep.ID, "action": action, "files": files})
		r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		r.SetPathValue("name", sess.Name)
		w := httptest.NewRecorder()
		err := b.openCodeTransfer(w, r)
		var result struct {
			Files []openCodeTransferFile `json:"files"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return result.Files, err
	}
	if _, err := call("preview", []openCodeTransferFile{file}); err == nil {
		t.Fatal("missing dependency accepted")
	}
	files, err := call("preview", []openCodeTransferFile{file, {Path: ".opencode/skills/acme/check.sh", Content: "#!/bin/sh\nprintf acme\n", Executable: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call("apply", files); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(sess.Dir, ".opencode/skills/acme/check.sh")); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatal("transferred script lost its executable flag", err)
	}
	if _, err := call("apply", files); err == nil {
		t.Fatal("stale destination version overwritten")
	}
	for _, name := range []string{"../AGENTS.md", ".opencode/../opencode.json", ".opencode/auth.json", ".opencode/skills/acme/.env"} {
		if _, err := call("preview", []openCodeTransferFile{{Path: name, Content: "acme"}}); err == nil {
			t.Fatalf("unsafe transfer path accepted: %s", name)
		}
	}
	if _, err := call("preview", []openCodeTransferFile{{Path: "AGENTS.md", Content: "API_KEY=acme-sentinel"}}); err == nil {
		t.Fatal("inline credential accepted")
	}
	victim := filepath.Join(t.TempDir(), "victim.md")
	if err := os.WriteFile(victim, []byte("acme user work"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(sess.Dir, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := call("preview", []openCodeTransferFile{{Path: "AGENTS.md", Content: "overwrite"}}); err == nil {
		t.Fatal("symlink transfer accepted")
	}
	data, _ := os.ReadFile(victim)
	if string(data) != "acme user work" {
		t.Fatal("user file changed")
	}
}

func TestOpenCodeJSONCPreservesCommentsAndRejectsInvalidDocuments(t *testing.T) {
	valid := []byte("{ // Acme\n\"model\":\"acme/model\",\n}")
	before := string(valid)
	if err := validateOpenCodeJSONC("opencode.jsonc", valid); err != nil || string(valid) != before {
		t.Fatal("valid JSONC changed or rejected", err)
	}
	for _, text := range []string{"{", "null", "[]"} {
		if err := validateOpenCodeJSONC(".opencode/opencode.jsonc", []byte(text)); err == nil {
			t.Fatalf("invalid config accepted: %s", text)
		}
	}
}

func TestOpenCodeFirstPromptHonorsSendGateBeforeLaunching(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, []byte(`{"hooks":[{"on":"before:session.send","run":"echo acme send refused; exit 1"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &Box{Hooks: &hooks.Runner{Path: path}, Locations: NewLocations(filepath.Join(t.TempDir(), "locations.json"))}
	repo := gitRepo(t)
	if _, err := b.Locations.Add(context.Background(), "acme", repo); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"box", "project"} {
		if scope == "project" {
			b.Hooks = nil
			writeRepoConfig(t, repo, RepoConfig{Hooks: []hooks.Hook{{On: "before:session.send", Run: "echo acme send refused; exit 1"}}})
			trustRepo(t, b.Locations, "acme")
		}
		for _, files := range [][]openCodeFile{nil, {{URI: "file:///acme/image.png"}}} {
			_, err := b.startSession(httptest.NewRequest("POST", "/sessions", nil), "acme", "acme", repo, "opencode mini --standalone", "opencode", true, files)
			if err == nil || !strings.Contains(err.Error(), "acme send refused") {
				t.Fatalf("first prompt crossed %s send gate: %v", scope, err)
			}
		}
	}
}

func TestOpenCodeFollowupGatesApplyOnceForAPIAndAutomation(t *testing.T) {
	s := testSessions(t)
	b := &Box{Sessions: s, Locations: NewLocations(filepath.Join(t.TempDir(), "locations.json"))}
	repo := gitRepo(t)
	if _, err := b.Locations.Add(context.Background(), "acme", repo); err != nil {
		t.Fatal(err)
	}
	sess, err := s.create(context.Background(), "acme-gated", "acme", repo, "cat", "opencode", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"box", "project"} {
		for _, transport := range []string{"api", "automation"} {
			for _, deny := range []bool{true, false} {
				count := filepath.Join(t.TempDir(), "calls")
				run := "echo called >> " + shellQuote(count)
				want := "no native runtime"
				if deny {
					run += "; echo acme send refused; exit 1"
					want = "acme send refused"
				}
				hk := hooks.Hook{On: "before:session.send", Run: run}
				b.Hooks = nil
				if scope == "box" {
					path := filepath.Join(t.TempDir(), "hooks.json")
					data, _ := json.Marshal(map[string]any{"hooks": []hooks.Hook{hk}})
					if err := os.WriteFile(path, data, 0o600); err != nil {
						t.Fatal(err)
					}
					b.Hooks = &hooks.Runner{Path: path}
				} else {
					writeRepoConfig(t, repo, RepoConfig{Hooks: []hooks.Hook{hk}})
					trustRepo(t, b.Locations, "acme")
				}
				if transport == "api" {
					r := httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"Acme followup"}`))
					r.SetPathValue("name", sess.Name)
					err = b.sendToSession(httptest.NewRecorder(), r)
				} else {
					_, err = b.sendPrompt(context.Background(), sess.Name, SendRequest{Text: "Acme followup", When: "idle"}, "berth", "berth:report")
				}
				calls, _ := os.ReadFile(count)
				if err == nil || !strings.Contains(err.Error(), want) || string(calls) != "called\n" {
					t.Fatalf("%s/%s/deny=%v: error %v, gate calls %q", scope, transport, deny, err, calls)
				}
			}
		}
	}
}

func TestOpenCodeAttachmentRejectsForeignMissingAndUnsupportedMedia(t *testing.T) {
	b, sess, ep := syntheticOpenCode(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi.json":
			writeJSON(w, map[string]any{"paths": map[string]any{"/model": map[string]any{"get": map[string]string{"operationId": "model.list"}, "post": map[string]string{"operationId": "session.switchModel"}}}})
		case "/api/session/ses_acme":
			writeJSON(w, map[string]any{"data": map[string]any{"model": map[string]string{"providerID": "acme", "id": "text"}}})
		case "/api/model":
			writeJSON(w, map[string]any{"data": []any{map[string]any{"id": "text", "providerID": "acme", "enabled": true, "capabilities": map[string]any{"input": []string{"text"}}}}})
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	_, owner, err := b.openCodeRuntime(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(sess.Dir, "acme été #1.png")
	if err := os.WriteFile(image, []byte{137, 80, 78, 71, 13, 10, 26, 10}, 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "foreign.txt")
	if err := os.WriteFile(outside, []byte("acme"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(sess.Dir, "escape.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{image, outside, link, filepath.Join(sess.Dir, "missing.txt")} {
		files := []openCodeFile{{URI: (&url.URL{Scheme: "file", Path: path}).String()}}
		if err := openCodeFiles(context.Background(), ep, owner, files); err == nil {
			t.Fatalf("invalid attachment admitted: %s", filepath.Base(path))
		}
	}
	text := filepath.Join(sess.Dir, "acme été #1.txt")
	if err := os.WriteFile(text, []byte("Acme text"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []openCodeFile{{URI: (&url.URL{Scheme: "file", Path: text}).String()}}
	if err := openCodeFiles(context.Background(), ep, owner, files); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files[0].URI, "%20%C3%A9t%C3%A9%20%231.txt") {
		t.Fatal("special characters lost in native URI")
	}
}

func TestOpenCodeAttachmentWaitsForSelectedModelCatalog(t *testing.T) {
	var reads atomic.Int32
	b, sess, ep := syntheticOpenCode(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi.json":
			writeJSON(w, map[string]any{"paths": map[string]any{"/model": map[string]any{"get": map[string]string{"operationId": "model.list"}, "post": map[string]string{"operationId": "session.switchModel"}}}})
		case "/api/session/ses_acme":
			writeJSON(w, map[string]any{"data": map[string]any{"model": map[string]string{"providerID": "acme", "id": "vision"}}})
		case "/api/model":
			models := []any{}
			if reads.Add(1) > 1 {
				models = append(models, map[string]any{"id": "vision", "providerID": "acme", "enabled": true, "capabilities": map[string]any{"input": []string{"text", "image"}}})
			}
			writeJSON(w, map[string]any{"data": models})
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	_, owner, err := b.openCodeRuntime(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sess.Dir, "acme.png")
	if err := os.WriteFile(path, []byte{137, 80, 78, 71, 13, 10, 26, 10}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := openCodeFiles(context.Background(), ep, owner, []openCodeFile{{URI: (&url.URL{Scheme: "file", Path: path}).String()}}); err != nil {
		t.Fatalf("partial startup catalog rejected a supported attachment: %v", err)
	}
}

func TestOpenCodeReconcilesNativeIDsWithoutCrossingAChildPermission(t *testing.T) {
	var phase atomic.Int32
	b, sess, ep := syntheticOpenCode(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi.json":
			writeJSON(w, map[string]any{"paths": map[string]any{"/permission": map[string]any{"get": map[string]string{"operationId": "session.permission.list"}, "post": map[string]string{"operationId": "session.permission.reply"}}}})
		case "/api/session/active":
			data := map[string]any{}
			if phase.Load() < 2 {
				data["ses_acme"] = map[string]string{"type": "running"}
			}
			writeJSON(w, map[string]any{"data": data})
		case "/api/session/ses_acme/inbox":
			writeJSON(w, map[string]any{"data": []any{}})
		case "/api/session/ses_acme/permission":
			writeJSON(w, map[string]any{"data": []any{}})
		case "/api/session":
			writeJSON(w, map[string]any{"data": []any{map[string]any{"id": "ses_child", "parentID": "ses_acme"}}})
		case "/api/session/ses_child/permission":
			p := []any{}
			if phase.Load() == 0 {
				p = append(p, map[string]string{"id": "per_acme", "sessionID": "ses_child"})
			}
			writeJSON(w, map[string]any{"data": p})
		case "/api/session/ses_acme/message/msg_acme":
			writeJSON(w, map[string]any{"data": map[string]string{"id": "msg_acme"}})
		default:
			t.Errorf("unexpected native endpoint: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	bus := &events.Bus{Sequence: true}
	turns := &Turns{}
	turns.Attach(bus)
	turns.Track(sess)
	b.Events, b.Turns = bus, turns
	e := bus.Publish(events.Event{Type: "session.sent", Data: map[string]any{"name": sess.Name, "native_id": "msg_acme", "idem_key": "acme"}})
	turn, ok := turns.ForSent(sess.Name, e.Seq)
	if !ok || turn.NativeID != "msg_acme" {
		t.Fatal("native admission not linked")
	}
	_, owner, err := b.openCodeRuntime(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"waiting", "running", "finished"} {
		phase.Store(int32(i))
		fresh := &Box{Sessions: b.Sessions, Events: bus, Turns: turns}
		if err := fresh.openCodeReconcile(context.Background(), sess, ep, owner); err != nil {
			t.Fatal(err)
		}
		got, ok := turns.ByIdem(sess.Name, "acme")
		if !ok || got.ID != turn.ID || got.State != want {
			t.Fatalf("phase %d: %+v, want %s", i, got, want)
		}
	}
	root := b.Sessions.EnvVar(context.Background(), sess, "BERTH_OPENCODE_RUNTIME")
	owner.Instance = "stale-instance"
	data, _ := json.Marshal(owner)
	if err := os.WriteFile(filepath.Join(root, "owner.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.openCodeRuntime(context.Background(), sess); err == nil {
		t.Fatal("reused task accepted a stale runtime")
	}
}
