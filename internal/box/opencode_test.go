package box

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, "data"),
		"XDG_STATE_HOME": filepath.Join(home, "state"), "XDG_CACHE_HOME": filepath.Join(home, "cache"), "OPENCODE_DB": "test.db",
		"OPENCODE_CONFIG": "", "OPENCODE_CONFIG_DIR": "", "OPENCODE_CONFIG_CONTENT": "", "BERTH_SESSION": "acme-opencode",
	} {
		t.Setenv(key, value)
		if value == "" {
			os.Unsetenv(key)
		}
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
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"acme\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Acme smoke reply\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"acme\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()
	t.Setenv("ACME_API_KEY", "synthetic")
	config := fmt.Sprintf(`{"model":"acme/model","enabled_providers":["acme"],"providers":{"acme":{"env":["ACME_API_KEY"],"package":"@opencode/ai/providers/openai-compatible","settings":{"baseURL":%q},"models":{"model":{"variants":[{"id":"high","settings":{}}]}}}}}`, model.URL+"/v1")
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), []byte(config), 0o600); err != nil {
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
	command = shellQuote(bin) + strings.TrimPrefix(command, "opencode")
	sess, err := s.create(context.Background(), "acme-opencode", "acme", project, command, "opencode", nil, nil)
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
	if err != nil || !strings.Contains(string(out), "session.created ") || !strings.Contains(string(out), "session.busy ") || !strings.Contains(string(out), "session.idle ") {
		t.Fatalf("real turn hooks: %q, %v", out, err)
	}
	var hook struct {
		ID string `json:"session_id"`
	}
	_, payload, _ := strings.Cut(strings.SplitN(string(out), "\n", 2)[0], " ")
	if err := json.Unmarshal([]byte(payload), &hook); err != nil || !openCodeID.MatchString(hook.ID) {
		t.Fatalf("session link: %s, %v", payload, err)
	}
	id := hook.ID
	out = api("session.message.list", "--param", "sessionID="+id, "--param", "limit=100", "--param", "order=desc")
	var page struct {
		Data []transcript.OpenCodeMessage `json:"data"`
	}
	if err := json.Unmarshal(out, &page); err != nil {
		t.Fatal(err)
	}
	live := transcript.OpenCode(id, project, page.Data, false)
	if len(live.Items) != 2 || live.Items[1].Text != "Acme smoke reply" {
		t.Fatalf("real model reply: %+v", live)
	}
	if err := s.Send(context.Background(), sess.Name, "Reply again", true); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the composer reply's idle hook", 20*time.Second, func() bool {
		out, _ := os.ReadFile(hooks)
		return strings.Count(string(out), "session.idle ") == 2
	})
	if err := s.Kill(context.Background(), sess.Name); err != nil {
		t.Fatal(err)
	}
	out = api("session.message.list", "--param", "sessionID="+id, "--param", "limit=100", "--param", "order=desc")
	if err := json.Unmarshal(out, &page); err != nil {
		t.Fatal(err)
	}
	live = transcript.OpenCode(id, project, page.Data, false)
	if len(live.Items) != 4 || live.Items[2].Text != "Reply again" || live.Items[3].Text != "Acme smoke reply" {
		t.Fatalf("reply after terminal exit: %+v", live)
	}
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
