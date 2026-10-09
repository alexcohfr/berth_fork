package agent

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
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cosscom/shipyard/internal/box"
	"github.com/cosscom/shipyard/internal/statefile"
	"github.com/cosscom/shipyard/internal/wire"
)

// The offline prompt queue. Boxes drop off — the laptop sleeps, Wi-Fi goes,
// a box restarts — and a prompt typed for an agent on one should not be
// lost when they do. The app or the CLI hands such a prompt to the agent,
// which keeps it in queue.json under its state directory and types it into
// the session once the box is back, oldest first for each session.
//
// Delivery is at most once. An item is marked "sending" and saved before
// the request goes out; if the agent stops, or the connection drops, before
// the box answers, nobody can tell whether the prompt arrived, so the item
// becomes "failed" with that reason instead of being sent again. A send
// that never left the laptop (the box could not be reached at all) goes
// back to "queued". Typing a prompt twice into an agent is worse than
// asking the person to look and press Retry.

// Queue item states. Delivered items leave the queue; "delivered" only
// appears in answers and events.
const (
	QueueQueued    = "queued"
	QueueWaiting   = "waiting"
	QueueSending   = "sending"
	QueueFailed    = "failed"
	QueueDelivered = "delivered"
)

const (
	EventQueueChanged   = "queue.changed"
	EventQueueDelivered = "queue.delivered"
	EventQueueFailed    = "queue.failed"
)

const (
	// The box takes at most 64 KB of request body for a send.
	maxQueuedText  = 60 << 10
	maxQueueItems  = 500
	queueOrigin    = "queue"
	queueSendLimit = time.Minute
	queueListLimit = 30 * time.Second
	// How long a queued prompt waits for a busy agent's turn to end before
	// it is typed anyway; agents take input mid-turn, they just see it later.
	defaultQueueIdleTimeout = 30 * time.Minute
	// One long poll of the box's wait, as the CLI's waitFor does.
	defaultQueueWaitStep = 5 * time.Minute
)

var (
	errUnknownQueueItem = errors.New("no queued prompt with that id")
	errQueueItemBusy    = errors.New("that prompt is being sent right now")
	errQueueBoxOffline  = errors.New("box is offline")
	queueID             = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	queueSessionName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)
)

// QueueItem is one prompt waiting for its box.
type QueueItem struct {
	Native  *box.SendRequest `json:"native,omitempty"`
	ID      string           `json:"id"`
	Box     string           `json:"box"`
	Session string           `json:"session"`
	Text    string           `json:"text"`
	// Enter presses Enter after the text, as a send does by default.
	Enter bool `json:"enter"`
	// Wait holds the prompt while the agent is mid-turn (state running),
	// until its turn ends or the idle timeout passes.
	Wait  bool   `json:"wait"`
	State string `json:"state"`
	// Error says why a failed item failed.
	Error       string    `json:"error,omitempty"`
	Created     time.Time `json:"created"`
	Attempts    int       `json:"attempts,omitempty"`
	LastAttempt time.Time `json:"last_attempt,omitzero"`
	Delivered   time.Time `json:"delivered,omitzero"`
	// Blocked is set in listings for an item held behind an earlier failed
	// one to the same session: the line only moves in order.
	Blocked bool  `json:"blocked,omitempty"`
	Seq     int64 `json:"seq"`
}

// QueueRequest adds a prompt. ID is optional: sending the same ID again
// returns the item already queued, so a client may retry an enqueue.
type QueueRequest struct {
	Native  *box.SendRequest `json:"native,omitempty"`
	ID      string           `json:"id,omitempty"`
	Box     string           `json:"box"`
	Session string           `json:"session"`
	Text    string           `json:"text"`
	Wait    *bool            `json:"wait,omitempty"`
	Enter   *bool            `json:"enter,omitempty"`
}

// QueueChange retargets or edits a queued or failed prompt; it goes back in
// the line as queued.
type QueueChange struct {
	Box     *string `json:"box,omitempty"`
	Session *string `json:"session,omitempty"`
	Text    *string `json:"text,omitempty"`
}

// queueBoxes is what delivery needs from the boxes: the agent's own
// connections in practice, a fake in tests.
type queueBoxes interface {
	known(box string) bool
	online(box string) bool
	sessions(ctx context.Context, box string) ([]box.Session, error)
	wait(ctx context.Context, box, session string, states []string, timeout time.Duration) (box.WaitResult, error)
	send(ctx context.Context, box, session string, req box.SendRequest) error
}

// unsentError is a request that never reached the box.
type unsentError struct{ err error }

func (e *unsentError) Error() string { return e.err.Error() }
func (e *unsentError) Unwrap() error { return e.err }

// refusedError is the box's own answer to a request it did not carry out.
type refusedError struct {
	status int
	msg    string
}

func (e *refusedError) Error() string { return e.msg }

// queueOfflineError is a send-now for a box that is not online.
type queueOfflineError struct{ box string }

func (e *queueOfflineError) Error() string {
	return e.box + " is offline; the prompt stays queued until it is back"
}
func (e *queueOfflineError) Is(target error) bool { return target == errQueueBoxOffline }

func isUnsent(err error) bool {
	var u *unsentError
	return errors.As(err, &u)
}

func refusedWith(err error, status int) bool {
	var r *refusedError
	return errors.As(err, &r) && r.status == status
}

type promptQueue struct {
	path        string
	boxes       queueBoxes
	publish     func(Event)
	now         func() time.Time
	logf        func(format string, args ...any)
	idleTimeout time.Duration
	waitStep    time.Duration
	ctx         context.Context

	mu      sync.Mutex
	items   []QueueItem
	seq     int64
	workers map[string]bool
	// sends counts goroutines delivering, so tests can wait for quiet.
	sends sync.WaitGroup
	// held stops sends for a restart, which waits for those under way
	// (sending) first (hold).
	held    bool
	sending sync.WaitGroup
}

func newPromptQueue(ctx context.Context, path string, boxes queueBoxes, publish func(Event), now func() time.Time, logf func(string, ...any), idleTimeout, waitStep time.Duration) *promptQueue {
	q := &promptQueue{path: path, boxes: boxes, publish: publish, now: now, logf: logf, idleTimeout: idleTimeout, waitStep: waitStep, ctx: ctx}
	if q.idleTimeout <= 0 {
		q.idleTimeout = defaultQueueIdleTimeout
	}
	if q.waitStep <= 0 {
		q.waitStep = defaultQueueWaitStep
	}
	q.load()
	return q
}

// load reads the saved queue. An item saved as "sending" was interrupted
// between being marked and the box's answer: it may have arrived, so it
// fails rather than going out again. An item that was waiting for an agent
// had not been sent and is simply queued again.
func (q *promptQueue) load() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.workers = map[string]bool{}
	b, err := os.ReadFile(q.path)
	if err != nil {
		if !os.IsNotExist(err) {
			q.logf("reading the prompt queue: %v", err)
		}
		return
	}
	var items []QueueItem
	if err := json.Unmarshal(b, &items); err != nil {
		aside := q.path + ".unreadable"
		os.Rename(q.path, aside)
		q.logf("the prompt queue was unreadable and was moved to %s: %v", aside, err)
		return
	}
	var interrupted []QueueItem
	for i := range items {
		it := &items[i]
		q.seq = max(q.seq, it.Seq)
		switch it.State {
		case QueueSending:
			it.State = QueueFailed
			it.Error = "Shipyard stopped while sending this, so it may have arrived. Check the session, then retry or discard it."
			interrupted = append(interrupted, *it)
		case QueueWaiting, "":
			it.State = QueueQueued
		}
	}
	q.items = items
	q.sort()
	if len(interrupted) > 0 {
		q.saveLocked()
		for _, it := range interrupted {
			q.publishFailed(it)
		}
	}
}

func (q *promptQueue) sort() {
	sort.SliceStable(q.items, func(i, j int) bool { return q.items[i].Seq < q.items[j].Seq })
}

func (q *promptQueue) saveLocked() error {
	b, err := json.MarshalIndent(q.items, "", "  ")
	if err != nil {
		return err
	}
	if err := statefile.Write(q.path, append(b, '\n')); err != nil {
		q.logf("saving the prompt queue: %v", err)
		return err
	}
	return nil
}

func (q *promptQueue) indexLocked(id string) int {
	for i := range q.items {
		if q.items[i].ID == id {
			return i
		}
	}
	return -1
}

// changedLocked saves the queue and tells listeners. A failed save is
// returned: callers that are about to send must not send unsaved.
func (q *promptQueue) changedLocked() error {
	err := q.saveLocked()
	queued, failed := 0, 0
	for _, it := range q.items {
		if it.State == QueueFailed {
			failed++
		} else {
			queued++
		}
	}
	q.publish(Event{Type: EventQueueChanged, Data: map[string]any{"queued": queued, "failed": failed}})
	return err
}

// Prompts never go into events, only where they were going.
func (q *promptQueue) publishFailed(it QueueItem) {
	q.publish(Event{Type: EventQueueFailed, Box: it.Box, Error: it.Error, Data: map[string]any{"id": it.ID, "box": it.Box, "session": it.Session, "reason": it.Error}})
}

func (q *promptQueue) list() []QueueItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]QueueItem, len(q.items))
	copy(out, q.items)
	failed := map[string]bool{}
	for i := range out {
		k := out[i].Box + "\x00" + out[i].Session
		if out[i].State == QueueFailed {
			failed[k] = true
		} else if failed[k] {
			out[i].Blocked = true
		}
	}
	return out
}

func newQueueID() string {
	var b [6]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (q *promptQueue) add(req QueueRequest) (QueueItem, error) {
	if req.ID != "" && !queueID.MatchString(req.ID) {
		return QueueItem{}, errors.New("id must be letters, digits, - or _")
	}
	if !queueSessionName.MatchString(req.Session) {
		return QueueItem{}, fmt.Errorf("%q is not a session name", req.Session)
	}
	if req.Text == "" && (req.Native == nil || len(req.Native.Files)+len(req.Native.Skills) == 0) {
		return QueueItem{}, errors.New("nothing to send")
	}
	if len(req.Text) > maxQueuedText {
		return QueueItem{}, fmt.Errorf("the prompt is longer than %d KB", maxQueuedText>>10)
	}
	if req.Native != nil && (len(req.Native.IdemKey) > 256 || len(req.Native.Files) > 16 || len(req.Native.Skills) > 16) {
		return QueueItem{}, errors.New("native prompt exceeds its budget")
	}
	if !q.boxes.known(req.Box) {
		return QueueItem{}, fmt.Errorf("no paired box named %q", req.Box)
	}
	q.mu.Lock()
	if req.ID != "" {
		if i := q.indexLocked(req.ID); i >= 0 {
			it := q.items[i]
			q.mu.Unlock()
			if (it.Native != nil || req.Native != nil) && (it.Box != req.Box || it.Session != req.Session || it.Text != req.Text || !reflect.DeepEqual(it.Native, req.Native)) {
				return QueueItem{}, errors.New("request ID belongs to a different native prompt")
			}
			return it, nil
		}
	}
	if len(q.items) >= maxQueueItems {
		q.mu.Unlock()
		return QueueItem{}, fmt.Errorf("the queue already holds %d prompts", maxQueueItems)
	}
	q.seq++
	it := QueueItem{
		Native:  req.Native,
		ID:      req.ID,
		Box:     req.Box,
		Session: req.Session,
		Text:    req.Text,
		Enter:   req.Enter == nil || *req.Enter,
		Wait:    req.Wait == nil || *req.Wait,
		State:   QueueQueued,
		Created: q.now(),
		Seq:     q.seq,
	}
	if it.ID == "" {
		it.ID = newQueueID()
	}
	q.items = append(q.items, it)
	if err := q.changedLocked(); err != nil {
		q.items = q.items[:len(q.items)-1]
		q.mu.Unlock()
		return QueueItem{}, err
	}
	q.mu.Unlock()
	q.kick(it.Box)
	return it, nil
}

func (q *promptQueue) remove(id string) (QueueItem, error) {
	q.mu.Lock()
	i := q.indexLocked(id)
	if i < 0 {
		q.mu.Unlock()
		return QueueItem{}, errUnknownQueueItem
	}
	it := q.items[i]
	if it.State == QueueSending {
		q.mu.Unlock()
		return QueueItem{}, errQueueItemBusy
	}
	q.items = append(q.items[:i], q.items[i+1:]...)
	err := q.changedLocked()
	q.mu.Unlock()
	// The next prompt to that session may have been held behind this one.
	q.kick(it.Box)
	return it, err
}

// change retargets or edits an item, or with no changes retries a failed
// one; either way it goes back in the line as queued, in its old place.
func (q *promptQueue) change(id string, ch QueueChange) (QueueItem, error) {
	if ch.Session != nil && !queueSessionName.MatchString(*ch.Session) {
		return QueueItem{}, fmt.Errorf("%q is not a session name", *ch.Session)
	}
	if ch.Text != nil && (*ch.Text == "" || len(*ch.Text) > maxQueuedText) {
		return QueueItem{}, errors.New("the prompt must be between 1 byte and 60 KB")
	}
	if ch.Box != nil && !q.boxes.known(*ch.Box) {
		return QueueItem{}, fmt.Errorf("no paired box named %q", *ch.Box)
	}
	q.mu.Lock()
	i := q.indexLocked(id)
	if i < 0 {
		q.mu.Unlock()
		return QueueItem{}, errUnknownQueueItem
	}
	it := &q.items[i]
	if it.State == QueueSending {
		q.mu.Unlock()
		return QueueItem{}, errQueueItemBusy
	}
	oldBox := it.Box
	if it.Native != nil && ((ch.Box != nil && *ch.Box != it.Box) || (ch.Session != nil && *ch.Session != it.Session) || (it.Attempts > 0 && ch.Text != nil && *ch.Text != it.Text)) {
		q.mu.Unlock()
		return QueueItem{}, errors.New("native delivery must keep its identity and uploaded files; discard it and prepare a new prompt to change destination or an attempted payload")
	}
	if ch.Box != nil {
		it.Box = *ch.Box
	}
	if ch.Session != nil {
		it.Session = *ch.Session
	}
	if ch.Text != nil {
		it.Text = *ch.Text
	}
	it.State, it.Error = QueueQueued, ""
	out := *it
	err := q.changedLocked()
	q.mu.Unlock()
	q.kick(out.Box)
	if oldBox != out.Box {
		q.kick(oldBox)
	}
	return out, err
}

// sendNow types an item into its session at once, without waiting for the
// agent's turn to end, and returns how it went.
func (q *promptQueue) sendNow(id string) (QueueItem, error) {
	q.mu.Lock()
	i := q.indexLocked(id)
	if i < 0 {
		q.mu.Unlock()
		return QueueItem{}, errUnknownQueueItem
	}
	if q.items[i].State == QueueSending {
		q.mu.Unlock()
		return QueueItem{}, errQueueItemBusy
	}
	if !q.boxes.online(q.items[i].Box) {
		b := q.items[i].Box
		q.mu.Unlock()
		return QueueItem{}, &queueOfflineError{b}
	}
	it, err := q.claimLocked(i)
	q.mu.Unlock()
	if err != nil {
		return QueueItem{}, err
	}
	out := q.sendClaimed(it)
	q.kick(it.Box)
	return out, nil
}

// claimLocked marks an item as sending and saves that before anything is
// sent: this is what makes delivery at most once.
func (q *promptQueue) claimLocked(i int) (QueueItem, error) {
	if q.held {
		return QueueItem{}, errDraining
	}
	prev := q.items[i]
	it := &q.items[i]
	it.State, it.Error = QueueSending, ""
	it.Attempts++
	it.LastAttempt = q.now()
	if err := q.changedLocked(); err != nil {
		q.items[i] = prev
		return QueueItem{}, fmt.Errorf("could not save the queue before sending: %w", err)
	}
	q.sending.Add(1)
	return *it, nil
}

// hold sends nothing more, for a restart, and waits for the sends under
// way. What waits in line stays queued for the next agent.
func (q *promptQueue) hold() {
	q.mu.Lock()
	q.held = true
	q.mu.Unlock()
	q.sending.Wait()
}

// sendClaimed sends a claimed item and records the outcome. It reports the
// item as it ended: delivered, queued again (never reached the box), or
// failed.
func (q *promptQueue) sendClaimed(it QueueItem) QueueItem {
	q.sends.Add(1)
	defer q.sends.Done()
	defer q.sending.Done()
	ctx, cancel := context.WithTimeout(q.ctx, queueSendLimit)
	req := box.SendRequest{Text: it.Text, Enter: &it.Enter, When: "now", IdemKey: "queue-" + it.ID}
	if it.Native != nil {
		req = *it.Native
		req.Text = it.Text
		req.Enter = &it.Enter
		if req.When == "" {
			req.When = "now"
		}
		req.Force = false
		if req.IdemKey == "" {
			req.IdemKey = "queue-" + it.ID
		}
	}
	err := q.boxes.send(ctx, it.Box, it.Session, req)
	cancel()

	q.mu.Lock()
	defer q.mu.Unlock()
	i := q.indexLocked(it.ID)
	if i < 0 {
		// Nothing removes a sending item, but be safe.
		return it
	}
	cur := &q.items[i]
	switch {
	case err == nil:
		out := *cur
		out.State, out.Delivered = QueueDelivered, q.now()
		q.items = append(q.items[:i], q.items[i+1:]...)
		q.changedLocked()
		q.publish(Event{Type: EventQueueDelivered, Box: out.Box, Data: map[string]any{"id": out.ID, "box": out.Box, "session": out.Session, "attempts": out.Attempts}})
		return out
	case isUnsent(err):
		cur.State = QueueQueued
		q.changedLocked()
		return *cur
	case refusedWith(err, http.StatusNotFound):
		cur.State, cur.Error = QueueFailed, fmt.Sprintf("%s is no longer running on %s.", it.Session, it.Box)
	case errors.As(err, new(*refusedError)):
		cur.State, cur.Error = QueueFailed, fmt.Sprintf("%s refused it: %v", it.Box, err)
	default:
		cur.State = QueueFailed
		cur.Error = fmt.Sprintf("The connection to %s dropped while sending (%v), so it may have arrived. Check the session, then retry or discard it.", it.Box, err)
	}
	q.changedLocked()
	q.publishFailed(*cur)
	return *cur
}

// fail marks an item failed before anything was sent.
func (q *promptQueue) fail(id, reason string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := q.indexLocked(id)
	if i < 0 || q.items[i].State == QueueSending {
		return
	}
	q.items[i].State, q.items[i].Error = QueueFailed, reason
	q.changedLocked()
	q.publishFailed(q.items[i])
}

// setState moves an item between queued and waiting, if it is still the
// same item for the same place; it reports whether it was.
func (q *promptQueue) setState(want QueueItem, state string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := q.indexLocked(want.ID)
	if i < 0 {
		return false
	}
	it := &q.items[i]
	if it.Box != want.Box || it.Session != want.Session || (it.State != QueueQueued && it.State != QueueWaiting) {
		return false
	}
	if it.State != state {
		it.State = state
		q.changedLocked()
	}
	return true
}

// kick starts delivery for every session on box with prompts in line, if
// the box is online. Each session has one worker, so its prompts go in order.
func (q *promptQueue) kick(boxName string) {
	if !q.boxes.online(boxName) {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ctx.Err() != nil {
		return
	}
	for _, it := range q.items {
		if it.Box != boxName || (it.State != QueueQueued && it.State != QueueWaiting) {
			continue
		}
		key := it.Box + "\x00" + it.Session
		if q.workers[key] {
			continue
		}
		q.workers[key] = true
		q.sends.Add(1)
		go q.worker(it.Box, it.Session)
	}
}

// kickAll retries every box with prompts in line; the health loop calls it,
// so a prompt queued during a blip the health check never saw still goes.
func (q *promptQueue) kickAll() {
	q.mu.Lock()
	boxes := map[string]bool{}
	for _, it := range q.items {
		if it.State == QueueQueued || it.State == QueueWaiting {
			boxes[it.Box] = true
		}
	}
	q.mu.Unlock()
	for b := range boxes {
		q.kick(b)
	}
}

// next is the session's head of line, if it can go now. When it cannot, the
// worker is retired under the same lock, so an item added meanwhile always
// finds either a running worker or none.
func (q *promptQueue) next(boxName, session string) (QueueItem, bool) {
	online := q.boxes.online(boxName)
	q.mu.Lock()
	defer q.mu.Unlock()
	if online && q.ctx.Err() == nil {
		for _, it := range q.items {
			if it.Box != boxName || it.Session != session {
				continue
			}
			if it.State == QueueQueued || it.State == QueueWaiting {
				return it, true
			}
			// A failed or sending prompt holds the ones behind it.
			break
		}
	}
	delete(q.workers, boxName+"\x00"+session)
	return QueueItem{}, false
}

func (q *promptQueue) worker(boxName, session string) {
	defer q.sends.Done()
	for {
		it, ok := q.next(boxName, session)
		if !ok {
			return
		}
		if !q.deliver(it) {
			// The box is unreachable again: retire, and wait to be kicked.
			q.mu.Lock()
			delete(q.workers, boxName+"\x00"+session)
			q.mu.Unlock()
			return
		}
	}
}

// busy says whether the agent is mid-turn as far as anyone can tell, or
// waiting for someone: typing into an agent at a question would answer it.
// An agent whose tool never reported a state is not known to be busy.
func busy(s box.Session) bool {
	return s.Agent != "" && (s.AgentState == "running" || s.AgentState == "waiting") && !s.StateSince.IsZero()
}

// deliver takes one item from queued to sent or failed. It returns false
// when the box could not be reached, leaving the item queued.
func (q *promptQueue) deliver(it QueueItem) bool {
	ctx, cancel := context.WithTimeout(q.ctx, queueListLimit)
	all, err := q.boxes.sessions(ctx, it.Box)
	cancel()
	if err != nil {
		q.setState(it, QueueQueued)
		return false
	}
	var s *box.Session
	for i := range all {
		if all[i].Name == it.Session {
			s = &all[i]
		}
	}
	if s == nil {
		q.fail(it.ID, fmt.Sprintf("%s is no longer running on %s.", it.Session, it.Box))
		return true
	}
	if s.Exited {
		q.fail(it.ID, fmt.Sprintf("%s has exited on %s.", it.Session, it.Box))
		return true
	}
	if it.Wait && busy(*s) {
		if !q.setState(it, QueueWaiting) {
			return true
		}
		deadline := q.now().Add(q.idleTimeout)
		for {
			left := deadline.Sub(q.now())
			if left <= 0 {
				break // its turn is taking long; agents take input mid-turn
			}
			step := min(left, q.waitStep)
			ctx, cancel := context.WithTimeout(q.ctx, step+30*time.Second)
			res, err := q.boxes.wait(ctx, it.Box, it.Session, []string{"idle", "finished"}, step)
			cancel()
			if refusedWith(err, http.StatusNotFound) {
				q.fail(it.ID, fmt.Sprintf("%s is no longer running on %s.", it.Session, it.Box))
				return true
			}
			var refused *refusedError
			if errors.As(err, &refused) {
				break // the box cannot say; agents take input mid-turn
			}
			if err != nil {
				q.setState(it, QueueQueued)
				return false
			}
			if res.State == "exited" {
				q.fail(it.ID, fmt.Sprintf("%s has exited on %s.", it.Session, it.Box))
				return true
			}
			if !res.TimedOut {
				break
			}
			// Discarded or retargeted while it waited: start over.
			if !q.setState(it, QueueWaiting) {
				return true
			}
		}
	}
	q.mu.Lock()
	i := q.indexLocked(it.ID)
	if i < 0 || q.items[i].Box != it.Box || q.items[i].Session != it.Session || (q.items[i].State != QueueQueued && q.items[i].State != QueueWaiting) {
		q.mu.Unlock()
		return true
	}
	claimed, err := q.claimLocked(i)
	q.mu.Unlock()
	if err != nil {
		q.logf("prompt queue: %v", err)
		return false
	}
	return q.sendClaimed(claimed).State != QueueQueued
}

// agentBoxes reaches boxes through the agent's own connections.
type agentBoxes struct{ a *Agent }

func (b agentBoxes) known(name string) bool {
	b.a.sync()
	_, ok := b.a.client(name)
	return ok
}

func (b agentBoxes) online(name string) bool {
	b.a.mu.Lock()
	defer b.a.mu.Unlock()
	st, ok := b.a.clients[name]
	return ok && st.status.State == StateOnline
}

func (b agentBoxes) call(ctx context.Context, name, method, path string, in, out any) error {
	c, ok := b.a.client(name)
	if !ok {
		return &refusedError{msg: "no paired box named " + name}
	}
	var body io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(buf)
	}
	header := http.Header{"Content-Type": {"application/json"}, box.OriginHeader: {queueOrigin}}
	resp, err := c.DoWithHeader(ctx, method, path, body, header)
	if err != nil {
		if wire.Unsent(err) {
			b.a.checkSoon()
			return &unsentError{err}
		}
		if errors.Is(err, wire.ErrUntrusted) {
			return &refusedError{status: http.StatusUnauthorized, msg: err.Error()}
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		if e.Error == "" {
			e.Error = "box replied " + resp.Status
		}
		return &refusedError{status: resp.StatusCode, msg: e.Error}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (b agentBoxes) sessions(ctx context.Context, name string) (out []box.Session, err error) {
	return out, b.call(ctx, name, http.MethodGet, "/v1/sessions", nil, &out)
}

func (b agentBoxes) wait(ctx context.Context, name, session string, states []string, timeout time.Duration) (out box.WaitResult, err error) {
	q := url.Values{"timeout": {timeout.String()}, "for": {strings.Join(states, ",")}}
	return out, b.call(ctx, name, http.MethodGet, "/v1/sessions/"+url.PathEscape(session)+"/wait?"+q.Encode(), nil, &out)
}

func (b agentBoxes) send(ctx context.Context, name, session string, req box.SendRequest) error {
	// when:"now" makes a box refuse to type into an agent waiting for
	// someone, rather than answer its question for them.
	return b.call(ctx, name, http.MethodPost, "/v1/sessions/"+url.PathEscape(session)+"/send", req, nil)
}

// queueRoutes serves the queue on the agent's socket and, through it, to
// the app.
func (a *Agent) queueRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/queue", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, a.queue.list())
	})
	mux.HandleFunc("POST /v1/queue", func(w http.ResponseWriter, r *http.Request) {
		var req QueueRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxQueuedText+4096)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request")
			return
		}
		it, err := a.queue.add(req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, it)
	})
	mux.HandleFunc("PATCH /v1/queue/{id}", func(w http.ResponseWriter, r *http.Request) {
		var ch QueueChange
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxQueuedText+4096)).Decode(&ch); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid request")
			return
		}
		it, err := a.queue.change(r.PathValue("id"), ch)
		writeQueueResult(w, it, err)
	})
	mux.HandleFunc("POST /v1/queue/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		it, err := a.queue.change(r.PathValue("id"), QueueChange{})
		writeQueueResult(w, it, err)
	})
	mux.HandleFunc("POST /v1/queue/{id}/send", func(w http.ResponseWriter, r *http.Request) {
		it, err := a.queue.sendNow(r.PathValue("id"))
		writeQueueResult(w, it, err)
	})
	mux.HandleFunc("DELETE /v1/queue/{id}", func(w http.ResponseWriter, r *http.Request) {
		it, err := a.queue.remove(r.PathValue("id"))
		writeQueueResult(w, it, err)
	})
}

func writeQueueResult(w http.ResponseWriter, it QueueItem, err error) {
	switch {
	case errors.Is(err, errUnknownQueueItem):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errQueueItemBusy), errors.Is(err, errQueueBoxOffline):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, it)
	}
}
