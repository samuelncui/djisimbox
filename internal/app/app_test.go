package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/djisimbox/internal/config"
	"github.com/samuelncui/djisimbox/internal/modem"
	"github.com/samuelncui/djisimbox/internal/queue"
	"github.com/samuelncui/djisimbox/internal/telegram"
)

const testSIM = "8901000000000000001"

type testInbox struct {
	queue   *queue.Queue
	deletes int
	fail    bool
}

func (i *testInbox) Run(ctx context.Context, _ func(context.Context, modem.Message) error) error {
	<-ctx.Done()
	return nil
}

func (i *testInbox) Delete(ctx context.Context, _ modem.Message) error {
	// The source can be released only after every destination has acknowledged it.
	if !i.queue.Complete(1) {
		return errors.New("delete before all acknowledgments")
	}
	i.deletes++
	if i.fail {
		return errors.New("synthetic release failure")
	}
	return nil
}

func (i *testInbox) Wake() {}

type testSender struct{ err error }

func (s testSender) Send(context.Context, string, string) (int64, error) { return 123, s.err }

func fixture(t *testing.T, maxPending int) (*worker, *testInbox) {
	t.Helper()
	// Create private, synthetic configuration through the same parser as production.
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(secrets, []byte(`{"bots":{"default":"123456789:FAKE_TEST_TOKEN_12345678901234567890"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	contents, err := json.Marshal(config.Config{Version: 1, RuntimeDir: dir,
		SecretsFile: secrets, MaxPending: maxPending, MaxBytes: 16777216,
		Routes: []config.Route{{ICCID: testSIM, Name: "测试 SIM", Recipients: []config.Recipient{{Bot: "default", ChatID: "123"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	// The queue has no files or persistent state; only synthetic static configuration uses test files.
	pending, err := queue.New(cfg.MaxPending, cfg.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	inbox := &testInbox{queue: pending}
	return &worker{config: cfg, queue: pending, inbox: inbox, wake: make(chan struct{}, 1),
		log: slog.New(slog.NewJSONHandler(io.Discard, nil))}, inbox
}

func TestSourceStaysUntilAllAcknowledgmentsAndReleaseCanRetry(t *testing.T) {
	// Receiving a complete SMS queues it in RAM without touching its modem original.
	w, inbox := fixture(t, 10)
	message := modem.Message{SourceKey: "source-1", SIMID: testSIM, Sender: "+1234", Text: "中文 😀"}
	if err := w.accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if err := w.accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	stats := w.queue.Stats()
	if inbox.deletes != 0 || stats.Messages != 1 || stats.Pending != 1 {
		t.Fatalf("premature release or duplicate intake: deletes=%d, stats=%+v", inbox.deletes, stats)
	}
	ready := w.queue.Due(time.Now(), 1, nil)
	if len(ready) != 1 {
		t.Fatal("delivery missing")
	}
	if ready[0].Text != "短信通知\nSIM: 测试 SIM\n号码: +1234\n\n中文 😀" {
		t.Fatal("Unicode delivery changed")
	}

	// A successful Telegram acknowledgment still retains the queue until source deletion succeeds.
	if err := w.queue.Delivered(ready[0].ID); err != nil {
		t.Fatal(err)
	}
	inbox.fail = true
	if err := w.accept(context.Background(), message); err == nil {
		t.Fatal("source release failure not reported")
	}
	if !w.queue.Complete(1) || w.queue.Stats().Messages != 1 {
		t.Fatal("failed deletion discarded completed source state")
	}
	inbox.fail = false
	if err := w.accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	stats = w.queue.Stats()
	if inbox.deletes != 2 || stats.Messages != 0 || stats.Forwarded != 1 || stats.Bytes != 0 {
		t.Fatalf("source cleanup failed: deletes=%d stats=%+v", inbox.deletes, stats)
	}
}

func TestUnknownSIMAndCapacityPreserveModemCopies(t *testing.T) {
	// Unknown identities have no fallback recipient and retain the original on the modem.
	w, inbox := fixture(t, 1)
	if err := w.accept(context.Background(), modem.Message{SourceKey: "unknown", Text: "private"}); err != nil {
		t.Fatal(err)
	}
	stats := w.queue.Stats()
	if inbox.deletes != 0 || stats.Quarantined != 1 || stats.Pending != 0 {
		t.Fatalf("unsafe unknown-SIM handling: %+v", stats)
	}

	// A full queue must fail before the known SIM's source copy can be released.
	err := w.accept(context.Background(), modem.Message{SourceKey: "known", SIMID: testSIM, Text: "retained"})
	if !errors.Is(err, queue.ErrCapacity) || inbox.deletes != 0 {
		t.Fatal("capacity failure released a modem copy")
	}
}

func TestRestartRebuildsFromRetainedOriginal(t *testing.T) {
	// A full old queue still owns its modem copy and has not deleted anything.
	w, original := fixture(t, 1)
	message := modem.Message{SourceKey: "old-owner/source", SIMID: testSIM, Text: "retained across restart"}
	if err := w.accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if original.deletes != 0 || w.queue.Stats().Messages != 1 {
		t.Fatal("original was released before delivery")
	}

	// Restart discards old RAM entries and accepts the same retained SMS under its new owner.
	restarted, source := fixture(t, 1)
	message.SourceKey = "new-owner/source"
	if err := restarted.accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if source.deletes != 0 || restarted.queue.Stats().Pending != 1 {
		t.Fatal("replacement queue could not resume from modem storage")
	}
}

func TestIncomingCallUsesSIMRouteAndAcknowledgmentLifecycle(t *testing.T) {
	// Call notices share the SIM's explicit recipients while using their own plain-text format.
	w, inbox := fixture(t, 10)
	message := modem.Message{Kind: modem.IncomingCall, SourceKey: "call-1", SIMID: testSIM,
		Sender: "+1234", Timestamp: "2026-10-22T12:00:00+08:00"}
	if err := w.accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	ready := w.queue.Due(time.Now(), 10, nil)
	want := "来电通知\nSIM: 测试 SIM\n号码: +1234\n时间: 2026-10-22T12:00:00+08:00"
	if len(ready) != 1 || ready[0].Bot != "default" || ready[0].ChatID != "123" || ready[0].Text != want {
		t.Fatalf("call notice was misrouted or formatted incorrectly: %+v", ready)
	}
	if inbox.deletes != 0 {
		t.Fatal("call notice released before delivery")
	}

	// The inbox owns acknowledgment semantics; the queue frees capacity only after it succeeds.
	if err := w.queue.Delivered(ready[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := w.accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if stats := w.queue.Stats(); stats.Messages != 0 || stats.Forwarded != 1 || stats.Bytes != 0 || inbox.deletes != 1 {
		t.Fatalf("acknowledged call notice retained capacity: %+v", stats)
	}

	// An unknown SIM cannot fall back to another recipient, even for incoming calls.
	message.SIMID, message.SourceKey = "", "unknown-call"
	if err := w.accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if stats := w.queue.Stats(); stats.Quarantined != 1 || stats.Pending != 0 || inbox.deletes != 1 {
		t.Fatalf("unknown call leaked to a recipient: %+v", stats)
	}
}

func TestRetryAndPermanentFailureDoNotBlockOtherBots(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"temporary", &telegram.Error{Code: 429, RetryAfter: 10 * time.Second}},
		{"permanent", &telegram.Error{Code: 403, Permanent: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Queue independent recipients and inject only the HTTP boundary.
			w, _ := fixture(t, 10)
			w.bots = map[string]sender{"bad": testSender{err: tc.err}, "good": testSender{}}
			_, _, err := w.queue.Accept(queue.Message{SourceKey: "one"},
				[]queue.Recipient{{Bot: "bad", ChatID: "123"}, {Bot: "good", ChatID: "456"}}, []string{"text"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- w.deliver(ctx) }()
			t.Cleanup(func() { cancel(); <-done })

			// Acknowledged progress for another bot continues while the original stays on the modem.
			deadline := time.Now().Add(3 * time.Second)
			for {
				stats := w.queue.Stats()
				if stats.Delivered == 1 {
					if tc.name == "temporary" && stats.Pending != 1 {
						t.Fatalf("temporary failure discarded: %+v", stats)
					}
					if tc.name == "permanent" && stats.Failed != 1 {
						t.Fatalf("permanent failure not retained: %+v", stats)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("other bot's delivery blocked")
				}
				time.Sleep(5 * time.Millisecond)
			}
			ready := w.queue.Due(time.Now().Add(time.Second), 10, nil)
			if len(ready) != 0 {
				t.Fatal("failed or rate-limited delivery became ready too early")
			}
		})
	}
}
