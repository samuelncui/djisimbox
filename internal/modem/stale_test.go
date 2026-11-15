package modem

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os/exec"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestStaleIncomplete(t *testing.T) {
	// Only an incoming multipart SMS still receiving after a day is stale; unknown age is kept.
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.FixedZone("+08", 8*3600))
	for _, tc := range []struct {
		name  string
		state uint32
		kind  uint32
		stamp string
		stale bool
	}{
		{"receiving for more than a day", smsReceiving, pduDeliver, "2026-11-14T11:00:00+08", true},
		{"receiving for less than a day", smsReceiving, pduDeliver, "2026-11-14T13:00:00+08", false},
		{"complete", smsReceived, pduDeliver, "2026-10-01T00:00:00+08", false},
		{"status report", smsReceiving, 3, "2026-10-01T00:00:00+08", false},
		{"no timestamp", smsReceiving, pduDeliver, "", false},
		{"unreadable timestamp", smsReceiving, pduDeliver, "yesterday", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]dbus.Variant{"State": dbus.MakeVariant(tc.state), "PduType": dbus.MakeVariant(tc.kind)}
			if tc.stamp != "" {
				values["Timestamp"] = dbus.MakeVariant(tc.stamp)
			}
			if got := staleIncomplete(values, now); got != tc.stale {
				t.Fatalf("staleIncomplete = %t, want %t", got, tc.stale)
			}
		})
	}
}

func TestReconcileRemovesStaleIncompleteSMS(t *testing.T) {
	// A stale incomplete SMS is deleted from the modem and never handed to the application.
	for _, tc := range []struct {
		name    string
		age     time.Duration
		deletes int64
	}{
		{"older than a day", 25 * time.Hour, 1},
		{"still waiting", time.Hour, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, c, manager := smsFixture(t)
			manager.stamp.Store(time.Now().Add(-tc.age).Format(time.RFC3339))
			devices, err := c.Devices(ctx)
			if err != nil || len(devices) != 1 {
				t.Fatalf("devices=%v err=%v", devices, err)
			}

			// Reconcile once; incomplete messages must never reach the handler either way.
			handled := 0
			err = c.reconcileMessages(ctx, devices[0], func(context.Context, Message) error {
				handled++
				return nil
			})
			if err != nil || handled != 0 || manager.deletes.Load() != tc.deletes {
				t.Fatalf("err=%v handled=%d deletes=%d, want deletes=%d", err, handled, manager.deletes.Load(), tc.deletes)
			}
		})
	}
}

func smsFixture(t *testing.T) (context.Context, *Client, *fakeManager) {
	t.Helper()
	// Serve one receiving SMS on a private bus; no voice objects replace the messaging properties.
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

	// Connect the client under test to the synthetic ModemManager.
	manager := fakeService(t, reader.Text(), smsReceiving)
	conn, err := dbus.Connect(reader.Text())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return ctx, &Client{conn: conn, bootID: "synthetic-boot", log: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		wake: make(chan struct{}, 1)}, manager
}
