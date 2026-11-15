package modem

import (
	"context"
	"errors"
	"time"

	"github.com/godbus/dbus/v5"
)

func (c *Client) reconcileMessages(ctx context.Context, device Device,
	handle func(context.Context, Message) error) error {
	// An unavailable snapshot leaves originals and uncertain deletions intact.
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	props, err := c.properties(callCtx, device.owner, device.Path, messagingIF)
	cancel()
	if err != nil {
		return err
	}
	value, ok := props["Messages"]
	if !ok {
		return errors.New("SMS path snapshot unavailable")
	}
	var paths []dbus.ObjectPath
	if err := value.Store(&paths); err != nil {
		return errors.New("invalid SMS path properties")
	}
	c.retryDeletions(ctx, device, paths, handle)

	// Incomplete multipart SMS wait on the modem for their missing parts; only complete incoming text or MMS
	// notifications enter the queue.
	for _, path := range paths {
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		values, err := c.properties(callCtx, device.owner, path, smsIF)
		cancel()
		if err != nil {
			c.log.Warn("SMS read deferred", "modem", device.Path, "error", err)
			continue
		}
		if staleIncomplete(values, time.Now()) {
			c.removeStale(ctx, device, path)
			continue
		}
		message, ready := incoming(c.bootID, device, path, values)
		if !ready {
			continue
		}
		if err := handle(ctx, message); err != nil {
			c.log.Error("SMS retained for later intake", "modem", device.Path, "error", err)
		}
	}
	return nil
}

// incompleteTTL bounds how long a multipart SMS may wait for missing parts. Networks retry for hours, but parts
// still missing after a day are not coming, and ModemManager never exposes the partial text.
const incompleteTTL = 24 * time.Hour

// staleIncomplete reports an incoming multipart SMS still missing parts after incompleteTTL. An unknown age keeps it.
func staleIncomplete(values map[string]dbus.Variant, now time.Time) bool {
	state, ok := values["State"].Value().(uint32)
	if !ok || state != smsReceiving {
		return false
	}
	kind, ok := values["PduType"].Value().(uint32)
	if !ok || kind != pduDeliver {
		return false
	}
	stamp, _ := values["Timestamp"].Value().(string)
	sent, ok := parseSMSTime(stamp)
	return ok && now.Sub(sent) > incompleteTTL
}

// removeStale frees the storage held by an incomplete multipart SMS; a failed delete is retried next snapshot.
func (c *Client) removeStale(ctx context.Context, device Device, path dbus.ObjectPath) {
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err := c.conn.Object(device.owner, device.Path).CallWithContext(callCtx, messagingIF+".Delete", 0, path).Err
	if err != nil {
		c.log.Warn("incomplete SMS removal deferred", "modem", device.Path, "error", safeError("remove incomplete SMS", err))
		return
	}
	c.log.Info("removed incomplete multipart SMS whose missing parts never arrived", "modem", device.Path)
}
