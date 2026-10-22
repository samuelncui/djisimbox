// Package app coordinates in-memory SMS/call intake and independent Telegram delivery.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samuelncui/djisimbox/internal/config"
	"github.com/samuelncui/djisimbox/internal/modem"
	"github.com/samuelncui/djisimbox/internal/queue"
	"github.com/samuelncui/djisimbox/internal/telegram"
)

// Inbox owns discovery and source acknowledgment: SMS deletion or local call-notice completion.
type Inbox interface {
	Run(context.Context, func(context.Context, modem.Message) error) error
	Delete(context.Context, modem.Message) error
	Wake()
}

type sender interface {
	Send(context.Context, string, string) (int64, error)
}

type worker struct {
	config *config.Config
	queue  *queue.Queue
	inbox  Inbox
	bots   map[string]sender
	log    *slog.Logger
	wake   chan struct{}
}

// Run keeps receiving notifications while the single delivery loop waits on Internet requests.
func Run(ctx context.Context, cfg *config.Config, pending *queue.Queue, inbox Inbox, logger *slog.Logger) error {
	// Construct immutable bot clients without retaining credentials in operational logs.
	w := &worker{config: cfg, queue: pending, inbox: inbox, log: logger,
		bots: make(map[string]sender, len(cfg.Bots)), wake: make(chan struct{}, 1)}
	for name, token := range cfg.Bots {
		client, err := telegram.New(token, &http.Client{Timeout: 20 * time.Second})
		if err != nil {
			return fmt.Errorf("initialize Telegram client: %w", err)
		}
		w.bots[name] = client
	}

	// Every background operation has one cancellation owner, an error result, and a joined lifetime.
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, 2)
	var tasks sync.WaitGroup
	tasks.Add(2)
	go func() {
		defer tasks.Done()
		results <- inbox.Run(ctx, w.accept)
	}()
	go func() {
		defer tasks.Done()
		results <- w.deliver(ctx)
	}()
	first := <-results
	cancel()
	second := <-results
	tasks.Wait()
	if parent.Err() != nil {
		return nil
	}
	return errors.Join(first, second)
}

func (w *worker) accept(ctx context.Context, message modem.Message) error {
	// Resolve routing once; an unknown SIM is retained privately without a default recipient.
	route, known := w.config.Route(message.SIMID)
	var recipients []queue.Recipient
	var parts []string
	if known {
		recipients = make([]queue.Recipient, 0, len(route.Recipients))
		for _, recipient := range route.Recipients {
			recipients = append(recipients, queue.Recipient{Bot: recipient.Bot, ChatID: recipient.ChatID})
		}
		switch message.Kind {
		case modem.SMS:
			parts = telegram.Format(telegram.Message{Label: route.Name, Sender: message.Sender,
				Timestamp: message.Timestamp, Text: message.Text})
		case modem.IncomingCall:
			parts = []string{telegram.FormatCall(telegram.Call{Label: route.Name, Number: message.Sender,
				ObservedAt: message.Timestamp})}
		default:
			return errors.New("unsupported notification kind")
		}
	}
	id, created, err := w.queue.Accept(queue.Message{SourceKey: message.SourceKey, SIMID: message.SIMID,
		Sender: message.Sender, Text: message.Text, Timestamp: message.Timestamp}, recipients, parts)
	if err != nil {
		return fmt.Errorf("queue incoming notification: %w", err)
	}
	if !known {
		if created {
			w.log.Warn("notification quarantined; SIM has no configured recipients", "message_id", id, "modem", message.Modem)
		}
		return nil
	}

	// Acknowledge sources only after every recipient confirms delivery; call acknowledgment is local RAM only.
	if created {
		w.log.Info("notification queued in memory", "message_id", id, "sim", route.Name,
			"kind", message.Kind, "destinations", len(recipients))
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
	if !w.queue.Complete(id) {
		return nil
	}
	if err := w.inbox.Delete(ctx, message); err != nil {
		return fmt.Errorf("notification delivered; source retained for release retry: %w", err)
	}
	if err := w.queue.Release(id); err != nil {
		return err
	}
	w.log.Info("notification delivered to all recipients; source released", "message_id", id)
	return nil
}

func (w *worker) deliver(ctx context.Context) error {
	// One worker owns sends; idle and retry waits stay in RAM and wake on fresh intake.
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	botNext := make(map[string]time.Time)
	chatNext := make(map[string]time.Time)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		// Exclude throttled destinations before the batch limit so other bots can still progress.
		now := time.Now()
		due := w.queue.Due(now, 64, func(delivery queue.Delivery) bool {
			if now.Before(botNext[delivery.Bot]) {
				return false
			}
			chat := delivery.Bot + "/" + canonicalChat(delivery.ChatID)
			return !now.Before(chatNext[chat])
		})

		// Recheck pacing because an earlier request in this batch can throttle another destination.
		for _, delivery := range due {
			now := time.Now()
			chat := delivery.Bot + "/" + canonicalChat(delivery.ChatID)
			if now.Before(botNext[delivery.Bot]) || now.Before(chatNext[chat]) {
				continue
			}

			// Missing credentials postpone only this recipient without invoking an HTTP client.
			client, found := w.bots[delivery.Bot]
			if !found {
				if err := w.queue.Retry(delivery.ID, now.Add(5*time.Minute)); err != nil {
					return err
				}
				w.log.Warn("delivery waiting for configured bot", "delivery_id", delivery.ID)
				continue
			}

			// Pace subsequent requests from the completed attempt, including failed sends.
			_, sendErr := client.Send(ctx, delivery.ChatID, delivery.Text)
			if ctx.Err() != nil {
				return nil
			}
			finished := time.Now()
			botNext[delivery.Bot] = finished.Add(100 * time.Millisecond)
			interval := 1100 * time.Millisecond
			if strings.HasPrefix(canonicalChat(delivery.ChatID), "-") {
				interval = 3100 * time.Millisecond
			}
			chatNext[chat] = finished.Add(interval)

			// Confirmed chunks advance independently; permanent errors retain the source for attention.
			if sendErr == nil {
				if err := w.queue.Delivered(delivery.ID); err != nil {
					return err
				}
				w.log.Info("Telegram delivery confirmed", "delivery_id", delivery.ID)
				w.inbox.Wake()
				continue
			}
			var failure *telegram.Error
			if errors.As(sendErr, &failure) && failure.Permanent {
				if err := w.queue.Fail(delivery.ID); err != nil {
					return err
				}
				w.log.Error("Telegram destination requires attention", "delivery_id", delivery.ID, "code", failure.Code)
				continue
			}

			// Transient failures honor Telegram's bot-wide retry time without blocking other bots.
			delay := retryDelay(delivery.Attempts)
			if failure != nil && failure.RetryAfter > delay {
				delay = failure.RetryAfter
			}
			when := finished.Add(delay)
			if failure != nil && failure.Code == http.StatusTooManyRequests {
				botNext[delivery.Bot] = when
			}
			if err := w.queue.Retry(delivery.ID, when); err != nil {
				return err
			}
			w.log.Warn("Telegram delivery deferred", "delivery_id", delivery.ID, "retry_seconds", int64(delay/time.Second))
		}

		// Idle waits are canceled immediately during service shutdown.
		delay := time.Minute
		if next := w.queue.NextDue(); !next.IsZero() {
			delay = max(time.Until(next), 250*time.Millisecond)
		}
		timer.Reset(delay)
		select {
		case <-ctx.Done():
			return nil
		case <-w.wake:
		case <-timer.C:
		}
	}
}

func retryDelay(attempts int) time.Duration {
	return min(5*time.Second<<min(max(attempts, 0), 8), 15*time.Minute)
}

func canonicalChat(value string) string {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return value
	}
	return strconv.FormatInt(id, 10)
}
