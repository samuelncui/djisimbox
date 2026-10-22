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

	// Incomplete multipart SMS remain on the modem; only complete incoming text enters the queue.
	for _, path := range paths {
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		values, err := c.properties(callCtx, device.owner, path, smsIF)
		cancel()
		if err != nil {
			c.log.Warn("SMS read deferred", "modem", device.Path, "error", err)
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
