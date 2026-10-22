package modem

import (
	"context"
	"errors"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	voiceIF        = modemIF + ".Voice"
	callIF         = service + ".Call"
	callIncoming   = int32(1)
	callRingingIn  = int32(3)
	callWaiting    = int32(6)
	callTerminated = int32(7)
)

type callNotice struct {
	message      Message
	acknowledged bool
}

func (c *Client) reconcileCalls(ctx context.Context, device Device,
	handle func(context.Context, Message) error) error {
	// Queued notices outlive ringing and retain their first observed metadata until delivery succeeds.
	c.mu.Lock()
	var pending []Message
	for _, notice := range c.calls {
		if notice.acknowledged {
			continue
		}
		if notice.message.belongsTo(device) {
			pending = append(pending, notice.message)
		}
	}
	c.mu.Unlock()
	for _, message := range pending {
		if err := handle(ctx, message); err != nil {
			c.log.Warn("call notification release deferred", "modem", device.Path, "error", err)
		}
	}
	if !device.Voice {
		return nil
	}

	// Read-only properties are sufficient; no call-control or carrier-service methods are invoked.
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	props, err := c.properties(callCtx, device.owner, device.Path, voiceIF)
	cancel()
	if err != nil {
		return err
	}
	value, ok := props["Calls"]
	if !ok {
		return errors.New("call path snapshot unavailable")
	}
	var paths []dbus.ObjectPath
	if err := value.Store(&paths); err != nil {
		return errors.New("invalid call path properties")
	}
	present := make(map[dbus.ObjectPath]struct{}, len(paths))
	ended := make(map[dbus.ObjectPath]struct{})
	for _, path := range paths {
		present[path] = struct{}{}
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		values, err := c.properties(callCtx, device.owner, path, callIF)
		cancel()
		if err != nil {
			c.log.Warn("call read deferred", "modem", device.Path, "error", err)
			continue
		}
		if state, ok := values["State"].Value().(int32); ok && state == callTerminated {
			ended[path] = struct{}{}
			continue
		}
		message, ready := incomingCall(c.bootID, device, path, values, time.Now())
		if !ready {
			continue
		}

		// One live call creates one bounded queue entry, independent of repeated ring/property events.
		c.mu.Lock()
		_, seen := c.calls[message.SourceKey]
		if !seen {
			if c.calls == nil {
				c.calls = make(map[string]callNotice)
			}
			c.calls[message.SourceKey] = callNotice{message: message}
		}
		c.mu.Unlock()
		if seen {
			continue
		}
		if err := handle(ctx, message); err != nil {
			c.mu.Lock()
			delete(c.calls, message.SourceKey)
			c.mu.Unlock()
			c.log.Warn("call notification intake deferred", "modem", device.Path, "error", err)
		}
	}

	// Acknowledged calls need deduplication only until termination or confirmed disappearance.
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, notice := range c.calls {
		if !notice.acknowledged {
			continue
		}
		if !notice.message.belongsTo(device) {
			continue
		}
		_, exists := present[notice.message.Path]
		_, terminated := ended[notice.message.Path]
		if !exists || terminated {
			delete(c.calls, key)
		}
	}
	return nil
}

func incomingCall(boot string, device Device, path dbus.ObjectPath,
	values map[string]dbus.Variant, observed time.Time) (Message, bool) {
	// Direction and ringing/waiting state must be explicit; past or outgoing calls do not notify.
	direction, ok := values["Direction"].Value().(int32)
	if !ok || direction != callIncoming {
		return Message{}, false
	}
	state, ok := values["State"].Value().(int32)
	if !ok {
		return Message{}, false
	}
	if state != callRingingIn && state != callWaiting {
		return Message{}, false
	}
	message := Message{Kind: IncomingCall, SourceKey: sourceKey(boot, device, path), SIMID: device.SIMID,
		Path: path, Modem: device.Path, owner: device.owner, Timestamp: observed.Format(time.RFC3339)}
	if number, ok := values["Number"]; ok {
		_ = number.Store(&message.Sender)
	}
	return message, true
}
