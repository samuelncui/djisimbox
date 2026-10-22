// Package queue holds bounded SMS delivery state for the running process.
package queue

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ErrCapacity means a new SMS or recipient assignment exceeds the active queue budget.
var ErrCapacity = errors.New("active message capacity reached")

const (
	messageBytes = int64(512)
	routeBytes   = int64(128)
	taskBytes    = int64(256)
)

type state uint8

const (
	pending state = iota
	delivered
	failed
)

// Message is an SMS or call notice from one modem object, identified by its live SourceKey.
type Message struct {
	SourceKey string
	SIMID     string
	Sender    string
	Text      string
	Timestamp string
}

// Recipient snapshots the configured bot alias and Telegram chat for an SMS.
type Recipient struct {
	Bot    string
	ChatID string
}

// Delivery is one ordered text chunk for one recipient.
type Delivery struct {
	ID        int64
	MessageID int64
	Bot       string
	ChatID    string
	Text      string
	Attempts  int
}

// Stats reports active notifications, pending/failed chunks, quarantined notices, and a logical byte budget.
// Delivered counts acknowledged chunks; Forwarded counts released complete notifications.
// Bytes includes raw strings, rendered chunks, recipient metadata, and conservative fixed overheads;
// it measures active queue contents rather than the Go allocator's retained memory.
type Stats struct {
	Messages    int64
	Pending     int64
	Delivered   int64
	Failed      int64
	Quarantined int64
	Forwarded   int64
	Bytes       int64
}

type message struct {
	id     int64
	value  Message
	routes []*route
	bytes  int64
}

type route struct {
	tasks []*task
}

type task struct {
	value  Delivery
	route  *route
	state  state
	nextAt time.Time
}

// Queue retains notifications until every chunk is acknowledged and the source owner confirms release.
// One sender calls Due; intake, acknowledgments, and inspection may run concurrently.
type Queue struct {
	mu            sync.Mutex
	maxMessages   int
	maxBytes      int64
	nextMessageID int64
	nextTaskID    int64
	messages      map[int64]*message
	sources       map[string]*message
	tasks         map[int64]*task
	delivered     int64
	forwarded     int64
	bytes         int64
}

// New creates an empty RAM queue with both SMS-count and logical byte limits.
func New(maxMessages int, maxBytes int64) (*Queue, error) {
	// Require both bounds so disconnected recipients cannot grow the queue indefinitely.
	if maxMessages < 1 {
		return nil, errors.New("active message capacity must be positive")
	}
	if maxBytes < 1 {
		return nil, errors.New("active byte capacity must be positive")
	}
	return &Queue{
		maxMessages: maxMessages,
		maxBytes:    maxBytes,
		messages:    make(map[int64]*message),
		sources:     make(map[string]*message),
		tasks:       make(map[int64]*task),
	}, nil
}

// Accept records one live source and freezes its first nonempty recipient assignment.
// A quarantined SMS can gain recipients later; capacity errors leave the queue unchanged.
func (q *Queue) Accept(msg Message, recipients []Recipient, parts []string) (int64, bool, error) {
	// Existing assigned sources remain usable even when the queue is at capacity.
	q.mu.Lock()
	defer q.mu.Unlock()
	if msg.SourceKey == "" {
		return 0, false, errors.New("message source key is empty")
	}
	existing := q.sources[msg.SourceKey]
	if existing != nil && len(existing.routes) > 0 {
		return existing.id, false, nil
	}
	if existing != nil && len(recipients) == 0 {
		return existing.id, false, nil
	}

	// Normalize the first assignment before charging or modifying active state.
	var assigned []Recipient
	if len(recipients) > 0 {
		if len(parts) == 0 {
			return 0, false, errors.New("assigned SMS has no delivery text")
		}
		for _, part := range parts {
			if part == "" {
				return 0, false, errors.New("assigned SMS has an empty text chunk")
			}
		}
		handled := make(map[Recipient]struct{}, len(recipients))
		for _, recipient := range recipients {
			if recipient.Bot == "" || recipient.ChatID == "" {
				return 0, false, errors.New("assigned SMS has an incomplete recipient")
			}
			if _, found := handled[recipient]; found {
				continue
			}
			assigned = append(assigned, recipient)
			handled[recipient] = struct{}{}
		}
	}

	// Check the complete additional charge before inserting any message, source, or task.
	created := existing == nil
	var charge int64
	if created {
		if len(q.messages) >= q.maxMessages {
			return 0, false, ErrCapacity
		}
		charge = messageBytes + int64(len(msg.SourceKey)) + int64(len(msg.SIMID)) +
			int64(len(msg.Sender)) + int64(len(msg.Text)) + int64(len(msg.Timestamp))
	}
	remaining := q.maxBytes - q.bytes
	if charge > remaining {
		return 0, false, ErrCapacity
	}
	for _, recipient := range assigned {
		charge += routeBytes + int64(len(recipient.Bot)) + int64(len(recipient.ChatID))
		if charge > remaining {
			return 0, false, ErrCapacity
		}
		for _, part := range parts {
			charge += taskBytes + int64(len(recipient.Bot)) + int64(len(recipient.ChatID)) + int64(len(part))
			if charge > remaining {
				return 0, false, ErrCapacity
			}
		}
	}

	// Construct owned task slices so later caller edits cannot change the assignment.
	if created {
		existing = &message{id: q.nextMessageID + 1, value: msg}
	}
	nextID := q.nextTaskID
	now := time.Now()
	routes := make([]*route, 0, len(assigned))
	for _, recipient := range assigned {
		r := &route{tasks: make([]*task, 0, len(parts))}
		for _, part := range parts {
			nextID++
			r.tasks = append(r.tasks, &task{
				value: Delivery{
					ID: nextID, MessageID: existing.id, Bot: recipient.Bot, ChatID: recipient.ChatID, Text: part,
				},
				route: r, nextAt: now,
			})
		}
		routes = append(routes, r)
	}

	// Publish the source and all recipient chunks as one mutex-protected change.
	if created {
		q.nextMessageID = existing.id
		q.messages[existing.id] = existing
		q.sources[msg.SourceKey] = existing
	}
	existing.routes = routes
	existing.bytes += charge
	q.bytes += charge
	q.nextTaskID = nextID
	for _, r := range routes {
		for _, task := range r.tasks {
			q.tasks[task.value.ID] = task
		}
	}
	return existing.id, created, nil
}

// Due returns at most one ready chunk per bot/chat, ordered by due time then task ID.
// A nil ready accepts every candidate; otherwise it filters candidates before the limit.
// ready runs under the queue lock and must not block or call queue methods.
func (q *Queue) Due(now time.Time, limit int, ready func(Delivery) bool) []Delivery {
	// Select a chat's oldest eligible SMS without admitting any blocked later chunks.
	q.mu.Lock()
	defer q.mu.Unlock()
	if limit < 1 {
		return nil
	}
	candidates := make(map[Recipient]*task)
	for _, message := range q.messages {
		for _, route := range message.routes {
			candidate := head(route)
			if candidate == nil || candidate.nextAt.After(now) {
				continue
			}
			recipient := Recipient{Bot: candidate.value.Bot, ChatID: candidate.value.ChatID}
			current := candidates[recipient]
			if current == nil || earlier(candidate, current) {
				candidates[recipient] = candidate
			}
		}
	}

	// Filter recipients before limiting a deterministic snapshot without changing queue state.
	ordered := make([]*task, 0, len(candidates))
	for _, candidate := range candidates {
		if ready != nil && !ready(candidate.value) {
			continue
		}
		ordered = append(ordered, candidate)
	}
	sort.Slice(ordered, func(i, j int) bool { return earlier(ordered[i], ordered[j]) })
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	deliveries := make([]Delivery, 0, len(ordered))
	for _, task := range ordered {
		deliveries = append(deliveries, task.value)
	}
	return deliveries
}

// NextDue returns the earliest pending head time, or zero when no recipient can advance.
func (q *Queue) NextDue() time.Time {
	// Include delayed heads while excluding chunks blocked by incomplete or failed predecessors.
	q.mu.Lock()
	defer q.mu.Unlock()
	var next time.Time
	for _, message := range q.messages {
		for _, route := range message.routes {
			candidate := head(route)
			if candidate == nil {
				continue
			}
			if next.IsZero() || candidate.nextAt.Before(next) {
				next = candidate.nextAt
			}
		}
	}
	return next
}

// Delivered acknowledges one pending chunk and allows its next chunk to advance.
func (q *Queue) Delivered(id int64) error {
	// Reject out-of-order acknowledgments and count each successful send once.
	q.mu.Lock()
	defer q.mu.Unlock()
	task := q.tasks[id]
	if task == nil || task.state != pending {
		return fmt.Errorf("acknowledge chunk %d: delivery is not pending", id)
	}
	if head(task.route) != task {
		return fmt.Errorf("acknowledge chunk %d: earlier chunk is incomplete", id)
	}
	task.state = delivered
	task.value.Attempts++
	q.delivered++
	return nil
}

// Retry records an unsuccessful attempt and postpones this pending chunk.
func (q *Queue) Retry(id int64, when time.Time) error {
	// A zero retry time means immediately ready, keeping zero reserved for an empty NextDue.
	q.mu.Lock()
	defer q.mu.Unlock()
	task := q.tasks[id]
	if task == nil || task.state != pending {
		return fmt.Errorf("retry chunk %d: delivery is not pending", id)
	}
	if when.IsZero() {
		when = time.Now()
	}
	task.nextAt = when
	task.value.Attempts++
	return nil
}

// Fail marks every remaining chunk for this SMS and recipient permanently failed.
func (q *Queue) Fail(id int64) error {
	// Preserve other recipients and the original SMS while suppressing this failed recipient's tail.
	q.mu.Lock()
	defer q.mu.Unlock()
	task := q.tasks[id]
	if task == nil || task.state != pending {
		return fmt.Errorf("fail chunk %d: delivery is not pending", id)
	}
	task.value.Attempts++
	for _, sibling := range task.route.tasks {
		if sibling.state == pending {
			sibling.state = failed
		}
	}
	return nil
}

// Complete reports whether every chunk of an assigned active SMS has been acknowledged.
func (q *Queue) Complete(id int64) bool {
	// Inspect the same active state protected by delivery and release transitions.
	q.mu.Lock()
	defer q.mu.Unlock()
	return complete(q.messages[id])
}

// Release forgets a complete SMS after the caller successfully deletes its modem copy.
func (q *Queue) Release(id int64) error {
	// Keep capacity charged until all recipients succeeded and the modem deletion is confirmed.
	q.mu.Lock()
	defer q.mu.Unlock()
	message := q.messages[id]
	if !complete(message) {
		return fmt.Errorf("release SMS %d: message is not complete", id)
	}
	for _, route := range message.routes {
		for _, task := range route.tasks {
			delete(q.tasks, task.value.ID)
		}
	}
	delete(q.sources, message.value.SourceKey)
	delete(q.messages, id)
	q.bytes -= message.bytes
	q.forwarded++
	return nil
}

// Stats returns active usage and lifetime acknowledged/released counts for this process.
func (q *Queue) Stats() Stats {
	// Snapshot active queue contents with the same lock as every state transition.
	q.mu.Lock()
	defer q.mu.Unlock()
	stats := Stats{
		Messages: int64(len(q.messages)), Delivered: q.delivered, Forwarded: q.forwarded, Bytes: q.bytes,
	}
	for _, message := range q.messages {
		if len(message.routes) == 0 {
			stats.Quarantined++
		}
	}
	for _, task := range q.tasks {
		switch task.state {
		case pending:
			stats.Pending++
		case failed:
			stats.Failed++
		}
	}
	return stats
}

func head(route *route) *task {
	for _, task := range route.tasks {
		if task.state == delivered {
			continue
		}
		if task.state == pending {
			return task
		}
		return nil
	}
	return nil
}

func earlier(a, b *task) bool {
	if a.nextAt.Equal(b.nextAt) {
		return a.value.ID < b.value.ID
	}
	return a.nextAt.Before(b.nextAt)
}

func complete(message *message) bool {
	if message == nil || len(message.routes) == 0 {
		return false
	}
	for _, route := range message.routes {
		for _, task := range route.tasks {
			if task.state != delivered {
				return false
			}
		}
	}
	return true
}
