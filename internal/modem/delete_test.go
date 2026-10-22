package modem

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

type delayedDeletion struct {
	finish chan struct{}
	done   chan struct{}
	calls  atomic.Int64
	absent atomic.Bool
}

func (d *delayedDeletion) Delete(path dbus.ObjectPath) *dbus.Error {
	// A server operation can complete even after the caller's deadline has expired.
	if path != root+"/SMS/0" || d.calls.Add(1) != 1 {
		return dbus.NewError("org.freedesktop.ModemManager1.Error.Core.NotFound", nil)
	}
	<-d.finish
	d.absent.Store(true)
	close(d.done)
	return nil
}

func TestDBusDeleteTimeoutReconcilesMissingSource(t *testing.T) {
	// Isolate a real D-Bus connection and clean up its process after all exported calls finish.
	path, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is required for the isolated D-Bus contract test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
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
	manager := fakeService(t, address, smsReceived)
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := &Client{conn: conn, bootID: "synthetic-boot", log: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		wake: make(chan struct{}, 1)}

	// Hold the server-side delete while independently controlling the authoritative Messages snapshot.
	delayed := &delayedDeletion{finish: make(chan struct{}), done: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-delayed.finish:
		default:
			close(delayed.finish)
		}
	})
	if err := manager.conn.Export(delayed, root+"/Modem/0", messagingIF); err != nil {
		t.Fatal(err)
	}
	var omitMessages atomic.Bool
	if err := manager.conn.Export(fakeProperties{messagingIF, func() map[string]dbus.Variant {
		if omitMessages.Load() {
			return map[string]dbus.Variant{}
		}
		paths := []dbus.ObjectPath{root + "/SMS/0"}
		if delayed.absent.Load() {
			paths = nil
		}
		return map[string]dbus.Variant{"Messages": dbus.MakeVariant(paths)}
	}}, root+"/Modem/0", propertiesIF); err != nil {
		t.Fatal(err)
	}
	var original Message
	if err := c.reconcile(ctx, func(_ context.Context, message Message) error {
		original = message
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if original.SourceKey == "" {
		t.Fatal("initial complete SMS missing")
	}

	// Lose the delete reply, as production does when its request deadline is exceeded.
	deleteCtx, stopDelete := context.WithTimeout(ctx, 100*time.Millisecond)
	err = c.Delete(deleteCtx, original)
	stopDelete()
	if !errors.Is(err, context.DeadlineExceeded) || delayed.calls.Load() != 1 {
		t.Fatalf("delete did not time out at its caller: %v", err)
	}
	close(delayed.finish)
	select {
	case <-delayed.done:
	case <-ctx.Done():
		t.Fatal("server-side delete never completed")
	}

	// Missing snapshot data cannot confirm deletion or cause the application to release capacity.
	released := 0
	handle := func(ctx context.Context, message Message) error {
		// A repeated source must retain its original queue identity and need no second wire delete.
		if message.SourceKey != original.SourceKey {
			t.Fatal("completed source identity changed")
		}
		if err := c.Delete(ctx, message); err != nil {
			return err
		}
		released++
		return nil
	}
	omitMessages.Store(true)
	if err := c.reconcile(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if released != 0 {
		t.Fatal("missing Messages property was treated as deletion confirmation")
	}

	// A valid empty snapshot retries completion once, without another request or repeated notification.
	omitMessages.Store(false)
	if err := c.reconcile(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if released != 1 || delayed.calls.Load() != 1 {
		t.Fatalf("missing completed source was never revisited safely: releases=%d deletes=%d", released, delayed.calls.Load())
	}
	if err := c.reconcile(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatal("completed source notified twice after release")
	}
}

func TestDeletionConfirmationPreservesSourceOwnership(t *testing.T) {
	// Only the original modem, SIM and owner may confirm absence of an uncertain deletion.
	original := Message{SourceKey: "acknowledged-source", SIMID: "8901000000000000001",
		owner: ":1.2", Modem: root + "/Modem/0", Path: root + "/SMS/0"}
	for _, tc := range []struct {
		name     string
		owner    string
		modem    dbus.ObjectPath
		sim      string
		paths    []dbus.ObjectPath
		released bool
	}{
		{"still present", original.owner, original.Modem, original.SIMID, []dbus.ObjectPath{original.Path}, false},
		{"different owner", ":1.3", original.Modem, original.SIMID, nil, false},
		{"different modem", original.owner, root + "/Modem/1", original.SIMID, nil, false},
		{"different SIM", original.owner, original.Modem, "8901000000000000002", nil, false},
		{"unknown SIM", original.owner, original.Modem, "", nil, false},
		{"confirmed absent", original.owner, original.Modem, original.SIMID, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// An acknowledged source remains registered until a matching snapshot confirms its absence.
			c := &Client{log: slog.New(slog.NewJSONHandler(io.Discard, nil)),
				deletions: map[string]Message{original.SourceKey: original}}
			released := false
			c.retryDeletions(context.Background(), Device{owner: tc.owner, Path: tc.modem, SIMID: tc.sim},
				tc.paths, func(ctx context.Context, message Message) error {
					// A confirmed absence completes deletion without accessing a bus connection.
					if message.SourceKey != original.SourceKey {
						t.Fatal("deletion confirmation changed source identity")
					}
					if err := c.Delete(ctx, message); err != nil {
						return err
					}
					released = true
					return nil
				})
			if released != tc.released {
				t.Fatalf("released = %t, want %t", released, tc.released)
			}
			if retained := len(c.deletions) == 1; retained == tc.released {
				t.Fatal("unconfirmed source was lost or confirmed source was retained")
			}
		})
	}
}
