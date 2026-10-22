// Package modem receives SMS and incoming-call notices through ModemManager's system D-Bus API.
package modem

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
)

const (
	service       = "org.freedesktop.ModemManager1"
	root          = dbus.ObjectPath("/org/freedesktop/ModemManager1")
	modemIF       = service + ".Modem"
	messagingIF   = modemIF + ".Messaging"
	smsIF         = service + ".Sms"
	simIF         = service + ".Sim"
	propertiesIF  = "org.freedesktop.DBus.Properties"
	objectsIF     = "org.freedesktop.DBus.ObjectManager"
	stateDisabled = int32(3)
	smsReceived   = uint32(3)
	pduDeliver    = uint32(1)
	maxTextBytes  = 256 * 1024
)

// ErrSourcesChanged asks the supervisor to rebuild RAM state from retained modem originals.
var ErrSourcesChanged = errors.New("modem source identity changed; restart to rebuild the memory queue")

// Kind distinguishes retained SMS originals from passive incoming-call notices.
type Kind uint8

const (
	SMS Kind = iota
	IncomingCall
)

// Message belongs to one SIM and one ModemManager object lifetime.
type Message struct {
	Kind      Kind
	SourceKey string
	SIMID     string
	Sender    string
	Text      string
	Timestamp string
	Path      dbus.ObjectPath
	Modem     dbus.ObjectPath
	owner     string
	removed   bool
}

func (m Message) belongsTo(device Device) bool {
	return m.owner == device.owner && m.Modem == device.Path && m.SIMID == device.SIMID
}

// Device describes a discovered modem without exposing its full SIM identifier in JSON.
type Device struct {
	Path       dbus.ObjectPath `json:"path"`
	Model      string          `json:"model"`
	State      int32           `json:"state"`
	SIMSuffix  string          `json:"sim_suffix,omitempty"`
	Interfaces []string        `json:"network_interfaces"`
	Voice      bool            `json:"voice"`
	SIMID      string          `json:"-"`
	owner      string
}

// Client owns a private D-Bus connection; Close interrupts blocked bus operations.
type Client struct {
	conn   *dbus.Conn
	bootID string
	log    *slog.Logger
	wake   chan struct{}
	mu     sync.Mutex
	// Ambiguous deletes stay in RAM until a same-owner snapshot confirms their outcome.
	deletions map[string]Message
	calls     map[string]callNotice
}

// Open connects to the system bus without starting a cellular data connection.
func Open(logger *slog.Logger) (*Client, error) {
	// Scope source identity to this OS boot and the daemon's unique bus owner.
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, fmt.Errorf("read boot identity: %w", err)
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, safeError("connect system bus", err)
	}
	return &Client{conn: conn, bootID: strings.TrimSpace(string(boot)), log: logger, wake: make(chan struct{}, 1)}, nil
}

// Close releases the client's system-bus connection.
func (c *Client) Close() error { return c.conn.Close() }

// Wake requests a fresh source snapshot after a delivery has been acknowledged.
func (c *Client) Wake() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Devices returns current modems and checks that their known host network ports are down.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	// Verify the persistent protection independently of ModemManager's port discovery.
	if err := CheckNetwork(); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var owner string
	if err := c.conn.BusObject().CallWithContext(callCtx, "org.freedesktop.DBus.GetNameOwner", 0, service).
		Store(&owner); err != nil {
		return nil, safeError("find ModemManager", err)
	}
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := c.conn.Object(owner, root).CallWithContext(callCtx, objectsIF+".GetManagedObjects", 0).
		Store(&objects); err != nil {
		return nil, safeError("discover modems", err)
	}

	// Resolve SIM identity from its owner and preserve unknown identity as absent.
	var devices []Device
	for path, interfaces := range objects {
		props, found := interfaces[modemIF]
		if !found {
			continue
		}
		d := Device{Path: path, owner: owner}
		_, d.Voice = interfaces[voiceIF]
		if value, ok := props["Model"]; ok {
			_ = value.Store(&d.Model)
		}
		if value, ok := props["State"]; ok {
			_ = value.Store(&d.State)
		}
		var ports []struct {
			Name string
			Type uint32
		}
		if value, ok := props["Ports"]; ok {
			if err := value.Store(&ports); err != nil {
				return nil, errors.New("invalid modem port properties")
			}
		}
		for _, port := range ports {
			iface, err := net.InterfaceByName(port.Name)
			if err != nil {
				continue
			}
			if err := checkInterface(iface); err != nil {
				return nil, err
			}
			d.Interfaces = append(d.Interfaces, port.Name)
		}
		var sim dbus.ObjectPath
		if value, ok := props["Sim"]; ok {
			_ = value.Store(&sim)
		}
		if sim.IsValid() && sim != "/" {
			values, err := c.properties(callCtx, owner, sim, simIF)
			if err != nil {
				return nil, err
			}
			if value, ok := values["SimIdentifier"]; ok {
				_ = value.Store(&d.SIMID)
			}
			if len(d.SIMID) >= 4 {
				d.SIMSuffix = d.SIMID[len(d.SIMID)-4:]
			}
		}
		devices = append(devices, d)
	}
	return devices, nil
}

// Run subscribes before the initial snapshot and reconciles after events and every minute.
// It enables an already-discovered disabled modem for SMS; it never creates or connects a bearer.
func (c *Client) Run(ctx context.Context, handle func(context.Context, Message) error) error {
	// Install both match rules before asking for any existing device or message.
	signals := make(chan *dbus.Signal, 128)
	c.conn.Signal(signals)
	defer c.conn.RemoveSignal(signals)
	rules := [][]dbus.MatchOption{
		{dbus.WithMatchSender(service), dbus.WithMatchPathNamespace(root)},
		{dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchInterface("org.freedesktop.DBus"),
			dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, service)},
	}
	for _, rule := range rules {
		if err := c.conn.AddMatchSignalContext(ctx, rule...); err != nil {
			return safeError("subscribe modem events", err)
		}
		defer c.conn.RemoveMatchSignal(rule...)
	}

	// Coalesce bursts of property updates without losing reconciliation after daemon restart.
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	trigger := time.NewTimer(0)
	defer trigger.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.conn.Context().Done():
			return errors.New("system bus disconnected")
		case signal, ok := <-signals:
			if !ok {
				return errors.New("modem signal stream closed")
			}
			if sourcesChanged(signal) {
				return ErrSourcesChanged
			}
			trigger.Reset(200 * time.Millisecond)
		case <-ticker.C:
			trigger.Reset(0)
		case <-c.wake:
			trigger.Reset(0)
		case <-trigger.C:
			if err := c.reconcile(ctx, handle); err != nil {
				if errors.Is(err, ErrNetworkActive) {
					return err
				}
				c.log.Warn("modem reconciliation deferred", "error", err)
			}
		}
	}
}

func sourcesChanged(signal *dbus.Signal) bool {
	// Source objects cannot survive owner replacement, modem removal, or SIM identity changes.
	switch signal.Name {
	case "org.freedesktop.DBus.NameOwnerChanged":
		if len(signal.Body) != 3 {
			return false
		}
		name, _ := signal.Body[0].(string)
		previous, _ := signal.Body[1].(string)
		return name == service && previous != ""
	case objectsIF + ".InterfacesRemoved":
		if len(signal.Body) != 2 {
			return false
		}
		interfaces, _ := signal.Body[1].([]string)
		for _, iface := range interfaces {
			if iface == modemIF {
				return true
			}
		}
	case propertiesIF + ".PropertiesChanged":
		if len(signal.Body) != 3 {
			return false
		}
		iface, _ := signal.Body[0].(string)
		changed, _ := signal.Body[1].(map[string]dbus.Variant)
		switch iface {
		case modemIF:
			_, found := changed["Sim"]
			return found
		case simIF:
			_, found := changed["SimIdentifier"]
			return found
		}
	}
	return false
}

func (c *Client) reconcile(ctx context.Context, handle func(context.Context, Message) error) error {
	// Refresh devices and identities rather than retaining stale paths across hotplug.
	devices, err := c.Devices(ctx)
	if err != nil {
		return err
	}
	for _, device := range devices {
		if device.State == stateDisabled {
			callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			err := c.conn.Object(device.owner, device.Path).
				CallWithContext(callCtx, modemIF+".Enable", 0, true).Err
			cancel()
			if err != nil {
				c.log.Warn("enable SMS modem failed", "modem", device.Path, "error", safeError("enable modem", err))
			}
			continue
		}

		// SMS and voice discovery fail independently; neither interface can suppress the other.
		if err := c.reconcileMessages(ctx, device, handle); err != nil {
			c.log.Warn("SMS reconciliation deferred", "modem", device.Path, "error", err)
		}
		if err := c.reconcileCalls(ctx, device, handle); err != nil {
			c.log.Warn("call reconciliation deferred", "modem", device.Path, "error", err)
		}
	}
	return nil
}

func (c *Client) retryDeletions(ctx context.Context, device Device, paths []dbus.ObjectPath,
	handle func(context.Context, Message) error) {
	// Only an authoritative snapshot from the same modem, SIM and unique bus owner proves absence.
	present := make(map[dbus.ObjectPath]struct{}, len(paths))
	for _, path := range paths {
		present[path] = struct{}{}
	}
	c.mu.Lock()
	var removed []Message
	for _, message := range c.deletions {
		if !message.belongsTo(device) {
			continue
		}
		if _, found := present[message.Path]; found {
			continue
		}
		message.removed = true
		removed = append(removed, message)
	}
	c.mu.Unlock()

	// Revisit the acknowledged source so the application can release its existing queue entry.
	for _, message := range removed {
		if err := handle(ctx, message); err != nil {
			c.log.Error("completed SMS release deferred", "modem", device.Path, "error", err)
			continue
		}
		c.mu.Lock()
		delete(c.deletions, message.SourceKey)
		c.mu.Unlock()
	}
}

// Delete releases an SMS source or acknowledges a call notice without controlling the call.
func (c *Client) Delete(ctx context.Context, message Message) error {
	// A call acknowledgment changes only local RAM; deleting a ModemManager Call could hang it up.
	if message.Kind == IncomingCall {
		c.mu.Lock()
		defer c.mu.Unlock()
		notice, found := c.calls[message.SourceKey]
		if !found {
			return errors.New("call notification source unavailable")
		}
		notice.acknowledged = true
		c.calls[message.SourceKey] = notice
		return nil
	}
	if message.Kind != SMS {
		return errors.New("unsupported notification kind")
	}

	// Reconciliation can confirm a timed-out delete without issuing a second wire request.
	if message.removed {
		return nil
	}

	// Use the unique owner so a restarted daemon cannot interpret a reused object path.
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err := c.conn.Object(message.owner, message.Modem).
		CallWithContext(callCtx, messagingIF+".Delete", 0, message.Path).Err

	// A failed reply leaves the outcome unknown; keep the source available for snapshot reconciliation.
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		if c.deletions == nil {
			c.deletions = make(map[string]Message)
		}
		c.deletions[message.SourceKey] = message
		return safeError("release stored SMS", err)
	}
	delete(c.deletions, message.SourceKey)
	return nil
}

func (c *Client) properties(ctx context.Context, owner string, path dbus.ObjectPath,
	iface string) (map[string]dbus.Variant, error) {
	var values map[string]dbus.Variant
	if err := c.conn.Object(owner, path).CallWithContext(ctx, propertiesIF+".GetAll", 0, iface).
		Store(&values); err != nil {
		return nil, safeError("read modem properties", err)
	}
	return values, nil
}

func incoming(boot string, device Device, path dbus.ObjectPath, values map[string]dbus.Variant) (Message, bool) {
	// Received state, incoming PDU type, and text representation must all be explicit.
	state, ok := values["State"].Value().(uint32)
	if !ok || state != smsReceived {
		return Message{}, false
	}
	kind, ok := values["PduType"].Value().(uint32)
	if !ok || kind != pduDeliver {
		return Message{}, false
	}
	text, ok := values["Text"].Value().(string)
	if !ok || !utf8.ValidString(text) || len(text) > maxTextBytes {
		return Message{}, false
	}
	if data, ok := values["Data"].Value().([]byte); ok && len(data) > 0 {
		return Message{}, false
	}

	// Object identity deduplicates only within the owner lifetime, never by SMS text or time.
	message := Message{SIMID: device.SIMID, Text: text, Path: path, Modem: device.Path, owner: device.owner}
	if value, ok := values["Number"]; ok {
		_ = value.Store(&message.Sender)
	}
	if value, ok := values["Timestamp"]; ok {
		_ = value.Store(&message.Timestamp)
	}
	message.SourceKey = sourceKey(boot, device, path)
	return message, true
}

func sourceKey(boot string, device Device, path dbus.ObjectPath) string {
	key := strings.Join([]string{boot, device.owner, string(device.Path), string(path), device.SIMID}, "\x00")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
}

func safeError(operation string, err error) error {
	// D-Bus error bodies can contain device data; retain only their stable names.
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	var failure dbus.Error
	if errors.As(err, &failure) {
		return fmt.Errorf("%s: %s", operation, failure.Name)
	}
	return fmt.Errorf("%s: bus operation failed", operation)
}
