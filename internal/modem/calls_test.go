package modem

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/samuelncui/djisimbox/internal/queue"
)

func TestIncomingCallRequiresExplicitRingingDirection(t *testing.T) {
	// Only incoming ringing or waiting calls become notices; historical calls stay untouched.
	device := Device{owner: ":1.2", Path: root + "/Modem/0", SIMID: "8901000000000000001"}
	observed := time.Date(2026, 10, 22, 12, 0, 0, 0, time.FixedZone("SG", 8*3600))
	for _, tc := range []struct {
		name      string
		direction int32
		state     int32
		ready     bool
	}{
		{"ringing", callIncoming, callRingingIn, true},
		{"waiting", callIncoming, callWaiting, true},
		{"outgoing", 2, callRingingIn, false},
		{"unknown direction", 0, callRingingIn, false},
		{"active", callIncoming, 4, false},
		{"terminated", callIncoming, callTerminated, false},
		{"unknown state", callIncoming, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Omit Number to verify that lack of caller ID never invents a phone number.
			values := map[string]dbus.Variant{"Direction": dbus.MakeVariant(tc.direction), "State": dbus.MakeVariant(tc.state)}
			message, ready := incomingCall("boot", device, root+"/Call/0", values, observed)
			if ready != tc.ready {
				t.Fatalf("ready = %t, want %t", ready, tc.ready)
			}
			if !ready {
				return
			}
			if message.Kind != IncomingCall || message.Sender != "" || message.SIMID != device.SIMID ||
				message.Timestamp != "2026-10-22T12:00:00+08:00" {
				t.Fatalf("call metadata changed: %+v", message)
			}

			// Polling time changes do not create a second identity for the same live call.
			again, _ := incomingCall("boot", device, message.Path, values, observed.Add(time.Second))
			other, _ := incomingCall("boot", device, root+"/Call/1", values, observed)
			if again.SourceKey != message.SourceKey || other.SourceKey == message.SourceKey {
				t.Fatal("call identity used observation time or collapsed distinct calls")
			}
		})
	}
	if _, ready := incomingCall("boot", device, root+"/Call/0", nil, observed); ready {
		t.Fatal("missing call properties inferred ringing")
	}
}

type fakeCalls struct {
	present  atomic.Bool
	state    atomic.Int32
	omit     atomic.Bool
	controls atomic.Int64
	read     chan struct{}
}

func (f *fakeCalls) DeleteCall(dbus.ObjectPath) *dbus.Error {
	f.controls.Add(1)
	return dbus.NewError("org.example.Forbidden", nil)
}

func (f *fakeCalls) Accept() *dbus.Error { return f.DeleteCall("/") }
func (f *fakeCalls) Hangup() *dbus.Error { return f.DeleteCall("/") }
func (f *fakeCalls) Start() *dbus.Error  { return f.DeleteCall("/") }

func callFixture(t *testing.T) (context.Context, *Client, *fakeManager, *fakeCalls) {
	t.Helper()
	// Use the actual D-Bus protocol on a private bus, with synthetic modem state only.
	path, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is required for the isolated D-Bus contract test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	process := exec.CommandContext(ctx, path, "--session", "--nofork", "--print-address=1")
	out, err := process.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
	reader := bufio.NewScanner(out)
	if !reader.Scan() {
		t.Fatal("private bus did not publish an address")
	}
	address := reader.Text()
	manager := fakeService(t, address, 2)
	manager.voice.Store(true)
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := &Client{conn: conn, bootID: "synthetic-boot", log: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		wake: make(chan struct{}, 1)}

	// Export read-only call metadata and fail every call-control method if the implementation invokes it.
	voice := &fakeCalls{read: make(chan struct{}, 16)}
	voice.present.Store(true)
	voice.state.Store(callRingingIn)
	for _, iface := range []struct {
		path dbus.ObjectPath
		name string
	}{
		{root + "/Modem/0", voiceIF}, {root + "/Call/0", callIF},
	} {
		if err := manager.conn.Export(voice, iface.path, iface.name); err != nil {
			t.Fatal(err)
		}
	}
	// This modem's SMS GetAll is deliberately unsupported, proving voice reads remain independent.
	if err := manager.conn.Export(fakeProperties{voiceIF, func() map[string]dbus.Variant {
		if voice.omit.Load() {
			return map[string]dbus.Variant{}
		}
		var paths []dbus.ObjectPath
		if voice.present.Load() {
			paths = append(paths, root+"/Call/0")
		}
		return map[string]dbus.Variant{"Calls": dbus.MakeVariant(paths)}
	}}, root+"/Modem/0", propertiesIF); err != nil {
		t.Fatal(err)
	}
	if err := manager.conn.Export(fakeProperties{callIF, func() map[string]dbus.Variant {
		values := map[string]dbus.Variant{"State": dbus.MakeVariant(voice.state.Load()),
			"Direction": dbus.MakeVariant(callIncoming), "Number": dbus.MakeVariant("+1234")}
		select {
		case voice.read <- struct{}{}:
		default:
		}
		return values
	}}, root+"/Call/0", propertiesIF); err != nil {
		t.Fatal(err)
	}
	return ctx, c, manager, voice
}

func TestDBusCallAddedSignalTriggersNotification(t *testing.T) {
	// Start with a non-ringing object and wait for the receiver's subscribed startup snapshot.
	parent, c, manager, voice := callFixture(t)
	voice.state.Store(0)
	ctx, cancel := context.WithCancel(parent)
	notices := make(chan Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, func(ctx context.Context, message Message) error {
			// The passive source can be acknowledged without changing the network call.
			notices <- message
			return c.Delete(ctx, message)
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("call receiver stopped: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("call receiver did not stop")
		}
	})
	select {
	case <-voice.read:
	case <-ctx.Done():
		t.Fatal("startup call snapshot missing")
	}

	// A real CallAdded signal must wake reconciliation without waiting for its minute-long fallback.
	voice.state.Store(callRingingIn)
	if err := manager.conn.Emit(root+"/Modem/0", voiceIF+".CallAdded", root+"/Call/0"); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-notices:
		if message.Kind != IncomingCall || message.Sender != "+1234" {
			t.Fatal("signal returned unrelated metadata")
		}
	case <-ctx.Done():
		t.Fatal("CallAdded signal did not trigger notification")
	}
}

func TestCallIntakeFailureDoesNotGrowDeduplicationState(t *testing.T) {
	// Full queues cannot leave uncharged call payloads in an independent cache.
	ctx, c, _, _ := callFixture(t)
	if err := c.reconcile(ctx, func(context.Context, Message) error { return queue.ErrCapacity }); err != nil {
		t.Fatal(err)
	}
	if len(c.calls) != 0 {
		t.Fatal("rejected call retained an unbounded source record")
	}

	// The same still-ringing call remains eligible after capacity becomes available.
	notices := 0
	if err := c.reconcile(ctx, func(ctx context.Context, message Message) error {
		notices++
		return c.Delete(ctx, message)
	}); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatal("failed intake suppressed a later retry")
	}
	if err := c.Delete(ctx, Message{Kind: IncomingCall, SourceKey: "unknown"}); err == nil {
		t.Fatal("unknown call acknowledgment did not fail locally")
	}
}

func TestDBusCallNoticeSurvivesRingingAndUsesNoCallControl(t *testing.T) {
	// Admit the call through the same source/acknowledgment contract used by the application.
	ctx, c, manager, voice := callFixture(t)
	q, err := queue.New(1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	handle := func(ctx context.Context, message Message) error {
		// Queue one independent notification, retaining it until every destination succeeds.
		if message.Kind != IncomingCall || message.Sender != "+1234" {
			t.Fatal("call parser returned an unrelated event")
		}
		id, fresh, err := q.Accept(queue.Message{SourceKey: message.SourceKey},
			[]queue.Recipient{{Bot: "test", ChatID: "123"}}, []string{"call notice"})
		if err != nil {
			return err
		}
		if fresh {
			created++
		}
		if !q.Complete(id) {
			return nil
		}

		// A source acknowledgment for a call must be local, even after its call object disappears.
		if err := c.Delete(ctx, message); err != nil {
			return err
		}
		return q.Release(id)
	}
	for range 2 {
		if err := c.reconcile(ctx, handle); err != nil {
			t.Fatal(err)
		}
	}
	if stats := q.Stats(); created != 1 || stats.Messages != 1 || stats.Pending != 1 {
		t.Fatalf("ring updates duplicated the notice: created=%d stats=%+v", created, stats)
	}

	// The network call can end before Telegram is available, while its queued notice remains deliverable.
	voice.present.Store(false)
	if err := c.reconcile(ctx, handle); err != nil {
		t.Fatal(err)
	}
	due := q.Due(time.Now(), 1, nil)
	if len(due) != 1 {
		t.Fatal("call ending discarded its notification")
	}
	if err := q.Delivered(due[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := c.reconcile(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if stats := q.Stats(); stats.Messages != 0 || stats.Bytes != 0 || stats.Forwarded != 1 || len(c.calls) != 0 {
		t.Fatalf("completed call notification did not release capacity: %+v", stats)
	}
	if voice.controls.Load() != 0 || manager.deletes.Load() != 0 {
		t.Fatal("notification changed a network call or SMS storage")
	}
}

func TestDBusAcknowledgedCallDoesNotNotifyOnEveryRing(t *testing.T) {
	// Acknowledge immediately so subsequent snapshots exercise deduplication after queue release.
	ctx, c, manager, voice := callFixture(t)
	notices := 0
	handle := func(ctx context.Context, message Message) error {
		notices++
		return c.Delete(ctx, message)
	}
	for range 3 {
		if err := c.reconcile(ctx, handle); err != nil {
			t.Fatal(err)
		}
	}
	if notices != 1 || len(c.calls) != 1 {
		t.Fatal("acknowledged ringing call was reintroduced")
	}

	// An unavailable snapshot cannot erase the deduplication state and create another notification.
	voice.omit.Store(true)
	if err := c.reconcile(ctx, handle); err != nil {
		t.Fatal(err)
	}
	voice.omit.Store(false)
	if err := c.reconcile(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatal("missing Calls property caused duplicate delivery")
	}

	// Terminated call objects may remain in ModemManager without growing our cache or notifying again.
	voice.state.Store(callTerminated)
	for range 2 {
		if err := c.reconcile(ctx, handle); err != nil {
			t.Fatal(err)
		}
	}
	if notices != 1 || len(c.calls) != 0 || voice.controls.Load() != 0 || manager.deletes.Load() != 0 {
		t.Fatal("terminated call was retained, repeated or controlled")
	}
}
