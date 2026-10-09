package box

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type openCodeOwner struct {
	Name      string `json:"name"`
	Directory string `json:"directory"`
	SessionID string `json:"session_id"`
	Instance  string `json:"instance"`
}

type openCodeEndpoint struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Password string `json:"password"`
	PID      int    `json:"pid"`
	Version  string `json:"version"`
}

func openCodeNonce() string {
	var v [16]byte
	if _, err := rand.Read(v[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(v[:])
}

// OpenCode's public service registration is private to this task. Its password
// never leaves this file/API client, including on error paths.
func openCodeEndpointAt(root string) (openCodeEndpoint, error) {
	var ep openCodeEndpoint
	data, err := os.ReadFile(filepath.Join(root, "state", "opencode", "service.json"))
	if err != nil {
		return ep, errors.New("OpenCode runtime is not connected")
	}
	if len(data) > 16<<10 || json.Unmarshal(data, &ep) != nil {
		return ep, errors.New("invalid OpenCode runtime registration")
	}
	u, err := url.Parse(ep.URL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || ep.Password == "" || ep.ID == "" || ep.PID <= 0 {
		return openCodeEndpoint{}, errors.New("invalid private OpenCode endpoint")
	}
	return ep, nil
}

func (ep openCodeEndpoint) call(ctx context.Context, method, path string, body, out any) error {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	if len(raw) > 4<<20 {
		return badRequest("OpenCode request is too large")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, ep.URL+path, bytes.NewReader(raw))
	if err != nil {
		return errors.New("invalid OpenCode request")
	}
	req.SetBasicAuth("opencode", ep.Password)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return httpError{http.StatusServiceUnavailable, "OpenCode runtime disconnected; delivery may be uncertain, retry the same request"}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil || len(data) > 16<<20 {
		return errors.New("OpenCode response exceeded its budget or was interrupted")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not relay provider/config errors verbatim: they may contain secrets.
		return httpError{resp.StatusCode, fmt.Sprintf("OpenCode rejected the request (HTTP %d); refresh the conversation", resp.StatusCode)}
	}
	if out != nil && len(data) != 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("invalid OpenCode response")
		}
	}
	return nil
}

func (b *Box) openCodeRuntime(ctx context.Context, sess Session) (openCodeEndpoint, openCodeOwner, error) {
	var owner openCodeOwner
	root := b.Sessions.EnvVar(ctx, sess, "BERTH_OPENCODE_RUNTIME")
	base := filepath.Join(filepath.Dir(b.Sessions.Commands), "opencode")
	if root == "" || filepath.Dir(root) != base {
		return openCodeEndpoint{}, owner, httpError{http.StatusConflict, "This OpenCode terminal has no native runtime; start a new task"}
	}
	for _, p := range []string{base, root} {
		st, err := os.Lstat(p)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0o077 != 0 {
			return openCodeEndpoint{}, owner, errors.New("OpenCode runtime directory must be private")
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "owner.json"))
	directory, dirErr := filepath.EvalSymlinks(sess.Dir)
	if err != nil || dirErr != nil || len(data) > 16<<10 || json.Unmarshal(data, &owner) != nil || owner.Name != sess.Name || owner.Directory != directory || !openCodeID.MatchString(owner.SessionID) || owner.SessionID != b.Sessions.EnvVar(ctx, sess, "BERTH_OPENCODE_SESSION") {
		return openCodeEndpoint{}, owner, errors.New("OpenCode runtime identity does not match this task")
	}
	ep, err := openCodeEndpointAt(root)
	if err != nil || ep.ID != owner.Instance {
		return openCodeEndpoint{}, owner, errors.New("OpenCode runtime instance has ended or changed")
	}
	var health struct {
		PID int `json:"pid"`
	}
	if err := ep.call(ctx, "GET", "/api/info", nil, &health); err != nil {
		return ep, owner, err
	}
	if health.PID != ep.PID {
		return ep, owner, errors.New("OpenCode runtime PID does not match its registration")
	}
	return ep, owner, nil
}

// RunOpenCode runs in the task's tmux pane, not in the daemon. Native discovery
// uses a per-task XDG state directory; config, credentials and durable data keep
// their original locations. No service is installed or shared service changed.
func RunOpenCode(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "mini" {
		return errors.New("usage: berthd opencode mini [OpenCode Mini flags]")
	}
	root := os.Getenv("BERTH_OPENCODE_RUNTIME")
	name := os.Getenv("BERTH_SESSION")
	id := os.Getenv("BERTH_OPENCODE_SESSION")
	if !filepath.IsAbs(root) || !sessionName.MatchString(name) || !openCodeID.MatchString(id) {
		return errors.New("OpenCode launcher requires a Shipyard task identity")
	}
	bin := os.Getenv("BERTH_OPENCODE_BIN")
	if bin == "" {
		var err error
		bin, err = exec.LookPath("opencode")
		if err != nil {
			return err
		}
	}
	file, err := os.Open(bin)
	if err != nil {
		return err
	}
	var magic [2]byte
	_, err = io.ReadFull(file, magic[:])
	file.Close()
	if err != nil || string(magic[:]) == "#!" {
		return errors.New("native OpenCode requires an executable, not a shell wrapper; set BERTH_OPENCODE_BIN to the compatible binary's absolute path")
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(root), id+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errOpenCodeOwned
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	env := append(os.Environ(), "XDG_STATE_HOME="+filepath.Join(root, "state"))
	server := exec.Command(bin, "serve", "--service", "--hostname", "127.0.0.1", "--port", "0")
	server.Env = env
	server.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// serve prints its generated password. Never send that output to a pane,
	// log or error response; the private registration is the handoff.
	server.Stdout, server.Stderr = io.Discard, io.Discard
	if err := server.Start(); err != nil {
		return errors.New("could not start the private OpenCode server")
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Wait() }()
	defer func() {
		select {
		case <-serverDone:
			return
		default:
		}
		_ = syscall.Kill(-server.Process.Pid, syscall.SIGTERM)
		select {
		case <-serverDone:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-server.Process.Pid, syscall.SIGKILL)
			<-serverDone
		}
	}()
	var ep openCodeEndpoint
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		ep, err = openCodeEndpointAt(root)
		if err == nil && ep.PID == server.Process.Pid {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("OpenCode did not register in its private XDG directory; check BERTH_OPENCODE_BIN")
		case <-serverDone:
			// Put the result back so cleanup never signals a reaped PID.
			serverDone <- errors.New("exited")
			return errors.New("private OpenCode server exited during startup")
		case <-tick.C:
		}
	}
	owner := openCodeOwner{Name: name, Directory: dir, SessionID: id, Instance: ep.ID}
	if os.Getenv("BERTH_OPENCODE_RESUME") == "1" {
		var info struct {
			Data openCodeConversation `json:"data"`
		}
		if err := ep.call(ctx, "GET", "/api/session/"+id, nil, &info); err != nil {
			return err
		}
		if info.Data.Location.Directory != dir {
			return errors.New("OpenCode conversation belongs to another directory")
		}
	} else if err := ep.call(ctx, "POST", "/api/session", map[string]any{"id": id, "location": map[string]string{"directory": dir}}, nil); err != nil {
		return err
	}
	data, _ := json.Marshal(owner)
	if err := os.WriteFile(filepath.Join(root, "owner.json"), data, 0o600); err != nil {
		return err
	}
	miniArgs := []string{"mini", "--session", id}
	var initialFiles []openCodeFile
	if raw := os.Getenv("BERTH_OPENCODE_FILES"); raw != "" {
		if json.Unmarshal([]byte(raw), &initialFiles) != nil || len(initialFiles) > 16 {
			return badRequest("invalid native initial attachments")
		}
	}
	prompt, model, agent := "", "", ""
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--standalone" || arg == "--server" || arg == "--session" || arg == "-s" || arg == "--continue" || arg == "-c" || strings.HasPrefix(arg, "--server=") || strings.HasPrefix(arg, "--session=") {
			return errors.New("the native launcher owns OpenCode's server and session selection")
		}
		if len(initialFiles) > 0 && (arg == "--prompt" || arg == "--model" || arg == "--agent") {
			if i+1 == len(args) {
				return badRequest("missing OpenCode launch option value")
			}
			i++
			switch arg {
			case "--prompt":
				prompt = args[i]
			case "--model":
				model = args[i]
			case "--agent":
				agent = args[i]
			}
			continue
		}
		miniArgs = append(miniArgs, arg)
	}
	if len(initialFiles) > 0 {
		if len(prompt) > 64<<10 {
			return badRequest("OpenCode prompt exceeds its budget")
		}
		if agent != "" {
			if err := ep.call(ctx, "POST", "/api/session/"+id+"/agent", map[string]string{"agent": agent}, nil); err != nil {
				return err
			}
		}
		if model != "" {
			provider, rest, ok := strings.Cut(model, "/")
			if !ok {
				return badRequest("native initial media requires provider/model")
			}
			modelID, variant, _ := strings.Cut(rest, "#")
			ref := map[string]string{"providerID": provider, "id": modelID}
			if variant != "" {
				ref["variant"] = variant
			}
			if err := ep.call(ctx, "POST", "/api/session/"+id+"/model", map[string]any{"model": ref}, nil); err != nil {
				return err
			}
		}
		if err := openCodeFiles(ctx, ep, owner, initialFiles); err != nil {
			return err
		}
		if err := ep.call(ctx, "POST", "/api/session/"+id+"/prompt", map[string]any{"id": "msg_first_" + id, "text": prompt, "files": initialFiles, "delivery": "queue"}, nil); err != nil {
			return err
		}
	}
	mini := exec.CommandContext(ctx, bin, miniArgs...)
	mini.Env, mini.Stdin, mini.Stdout, mini.Stderr = env, in, out, stderr
	if err := mini.Start(); err != nil {
		return err
	}
	miniDone := make(chan error, 1)
	go func() { miniDone <- mini.Wait() }()
	select {
	case err := <-miniDone:
		return err
	case err := <-serverDone:
		serverDone <- err
		_ = mini.Process.Kill()
		<-miniDone
		return errors.New("the private OpenCode server stopped")
	}
}
