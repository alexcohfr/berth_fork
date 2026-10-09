package box

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cosscom/shipyard/internal/events"
	"github.com/cosscom/shipyard/internal/integrations"
	"github.com/cosscom/shipyard/internal/transcript"
)

func TestOpenCodeChatReadsItsLinkedSessionThroughTheAPI(t *testing.T) {
	s := testSessions(t)
	dir := t.TempDir()
	fixture, err := filepath.Abs("../transcript/testdata/opencode.json")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "opencode")
	script := "#!/bin/sh\n[ \"$1 $2 $3\" = 'api --standalone session.message.list' ] || exit 3\n[ \"$5\" = sessionID=ses_acme ] || exit 4\n[ -z \"$BERTH_SESSION\" ] || exit 5\ncat \"$TEST_OPENCODE_FIXTURE\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("TEST_OPENCODE_FIXTURE", fixture)
	ctx := context.Background()
	sess, err := s.create(ctx, "acme-opencode", "acme", dir, "cat", "opencode", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bus := &events.Bus{Sequence: true}
	turns := &Turns{}
	turns.Attach(bus)
	turns.Track(sess)
	bus.Publish(events.Event{Type: "agent.ready", Origin: "opencode", Data: map[string]any{"session": sess.Name, "path": dir, "agent": "opencode", "agent_session_id": "ses_acme"}})
	b := &Box{Sessions: s, Turns: turns}
	r := httptest.NewRequest("GET", "/transcript", nil)
	w := httptest.NewRecorder()
	if err := b.openCodeTranscript(w, r, sess); err != nil {
		t.Fatal(err)
	}
	var got transcript.Result
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != "opencode" || len(got.Items) != 6 || got.File != "ses_acme" {
		t.Fatalf("chat = %+v", got)
	}
	// Never guess by directory when two agents share a worktree.
	other := sess
	other.Name = "another-opencode"
	w = httptest.NewRecorder()
	if err := b.openCodeTranscript(w, r, other); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.Body.String(), `"source":"none"`) {
		t.Fatalf("unlinked chat = %s", w.Body)
	}
}

func TestOpenCodeModelsUseTheProjectAndOnlyExposeEnabledChoices(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	script := `#!/bin/sh
[ "$1 $2" = 'api model.list' ] || exit 3
[ "$3" = --param ] && [ "$4" = "location[directory]=$PWD" ] || exit 4
[ -z "$BERTH_SESSION" ] && [ -z "$BERTH_AGENT" ] || exit 5
[ "$ACME_MODEL_ENV" = project ] || exit 6
cat <<'JSON'
{"data":[{"id":"coder","providerID":"acme","name":"Acme Coder","enabled":true,"headers":{"Authorization":"must-not-leak"},"variants":[{"id":"high","settings":{"apiKey":"must-not-leak"}},{"id":"bad;id"}]},{"id":"hidden","providerID":"acme","enabled":false},{"id":"bad;id","providerID":"acme","enabled":true}]}
JSON
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("BERTH_SESSION", "another-session")
	t.Setenv("BERTH_AGENT", "opencode")
	c, _ := servedBox(t)
	repo := gitRepo(t)
	call(t, c, "POST", "/v1/locations", "", map[string]string{"name": "acme", "path": repo}, nil)
	if status := call(t, c, "PUT", "/v1/locations/acme/config", "", map[string]any{"local": RepoConfig{Env: map[string]string{"ACME_MODEL_ENV": "project"}}}, nil); status != 200 {
		t.Fatalf("config status = %d", status)
	}
	var got []map[string]any
	if status := call(t, c, "GET", "/v1/agents/opencode/models?at=acme", "", nil, &got); status != 200 {
		t.Fatalf("models status = %d", status)
	}
	data, _ := json.Marshal(got)
	if string(data) != `[{"id":"acme/coder","name":"Acme Coder","variants":["high"]}]` {
		t.Fatalf("models = %s", data)
	}
	if status := call(t, c, "GET", "/v1/agents/opencode/models?at=missing", "", nil, nil); status == 200 {
		t.Fatal("accepted an unknown project")
	}
}

// Opt-in contract check against an installed OpenCode V2, with an entirely
// isolated home/store and a localhost model stub. No paid model requests.
func TestOpenCodeLiveV2Contract(t *testing.T) {
	bin := os.Getenv("OPENCODE_TEST_BIN")
	if bin == "" {
		t.Skip("set OPENCODE_TEST_BIN to run the OpenCode V2 contract check")
	}
	var err error
	bin, err = filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	// Only the executable and ordinary process plumbing cross the boundary.
	// In particular, never inherit provider keys, CLI config, service endpoints,
	// or plugin environment from the developer running this opt-in check.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "PATH", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE", "TERM":
			continue
		}
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(home, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"HOME": home, "TMPDIR": tmp, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, "data"),
		"XDG_STATE_HOME": filepath.Join(home, "state"), "XDG_CACHE_HOME": filepath.Join(home, "cache"), "OPENCODE_DB": filepath.Join(home, "data", "test.db"),
		"OPENCODE_CONFIG": "", "OPENCODE_CONFIG_DIR": "", "OPENCODE_CONFIG_CONTENT": "", "BERTH_SESSION": "acme-opencode",
		"BERTH_OPENCODE_TEST_HELPER": "1",
		"BERTH_OPENCODE_BIN":         bin,
	} {
		t.Setenv(key, value)
		if value == "" {
			os.Unsetenv(key)
		}
	}
	versionCtx, versionCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer versionCancel()
	version := exec.CommandContext(versionCtx, bin, "--version")
	version.Dir = project
	if out, err := version.CombinedOutput(); err != nil {
		t.Fatalf("isolated OpenCode executable: %v: %s (use the executable, not a HOME-dependent launcher)", err, out)
	} else {
		t.Logf("contract version: %s", strings.TrimSpace(string(out)))
	}
	hooks := filepath.Join(home, "hooks")
	fake := filepath.Join(home, "berthd")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s %s\\n' \"$3\" \"$4\" >>\"$TEST_HOOKS\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_HOOKS", hooks)
	if err := integrations.InstallTool(home, "opencode", fake, os.Stdout); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(home, "context.json")
	t.Setenv("ACME_CONTEXT_PROBE", probe)
	if err := os.WriteFile(filepath.Join(home, ".config", "opencode", "plugins", "acme-contract.js"), []byte(`
import { writeFileSync } from "node:fs";
export default { id: "acme-contract", setup(ctx) {
  const methods = (value, prefix = "", depth = 0) => Object.entries(value).flatMap(([key, item]) => {
    const name = prefix + key;
    if (typeof item === "function") return [name];
    return item && typeof item === "object" && depth < 2 ? methods(item, name + ".", depth + 1) : [];
  });
  writeFileSync(process.env.ACME_CONTEXT_PROBE, JSON.stringify({
    version: ctx.app.version, methods: methods(ctx).sort(),
    compact: typeof ctx.session.compact,
    form: typeof ctx.form, sessionForm: typeof ctx.session.form,
  }));
} };
`), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests sync.Map
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("data:image/png;base64,")) {
			requests.Store("image", true)
		}
		if bytes.Contains(body, []byte("acme-native-skill-marker")) {
			requests.Store("skill", true)
		}
		for _, marker := range []string{"acme-block-a", "acme-block-b"} {
			if strings.Contains(string(body), marker) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"acme\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Working\"},\"finish_reason\":null}]}\n\n")
				w.(http.Flusher).Flush()
				requests.Store(marker, r.Header.Get("Authorization"))
				<-r.Context().Done()
				requests.Delete(marker)
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"acme\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Acme smoke reply\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"acme\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()
	t.Setenv("ACME_API_KEY", "synthetic")
	config := fmt.Sprintf(`{"model":"acme/model","commands":{"acme":{"template":"Acme command $ARGUMENTS"}},"permissions":[{"action":"acme.contract","resource":"*","effect":"ask"}],"enabled_providers":["acme"],"providers":{"acme":{"env":["ACME_API_KEY"],"package":"@opencode/ai/providers/openai-compatible","settings":{"baseURL":%q},"models":{"model":{"capabilities":{"input":["text","image"],"output":["text"],"tools":true},"variants":[{"id":"high","settings":{}}]}}}}}`, model.URL+"/v1")
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(project, ".opencode", "skills", "acme")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: acme\ndescription: Acme contract skill\n---\nacme-native-skill-marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, append([]string{"api", "--standalone"}, args...)...)
		cmd.Dir = project
		out, err := cmd.Output()
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				t.Log(string(e.Stderr))
			}
			t.Fatalf("OpenCode API %s: %v", args[0], err)
		}
		return out
	}
	s := testSessions(t)
	command, err := AgentCommandWith(AgentPreset{ID: "opencode", Command: "opencode", PromptFlag: "--prompt", ModelFlag: "--model"}, "Reply with the smoke response", "acme/model#high", "")
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(filepath.Dir(s.Commands), "opencode")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "runtime-")
	if err != nil {
		t.Fatal(err)
	}
	id := "ses_" + openCodeNonce()
	command = shellQuote(launcher) + " -test.run=^TestOpenCodeLauncherProcess$ -- mini" + strings.TrimPrefix(command, "opencode mini --standalone")
	sess, err := s.create(context.Background(), "acme-opencode", "acme", project, command, "opencode", []string{"BERTH_OPENCODE_TEST_HELPER=1", "BERTH_OPENCODE_BIN=" + bin, "BERTH_OPENCODE_RUNTIME=" + root, "BERTH_OPENCODE_SESSION=" + id}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if t.Failed() {
			screen, _ := s.Screen(context.Background(), sess.Name, 100)
			out, _ := os.ReadFile(hooks)
			t.Logf("Mini: %s\nHooks: %s", screen, out)
		}
	}()
	waitUntil(t, "OpenCode's idle hook", 20*time.Second, func() bool {
		out, _ := os.ReadFile(hooks)
		return strings.Contains(string(out), "session.idle ")
	})
	out, err := os.ReadFile(hooks)
	if err != nil || !strings.Contains(string(out), "session.busy ") || !strings.Contains(string(out), "session.idle ") {
		t.Fatalf("real turn hooks: %q, %v", out, err)
	}
	var hook struct {
		ID string `json:"session_id"`
	}
	_, payload, _ := strings.Cut(strings.SplitN(string(out), "\n", 2)[0], " ")
	if err := json.Unmarshal([]byte(payload), &hook); err != nil || !openCodeID.MatchString(hook.ID) {
		t.Fatalf("session link: %s, %v", payload, err)
	}
	if hook.ID != id {
		t.Fatalf("Mini linked %s, want the owner's session %s", hook.ID, id)
	}
	b := &Box{Sessions: s, Locations: NewLocations(filepath.Join(home, "locations.json"))}
	ep, owner, err := b.openCodeRuntime(context.Background(), sess)
	if err != nil || owner.SessionID != id {
		t.Fatalf("owner discovery: %v, %+v", err, owner)
	}
	// A fresh daemon client reconstructs the connection from the pane and its
	// private registration without owning or restarting the OpenCode process.
	fresh := &Box{Sessions: s}
	ep2, _, err := fresh.openCodeRuntime(context.Background(), sess)
	if err != nil || ep2.ID != ep.ID || ep2.PID != ep.PID {
		t.Fatalf("reconnected owner: %v", err)
	}
	unauth, err := http.Get(ep.URL + "/api/info")
	if err != nil {
		t.Fatal(err)
	}
	unauth.Body.Close()
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated owner status = %d", unauth.StatusCode)
	}
	inventory, err := os.ReadFile(probe)
	if err != nil {
		t.Fatal(err)
	}
	var surface struct {
		Version     string   `json:"version"`
		Methods     []string `json:"methods"`
		Compact     string   `json:"compact"`
		Form        string   `json:"form"`
		SessionForm string   `json:"sessionForm"`
	}
	if err := json.Unmarshal(inventory, &surface); err != nil {
		t.Fatal(err)
	}
	t.Logf("public plugin context: %s", inventory)
	schemaBytes := api("get", "/openapi.json")
	t.Run("published_http_surface", func(t *testing.T) {
		var schema struct {
			Paths map[string]map[string]struct {
				ID string `json:"operationId"`
			} `json:"paths"`
		}
		if err := json.Unmarshal(schemaBytes, &schema); err != nil {
			t.Fatal(err)
		}
		var operations []string
		for _, path := range schema.Paths {
			for _, method := range path {
				operations = append(operations, method.ID)
			}
		}
		for _, operation := range []string{"session.prompt", "session.interrupt", "session.permission.reply", "session.form.reply", "session.message.list", "session.inbox.list", "session.compact", "session.fork", "session.revert.stage", "session.revert.clear", "session.revert.commit", "event.subscribe"} {
			if !slices.Contains(operations, operation) {
				t.Errorf("OpenCode %s HTTP schema lacks %s", surface.Version, operation)
			}
		}
	})
	t.Logf("historical plugin limits (not the control transport): compact=%s form=%s session.form=%s", surface.Compact, surface.Form, surface.SessionForm)
	out = api("session.message.list", "--param", "sessionID="+id, "--param", "limit=100", "--param", "order=desc")
	var page struct {
		Data []transcript.OpenCodeMessage `json:"data"`
	}
	if err := json.Unmarshal(out, &page); err != nil {
		t.Fatal(err)
	}
	live := transcript.OpenCode(id, project, page.Data, false)
	if len(live.Items) != 3 || live.Items[0].Kind != "notice" || live.Items[2].Text != "Acme smoke reply" {
		t.Fatalf("real model reply: %+v", live)
	}
	if err := ep.call(context.Background(), "POST", "/api/session/"+id+"/prompt", map[string]any{"id": "msg_acme_native_reply", "text": "Reply again"}, nil); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the composer reply's idle hook", 20*time.Second, func() bool {
		out, _ := os.ReadFile(hooks)
		return strings.Count(string(out), "session.idle ") == 2
	})
	t.Run("catalog_and_attachments", func(t *testing.T) {
		catalog, err := openCodeCatalogAt(context.Background(), ep, owner.Directory)
		if err != nil || len(catalog.Models) != 1 || len(catalog.Agents) == 0 {
			t.Fatalf("runtime catalog: %+v, %v", catalog, err)
		}
		found := false
		for _, c := range catalog.Commands {
			if c.Name == "acme" {
				found = true
			}
		}
		if !found {
			t.Fatal("project command missing")
		}
		var skillID string
		for _, skill := range catalog.Skills {
			if skill.Name == "acme" {
				skillID = skill.ID
			}
		}
		if skillID == "" {
			t.Fatal("project skill missing from effective catalog")
		}
		if _, err := b.sendPrompt(context.Background(), sess.Name, SendRequest{Text: "Acme native skill invocation", Skills: []openCodeSkill{{ID: skillID}}, IdemKey: "acme-skill"}, "test", "test"); err != nil {
			t.Fatal(err)
		}
		waitUntil(t, "native skill reaches provider", 10*time.Second, func() bool { _, ok := requests.Load("skill"); return ok })
		if _, err := b.sendPrompt(context.Background(), sess.Name, SendRequest{Text: "/acme retry arguments", IdemKey: "acme-command"}, "test", "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := b.sendPrompt(context.Background(), sess.Name, SendRequest{Text: "/unknown", IdemKey: "acme-unknown"}, "test", "test"); err == nil {
			t.Fatal("unknown command sent as a prompt")
		}
		img := image.NewRGBA(image.Rect(0, 0, 2, 2))
		img.Set(0, 0, color.RGBA{R: 255, A: 255})
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, img); err != nil {
			t.Fatal(err)
		}
		attachment, err := SaveAttachment(context.Background(), project, "acme image été.png", encoded.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		files := []openCodeFile{{URI: (&url.URL{Scheme: "file", Path: attachment.Path}).String(), Name: "Acme image"}}
		if _, err := b.sendPrompt(context.Background(), sess.Name, SendRequest{Text: "Acme image", Files: files, IdemKey: "acme-image"}, "test", "test"); err != nil {
			t.Fatal(err)
		}
		waitUntil(t, "structured image reaches provider", 10*time.Second, func() bool { _, ok := requests.Load("image"); return ok })
	})
	t.Run("native_form", func(t *testing.T) {
		ctx := context.Background()
		if err := ep.call(ctx, "POST", "/api/session/"+id+"/form", map[string]any{"id": "frm_acme", "title": "Acme form", "fields": []any{map[string]any{"key": "count", "type": "integer", "required": true, "minimum": 1, "maximum": 5}, map[string]any{"key": "enabled", "type": "boolean"}}}, nil); err != nil {
			t.Fatal(err)
		}
		answer := func(sessionID, instance string, value any) error {
			body, _ := json.Marshal(map[string]any{"session_id": sessionID, "instance": instance, "id": "frm_acme", "answer": map[string]any{"count": value, "enabled": true}})
			r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
			r.SetPathValue("name", sess.Name)
			r.SetPathValue("action", "form")
			return b.openCodeAction(httptest.NewRecorder(), r)
		}
		if err := answer("ses_foreign", ep.ID, 3); err == nil {
			t.Fatal("foreign form accepted")
		}
		if err := answer(id, "stale", 3); err == nil {
			t.Fatal("stale instance accepted")
		}
		if err := answer(id, ep.ID, 2.5); err == nil {
			t.Fatal("invalid integer accepted")
		}
		if err := answer(id, ep.ID, 3); err != nil {
			t.Fatal(err)
		}
		if err := answer(id, ep.ID, 3); err == nil {
			t.Fatal("answered form accepted twice")
		}
	})
	t.Run("live_events_and_lost_ack", func(t *testing.T) {
		bus := &events.Bus{Sequence: true}
		watch := &Box{Sessions: s, Events: bus}
		stream, unsubscribe := bus.Subscribe()
		defer unsubscribe()
		watch.openCodeWatch(sess, ep, owner)
		waitEvent := func() {
			t.Helper()
			select {
			case event := <-stream:
				if event.Type != events.TranscriptChanged || event.Data["session"] != sess.Name || len(event.Data) != 2 {
					t.Fatalf("unexpected native invalidation: %+v", event)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("native event subscription did not invalidate history")
			}
		}
		waitEvent() // New subscriptions invalidate because events have no replay.
		var calls atomic.Int32
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				w.WriteHeader(400)
				return
			}
			var admission any
			if err := ep.call(r.Context(), "POST", "/api/session/"+id+"/prompt", body, &admission); err != nil {
				w.WriteHeader(502)
				return
			}
			if calls.Add(1) == 1 {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
				return
			}
			writeJSON(w, admission)
		}))
		defer proxy.Close()
		lost := ep
		lost.URL = proxy.URL
		body := map[string]any{"id": "msg_acme_lost_ack", "text": "Acme private event content", "resume": false}
		if err := lost.call(context.Background(), "POST", "/prompt", body, nil); err == nil {
			t.Fatal("lost acknowledgement reported as success")
		}
		waitEvent()
		if err := lost.call(context.Background(), "POST", "/prompt", body, nil); err != nil {
			t.Fatal(err)
		}
		var inbox struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := ep.call(context.Background(), "GET", "/api/session/"+id+"/inbox", nil, &inbox); err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, item := range inbox.Data {
			if item.ID == "msg_acme_lost_ack" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("lost ACK admitted %d copies", count)
		}
		if err := ep.call(context.Background(), "DELETE", "/api/session/"+id+"/inbox/msg_acme_lost_ack", nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("durable_admission", func(t *testing.T) {
		req := SendRequest{Text: "Acme retry", IdemKey: "acme-retry"}
		var results [2]SendResult
		var failures [2]error
		var wg sync.WaitGroup
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], failures[i] = b.sendPrompt(context.Background(), sess.Name, req, "test", "test")
			}()
		}
		wg.Wait()
		if failures[0] != nil || failures[1] != nil || results[0].NativeID == "" || results[0].NativeID != results[1].NativeID {
			t.Fatalf("concurrent admissions: %+v, %+v", results, failures)
		}
		// Simulate an acknowledgement lost before the receipt was saved. The
		// already-admitted native ID must still not produce a second prompt.
		path := filepath.Join(base, "requests", id, results[0].NativeID+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var receipt openCodeReceipt
		if err := json.Unmarshal(data, &receipt); err != nil {
			t.Fatal(err)
		}
		receipt.Result = nil
		data, _ = json.Marshal(receipt)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		retried, err := fresh.sendPrompt(context.Background(), sess.Name, req, "test", "test")
		if err != nil || retried.NativeID != results[0].NativeID {
			t.Fatalf("lost ACK retry: %+v, %v", retried, err)
		}
		req.Text = "Different content"
		if _, err := b.sendPrompt(context.Background(), sess.Name, req, "test", "test"); err == nil {
			t.Fatal("reused ID accepted different content")
		}
		waitUntil(t, "one delivered retry", 10*time.Second, func() bool {
			var page struct {
				Data []transcript.OpenCodeMessage `json:"data"`
			}
			if ep.call(context.Background(), "GET", "/api/session/"+id+"/message?limit=100&order=desc", nil, &page) != nil {
				return false
			}
			count := 0
			for _, m := range page.Data {
				if m.ID == retried.NativeID {
					count++
				}
			}
			return count == 1
		})
	})
	t.Run("independent_owners", func(t *testing.T) {
		rootB, err := os.MkdirTemp(base, "runtime-")
		if err != nil {
			t.Fatal(err)
		}
		idB := "ses_" + openCodeNonce()
		sessB, err := s.create(context.Background(), "acme-opencode-b", "acme", project,
			shellQuote(launcher)+" -test.run=^TestOpenCodeLauncherProcess$ -- mini --model acme/model#high",
			"opencode", []string{"BERTH_OPENCODE_TEST_HELPER=1", "BERTH_OPENCODE_BIN=" + bin, "BERTH_OPENCODE_RUNTIME=" + rootB, "BERTH_OPENCODE_SESSION=" + idB, "ACME_API_KEY=synthetic-b"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Kill(context.Background(), sessB.Name)
		var other openCodeEndpoint
		waitUntil(t, "second owner", 15*time.Second, func() bool {
			other, _, err = b.openCodeRuntime(context.Background(), sessB)
			return err == nil
		})
		if ep.ID == other.ID || ep.PID == other.PID {
			t.Fatal("tasks share an owner")
		}
		ctx := context.Background()
		for _, task := range []struct {
			ep       openCodeEndpoint
			id, text string
		}{{ep, id, "acme-block-a"}, {other, idB, "acme-block-b"}} {
			if err := task.ep.call(ctx, "POST", "/api/session/"+task.id+"/prompt", map[string]any{"text": task.text}, nil); err != nil {
				t.Fatal(err)
			}
		}
		waitUntil(t, "two active provider requests", 10*time.Second, func() bool {
			_, a := requests.Load("acme-block-a")
			_, b := requests.Load("acme-block-b")
			return a && b
		})
		a, _ := requests.Load("acme-block-a")
		c, _ := requests.Load("acme-block-b")
		if a != "Bearer synthetic" || c != "Bearer synthetic-b" {
			t.Fatal("provider environments were not isolated")
		}
		var interrupted struct {
			Interrupted bool `json:"interrupted"`
		}
		if err := ep.call(ctx, "POST", "/api/session/"+id+"/interrupt", nil, &interrupted); err != nil || !interrupted.Interrupted {
			t.Fatalf("interrupt A: %v, %+v", err, interrupted)
		}
		waitUntil(t, "A interrupted", 5*time.Second, func() bool { _, ok := requests.Load("acme-block-a"); return !ok })
		if _, ok := requests.Load("acme-block-b"); !ok {
			t.Fatal("interrupt A stopped B")
		}
		for _, task := range []struct {
			ep          openCodeEndpoint
			id, request string
		}{{ep, id, "per_acme_a"}, {other, idB, "per_acme_b"}} {
			if err := task.ep.call(ctx, "POST", "/api/session/"+task.id+"/permission", map[string]any{"id": task.request, "action": "acme.contract", "resources": []string{"acme"}}, nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := ep.call(ctx, "POST", "/api/session/"+id+"/permission/per_acme_a/reply", map[string]string{"decision": "once"}, nil); err != nil {
			t.Fatal(err)
		}
		var pending struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := other.call(ctx, "GET", "/api/session/"+idB+"/permission", nil, &pending); err != nil || len(pending.Data) != 1 || pending.Data[0].ID != "per_acme_b" {
			t.Fatalf("B permission isolation: %v, %+v", err, pending)
		}
		if err := other.call(ctx, "POST", "/api/session/"+idB+"/permission/per_acme_b/reply", map[string]string{"decision": "reject"}, nil); err != nil {
			t.Fatal(err)
		}
		if err := other.call(ctx, "POST", "/api/session/"+idB+"/interrupt", nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("fork_compact_and_safe_revert", func(t *testing.T) {
		ctx := context.Background()
		t.Setenv("BERTH_OPENCODE_TEST_HELPER", "1")
		routeBox := &Box{Sessions: s, Locations: NewLocations(filepath.Join(home, "locations.json")), Events: &events.Bus{Sequence: true}}
		var fork struct {
			Data openCodeConversation `json:"data"`
		}
		r := httptest.NewRequest("POST", "/fork", nil)
		w := httptest.NewRecorder()
		request := ForkRequest{At: "msg_acme_native_reply", IdemKey: "acme-route-fork", Title: "Acme route fork", Text: "Acme route first instruction"}
		if err := routeBox.openCodeFork(w, r, sess, request); err != nil {
			t.Fatal(err)
		}
		var forkSession Session
		if err := json.Unmarshal(w.Body.Bytes(), &forkSession); err != nil {
			t.Fatal(err)
		}
		fork.Data.ID = routeBox.openCodeSession(forkSession)
		retry := httptest.NewRecorder()
		if err := routeBox.openCodeFork(retry, r, sess, request); err != nil || retry.Body.String() != w.Body.String() {
			t.Fatal("fork retry changed owner", err)
		}
		if _, err := routeBox.startOpenCodeConversation(r, sess, fork.Data.ID, "duplicate"); err == nil {
			t.Fatal("fork acquired a second owner")
		}
		defer s.Kill(ctx, forkSession.Name)
		var forkEP openCodeEndpoint
		waitUntil(t, "fork owner", 15*time.Second, func() bool { var err error; forkEP, _, err = b.openCodeRuntime(ctx, forkSession); return err == nil })
		first, err := b.sendPrompt(ctx, forkSession.Name, SendRequest{Text: "Acme fork first instruction", IdemKey: "fork-first"}, "test", "test")
		if err != nil {
			t.Fatal(err)
		}
		waitUntil(t, "fork completed first instruction", 10*time.Second, func() bool {
			var active struct {
				Data map[string]any `json:"data"`
			}
			if forkEP.call(ctx, "GET", "/api/session/active", nil, &active) != nil || active.Data[fork.Data.ID] != nil {
				return false
			}
			var page struct {
				Data []transcript.OpenCodeMessage `json:"data"`
			}
			if forkEP.call(ctx, "GET", "/api/session/"+fork.Data.ID+"/message?order=desc&limit=100", nil, &page) != nil {
				return false
			}
			seen := false
			reply := false
			for _, m := range page.Data {
				if m.ID == first.NativeID {
					seen = true
				}
				if !seen && m.Type == "assistant" && m.Error == nil {
					for _, p := range m.Content {
						reply = reply || p.Text == "Acme smoke reply"
					}
				}
			}
			return seen && reply
		})
		action := func(action, id string) error {
			body, _ := json.Marshal(map[string]string{"session_id": fork.Data.ID, "instance": forkEP.ID, "id": id})
			r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
			r.SetPathValue("name", forkSession.Name)
			r.SetPathValue("action", action)
			return b.openCodeAction(httptest.NewRecorder(), r)
		}
		if err := action("compact", "msg_acme_compaction"); err != nil {
			t.Fatal(err)
		}
		waitUntil(t, "native compaction delivered", 10*time.Second, func() bool {
			var page struct {
				Data transcript.OpenCodeMessage `json:"data"`
			}
			return forkEP.call(ctx, "GET", "/api/session/"+fork.Data.ID+"/message/msg_acme_compaction", nil, &page) == nil && page.Data.Type == "compaction"
		})
		waitUntil(t, "compaction idle", 10*time.Second, func() bool {
			var active struct {
				Data map[string]any `json:"data"`
			}
			return forkEP.call(ctx, "GET", "/api/session/active", nil, &active) == nil && active.Data[fork.Data.ID] == nil
		})
		userFile := filepath.Join(project, "acme-unrelated.txt")
		if err := os.WriteFile(userFile, []byte("Acme user edit"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, op := range []string{"revert-preview", "revert-stage", "revert-clear", "revert-stage", "revert-commit"} {
			if err := action(op, first.NativeID); err != nil {
				t.Fatalf("%s: %v", op, err)
			}
		}
		if data, _ := os.ReadFile(userFile); string(data) != "Acme user edit" {
			t.Fatal("revert changed unrelated user file")
		}
		var parent struct {
			Data transcript.OpenCodeMessage `json:"data"`
		}
		if err := ep.call(ctx, "GET", "/api/session/"+id+"/message/msg_acme_native_reply", nil, &parent); err != nil || parent.Data.Text != "Reply again" {
			t.Fatal("fork mutated parent", err)
		}
	})
	t.Run("paginated_child_history", func(t *testing.T) {
		ctx := context.Background()
		var original struct {
			Data map[string]any `json:"data"`
		}
		if err := ep.call(ctx, "GET", "/api/session/"+id, nil, &original); err != nil {
			t.Fatal(err)
		}
		original.Data["id"] = "ses_acme_history"
		original.Data["parentID"] = id
		messages := []any{}
		for i := range 260 {
			messages = append(messages, map[string]any{"id": fmt.Sprintf("msg_acme_history_%03d", i), "type": "user", "text": fmt.Sprintf("Acme %d", i), "time": map[string]int64{"created": int64(i + 1)}})
		}
		if err := ep.call(ctx, "POST", "/api/experimental/session/import", map[string]any{"info": original.Data, "messages": messages, "location": map[string]string{"directory": owner.Directory}}, nil); err != nil {
			t.Fatal(err)
		}
		cursor := ""
		var ids []string
		for n := 0; n < 4; n++ {
			r := httptest.NewRequest("GET", "/transcript?cursor="+url.QueryEscape(cursor), nil)
			r.SetPathValue("id", "ses_acme_history")
			w := httptest.NewRecorder()
			if err := b.openCodeTranscript(w, r, sess); err != nil {
				t.Fatal(err)
			}
			var res transcript.Result
			if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
			batch := []string{}
			for _, item := range res.Items {
				batch = append(batch, item.ID)
			}
			ids = append(batch, ids...)
			if res.Cursor == nil || *res.Cursor == "" {
				break
			}
			cursor = *res.Cursor
		}
		if len(ids) != 260 {
			t.Fatalf("pagination returned %d messages", len(ids))
		}
		for i, id := range ids {
			if id != fmt.Sprintf("msg_acme_history_%03d", i) {
				t.Fatalf("message %d out of order: %s", i, id)
			}
		}
		if err := openCodeRelated(ctx, ep, owner, "ses_acme_history"); err != nil {
			t.Fatal(err)
		}
		if err := openCodeRelated(ctx, ep, owner, "ses_foreign"); err == nil {
			t.Fatal("foreign history readable")
		}
		children, err := b.openCodeChildren(ctx, sess)
		found := false
		for _, child := range children {
			found = found || child.ID == "ses_acme_history"
		}
		if err != nil || !found {
			t.Fatalf("child list: %+v %v", children, err)
		}
		routeBox := &Box{Sessions: s, Locations: NewLocations(filepath.Join(home, "locations.json")), Events: &events.Bus{Sequence: true}}
		body, _ := json.Marshal(map[string]string{"instance": ep.ID, "id": "ses_acme_history"})
		r := httptest.NewRequest("POST", "/resume", bytes.NewReader(body))
		r.SetPathValue("name", sess.Name)
		w := httptest.NewRecorder()
		if err := routeBox.openCodeResume(w, r); err != nil {
			t.Fatal(err)
		}
		var resumed Session
		if err := json.Unmarshal(w.Body.Bytes(), &resumed); err != nil {
			t.Fatal(err)
		}
		defer s.Kill(ctx, resumed.Name)
		waitUntil(t, "resume route linked original identity", 15*time.Second, func() bool {
			_, linked, err := routeBox.openCodeRuntime(ctx, resumed)
			return err == nil && linked.SessionID == "ses_acme_history"
		})
		r = httptest.NewRequest("POST", "/resume", bytes.NewReader(body))
		r.SetPathValue("name", sess.Name)
		if err := routeBox.openCodeResume(httptest.NewRecorder(), r); err == nil {
			t.Fatal("resume route created a second owner")
		}
	})
	t.Run("shipyard_task_initial_media", func(t *testing.T) {
		ctx := context.Background()
		for _, args := range [][]string{{"init", "-b", "main", project}, {"-C", project, "add", "opencode.json"}, {"-C", project, "-c", "user.name=Acme", "-c", "user.email=acme@example.com", "commit", "-m", "acme baseline"}} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("synthetic repo: %v %s", err, out)
			}
		}
		locations := NewLocations(filepath.Join(home, "task-locations.json"))
		if _, err := locations.Add(ctx, "acme", project); err != nil {
			t.Fatal(err)
		}
		bus := &events.Bus{Sequence: true}
		turns := &Turns{}
		turns.Attach(bus)
		taskBox := &Box{Sessions: s, Locations: locations, Events: bus, Turns: turns}
		stream, unsubscribe := bus.Subscribe()
		defer unsubscribe()
		var imageData bytes.Buffer
		if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
			t.Fatal(err)
		}
		attachment, err := SaveAttachment(ctx, project, "acme first été.png", imageData.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		requests.Delete("image")
		body, _ := json.Marshal(TaskRequest{Location: "acme", Name: "native-media", Agent: "opencode", Model: "acme/model#high", Prompt: "Acme first image instruction", Files: []openCodeFile{{URI: (&url.URL{Scheme: "file", Path: attachment.Path}).String(), Name: attachment.Name}}})
		r := httptest.NewRequest("POST", "/tasks", bytes.NewReader(body))
		w := httptest.NewRecorder()
		if err := taskBox.addTask(w, r); err != nil {
			t.Fatal(err)
		}
		var task Task
		if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
			t.Fatal(err)
		}
		defer s.Kill(ctx, task.Session.Name)
		defer func() {
			if t.Failed() {
				screen, _ := s.Screen(ctx, task.Session.Name, 100)
				current, err := s.Get(ctx, task.Session.Name)
				t.Logf("initial-media launcher exited=%v read=%v screen=%s", current.Exited, err, screen)
			}
		}()
		waitUntil(t, "first native attachment reaches provider", 15*time.Second, func() bool { _, ok := requests.Load("image"); return ok })
		firstID := "msg_first_" + taskBox.openCodeSession(task.Session)
		tr, ok := turns.ByIdem(task.Session.Name, "initial")
		if !ok || tr.NativeID != firstID {
			t.Fatalf("first prompt not tracked by native ID: %+v", tr)
		}
		for len(stream) > 0 {
			event := <-stream
			data, _ := json.Marshal(event)
			if bytes.Contains(data, []byte("Acme first image instruction")) {
				t.Fatal("initial prompt leaked into event journal")
			}
		}
		if !sameDir(filepath.Dir(task.Worktree.Path), filepath.Dir(project)) || sameDir(task.Worktree.Path, project) {
			t.Fatal("task did not create an independent worktree")
		}
	})
	if err := s.Kill(context.Background(), sess.Name); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "owned server cleanup", 10*time.Second, func() bool { return ep.call(context.Background(), "GET", "/api/info", nil, nil) != nil })
	out = api("session.message.list", "--param", "sessionID="+id, "--param", "limit=100", "--param", "order=desc")
	if err := json.Unmarshal(out, &page); err != nil {
		t.Fatal(err)
	}
	live = transcript.OpenCode(id, project, page.Data, false)
	if len(live.Items) < 5 || live.Items[3].ID != "msg_acme_native_reply" || live.Items[4].Text != "Acme smoke reply" {
		t.Fatalf("reply after terminal exit: %+v", live)
	}
	t.Run("resume_same_durable_identity", func(t *testing.T) {
		resumeRoot, err := os.MkdirTemp(base, "runtime-")
		if err != nil {
			t.Fatal(err)
		}
		resumed, err := s.create(context.Background(), "acme-resumed", "acme", project, shellQuote(launcher)+" -test.run=^TestOpenCodeLauncherProcess$ -- mini", "opencode", []string{"BERTH_OPENCODE_TEST_HELPER=1", "BERTH_OPENCODE_BIN=" + bin, "BERTH_OPENCODE_RUNTIME=" + resumeRoot, "BERTH_OPENCODE_SESSION=" + id, "BERTH_OPENCODE_RESUME=1"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Kill(context.Background(), resumed.Name)
		var restored openCodeEndpoint
		var linked openCodeOwner
		waitUntil(t, "resumed owner", 15*time.Second, func() bool {
			restored, linked, err = b.openCodeRuntime(context.Background(), resumed)
			return err == nil
		})
		if linked.SessionID != id || restored.ID == ep.ID {
			t.Fatal("resume copied identity or reused stale instance")
		}
		lock, err := os.OpenFile(filepath.Join(base, id+".lock"), os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			t.Fatal("another owner can execute the resumed session")
		}
		var response struct {
			Data []transcript.OpenCodeMessage `json:"data"`
		}
		if err := restored.call(context.Background(), "GET", "/api/session/"+id+"/message?limit=100&order=asc", nil, &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Data) < 4 {
			t.Fatal("resume lost durable history")
		}
	})
	var response struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(api("session.get", "--param", "sessionID="+id), &response); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../transcript/testdata/opencode.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(fixture, &raw); err != nil {
		t.Fatal(err)
	}
	// Import completed history oldest first. Live streaming states are covered
	// by the normalizer tests; import only projects completed messages.
	response.Data["id"] = "ses_acme_imported"
	body, _ := json.Marshal(map[string]any{"info": response.Data, "messages": []json.RawMessage{raw.Data[2], raw.Data[1]}})
	api("experimental.session.import", "--data", string(body))
	out = api("session.message.list", "--param", "sessionID=ses_acme_imported", "--param", "limit=100", "--param", "order=desc")
	if err := json.Unmarshal(out, &page); err != nil {
		t.Fatal(err)
	}
	r := transcript.OpenCode("ses_acme_imported", "/acme", page.Data, false)
	if len(r.Items) != 3 || r.Items[0].Text != "Fix the retry test" {
		t.Fatalf("real projected chat = %+v", r)
	}
	out = api("session.message.get", "--param", "sessionID=ses_acme_imported", "--param", "messageID=msg_acme_tools")
	var message struct {
		Data transcript.OpenCodeMessage `json:"data"`
	}
	if err := json.Unmarshal(out, &message); err != nil {
		t.Fatal(err)
	}
	detail, err := transcript.OpenCodeDetail(message.Data, "/acme", "msg_acme_tools:call_test")
	if err != nil || detail.Command != "pnpm test" || !detail.Error {
		t.Fatalf("real tool = %+v, %v", detail, err)
	}
}

func TestOpenCodeLauncherProcess(t *testing.T) {
	if os.Getenv("BERTH_OPENCODE_TEST_HELPER") != "1" {
		return
	}
	index := slices.Index(os.Args, "--")
	if index < 0 {
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if err := RunOpenCode(ctx, os.Args[index+1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
