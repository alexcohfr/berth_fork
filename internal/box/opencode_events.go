package box

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cosscom/shipyard/internal/events"
	"github.com/cosscom/shipyard/internal/integrations/adapters"
)

type openCodeWatchState struct {
	mu   sync.Mutex
	seen time.Time
}

var openCodeWatches sync.Map

// One volatile subscription per useful owner. Only invalidations cross the
// journal; text is read through the authenticated transcript route. Every new
// subscription invalidates first, because OpenCode does not replay events.
func (b *Box) openCodeWatch(sess Session, ep openCodeEndpoint, owner openCodeOwner) {
	if b.Events == nil {
		return
	}
	key := struct {
		b  *Box
		id string
	}{b, ep.ID}
	now := time.Now()
	w := &openCodeWatchState{seen: now}
	got, loaded := openCodeWatches.LoadOrStore(key, w)
	if loaded {
		w = got.(*openCodeWatchState)
		w.mu.Lock()
		w.seen = now
		w.mu.Unlock()
		return
	}
	go func() {
		defer openCodeWatches.Delete(key)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			tick := time.NewTicker(time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					w.mu.Lock()
					stale := time.Since(w.seen) > watchFor
					w.mu.Unlock()
					if stale {
						cancel()
						return
					}
					current, linked, err := b.openCodeRuntime(ctx, sess)
					if err != nil || current.ID != ep.ID || linked.SessionID != owner.SessionID {
						cancel()
						return
					}
					_ = b.openCodeReconcile(ctx, sess, ep, owner)
				}
			}
		}()
		ping := func() {
			b.Events.Publish(events.Event{Type: events.TranscriptChanged, Box: b.Name, Data: map[string]any{"session": sess.Name, "instance": ep.ID}})
		}
		client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		defer client.CloseIdleConnections()
		for ctx.Err() == nil {
			req, _ := http.NewRequestWithContext(ctx, "GET", ep.URL+"/api/event", nil)
			req.SetBasicAuth("opencode", ep.Password)
			resp, err := client.Do(req)
			if err == nil {
				if resp.StatusCode == http.StatusOK {
					_ = b.openCodeReconcile(ctx, sess, ep, owner)
					ping()
					scanner := bufio.NewScanner(resp.Body)
					scanner.Buffer(make([]byte, 4096), 1<<20)
					var last time.Time
					for scanner.Scan() {
						line := scanner.Text()
						if !strings.HasPrefix(line, "data:") {
							continue
						}
						raw := []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
						var encoded string
						if json.Unmarshal(raw, &encoded) == nil {
							raw = []byte(encoded)
						}
						var event struct {
							Data struct {
								SessionID string `json:"sessionID"`
								Form      struct {
									SessionID string `json:"sessionID"`
								} `json:"form"`
							} `json:"data"`
						}
						if json.Unmarshal(raw, &event) != nil {
							continue
						}
						id := firstNonEmpty(event.Data.SessionID, event.Data.Form.SessionID)
						if id == owner.SessionID && time.Since(last) > 200*time.Millisecond {
							ping()
							last = time.Now()
						}
					}
				}
				resp.Body.Close()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
}

// Events are an accelerator. A new backend reconstructs turn state from the
// inbox and delivered native IDs instead of treating missing events as delivery.
func (b *Box) openCodeReconcile(ctx context.Context, sess Session, ep openCodeEndpoint, owner openCodeOwner) error {
	if b.Turns == nil || b.Events == nil {
		return nil
	}
	var active struct {
		Data map[string]any `json:"data"`
	}
	if err := ep.call(ctx, "GET", "/api/session/active", nil, &active); err != nil {
		return err
	}
	var inbox struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := ep.call(ctx, "GET", "/api/session/"+owner.SessionID+"/inbox", nil, &inbox); err != nil {
		return err
	}
	queued := map[string]bool{}
	for _, item := range inbox.Data {
		queued[item.ID] = true
	}
	permissions, forms, err := openCodePending(ctx, ep, owner.SessionID)
	if err != nil {
		return err
	}
	waiting := len(permissions)+len(forms) > 0
	if children, err := b.openCodeChildren(ctx, sess); err == nil {
		for _, c := range children {
			p, f, err := openCodePending(ctx, ep, c.ID)
			if err != nil {
				return err
			}
			waiting = waiting || len(p)+len(f) > 0
		}
	}
	running := active.Data[owner.SessionID] != nil
	delivered := map[string]bool{}
	for _, turn := range b.Turns.List(sess.Name, 100) {
		if !turn.open() || turn.NativeID == "" || queued[turn.NativeID] {
			continue
		}
		var message struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := ep.call(ctx, "GET", "/api/session/"+owner.SessionID+"/message/"+turn.NativeID, nil, &message); err == nil {
			delivered[turn.NativeID] = message.Data.ID == turn.NativeID
		}
	}
	t := b.Turns
	t.mu.Lock()
	t.init()
	s := t.track(sess.Name, "opencode", sess.Dir, sess.Created)
	for _, turn := range s.Turns {
		if turn.NativeID != "" && turn.open() && delivered[turn.NativeID] {
			if !running && !waiting {
				s.end(turn, "finished", events.Event{Time: time.Now().UTC(), Seq: t.applied})
			} else {
				turn.State = "running"
				if waiting {
					turn.State = "waiting"
				}
				if turn.Started.IsZero() {
					turn.Started = time.Now().UTC()
				}
				turn.Fidelity = "hooks"
			}
		}
	}
	old := s.State
	t.bump()
	t.mu.Unlock()
	state, event := "finished", adapters.Finished
	if running {
		state, event = "running", adapters.Started
	}
	if waiting {
		state, event = "waiting", adapters.Waiting
	}
	if old != state {
		b.Events.Publish(events.Event{Type: event, Box: b.Name, Origin: "opencode", Data: map[string]any{"session": sess.Name, "path": owner.Directory, "agent": "opencode", "agent_session_id": owner.SessionID, "source": "native", "signal": "tool", "reason": "native request"}})
	}
	return nil
}
