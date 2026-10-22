package modem

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestIncomingRequiresCompleteReceivedText(t *testing.T) {
	// SMS objects can exist before a multipart message is complete or represent outgoing traffic.
	device := Device{Path: root + "/Modem/0", owner: ":1.2", SIMID: "8901000000000000001"}
	for _, tc := range []struct {
		name  string
		state uint32
		kind  uint32
		data  []byte
		ready bool
	}{
		{"receiving", 2, pduDeliver, nil, false},
		{"received", smsReceived, pduDeliver, nil, true},
		{"outgoing", smsReceived, 2, nil, false},
		{"report", smsReceived, 3, nil, false},
		{"binary", smsReceived, pduDeliver, []byte{1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Supply the same synthetic Unicode content across the distinct message states.
			values := map[string]dbus.Variant{
				"State": dbus.MakeVariant(tc.state), "PduType": dbus.MakeVariant(tc.kind),
				"Text": dbus.MakeVariant("短信测试 😀"), "Data": dbus.MakeVariant(tc.data),
			}
			message, ready := incoming("boot", device, root+"/SMS/0", values)
			if ready != tc.ready {
				t.Fatalf("ready = %t, want %t", ready, tc.ready)
			}
			if ready && message.Text != "短信测试 😀" {
				t.Fatal("Unicode SMS text changed")
			}
		})
	}
}

func TestSourceIdentityUsesObjectLifetime(t *testing.T) {
	// Identical sender/time/text can be two legitimate SMS; object identity owns deduplication.
	device := Device{Path: root + "/Modem/0", owner: ":1.2", SIMID: "8901000000000000001"}
	values := map[string]dbus.Variant{"State": dbus.MakeVariant(smsReceived),
		"PduType": dbus.MakeVariant(pduDeliver), "Text": dbus.MakeVariant("same text")}
	first, _ := incoming("boot", device, root+"/SMS/0", values)
	again, _ := incoming("boot", device, root+"/SMS/0", values)
	other, _ := incoming("boot", device, root+"/SMS/1", values)
	device.owner = ":1.3"
	restarted, _ := incoming("boot", device, root+"/SMS/0", values)
	if first.SourceKey != again.SourceKey || first.SourceKey == other.SourceKey || first.SourceKey == restarted.SourceKey {
		t.Fatal("source identity collapsed a different SMS or daemon lifetime")
	}
}

func TestUnknownAndUnsupportedSMSRemainAbsent(t *testing.T) {
	// Missing state or representation must never be inferred from plausible text.
	if _, ready := incoming("boot", Device{}, root+"/SMS/0", nil); ready {
		t.Fatal("incomplete properties accepted")
	}
	values := map[string]dbus.Variant{"State": dbus.MakeVariant(smsReceived),
		"PduType": dbus.MakeVariant(pduDeliver), "Text": dbus.MakeVariant(strings.Repeat("x", maxTextBytes+1))}
	if _, ready := incoming("boot", Device{}, root+"/SMS/0", values); ready {
		t.Fatal("oversized SMS accepted")
	}
	values["Text"] = dbus.MakeVariant("known text")
	message, ready := incoming("boot", Device{}, root+"/SMS/0", values)
	if !ready || message.SIMID != "" || message.Sender != "" || message.Timestamp != "" {
		t.Fatal("unknown metadata was invented")
	}
}

func TestBusErrorsOmitDeviceData(t *testing.T) {
	// Only the fixed bus error name may reach operational logs.
	err := safeError("read", dbus.Error{Name: "org.example.Error", Body: []any{"private SMS and SIM data"}})
	if strings.Contains(err.Error(), "private") || !strings.Contains(err.Error(), "org.example.Error") {
		t.Fatal("unsafe D-Bus error classification")
	}
	if !errors.Is(safeError("read", context.Canceled), context.Canceled) {
		t.Fatal("cancellation sentinel lost")
	}
}

func TestSourceReplacementRequiresFreshMemoryQueue(t *testing.T) {
	// Retain normal SMS deletion and state events while rebuilding after source identity changes.
	for _, tc := range []struct {
		name   string
		signal dbus.Signal
		reset  bool
	}{
		{"daemon replaced", dbus.Signal{Name: "org.freedesktop.DBus.NameOwnerChanged",
			Body: []any{service, ":1.1", ":1.2"}}, true},
		{"daemon starts", dbus.Signal{Name: "org.freedesktop.DBus.NameOwnerChanged",
			Body: []any{service, "", ":1.2"}}, false},
		{"modem removed", dbus.Signal{Name: objectsIF + ".InterfacesRemoved",
			Body: []any{root + "/Modem/0", []string{modemIF, messagingIF}}}, true},
		{"SMS deleted", dbus.Signal{Name: objectsIF + ".InterfacesRemoved",
			Body: []any{root + "/SMS/0", []string{smsIF}}}, false},
		{"SIM slot changes", dbus.Signal{Name: propertiesIF + ".PropertiesChanged",
			Body: []any{modemIF, map[string]dbus.Variant{"Sim": dbus.MakeVariant(root + "/SIM/1")}, []string{}}}, true},
		{"SIM identity changes", dbus.Signal{Name: propertiesIF + ".PropertiesChanged",
			Body: []any{simIF, map[string]dbus.Variant{"SimIdentifier": dbus.MakeVariant("new")}, []string{}}}, true},
		{"SMS completes", dbus.Signal{Name: propertiesIF + ".PropertiesChanged",
			Body: []any{smsIF, map[string]dbus.Variant{"State": dbus.MakeVariant(smsReceived)}, []string{}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourcesChanged(&tc.signal); got != tc.reset {
				t.Fatalf("queue reset = %t, want %t", got, tc.reset)
			}
		})
	}
}
