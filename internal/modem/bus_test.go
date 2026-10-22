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

type fakeProperties struct {
	iface  string
	values func() map[string]dbus.Variant
}

func (p fakeProperties) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface != p.iface {
		return nil, dbus.NewError("org.freedesktop.DBus.Error.UnknownInterface", nil)
	}
	return p.values(), nil
}

type fakeManager struct {
	conn    *dbus.Conn
	state   atomic.Uint32
	deletes atomic.Int64
	read    chan struct{}
	voice   atomic.Bool
}

func (m *fakeManager) GetManagedObjects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, *dbus.Error) {
	objects := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
		root + "/Modem/0": {modemIF: {
			"State": dbus.MakeVariant(int32(8)), "Model": dbus.MakeVariant("Synthetic modem"),
			"Sim": dbus.MakeVariant(root + "/SIM/0"),
		}},
	}
	if m.voice.Load() {
		objects[root+"/Modem/0"][voiceIF] = map[string]dbus.Variant{}
	}
	return objects, nil
}

func (m *fakeManager) Delete(path dbus.ObjectPath) *dbus.Error {
	if path != root+"/SMS/0" {
		return dbus.NewError("org.freedesktop.ModemManager1.Error.Core.NotFound", nil)
	}
	m.deletes.Add(1)
	return nil
}

func fakeService(t *testing.T, address string, state uint32) *fakeManager {
	t.Helper()
	// Export the real D-Bus wire signatures on an isolated bus, without any physical modem.
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	m := &fakeManager{conn: conn, read: make(chan struct{}, 4)}
	m.state.Store(state)
	if err := conn.Export(m, root, objectsIF); err != nil {
		t.Fatal(err)
	}
	if err := conn.Export(m, root+"/Modem/0", messagingIF); err != nil {
		t.Fatal(err)
	}
	for _, object := range []struct {
		path  dbus.ObjectPath
		props fakeProperties
	}{
		{root + "/SIM/0", fakeProperties{simIF, func() map[string]dbus.Variant {
			return map[string]dbus.Variant{"SimIdentifier": dbus.MakeVariant("8901000000000000001")}
		}}},
		{root + "/Modem/0", fakeProperties{messagingIF, func() map[string]dbus.Variant {
			return map[string]dbus.Variant{"Messages": dbus.MakeVariant([]dbus.ObjectPath{root + "/SMS/0"})}
		}}},
		{root + "/SMS/0", fakeProperties{smsIF, func() map[string]dbus.Variant {
			// Notify the test only after taking this message state's immutable snapshot.
			values := map[string]dbus.Variant{"State": dbus.MakeVariant(m.state.Load()),
				"PduType": dbus.MakeVariant(pduDeliver), "Text": dbus.MakeVariant("你好 🌍")}
			select {
			case m.read <- struct{}{}:
			default:
			}
			return values
		}}},
	} {
		if err := conn.Export(object.props, object.path, propertiesIF); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.RequestName(service, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDBusReceptionAndDaemonReplacement(t *testing.T) {
	// Own an isolated bus process and terminate it after all connections and goroutines finish.
	path, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is required for the isolated D-Bus contract test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	// Keep the bus alive while receiver cancellation is joined during cleanup.
	busCtx, stopBus := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(stopBus)
	process := exec.CommandContext(busCtx, path, "--session", "--nofork", "--print-address=1")
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
	first := fakeService(t, address, 2)
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := &Client{conn: conn, bootID: "synthetic-boot", log: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		wake: make(chan struct{}, 1)}

	// Observe startup storage before promoting the partial SMS through a real PropertiesChanged signal.
	reads := make(chan Message, 4)
	done := make(chan error, 1)
	stopped := false
	go func() {
		done <- c.Run(ctx, func(_ context.Context, message Message) error {
			reads <- message
			return nil
		})
	}()
	t.Cleanup(func() {
		cancel()
		if stopped {
			return
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("receiver stopped: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("receiver did not stop")
		}
	})
	select {
	case <-first.read:
	case <-ctx.Done():
		t.Fatal("startup SMS snapshot missing")
	}
	first.state.Store(smsReceived)
	if err := first.conn.Emit(root+"/SMS/0", propertiesIF+".PropertiesChanged", smsIF,
		map[string]dbus.Variant{"State": dbus.MakeVariant(smsReceived)}, []string{}); err != nil {
		t.Fatal(err)
	}
	var original Message
	select {
	case original = <-reads:
	case <-ctx.Done():
		t.Fatal("complete SMS notification missing")
	}
	if original.Text != "你好 🌍" || original.SIMID != "8901000000000000001" {
		t.Fatal("wire properties or SIM ownership changed")
	}

	// Exit on owner replacement so the supervisor rebuilds the RAM queue rather than orphaning old entries.
	if err := first.conn.Close(); err != nil {
		t.Fatal(err)
	}
	second := fakeService(t, address, smsReceived)
	select {
	case err := <-done:
		stopped = true
		if !errors.Is(err, ErrSourcesChanged) {
			t.Fatalf("owner replacement did not request queue rebuild: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("old receiver did not exit on daemon replacement")
	}

	// A fresh receiver finds the retained original under the replacement owner's identity.
	conn2, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn2.Close() })
	c2 := &Client{conn: conn2, bootID: "synthetic-boot", log: c.log, wake: make(chan struct{}, 1)}
	reads2 := make(chan Message, 4)
	done2 := make(chan error, 1)
	go func() {
		done2 <- c2.Run(ctx, func(_ context.Context, message Message) error {
			reads2 <- message
			return nil
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done2:
			if err != nil {
				t.Errorf("replacement receiver stopped: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("replacement receiver did not stop")
		}
	})
	var replacement Message
	select {
	case replacement = <-reads2:
	case <-ctx.Done():
		t.Fatal("daemon replacement was not reconciled")
	}
	if replacement.SourceKey == original.SourceKey {
		t.Fatal("new daemon reused an old source identity")
	}
	if err := c2.Delete(ctx, original); err == nil || second.deletes.Load() != 0 {
		t.Fatal("old source deletion reached the replacement daemon")
	}
	if err := c2.Delete(ctx, replacement); err != nil || second.deletes.Load() != 1 {
		t.Fatalf("current source deletion failed: %v", err)
	}

	// Delivery acknowledgments can wake reconciliation without waiting for the periodic fallback.
	select {
	case <-second.read:
	default:
	}
	c2.Wake()
	select {
	case <-second.read:
	case <-ctx.Done():
		t.Fatal("acknowledgment did not wake source reconciliation")
	}
}
