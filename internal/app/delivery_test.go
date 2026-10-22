package app

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/samuelncui/djisimbox/internal/queue"
	"github.com/samuelncui/djisimbox/internal/telegram"
)

type observedSender struct {
	calls chan string
	err   error
}

func (s observedSender) Send(_ context.Context, chat, _ string) (int64, error) {
	s.calls <- chat
	return 123, s.err
}

func TestRateLimitedBotCannotFillBatchesAndStarveOtherBots(t *testing.T) {
	// Put enough chats ahead of another bot to occupy a complete batch after the first 429 response.
	w, _ := fixture(t, 10)
	blocked := observedSender{calls: make(chan string, 66),
		err: &telegram.Error{Code: 429, RetryAfter: time.Hour}}
	available := observedSender{calls: make(chan string, 1)}
	w.bots = map[string]sender{"limited": blocked, "available": available}
	recipients := make([]queue.Recipient, 0, 66)
	for i := 1; i <= 65; i++ {
		recipients = append(recipients, queue.Recipient{Bot: "limited", ChatID: strconv.Itoa(i)})
	}
	recipients = append(recipients, queue.Recipient{Bot: "available", ChatID: "1000"})
	if _, _, err := w.queue.Accept(queue.Message{SourceKey: "many-chats"}, recipients, []string{"text"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.deliver(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("delivery loop stopped: %v", err)
		}
	})

	// The unaffected bot must progress before the rate limit expires, without sending to blocked chats.
	select {
	case <-blocked.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("first bot was never attempted")
	}
	select {
	case chat := <-available.calls:
		if chat != "1000" {
			t.Fatal("wrong recipient selected")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rate-limited bot filled batches and blocked another bot")
	}
	select {
	case <-blocked.calls:
		t.Fatal("bot-wide rate limit was ignored for another chat")
	default:
	}
}
