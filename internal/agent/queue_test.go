package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosscom/shipyard/internal/box"
	"github.com/cosscom/shipyard/internal/wire"
)

type sentPrompt struct{ box, session, text string }

func TestOpenCodeQueueRetainsPayloadAndIdentityAcrossRestart(t *testing.T) {
	boxes := newFakeQueueBoxes()
	boxes.setSessions("devl", box.Session{Name: "acme", Agent: "opencode"})
	r := newQueueRig(t, boxes, "")
	var native box.SendRequest
	if err := json.Unmarshal([]byte(`{"idem_key":"acme-native-id","when":"idle","files":[{"uri":"file:///acme/image%20%C3%A9.png"}],"skills":[{"id":"acme-skill"}]}`), &native); err != nil {
		t.Fatal(err)
	}
	req := QueueRequest{ID: "acme-queue-id", Box: "devl", Session: "acme", Text: "Acme instruction", Native: &native}
	it, err := r.q.add(req)
	if err != nil {
		t.Fatal(err)
	}
	wrong := req
	wrong.Text = "different"
	if _, err := r.q.add(wrong); err == nil {
		t.Fatal("idempotency key accepted a different payload")
	}
	r.start(time.Second, 10*time.Millisecond)
	boxes.onSend = func(n int, p sentPrompt) error {
		if n == 1 {
			return errors.New("ack lost")
		}
		return nil
	}
	boxes.setOnline("devl", true)
	if out, err := r.q.sendNow(it.ID); err != nil || out.State != QueueFailed {
		t.Fatalf("uncertain send: %+v %v", out, err)
	}
	r.start(time.Second, 10*time.Millisecond)
	changed := "another destination"
	if _, err := r.q.change(it.ID, QueueChange{Text: &changed}); err == nil {
		t.Fatal("an attempted native payload was rewritten")
	}
	if _, err := r.q.sendNow(it.ID); err != nil {
		t.Fatal(err)
	}
	boxes.mu.Lock()
	defer boxes.mu.Unlock()
	if len(boxes.requests) != 2 {
		t.Fatalf("requests: %d", len(boxes.requests))
	}
	for _, got := range boxes.requests {
		if got.IdemKey != "acme-native-id" || got.When != "idle" || got.Text != req.Text || len(got.Files) != 1 || got.Files[0].URI != native.Files[0].URI || len(got.Skills) != 1 || got.Skills[0].ID != "acme-skill" || got.Force {
			t.Fatalf("native delivery changed: %+v", got)
		}
	}
}

// fakeQueueBoxes stands in for the boxes: which are online, what sessions
// they run, and what happens to a send or a wait.
type fakeQueueBoxes struct {
	mu       sync.Mutex
	up       map[string]bool
	sessions map[string][]box.Session
	sent     []sentPrompt
	requests []box.SendRequest
	// onSend, when set, decides a send's outcome; it runs before the send
	// is recorded, and only a nil error records it.
	onSend func(n int, p sentPrompt) error
	sends  int
	onWait func(n int) (box.WaitResult, error)
	waits  int
}

func newFakeQueueBoxes() *fakeQueueBoxes {
	return &fakeQueueBoxes{up: map[string]bool{}, sessions: map[string][]box.Session{}}
}

func (f *fakeQueueBoxes) known(name string) bool { return name == "devl" || name == "gpu" }

func (f *fakeQueueBoxes) online(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.up[name]
}

func (f *fakeQueueBoxes) setOnline(name string, on bool) {
	f.mu.Lock()
	f.up[name] = on
	f.mu.Unlock()
}

func (f *fakeQueueBoxes) setSessions(name string, s ...box.Session) {
	f.mu.Lock()
	f.sessions[name] = s
	f.mu.Unlock()
}

func (f *fakeQueueBoxes) sessionsFor(name string) []box.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]box.Session(nil), f.sessions[name]...)
}

func (f *fakeQueueBoxes) sessions_(ctx context.Context, name string) ([]box.Session, error) {
	if !f.online(name) {
		return nil, &unsentError{errors.New("dial tcp: connection refused")}
	}
	return f.sessionsFor(name), nil
}

func (f *fakeQueueBoxes) wait(ctx context.Context, name, session string, states []string, timeout time.Duration) (box.WaitResult, error) {
	f.mu.Lock()
	f.waits++
	n, fn := f.waits, f.onWait
	f.mu.Unlock()
	if fn != nil {
		return fn(n)
	}
	return box.WaitResult{State: "finished"}, nil
}

func (f *fakeQueueBoxes) send(ctx context.Context, name, session string, req box.SendRequest) error {
	p := sentPrompt{name, session, req.Text}
	f.mu.Lock()
	f.sends++
	f.requests = append(f.requests, req)
	n, fn := f.sends, f.onSend
	f.mu.Unlock()
	if !f.online(name) {
		return &unsentError{errors.New("dial tcp: connection refused")}
	}
	if fn != nil {
		if err := fn(n, p); err != nil {
			return err
		}
	}
	f.mu.Lock()
	f.sent = append(f.sent, p)
	f.mu.Unlock()
	return nil
}

func (f *fakeQueueBoxes) sentSoFar() []sentPrompt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentPrompt(nil), f.sent...)
}

// queueBoxesAdapter gives fakeQueueBoxes the method name the interface
// wants without clashing with its sessions field.
type queueBoxesAdapter struct{ *fakeQueueBoxes }

func (a queueBoxesAdapter) sessions(ctx context.Context, name string) ([]box.Session, error) {
	return a.fakeQueueBoxes.sessions_(ctx, name)
}

type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func (l *eventLog) publish(e Event) {
	l.mu.Lock()
	l.events = append(l.events, e)
	l.mu.Unlock()
}

func (l *eventLog) count(typ string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func (l *eventLog) all() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Event(nil), l.events...)
}

type queueRig struct {
	t      *testing.T
	path   string
	boxes  *fakeQueueBoxes
	events *eventLog
	q      *promptQueue
	cancel context.CancelFunc
}

func newQueueRig(t *testing.T, boxes *fakeQueueBoxes, path string) *queueRig {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), "queue.json")
	}
	r := &queueRig{t: t, path: path, boxes: boxes, events: &eventLog{}}
	r.start(50*time.Millisecond, 10*time.Millisecond)
	return r
}

// start (re)opens the queue from its file, as a restarted agent does.
func (r *queueRig) start(idle, step time.Duration) {
	r.stop()
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.q = newPromptQueue(ctx, r.path, queueBoxesAdapter{r.boxes}, r.events.publish, func() time.Time { return time.Now().Round(0) }, r.t.Logf, idle, step)
	r.t.Cleanup(r.stop)
}

func (r *queueRig) stop() {
	if r.cancel != nil {
		r.cancel()
		r.q.sends.Wait()
		r.cancel = nil
	}
}

func (r *queueRig) add(session, text string) QueueItem {
	r.t.Helper()
	it, err := r.q.add(QueueRequest{Box: "devl", Session: session, Text: text})
	if err != nil {
		r.t.Fatal(err)
	}
	return it
}

func (r *queueRig) item(id string) (QueueItem, bool) {
	for _, it := range r.q.list() {
		if it.ID == id {
			return it, true
		}
	}
	return QueueItem{}, false
}

func agentSession(name string) box.Session {
	return box.Session{Name: name, Agent: "claude", AgentState: "idle", StateSince: time.Now()}
}

func TestQueuedPromptsSurviveARestartAndGoInOrderWhenTheBoxIsBack(t *testing.T) {
	boxes := newFakeQueueBoxes()
	boxes.setSessions("devl", agentSession("billing"), agentSession("docs"))
	r := newQueueRig(t, boxes, "")
	r.add("billing", "first")
	r.add("docs", "for docs")
	r.add("billing", "second")
	r.q.kickAll()
	if len(boxes.sentSoFar()) != 0 {
		t.Fatal("sent to a box that is offline")
	}

	// The laptop agent restarts while the box is away.
	r.start(time.Second, 10*time.Millisecond)
	list := r.q.list()
	if len(list) != 3 || list[0].Text != "first" || list[2].Text != "second" {
		t.Fatalf("after a restart the queue is %+v", list)
	}
	for _, it := range list {
		if it.State != QueueQueued {
			t.Fatalf("%s is %s after a restart, want queued", it.ID, it.State)
		}
	}

	boxes.setOnline("devl", true)
	r.q.kick("devl")
	eventually(t, "every prompt delivered", func() bool { return len(r.q.list()) == 0 })
	var billing []string
	for _, p := range boxes.sentSoFar() {
		if p.session == "billing" {
			billing = append(billing, p.text)
		}
	}
	if strings.Join(billing, ",") != "first,second" {
		t.Fatalf("billing got %v, want first then second", billing)
	}
	if n := len(boxes.sentSoFar()); n != 3 {
		t.Fatalf("%d sends, want 3", n)
	}
	if n := r.events.count(EventQueueDelivered); n != 3 {
		t.Fatalf("%d queue.delivered events, want 3", n)
	}
	// Prompts stay out of events.
	for _, e := range r.events.all() {
		b, _ := json.Marshal(e)
		if bytes.Contains(b, []byte("first")) || bytes.Contains(b, []byte("for docs")) {
			t.Fatalf("an event carries the prompt: %s", b)
		}
	}
	// Nothing goes twice when the box comes back again.
	r.q.kickAll()
	time.Sleep(50 * time.Millisecond)
	if n := len(boxes.sentSoFar()); n != 3 {
		t.Fatalf("%d sends after another kick, want 3", n)
	}
}

func TestAPromptCaughtMidSendIsNeverSentAgainAutomatically(t *testing.T) {
	boxes := newFakeQueueBoxes()
	boxes.setSessions("devl", agentSession("billing"))
	r := newQueueRig(t, boxes, "")
	// The agent "crashes" between saving the item as sending and the box's
	// answer: keep the file exactly as it was at that moment.
	var atSend []byte
	boxes.onSend = func(n int, p sentPrompt) error {
		b, err := os.ReadFile(r.path)
		if err != nil {
			t.Error(err)
		}
		atSend = b
		return nil
	}
	it := r.add("billing", "migrate the bookings table")
	boxes.setOnline("devl", true)
	r.q.kick("devl")
	eventually(t, "delivered", func() bool { return len(r.q.list()) == 0 })
	var saved []QueueItem
	if err := json.Unmarshal(atSend, &saved); err != nil || len(saved) != 1 || saved[0].State != QueueSending {
		t.Fatalf("the item was not saved as sending before the send: %s", atSend)
	}

	// Restart from that file: the prompt may have arrived, so it fails.
	r.stop()
	if err := os.WriteFile(r.path, atSend, 0o600); err != nil {
		t.Fatal(err)
	}
	boxes.onSend = nil
	r.start(time.Second, 10*time.Millisecond)
	got, ok := r.item(it.ID)
	if !ok || got.State != QueueFailed || !strings.Contains(got.Error, "may have arrived") {
		t.Fatalf("after a crash mid-send the item is %+v", got)
	}
	if r.events.count(EventQueueFailed) != 1 {
		t.Fatal("no queue.failed event for the interrupted send")
	}
	r.q.kickAll()
	time.Sleep(50 * time.Millisecond)
	if n := len(boxes.sentSoFar()); n != 1 {
		t.Fatalf("%d sends; an interrupted prompt went again on its own", n)
	}

	// The person looks, and retries: then it goes again, once.
	if _, err := r.q.change(it.ID, QueueChange{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "retried", func() bool { return len(r.q.list()) == 0 })
	if n := len(boxes.sentSoFar()); n != 2 {
		t.Fatalf("%d sends after a retry, want 2", n)
	}
}

func TestAPromptThatNeverLeftWaitsAndOneThatMayHaveArrivedFails(t *testing.T) {
	boxes := newFakeQueueBoxes()
	boxes.setSessions("devl", agentSession("billing"))
	boxes.setOnline("devl", true)
	r := newQueueRig(t, boxes, "")

	// The connection could not be opened: back in line, not failed.
	boxes.onSend = func(n int, p sentPrompt) error {
		return &unsentError{errors.New("dial tcp: connection refused")}
	}
	a := r.add("billing", "one")
	eventually(t, "tried", func() bool {
		boxes.mu.Lock()
		defer boxes.mu.Unlock()
		return boxes.sends >= 1
	})
	r.q.sends.Wait()
	if got, _ := r.item(a.ID); got.State != QueueQueued || got.Attempts != 1 {
		t.Fatalf("an unsent prompt is %+v, want queued after 1 attempt", got)
	}

	// The connection dropped after the request went: it may have arrived.
	boxes.onSend = func(n int, p sentPrompt) error { return errors.New("http2: client connection lost") }
	r.q.kick("devl")
	eventually(t, "failed", func() bool { got, _ := r.item(a.ID); return got.State == QueueFailed })
	if got, _ := r.item(a.ID); !strings.Contains(got.Error, "may have arrived") {
		t.Fatalf("reason = %q", got.Error)
	}

	// A box that refuses it (a before: hook, say) fails it with its reason.
	boxes.onSend = func(n int, p sentPrompt) error {
		return &refusedError{status: http.StatusBadRequest, msg: "blocked by before:session.send hook"}
	}
	if _, err := r.q.remove(a.ID); err != nil {
		t.Fatal(err)
	}
	b := r.add("billing", "two")
	eventually(t, "refused", func() bool { got, _ := r.item(b.ID); return got.State == QueueFailed })
	if got, _ := r.item(b.ID); !strings.Contains(got.Error, "blocked by before:session.send hook") {
		t.Fatalf("reason = %q", got.Error)
	}
	if len(boxes.sentSoFar()) != 0 {
		t.Fatal("recorded a send that failed")
	}
}

func TestAPromptForAGoneSessionFailsHoldsItsLineAndCanBeRetargeted(t *testing.T) {
	boxes := newFakeQueueBoxes()
	boxes.setSessions("devl", agentSession("docs"))
	r := newQueueRig(t, boxes, "")
	first := r.add("billing", "first")
	second := r.add("billing", "second")
	boxes.setOnline("devl", true)
	r.q.kick("devl")
	eventually(t, "first failed", func() bool { got, _ := r.item(first.ID); return got.State == QueueFailed })
	got, _ := r.item(first.ID)
	if !strings.Contains(got.Error, "no longer running") {
		t.Fatalf("reason = %q", got.Error)
	}
	time.Sleep(30 * time.Millisecond)
	held, _ := r.item(second.ID)
	if held.State != QueueQueued || !held.Blocked {
		t.Fatalf("the prompt behind a failed one is %+v, want queued and blocked", held)
	}
	if len(boxes.sentSoFar()) != 0 {
		t.Fatal("sent something to a session that is gone")
	}

	// Retarget the failed one to a session that exists.
	docs := "docs"
	if _, err := r.q.change(first.ID, QueueChange{Session: &docs}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "retargeted prompt delivered", func() bool { _, ok := r.item(first.ID); return !ok })
	if s := boxes.sentSoFar(); len(s) != 1 || s[0].session != "docs" || s[0].text != "first" {
		t.Fatalf("sent %+v", s)
	}
	// The one behind it now reaches the head of its line, and fails too.
	eventually(t, "second failed", func() bool { got, _ := r.item(second.ID); return got.State == QueueFailed })
	if _, err := r.q.remove(second.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.q.list()) != 0 {
		t.Fatal("discarded prompt is still queued")
	}
}

func TestAPromptWaitsForABusyAgentsTurnToEnd(t *testing.T) {
	boxes := newFakeQueueBoxes()
	busyAgent := agentSession("billing")
	busyAgent.AgentState = "running"
	boxes.setSessions("devl", busyAgent)
	boxes.setOnline("devl", true)
	r := newQueueRig(t, boxes, "")
	r.start(time.Minute, 10*time.Millisecond)

	release := make(chan struct{})
	boxes.onWait = func(n int) (box.WaitResult, error) {
		select {
		case <-release:
			return box.WaitResult{State: "finished"}, nil
		case <-time.After(5 * time.Millisecond):
			return box.WaitResult{State: "running", TimedOut: true}, nil
		}
	}
	it := r.add("billing", "after your turn")
	eventually(t, "waiting", func() bool { got, _ := r.item(it.ID); return got.State == QueueWaiting })
	time.Sleep(30 * time.Millisecond)
	if len(boxes.sentSoFar()) != 0 {
		t.Fatal("typed into an agent mid-turn")
	}
	close(release)
	eventually(t, "delivered after the turn", func() bool { return len(r.q.list()) == 0 })

	// Past the idle timeout it goes anyway: agents take input mid-turn.
	r.start(40*time.Millisecond, 10*time.Millisecond)
	boxes.onWait = func(n int) (box.WaitResult, error) {
		time.Sleep(5 * time.Millisecond)
		return box.WaitResult{State: "running", TimedOut: true}, nil
	}
	r.add("billing", "eventually")
	eventually(t, "delivered after the idle timeout", func() bool { return len(r.q.list()) == 0 })

	// wait: false sends straight away, and so does an agent that never
	// reported a state (its hooks are not installed).
	boxes.onWait = func(n int) (box.WaitResult, error) {
		t.Error("waited when it should not have")
		return box.WaitResult{}, nil
	}
	no := false
	if _, err := r.q.add(QueueRequest{Box: "devl", Session: "billing", Text: "now", Wait: &no}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "sent without waiting", func() bool { return len(r.q.list()) == 0 })
	unreported := busyAgent
	unreported.StateSince = time.Time{}
	boxes.setSessions("devl", unreported)
	r.add("billing", "unknown state")
	eventually(t, "sent to an agent of unknown state", func() bool { return len(r.q.list()) == 0 })
	if n := len(boxes.sentSoFar()); n != 4 {
		t.Fatalf("%d sends, want 4", n)
	}
}

func TestDiscardingAWaitingPromptStopsIt(t *testing.T) {
	boxes := newFakeQueueBoxes()
	busyAgent := agentSession("billing")
	busyAgent.AgentState = "running"
	boxes.setSessions("devl", busyAgent)
	boxes.setOnline("devl", true)
	r := newQueueRig(t, boxes, "")
	r.start(time.Minute, 10*time.Millisecond)
	boxes.onWait = func(n int) (box.WaitResult, error) {
		time.Sleep(5 * time.Millisecond)
		return box.WaitResult{State: "running", TimedOut: true}, nil
	}
	it := r.add("billing", "never mind")
	eventually(t, "waiting", func() bool { got, _ := r.item(it.ID); return got.State == QueueWaiting })
	if _, err := r.q.remove(it.ID); err != nil {
		t.Fatal(err)
	}
	boxes.onWait = nil // the turn ends
	time.Sleep(50 * time.Millisecond)
	if len(boxes.sentSoFar()) != 0 {
		t.Fatal("sent a prompt that was discarded while it waited")
	}
}

func TestEnqueueIsIdempotentAndSendNowNeedsTheBox(t *testing.T) {
	boxes := newFakeQueueBoxes()
	boxes.setSessions("devl", agentSession("billing"))
	r := newQueueRig(t, boxes, "")
	a, err := r.q.add(QueueRequest{ID: "app-1", Box: "devl", Session: "billing", Text: "once"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.q.add(QueueRequest{ID: "app-1", Box: "devl", Session: "billing", Text: "once"})
	if err != nil || b.Seq != a.Seq || len(r.q.list()) != 1 {
		t.Fatalf("enqueueing the same id twice made %d items", len(r.q.list()))
	}
	for _, bad := range []QueueRequest{
		{Box: "nope", Session: "billing", Text: "x"},
		{Box: "devl", Session: "../etc", Text: "x"},
		{Box: "devl", Session: "billing", Text: ""},
		{Box: "devl", Session: "billing", Text: strings.Repeat("x", maxQueuedText+1)},
	} {
		if _, err := r.q.add(bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if _, err := r.q.sendNow("app-1"); !errors.Is(err, errQueueBoxOffline) {
		t.Fatalf("send now to an offline box: %v", err)
	}
	if _, err := r.q.sendNow("missing"); !errors.Is(err, errUnknownQueueItem) {
		t.Fatalf("send now of a missing item: %v", err)
	}
	// Online, send now goes at once and reports it delivered.
	boxes.setOnline("devl", true)
	got, err := r.q.sendNow("app-1")
	if err != nil || got.State != QueueDelivered {
		t.Fatalf("send now = %+v, %v", got, err)
	}
	if n := len(boxes.sentSoFar()); n != 1 {
		t.Fatalf("%d sends, want 1", n)
	}
}

func TestAnUnreadableQueueIsSetAsideNotFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	r := newQueueRig(t, newFakeQueueBoxes(), path)
	if len(r.q.list()) != 0 {
		t.Fatal("expected an empty queue")
	}
	if _, err := os.Stat(path + ".unreadable"); err != nil {
		t.Fatalf("the unreadable queue was not kept aside: %v", err)
	}
}

// End to end: a real agent and box, through the API the app and CLI use.
func TestTheAgentDeliversAQueuedPromptWhenTheBoxComesBack(t *testing.T) {
	var mu sync.Mutex
	var got []string
	b := newBoxWith(t, func(s *wire.Server) {
		s.Handle("GET /v1/sessions", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode([]box.Session{agentSession("billing")})
		}))
		s.Handle("POST /v1/sessions/{name}/send", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Text  string `json:"text"`
				Enter bool   `json:"enter"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if r.PathValue("name") != "billing" {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"error":"no session with that name"}`)
				return
			}
			mu.Lock()
			got = append(got, req.Text)
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"sent": true})
		}))
	})
	dir := b.pairLaptop()
	a := startAgent(t, dir)
	eventually(t, "box online", func() bool { return stateOf(t, a) == StateOnline })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var emu sync.Mutex
	var seen []string
	go a.client.Events(ctx, func(e Event) {
		emu.Lock()
		seen = append(seen, e.Type)
		emu.Unlock()
	})
	saw := func(typ string) bool {
		emu.Lock()
		defer emu.Unlock()
		for _, s := range seen {
			if s == typ {
				return true
			}
		}
		return false
	}
	time.Sleep(50 * time.Millisecond)

	addr := b.address
	b.stop()
	eventually(t, "box offline", func() bool { return stateOf(t, a) == StateOffline })

	// While it is away the app's send gets a 503: nothing reached the box.
	tok := uiToken(t, a)
	resp, body := uiCall(t, a, http.MethodGet, "/v1/boxes/devbox/api/sessions", tok)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a call to an offline box answered %d %s, want 503", resp.StatusCode, body)
	}

	var item QueueItem
	if err := a.client.Call(ctx, http.MethodPost, "/v1/queue", map[string]any{"box": "devbox", "session": "billing", "text": "rebase on main"}, &item); err != nil {
		t.Fatal(err)
	}
	var gone QueueItem
	if err := a.client.Call(ctx, http.MethodPost, "/v1/queue", map[string]any{"box": "devbox", "session": "nope", "text": "lost"}, &gone); err != nil {
		t.Fatal(err)
	}
	if err := a.client.Call(ctx, http.MethodPost, "/v1/queue/"+item.ID+"/send", nil, nil); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("send now to an offline box: %v", err)
	}

	// The agent restarts too, for good measure, then the box comes back.
	a.stop()
	a = startAgent(t, dir)
	go a.client.Events(ctx, func(e Event) {
		emu.Lock()
		seen = append(seen, e.Type)
		emu.Unlock()
	})
	b.start(addr)
	eventually(t, "delivered", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	})
	if got[0] != "rebase on main" {
		t.Fatalf("box got %q", got[0])
	}
	eventually(t, "queue.delivered", func() bool { return saw(EventQueueDelivered) })
	var list []QueueItem
	eventually(t, "the gone session's prompt failed", func() bool {
		if err := a.client.Call(ctx, http.MethodGet, "/v1/queue", nil, &list); err != nil {
			return false
		}
		return len(list) == 1 && list[0].ID == gone.ID && list[0].State == QueueFailed
	})
	if err := a.client.Call(ctx, http.MethodDelete, "/v1/queue/"+gone.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.client.Call(ctx, http.MethodDelete, "/v1/queue/"+gone.ID, nil, nil); err == nil {
		t.Fatal("deleting a missing item succeeded")
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("the box got %d prompts, want 1", len(got))
	}
}
