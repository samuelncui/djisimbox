package queue

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSourceIdentityAndAssignmentAreStable(t *testing.T) {
	// Identical SMS with distinct modem identities remain separate active messages.
	q := newQueue(t, 2, 1<<20)
	msg := Message{SourceKey: "source-1", SIMID: "sim", Sender: "sender", Text: "same", Timestamp: "same time"}
	recipients := []Recipient{{Bot: "original", ChatID: "chat"}}
	parts := []string{"first", "second"}
	id, created, err := q.Accept(msg, recipients, parts)
	if err != nil || !created {
		t.Fatalf("first intake = %d, %t, %v", id, created, err)
	}
	msg.SourceKey = "source-2"
	otherID, created, err := q.Accept(msg, []Recipient{{Bot: "other", ChatID: "other"}}, []string{"same"})
	if err != nil || !created || otherID == id {
		t.Fatalf("independent intake = %d, %t, %v", otherID, created, err)
	}

	// Caller slice edits and repeated intake cannot alter the frozen assignment, even at capacity.
	recipients[0].Bot = "edited"
	parts[0] = "edited"
	msg.SourceKey = "source-1"
	gotID, created, err := q.Accept(msg, []Recipient{{Bot: "replacement", ChatID: "replacement"}}, []string{"replacement"})
	if err != nil || created || gotID != id {
		t.Fatalf("repeated source = %d, %t, %v", gotID, created, err)
	}
	due := q.Due(time.Now(), 10, nil)
	if len(due) != 2 || due[0].Bot != "original" || due[0].ChatID != "chat" || due[0].Text != "first" {
		t.Fatalf("assignment changed: %+v", due)
	}
	if stats := q.Stats(); stats.Messages != 2 || stats.Pending != 3 {
		t.Fatalf("SMS identity or recipient tasks lost: %+v", stats)
	}
	if _, _, err := q.Accept(Message{SourceKey: "source-3"}, nil, nil); !errors.Is(err, ErrCapacity) {
		t.Fatalf("new SMS beyond count bound = %v", err)
	}
}

func TestByteBoundAndQuarantineAssignmentAreAtomic(t *testing.T) {
	// Derive the public logical charge for the same SMS before and after assignment.
	msg := Message{SourceKey: "unknown", SIMID: "sim", Sender: "sender", Text: strings.Repeat("中", 100), Timestamp: "time"}
	recipients := []Recipient{{Bot: "bot", ChatID: "chat"}}
	parts := []string{strings.Repeat("rendered", 50), "tail"}
	probe := newQueue(t, 1, 1<<20)
	if _, _, err := probe.Accept(msg, nil, nil); err != nil {
		t.Fatal(err)
	}
	quarantineBytes := probe.Stats().Bytes
	if quarantineBytes <= int64(len(msg.Text)) {
		t.Fatal("raw metadata and fixed overhead were not charged")
	}
	if _, _, err := probe.Accept(msg, recipients, parts); err != nil {
		t.Fatal(err)
	}
	assignedBytes := probe.Stats().Bytes
	if assignedBytes <= quarantineBytes+int64(len(parts[0])+len(parts[1])) {
		t.Fatal("recipient metadata and task overhead were not charged")
	}

	// Reject a new SMS before mutation when its raw charge exceeds the budget by one byte.
	tooSmall := newQueue(t, 1, quarantineBytes-1)
	if _, _, err := tooSmall.Accept(msg, nil, nil); !errors.Is(err, ErrCapacity) {
		t.Fatalf("new SMS beyond byte bound = %v", err)
	}
	if stats := tooSmall.Stats(); stats != (Stats{}) {
		t.Fatalf("rejected intake changed queue: %+v", stats)
	}

	// Rejected assignment leaves the existing quarantine, byte charge, and task state untouched.
	q := newQueue(t, 1, assignedBytes-1)
	id, _, err := q.Accept(msg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := q.Stats()
	if _, _, err := q.Accept(msg, recipients, parts); !errors.Is(err, ErrCapacity) {
		t.Fatalf("assignment beyond byte bound = %v", err)
	}
	if stats := q.Stats(); stats != before || stats.Quarantined != 1 {
		t.Fatalf("rejected assignment changed queue: before %+v, after %+v", before, stats)
	}
	if len(q.Due(time.Now(), 10, nil)) != 0 || q.Complete(id) || q.Release(id) == nil {
		t.Fatal("unassigned SMS became deliverable or releasable")
	}
	if gotID, created, err := q.Accept(msg, nil, nil); err != nil || created || gotID != id {
		t.Fatalf("quarantine duplicate at capacity = %d, %t, %v", gotID, created, err)
	}

	// An exact byte budget admits the first explicit assignment without a new message slot.
	exact := newQueue(t, 1, assignedBytes)
	id, _, err = exact.Accept(msg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	gotID, created, err := exact.Accept(msg, append(recipients, recipients[0]), parts)
	if err != nil || created || gotID != id {
		t.Fatalf("exact-bound assignment = %d, %t, %v", gotID, created, err)
	}
	if stats := exact.Stats(); stats.Bytes != assignedBytes || stats.Quarantined != 0 || stats.Pending != 2 {
		t.Fatalf("assignment duplicated recipients or lost byte accounting: %+v", stats)
	}
}

func TestReleaseWaitsForAllRecipientsAndFreesQueue(t *testing.T) {
	// Each recipient has an independent ordered pair of chunks.
	q := newQueue(t, 1, 1<<20)
	id, _, err := q.Accept(Message{SourceKey: "source"},
		[]Recipient{{Bot: "bot", ChatID: "one"}, {Bot: "bot", ChatID: "two"}}, []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	due := q.Due(time.Now(), 10, nil)
	if len(due) != 2 || due[0].Text != "first" || due[1].Text != "first" {
		t.Fatalf("first chunks = %+v", due)
	}
	if err := q.Delivered(due[0].ID); err != nil {
		t.Fatal(err)
	}
	if q.Complete(id) || q.Release(id) == nil {
		t.Fatal("one acknowledged chunk released the SMS")
	}

	// Finishing one recipient still retains the SMS until every other recipient succeeds.
	due = q.Due(time.Now(), 10, nil)
	if len(due) != 2 || due[0].ChatID != "one" || due[0].Text != "second" {
		t.Fatalf("recipient did not advance in order: %+v", due)
	}
	if err := q.Delivered(due[0].ID); err != nil {
		t.Fatal(err)
	}
	if q.Complete(id) || q.Release(id) == nil {
		t.Fatal("one completed recipient released the SMS")
	}
	for range 2 {
		due = q.Due(time.Now(), 10, nil)
		if len(due) != 1 || due[0].ChatID != "two" {
			t.Fatalf("remaining recipient = %+v", due)
		}
		if err := q.Delivered(due[0].ID); err != nil {
			t.Fatal(err)
		}
	}
	if !q.Complete(id) {
		t.Fatal("all acknowledged chunks did not complete the SMS")
	}
	if _, _, err := q.Accept(Message{SourceKey: "new"}, nil, nil); !errors.Is(err, ErrCapacity) {
		t.Fatalf("SMS awaiting modem deletion did not consume capacity: %v", err)
	}

	// Only confirmed modem deletion releases identity, tasks, and all active logical bytes.
	if err := q.Release(id); err != nil {
		t.Fatal(err)
	}
	if stats := q.Stats(); stats.Messages != 0 || stats.Pending != 0 || stats.Failed != 0 || stats.Bytes != 0 ||
		stats.Delivered != 4 || stats.Forwarded != 1 {
		t.Fatalf("release leaked active state or lost lifetime counters: %+v", stats)
	}
	if q.Complete(id) || !q.NextDue().IsZero() || len(q.Due(time.Now(), 10, nil)) != 0 || q.Release(id) == nil {
		t.Fatal("released SMS remained active")
	}
	newID, created, err := q.Accept(Message{SourceKey: "source"}, nil, nil)
	if err != nil || !created || newID == id {
		t.Fatalf("released source could not be accepted anew: %d, %t, %v", newID, created, err)
	}
}

func TestPermanentFailurePreservesOtherRecipientsAndOriginal(t *testing.T) {
	// A permanent recipient failure marks its entire unsent tail and leaves other chats ready.
	q := newQueue(t, 1, 1<<20)
	id, _, err := q.Accept(Message{SourceKey: "source"},
		[]Recipient{{Bot: "bot", ChatID: "failed"}, {Bot: "bot", ChatID: "success"}}, []string{"one", "two"})
	if err != nil {
		t.Fatal(err)
	}
	due := q.Due(time.Now(), 10, nil)
	if len(due) != 2 {
		t.Fatalf("initial recipients = %+v", due)
	}
	if err := q.Fail(due[0].ID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		due = q.Due(time.Now(), 10, nil)
		if len(due) != 1 || due[0].ChatID != "success" {
			t.Fatalf("failure blocked unrelated recipient: %+v", due)
		}
		if err := q.Delivered(due[0].ID); err != nil {
			t.Fatal(err)
		}
	}

	// Failed chunks keep the original SMS active and charged for later operator recovery.
	if q.Complete(id) || q.Release(id) == nil || !q.NextDue().IsZero() {
		t.Fatal("failed SMS was released or scheduled for later chunks")
	}
	if stats := q.Stats(); stats.Messages != 1 || stats.Pending != 0 || stats.Delivered != 2 || stats.Failed != 2 ||
		stats.Forwarded != 0 || stats.Bytes == 0 {
		t.Fatalf("failure lost original or crossed recipient boundary: %+v", stats)
	}
	if _, _, err := q.Accept(Message{SourceKey: "new"}, nil, nil); !errors.Is(err, ErrCapacity) {
		t.Fatalf("failed SMS stopped consuming capacity: %v", err)
	}
}

func TestDueSharesBatchAcrossChatsAndHonorsRetry(t *testing.T) {
	// More ready SMS than the batch limit arrive for one chat before another chat arrives.
	q := newQueue(t, 20, 1<<20)
	var ids []int64
	for index := range 10 {
		id, _, err := q.Accept(Message{SourceKey: fmt.Sprintf("busy-%d", index)},
			[]Recipient{{Bot: "bot", ChatID: "busy"}}, []string{"first", "second"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	acceptOne(t, q, "quiet", "quiet")
	now := time.Now()
	due := q.Due(now, 2, nil)
	if len(due) != 2 || due[0].MessageID != ids[0] || due[0].ChatID != "busy" || due[1].ChatID != "quiet" {
		t.Fatalf("busy chat hid another chat or changed oldest-first order: %+v", due)
	}

	// Backoff admits a later SMS without admitting the deferred SMS's incomplete second chunk.
	deferredID := due[0].ID
	if err := q.Retry(deferredID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	due = q.Due(now, 2, nil)
	if len(due) != 2 || due[0].MessageID != ids[1] || due[0].Text != "first" || due[1].ChatID != "quiet" {
		t.Fatalf("retry hid another chat or admitted a blocked chunk: %+v", due)
	}
	if err := q.Delivered(due[0].ID); err != nil {
		t.Fatal(err)
	}
	due = q.Due(now, 2, nil)
	if len(due) != 2 || due[0].MessageID != ids[1] || due[0].Text != "second" {
		t.Fatalf("acknowledgment did not unblock the next chunk: %+v", due)
	}
}

func TestDueFiltersBeforeBatchLimit(t *testing.T) {
	// Older blocked chats must leave the entire batch available to later ready chats.
	q := newQueue(t, 66, 1<<20)
	for index := range 64 {
		chat := fmt.Sprintf("blocked-%d", index)
		acceptOne(t, q, chat, chat)
	}
	first := acceptOne(t, q, "first-ready", "ready-one")
	second := acceptOne(t, q, "second-ready", "ready-two")
	ready := func(delivery Delivery) bool { return !strings.HasPrefix(delivery.ChatID, "blocked-") }
	now := time.Now()
	due := q.Due(now, 64, ready)
	if len(due) != 2 || due[0].MessageID != first || due[1].MessageID != second {
		t.Fatalf("blocked chats hid later ready deliveries or changed their order: %+v", due)
	}

	// The limit still applies to the remaining ready deliveries.
	due = q.Due(now, 1, ready)
	if len(due) != 1 || due[0].MessageID != first {
		t.Fatalf("filtered batch did not honor its limit: %+v", due)
	}
}

func TestDueFilterPreservesHeadsAndOrdering(t *testing.T) {
	// Multiple chunks and another SMS for the same chat retain their existing ordering.
	q := newQueue(t, 2, 1<<20)
	id, _, err := q.Accept(Message{SourceKey: "original"}, []Recipient{
		{Bot: "bot", ChatID: "blocked"}, {Bot: "bot", ChatID: "one"}, {Bot: "bot", ChatID: "two"},
	}, []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	acceptOne(t, q, "later", "one")
	ready := func(delivery Delivery) bool { return delivery.ChatID != "blocked" }
	now := time.Now()
	before := q.Stats()
	due := q.Due(now, 10, ready)
	if len(due) != 2 || due[0].ChatID != "one" || due[1].ChatID != "two" ||
		due[0].MessageID != id || due[1].MessageID != id || due[0].Text != "first" || due[1].Text != "first" {
		t.Fatalf("filter changed recipient heads or task order: %+v", due)
	}

	// Filtering can hide every candidate without acknowledging or postponing any task.
	if rejected := q.Due(now, 10, func(Delivery) bool { return false }); len(rejected) != 0 {
		t.Fatalf("rejected deliveries remained visible: %+v", rejected)
	}
	if stats := q.Stats(); stats != before {
		t.Fatalf("filter changed queue state: before %+v, after %+v", before, stats)
	}
	unfiltered := q.Due(now, 10, nil)
	if len(unfiltered) != 3 || unfiltered[0].ChatID != "blocked" || unfiltered[0].MessageID != id {
		t.Fatalf("nil filter failed to restore the original candidates: %+v", unfiltered)
	}

	// Acknowledgment advances only that recipient to the next chunk of its original SMS.
	if err := q.Delivered(due[0].ID); err != nil {
		t.Fatal(err)
	}
	due = q.Due(now, 10, ready)
	if len(due) != 2 || due[0].ChatID != "one" || due[0].Text != "second" || due[0].MessageID != id ||
		due[1].ChatID != "two" || due[1].Text != "first" || due[1].MessageID != id {
		t.Fatalf("filter exposed a blocked chunk or later SMS: %+v", due)
	}
}

func TestNextDueExcludesBlockedAndFailedChunks(t *testing.T) {
	// A delayed first chunk owns the next due time even when its tail was originally ready earlier.
	q := newQueue(t, 2, 1<<20)
	if !q.NextDue().IsZero() {
		t.Fatal("empty queue has a scheduled send")
	}
	id, _, err := q.Accept(Message{SourceKey: "source"}, []Recipient{{Bot: "bot", ChatID: "chat"}}, []string{"one", "two"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	due := q.Due(now, 1, nil)
	when := now.Add(time.Hour)
	if len(due) != 1 || q.NextDue().IsZero() {
		t.Fatalf("new queue head is not scheduled: %+v", due)
	}
	if err := q.Retry(due[0].ID, when); err != nil {
		t.Fatal(err)
	}
	if !q.NextDue().Equal(when) || len(q.Due(now, 10, nil)) != 0 {
		t.Fatal("delayed head exposed its blocked tail")
	}

	// Acknowledgment exposes the second chunk; permanent failure removes it from scheduling.
	due = q.Due(when, 1, nil)
	if len(due) != 1 || due[0].Attempts != 1 {
		t.Fatalf("retry state was not retained: %+v", due)
	}
	if err := q.Delivered(due[0].ID); err != nil {
		t.Fatal(err)
	}
	if next := q.NextDue(); next.IsZero() || next.After(now) {
		t.Fatalf("next chunk due time = %s", next)
	}
	due = q.Due(when, 1, nil)
	if len(due) != 1 || due[0].Text != "two" {
		t.Fatalf("second chunk = %+v", due)
	}
	if err := q.Fail(due[0].ID); err != nil {
		t.Fatal(err)
	}
	if !q.NextDue().IsZero() || q.Complete(id) {
		t.Fatal("failed chunk remained scheduled or completed its SMS")
	}
}

func TestConcurrentIntakeDeliveryAndInspection(t *testing.T) {
	// Producers enqueue unique sources while a single worker delivers and releases active SMS.
	const producers, perProducer = 8, 20
	q := newQueue(t, producers*perProducer, 1<<24)
	ids := make(chan int64, producers*perProducer)
	var intake, worker, observer sync.WaitGroup
	worker.Go(func() {
		// Drive the normal single-sender path while intake continues to append messages.
		for id := range ids {
			for !q.Complete(id) {
				due := q.Due(time.Now(), 8, nil)
				if len(due) == 0 {
					t.Error("active intake had no eligible delivery")
					return
				}
				for _, delivery := range due {
					if err := q.Delivered(delivery.ID); err != nil {
						t.Error(err)
						return
					}
				}
			}

			// Model successful modem deletion only after every recipient has acknowledged its chunks.
			if err := q.Release(id); err != nil {
				t.Error(err)
				return
			}
		}
	})
	for producer := range producers {
		intake.Go(func() {
			// Publish accepted source IDs to the sender without sharing caller-owned slices.
			for index := range perProducer {
				id, _, err := q.Accept(Message{SourceKey: fmt.Sprintf("source-%d-%d", producer, index)},
					[]Recipient{{Bot: "bot", ChatID: "chat"}}, []string{"text"})
				if err != nil {
					t.Error(err)
					continue
				}
				ids <- id
			}
		})
	}
	observer.Go(func() {
		// Exercise concurrent inspection without changing the delivery loop's ownership.
		for range 500 {
			q.Stats()
			q.NextDue()
			q.Complete(-1)
		}
	})

	// Join all work and verify exact lifetime counts and complete active-state cleanup.
	intake.Wait()
	close(ids)
	worker.Wait()
	observer.Wait()
	if stats := q.Stats(); stats.Messages != 0 || stats.Bytes != 0 || stats.Pending != 0 || stats.Failed != 0 ||
		stats.Delivered != producers*perProducer || stats.Forwarded != producers*perProducer {
		t.Fatalf("concurrent work lost or leaked state: %+v", stats)
	}
}

func TestValidationLeavesQueueUnchanged(t *testing.T) {
	// Invalid bounds must fail before a queue is constructed.
	for _, bounds := range []struct {
		messages int
		bytes    int64
	}{{0, 1000}, {1, 0}, {-1, 1000}, {1, -1}} {
		if _, err := New(bounds.messages, bounds.bytes); err == nil {
			t.Errorf("invalid bounds accepted: %+v", bounds)
		}
	}

	// Invalid source, recipient, and chunk inputs leave no partial intake or assignment.
	q := newQueue(t, 2, 1<<20)
	for _, input := range []struct {
		message    Message
		recipients []Recipient
		parts      []string
	}{
		{Message{}, nil, nil},
		{Message{SourceKey: "source"}, []Recipient{{Bot: "bot"}}, []string{"text"}},
		{Message{SourceKey: "source"}, []Recipient{{Bot: "bot", ChatID: "chat"}}, nil},
		{Message{SourceKey: "source"}, []Recipient{{Bot: "bot", ChatID: "chat"}}, []string{""}},
	} {
		if _, _, err := q.Accept(input.message, input.recipients, input.parts); err == nil {
			t.Errorf("invalid intake accepted: %+v", input)
		}
		if stats := q.Stats(); stats != (Stats{}) {
			t.Fatalf("invalid intake mutated queue: %+v", stats)
		}
	}
	if q.Delivered(-1) == nil || q.Retry(-1, time.Now()) == nil || q.Fail(-1) == nil || q.Release(-1) == nil {
		t.Fatal("unknown state transition succeeded")
	}
}

func newQueue(t *testing.T, maxMessages int, maxBytes int64) *Queue {
	t.Helper()
	q, err := New(maxMessages, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func acceptOne(t *testing.T, q *Queue, source, chat string) int64 {
	t.Helper()
	id, created, err := q.Accept(Message{SourceKey: source}, []Recipient{{Bot: "bot", ChatID: chat}}, []string{"text"})
	if err != nil || !created {
		t.Fatalf("intake = %d, %t, %v", id, created, err)
	}
	return id
}
